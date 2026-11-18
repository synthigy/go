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

func TestWhoami(t *testing.T) {
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeResults(w, []map[string]any{{"ok": true, "data": map[string]any{
			"xid": "x-bgates", "name": "bgates", "active": true, "superuser": false,
			"roles": []string{"Viewer"}, "groups": []string{},
			"client": map[string]any{"id": "viewer", "trusted": true}}}})
	}))
	me, err := c.Whoami(context.Background(), ActingAs("x-bgates"))
	if err != nil {
		t.Fatal(err)
	}
	if me.XID != "x-bgates" || me.Superuser || len(me.Roles) != 1 || me.Roles[0] != "Viewer" ||
		me.Client == nil || me.Client.ID != "viewer" || !me.Client.Trusted {
		t.Fatalf("whoami = %+v", me)
	}
	if gotBody["acting_as"] != "x-bgates" {
		t.Errorf("acting_as = %v", gotBody["acting_as"])
	}
	if ops, _ := gotBody["operations"].([]any); len(ops) != 1 || ops[0].(map[string]any)["op"] != "whoami" {
		t.Errorf("operations = %v", gotBody["operations"])
	}
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

func TestOperationSelectionWireShape(t *testing.T) {
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		writeResults(w, []map[string]any{
			{"ok": true, "data": []map[string]any{{"title": "Toy Story", "cast_size": 11}}},
		})
	}))
	rows, err := c.Search(context.Background(), "movie", Args{"_limit": 1},
		Selection{"title": nil, "actors": []any{map[string]any{"alias": "cast_size", "op": "count"}}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if n, _ := rows[0]["cast_size"].(float64); n != 11 {
		t.Fatalf("unexpected cast_size: %v", rows[0])
	}
	op := gotBody["operations"].([]any)[0].(map[string]any)
	arr, ok := op["selections"].(map[string]any)["actors"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("actors not sent as an entry array: %v", op["selections"])
	}
	if cfg := arr[0].(map[string]any); cfg["alias"] != "cast_size" || cfg["op"] != "count" {
		t.Errorf("operation entry changed: %v", cfg)
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

func busyThen(t *testing.T, busy int, code string) (*Client, *int) {
	t.Helper()
	saved := busyDelay
	busyDelay = func(string) time.Duration { return time.Millisecond }
	t.Cleanup(func() { busyDelay = saved })
	calls := 0
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= busy {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "busy", "code": code, "retryable": true},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"ok": true, "data": []any{}}}})
	}))
	return c, &calls
}

func TestServerBusyIsResent(t *testing.T) {
	c, calls := busyThen(t, 2, "SERVER_BUSY")
	if _, err := c.Search(context.Background(), "movie", nil, Selection{"x": nil}); err != nil || *calls != 3 {
		t.Fatalf("expected success on the third attempt, got %v after %d", err, *calls)
	}
}

func TestServerBusyGivesUpAfterThreeAttempts(t *testing.T) {
	c, calls := busyThen(t, 99, "SERVER_BUSY")
	_, err := c.Search(context.Background(), "movie", nil, Selection{"x": nil})
	var se *Error
	if !errors.As(err, &se) || se.Code != "SERVER_BUSY" || se.Category != "rate_limit" || !se.Retryable || se.Status != 503 || *calls != 3 {
		t.Fatalf("expected a retryable SERVER_BUSY 503 after 3 attempts, got %+v after %d", err, *calls)
	}
}

func TestOtherRefusalsAreNotResent(t *testing.T) {
	c, calls := busyThen(t, 99, "MODULE_NOT_LOADED")
	if _, err := c.Search(context.Background(), "movie", nil, Selection{"x": nil}); err == nil || *calls != 1 {
		t.Fatalf("expected one attempt, got %v after %d", err, *calls)
	}
}

func TestACancelledCallStopsWaiting(t *testing.T) {
	c, calls := busyThen(t, 99, "SERVER_BUSY")
	busyDelay = func(string) time.Duration { return time.Hour }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Search(ctx, "movie", nil, Selection{"x": nil})
	if !errors.Is(err, context.DeadlineExceeded) || *calls != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("expected the wait to end with the context, got %v after %d", err, *calls)
	}
}

func TestTheClientTimeoutBoundsTheBusyWait(t *testing.T) {
	saved := busyDelay
	busyDelay = func(string) time.Duration { return time.Hour }
	t.Cleanup(func() { busyDelay = saved })
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "busy", "code": "SERVER_BUSY"}})
	}))
	t.Cleanup(srv.Close)
	c, _ := New(Config{Endpoint: srv.URL, Token: "t", Timeout: 200 * time.Millisecond})
	start := time.Now()
	_, err := c.Search(context.Background(), "movie", nil, Selection{"x": nil})
	var se *Error
	if !errors.As(err, &se) || se.Code != "SERVER_BUSY" || calls != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("expected SERVER_BUSY at once, got %v after %d", err, calls)
	}
}

func TestABusyResendGetsOnlyTheTimeLeft(t *testing.T) {
	saved := busyDelay
	busyDelay = func(string) time.Duration { return time.Millisecond }
	t.Cleanup(func() { busyDelay = saved })
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			time.Sleep(300 * time.Millisecond)
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "busy", "code": "SERVER_BUSY"}})
			return
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	c, _ := New(Config{Endpoint: srv.URL, Token: "t", Timeout: 500 * time.Millisecond})
	start := time.Now()
	_, err := c.Search(context.Background(), "movie", nil, Selection{"x": nil})
	if took := time.Since(start); err == nil || calls.Load() != 2 || took > 700*time.Millisecond {
		t.Fatalf("expected the resend to end near the 500ms client timeout, got %v after %d calls in %v", err, calls.Load(), took)
	}
}

func TestBusyDelayFollowsRetryAfter(t *testing.T) {
	for i := 0; i < 200; i++ {
		if d := busyDelay("2"); d < time.Second || d >= 3*time.Second {
			t.Fatalf("Retry-After 2 gave %v", d)
		}
		if d := busyDelay(""); d < 500*time.Millisecond || d >= 1500*time.Millisecond {
			t.Fatalf("no Retry-After gave %v", d)
		}
		if d := busyDelay("10000000000"); d < 5*time.Second || d >= 15*time.Second {
			t.Fatalf("a huge Retry-After gave %v", d)
		}
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

	_, err := c.subscribe(context.Background(),
		subDescriptor{Records: []string{"b", "a"}, Operations: []string{"update"}})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
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
	_, err := c.subscribe(context.Background(), subDescriptor{Records: nil})
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
	err := c.sseSession(ctx, "", "", func(f sseFrame) bool {
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
