package synthigy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestServer returns an httptest server and a client pointed at it (static
// token mode unless overridden).
func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

// dataResults writes a /data response with the given per-op results.
func writeResults(w http.ResponseWriter, results []map[string]any) {
	w.Header().Set("X-Request-Id", "rid-test")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
}

func TestSearch(t *testing.T) {
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/data" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing auth header")
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		writeResults(w, []map[string]any{
			{"ok": true, "data": []map[string]any{{"name": "Alice"}, {"name": "Bob"}}},
		})
	}))

	rows, err := c.Search(context.Background(), "user",
		Args{"active": Eq(true)}, Selection{"name": nil})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rows) != 2 || rows[0]["name"] != "Alice" {
		t.Fatalf("unexpected rows: %v", rows)
	}
	ops, _ := gotBody["operations"].([]any)
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %v", gotBody)
	}
	op := ops[0].(map[string]any)
	if op["op"] != "search" || op["entity"] != "user" {
		t.Errorf("bad op shape: %v", op)
	}
}

func TestPerOpError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResults(w, []map[string]any{
			{"ok": false, "error": map[string]any{"message": "no such entity", "code": "UNKNOWN_ENTITY"}},
		})
	}))
	_, err := c.Search(context.Background(), "nope", nil, Selection{"x": nil})
	var se *Error
	if !errors.As(err, &se) || se.Code != "UNKNOWN_ENTITY" || se.Category != "not_found" {
		t.Fatalf("expected UNKNOWN_ENTITY not_found, got %v", err)
	}
	if se.RequestID != "rid-test" {
		t.Errorf("request id not threaded: %q", se.RequestID)
	}
}

func TestCountSelectionWireShape(t *testing.T) {
	// _count is a selection key (related-entity rollup), not a standalone op.
	// Verify it normalizes to the relation rel-config wire shape inside search.
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		writeResults(w, []map[string]any{
			{"ok": true, "data": []map[string]any{{"title": "Toy Story", "_count": map[string]any{"actors": 11}}}},
		})
	}))
	rows, err := c.Search(context.Background(), "movie", Args{"_limit": 1},
		Selection{"title": nil, "_count": Selection{"actors": nil}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if cnt, _ := rows[0]["_count"].(map[string]any); cnt["actors"].(float64) != 11 {
		t.Fatalf("unexpected _count: %v", rows[0])
	}
	op := gotBody["operations"].([]any)[0].(map[string]any)
	sel := op["selections"].(map[string]any)
	arr, ok := sel["_count"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("_count not normalized to rel-config array: %v", sel["_count"])
	}
	cfg := arr[0].(map[string]any)
	if _, ok := cfg["selections"].(map[string]any)["actors"]; !ok {
		t.Errorf("_count selections missing actors: %v", cfg)
	}
}

func Test403Forbidden(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "rid-403")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "denied", "code": "ENTITY_FORBIDDEN"},
		})
	}))
	_, err := c.Search(context.Background(), "secret", nil, Selection{"x": nil})
	var se *Error
	if !errors.As(err, &se) || se.Code != "ENTITY_FORBIDDEN" || se.Status != 403 {
		t.Fatalf("expected ENTITY_FORBIDDEN 403, got %v", err)
	}
}

