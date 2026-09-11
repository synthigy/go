package synthigy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestWatchQueryDerivesAddedChangedRemoved drives the crown-jewel live-data
// path against a stub: an initial snapshot, then a record event triggers a
// coalesced refetch whose diff surfaces query/added + query/changed +
// query/removed. Mirrors the sync-SDK contract the other languages test.
func TestWatchQueryDerivesAddedChangedRemoved(t *testing.T) {
	var dataCalls int32
	// snapshot (call 1) → m-1(A), m-2(B); refetch (call 2+) → m-1(A2), m-3(C).
	rows1 := []map[string]any{{"xid": "m-1", "title": "A"}, {"xid": "m-2", "title": "B"}}
	rows2 := []map[string]any{{"xid": "m-1", "title": "A2"}, {"xid": "m-3", "title": "C"}}

	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&dataCalls, 1)
		rows := rows2
		if n == 1 {
			rows = rows1
		}
		writeResults(w, []map[string]any{{"ok": true, "data": rows}})
	})
	mux.HandleFunc("/data/events", blockingSSE([]string{
		"event: data\ndata: {\"type\":\"record/update\",\"record-xid\":\"m-1\",\"ts\":\"t1\",\"before\":{\"title\":\"A\"},\"after\":{\"title\":\"A2\"}}\n\n",
	}))
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

	if len(qw.Initial()) != 2 {
		t.Fatalf("initial snapshot should have 2 rows, got %v", qw.Initial())
	}

	// The event fans out record/* → coalesced refetch → three derived events.
	got := map[string]WatchEvent{}
	deadline := time.After(5 * time.Second)
	for len(got) < 3 {
		select {
		case ev := <-qw.Events():
			switch ev.Type {
			case "query/added", "query/changed", "query/removed":
				got[ev.Type] = ev
			}
		case <-deadline:
			t.Fatalf("timed out; derived events so far: %v", keysOf(got))
		}
	}

	if got["query/added"].Record != "m-3" {
		t.Errorf("added should be m-3, got %q", got["query/added"].Record)
	}
	if got["query/removed"].Record != "m-2" {
		t.Errorf("removed should be m-2, got %q", got["query/removed"].Record)
	}
	ch := got["query/changed"]
	if ch.Record != "m-1" {
		t.Errorf("changed should be m-1, got %q", ch.Record)
	}
	if len(ch.Changed) != 1 || ch.Changed[0] != "title" {
		t.Errorf("changed keys = %v, want [title]", ch.Changed)
	}
}

func keysOf(m map[string]WatchEvent) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
