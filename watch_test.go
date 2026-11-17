package synthigy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// blockingSSE streams the given frames then keeps the connection open. Each
// time the handler is hit it sends one extra "tick" frame so reconnects don't
// matter for these tests.
func blockingSSE(frames []string) http.HandlerFunc {
	return sseHandler(frames)
}

func TestWatchDispatchesRecordEvent(t *testing.T) {
	frames := []string{
		"event: data\ndata: {\"type\":\"record/update\",\"record-xid\":\"r1\",\"before\":{\"n\":1},\"after\":{\"n\":2}}\n\n",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data/events", blockingSSE(frames))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w, err := c.Watch(ctx, WatchInterest{Records: []string{"r1"}})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Close()

	select {
	case ev := <-w.Events():
		if ev.Type != "record/update" || ev.Record != "r1" {
			t.Fatalf("unexpected event: %+v", ev)
		}
		if len(ev.Changed) != 1 || ev.Changed[0] != "n" {
			t.Errorf("expected changed [n], got %v", ev.Changed)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
}

// TestWatchEventsFansOutToMultipleConsumers proves Events() is fan-out, not
// a shared queue: two independent channels off the SAME handle both see
// every event — neither steals it from the other.
func TestWatchEventsFansOutToMultipleConsumers(t *testing.T) {
	frames := []string{
		"event: data\ndata: {\"type\":\"record/update\",\"record-xid\":\"r1\",\"before\":{\"n\":1},\"after\":{\"n\":2}}\n\n",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data/events", blockingSSE(frames))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w, err := c.Watch(ctx, WatchInterest{Records: []string{"r1"}})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Close()

	// THREE independent consumers on the SAME handle.
	ch1, ch2, ch3 := w.Events(), w.Events(), w.Events()

	for i, ch := range []<-chan WatchEvent{ch1, ch2, ch3} {
		select {
		case ev := <-ch:
			if ev.Type != "record/update" || ev.Record != "r1" {
				t.Fatalf("consumer %d: unexpected event: %+v", i, ev)
			}
		case <-ctx.Done():
			t.Fatalf("consumer %d: timed out waiting for event — fan-out broken, "+
				"one consumer stole it from the others", i)
		}
	}
}

func TestWatchSqlTemplateRefreshesOnTouch(t *testing.T) {
	var sqlCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		// Each sql-template call returns an incrementing count so the second
		// call differs from the first and triggers result/changed.
		n := atomic.AddInt32(&sqlCalls, 1)
		writeResults(w, []map[string]any{
			{"ok": true, "data": []map[string]any{{"n": int(n)}}},
		})
	})
	mux.HandleFunc("/data/events", blockingSSE([]string{
		"event: data\ndata: {\"type\":\"entity/touched\",\"entity\":\"user\",\"ts\":\"2026-06-16T00:00:00Z\"}\n\n",
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	tile, err := c.WatchSqlTemplate(ctx, "SELECT count(*) AS n FROM {user}", nil,
		Entities("user"))
	if err != nil {
		t.Fatalf("WatchSqlTemplate: %v", err)
	}
	defer tile.Close()

	if tile.First()["n"].(float64) != 2 {
		t.Fatalf("first after open = %v, want 2 (snapshot, then one refetch once subscribed)", tile.First())
	}

	for {
		select {
		case ev := <-tile.Events():
			if ev.Type != "result/changed" {
				t.Fatalf("unexpected event: %+v", ev)
			}
			if tile.First()["n"].(float64) >= 3 {
				return
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for the touch to refresh the result")
		}
	}
}

func TestWatchKeepsEventsBeforeTheFirstEventsCall(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	w, err := c.Watch(context.Background(), WatchInterest{Records: []string{"A"}})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.push(WatchEvent{Type: "record/update", Record: "A"})
	select {
	case ev := <-w.Events():
		if ev.Type != "record/update" || ev.Record != "A" {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an event pushed before Events() was lost")
	}
}

var rejection = map[string]any{"message": "no", "code": "FORBIDDEN", "hint": "h", "rule": "stamp", "doc": "errors/stamp"}

func nextRejected(t *testing.T, events <-chan WatchEvent) WatchEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == "subscription/rejected" {
				return ev
			}
		case <-deadline:
			t.Fatal("no subscription/rejected event")
		}
	}
}

func assertRejectionError(t *testing.T, ev WatchEvent) {
	t.Helper()
	var se *Error
	if !errors.As(ev.Err, &se) {
		t.Fatalf("Err = %#v, want *Error", ev.Err)
	}
	if ev.Reason != ev.Err.Error() {
		t.Errorf("Reason = %q, want the error's message %q", ev.Reason, ev.Err.Error())
	}
	got := []string{se.Code, se.Hint, se.Rule, se.Doc}
	want := []string{"FORBIDDEN", "h", "stamp", "errors/stamp"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("code, hint, rule, doc = %q, want %q", got, want)
		}
	}
}

func TestWatchRejectedSubscribeCarriesTheError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": rejection})
	})
	mux.HandleFunc("/data/events", blockingSSE(nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, err := c.Watch(ctx, WatchInterest{Records: []string{"r1"}})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Close()
	assertRejectionError(t, nextRejected(t, w.Events()))
}

func TestWatchQueryRejectedRefreshCarriesTheError(t *testing.T) {
	var dataCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&dataCalls, 1) == 1 {
			writeResults(w, []map[string]any{{"ok": true, "data": []map[string]any{{"xid": "m-1", "title": "A"}}}})
			return
		}
		writeResults(w, []map[string]any{{"ok": false, "error": rejection}})
	})
	mux.HandleFunc("/data/events", blockingSSE(nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	qw, err := c.WatchQuery(ctx, "movie", nil, Selection{"title": nil})
	if err != nil {
		t.Fatalf("WatchQuery: %v", err)
	}
	defer qw.Close()
	assertRejectionError(t, nextRejected(t, qw.Events()))
}