func TestClientCredentialsAndRefreshOn401(t *testing.T) {
	var tokenCalls int32
	var dataCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&tokenCalls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-" + string(rune('0'+n)),
			"expires_in":   3600,
		})
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&dataCalls, 1)
		// First data call: reject with 401 to force a refresh + retry.
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeResults(w, []map[string]any{{"ok": true, "data": []map[string]any{}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(Config{Endpoint: srv.URL, ClientID: "id", ClientSecret: "secret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Search(context.Background(), "user", nil, Selection{"x": nil}); err != nil {
		t.Fatalf("Search after refresh: %v", err)
	}
	if atomic.LoadInt32(&dataCalls) != 2 {
		t.Errorf("expected 2 data calls (401 then retry), got %d", dataCalls)
	}
	if atomic.LoadInt32(&tokenCalls) != 2 {
		t.Errorf("expected 2 token fetches (initial + refresh), got %d", tokenCalls)
	}
}

func TestSubscribeSetBody(t *testing.T) {
	var got map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/data/subscription/set" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))

	_, err := c.Subscribe(context.Background(),
		Descriptor{Records: []string{"b", "a"}, Operations: []string{"update"}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	subs := got["subscriptions"].([]any)
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %v", got)
	}
	item := subs[0].(map[string]any)
	recs := item["records"].([]any)
	if recs[0] != "a" || recs[1] != "b" { // sorted
		t.Errorf("records not sorted: %v", recs)
	}
	if item["type"] != "data" {
		t.Errorf("type = %v", item["type"])
	}
}

func TestSubscribeRejectsEmptyRecords(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	_, err := c.Subscribe(context.Background(), Descriptor{Records: nil})
	var se *Error
	if !errors.As(err, &se) || se.Code != "EMPTY_RECORDS" {
		t.Fatalf("expected EMPTY_RECORDS, got %v", err)
	}
}

// sseHandler streams a fixed set of frames then holds until the request
// context is cancelled.
func sseHandler(frames []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flush", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl.Flush()
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
			fl.Flush()
		}
		<-r.Context().Done()
	}
}

func TestObserveFiltersByDescriptor(t *testing.T) {
	frames := []string{
		"event: data\ndata: {\"type\":\"record/update\",\"record-xid\":\"u-1\",\"after\":{\"name\":\"X\"}}\n\n",
		"event: data\ndata: {\"type\":\"record/update\",\"record-xid\":\"other\",\"after\":{\"name\":\"Y\"}}\n\n",
		"event: data\ndata: {\"type\":\"record/delete\",\"record-xid\":\"u-1\"}\n\n",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data/events", sseHandler(frames))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := c.Observe(ctx, RecordsDesc("u-1"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	defer stream.Close()

	var got []string
	for ev := range stream.Events() {
		got = append(got, ev.Type+":"+ev.RecordXID)
		if ev.Type == "record/delete" {
			break
		}
	}
	if len(got) != 2 || got[0] != "record/update:u-1" || got[1] != "record/delete:u-1" {
		t.Fatalf("expected only u-1 events, got %v", got)
	}
}

func TestStaticTokenEmptyDev(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no auth header in empty-token dev mode")
		}
		writeResults(w, []map[string]any{{"ok": true, "data": []map[string]any{}}})
	}))
	// Override to empty static token.
	c.staticToken = ""
	c.tokenSource = nil
	if _, err := c.Search(context.Background(), "user", nil, Selection{"x": nil}); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

func TestSSEFrameParsing(t *testing.T) {
	frames := []string{
		"id: 7\nevent: data\ndata: {\"type\":\"record/insert\",\"record-xid\":\"r1\"}\n\n",
	}
	srv := httptest.NewServer(sseHandler(frames))
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var sawOpen, sawData bool
	err := c.sseSession(ctx, "", func(f sseFrame) bool {
		if f.event == "open" {
			sawOpen = true
			return true
		}
		if strings.Contains(f.data, "record/insert") && f.id == "7" {
			sawData = true
			return false // stop
		}
		return true
	})
	if err != nil {
		t.Fatalf("sseSession: %v", err)
	}
	if !sawOpen || !sawData {
		t.Errorf("open=%v data=%v", sawOpen, sawData)
	}
}

func TestQuerySendsXsqlDocumentOp(t *testing.T) {
	// STRICT wire: XSQL travels only as {op: "xsql", xsql: <document>} — a
	// bare rooted body gets a synthetic `@<verb> _q` header client-side, and
	// no entity/selections fields ride the op.
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		writeResults(w, []map[string]any{{"ok": true, "data": []map[string]any{{"name": "A"}}}})
	}))
	if _, err := c.Query(context.Background(), "user\n  name\n",
		map[string]any{"n": 5}); err != nil {
		t.Fatalf("Query: %v", err)
	}
	op := gotBody["operations"].([]any)[0].(map[string]any)
	if op["op"] != "xsql" {
		t.Fatalf("op = %v, want xsql", op["op"])
	}
	doc, _ := op["xsql"].(string)
	if !strings.HasPrefix(doc, "@search _q\n") {
		t.Fatalf("xsql = %q, want @search _q header", doc)
	}
	for _, k := range []string{"entity", "selections"} {
		if _, ok := op[k]; ok {
			t.Errorf("op carries forbidden field %q", k)
		}
	}
	if _, err := c.Query(context.Background(), "@get one\nuser (xid = ?x:string)\n  name\n",
		map[string]any{"x": "u1"}); err != nil {
		t.Fatalf("Query doc: %v", err)
	}
	op = gotBody["operations"].([]any)[0].(map[string]any)
	if doc, _ := op["xsql"].(string); !strings.HasPrefix(doc, "@get one\n") {
		t.Fatalf("document source not passed verbatim: %q", doc)
	}
}

func TestEndpointDefaultsToEnv(t *testing.T) {
	t.Setenv("SYNTHIGY_ENDPOINT", "http://example.test/")
	c, err := New(Config{Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if c.endpoint != "http://example.test" {
		t.Fatalf("endpoint = %q", c.endpoint)
	}
}

func TestNoEndpointIsTyped(t *testing.T) {
	t.Setenv("SYNTHIGY_ENDPOINT", "")
	_, err := New(Config{Token: "t"})
	var se *Error
	if !errors.As(err, &se) || se.Code != "NO_ENDPOINT" || se.Category != "validation" || se.Retryable {
		t.Fatalf("got %#v", err)
	}
}
