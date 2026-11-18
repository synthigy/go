package synthigy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestFirstStr(t *testing.T) {
	m := map[string]any{"a": "x", "b": 3, "c": "y"}
	if got := firstStr(m, "missing", "a"); got != "x" {
		t.Errorf("firstStr picked %q, want x", got)
	}
	if got := firstStr(m, "b", "c"); got != "y" {
		t.Errorf("non-string b should be skipped, got %q", got)
	}
	if got := firstStr(m, "nope"); got != "" {
		t.Errorf("absent keys → empty, got %q", got)
	}
}

func TestFoldHistoryFoldsPerRecordOpAfterFrom(t *testing.T) {
	rows := []map[string]any{
		{"record-xid": "u-2", "op": "delete", "ts": "t3", "attribute-xid": "__delete__"},
		{"record-xid": "u-1", "op": "change", "ts": "t2", "attribute-xid": "a-age", "value": 30.0},
		{"record-xid": "u-1", "op": "change", "ts": "t1", "attribute-xid": "a-name", "value": "A"},
		{"record-xid": "outside", "op": "change", "ts": "t1", "attribute-xid": "a-x", "value": 1},
		{"record-xid": "u-1", "op": "insert", "ts": "t0", "attribute-xid": "a-name", "value": "old"},
	}
	got := foldHistory(rows, map[string]struct{}{"u-1": {}, "u-2": {}}, "t0")
	if len(got) != 2 {
		t.Fatalf("want u-1 update then u-2 delete, got %+v", got)
	}
	upd, del := got[0], got[1]
	if upd.Type != "record/update" || upd.Record != "u-1" || upd.Ts != "t2" || !upd.FromBackfill {
		t.Fatalf("update = %+v", upd)
	}
	if upd.After["a-name"] != "A" || upd.After["a-age"] != 30.0 {
		t.Errorf("attributes not folded: %v", upd.After)
	}
	if del.Type != "record/delete" || del.Record != "u-2" || del.After != nil || !del.FromBackfill {
		t.Errorf("delete = %+v", del)
	}
}

type backfillServer struct {
	mu      sync.Mutex
	calls   []map[string]any
	release chan struct{}
	called  chan struct{}
	fail    bool
}

func newBackfillWatch(t *testing.T, bs *backfillServer, interest WatchInterest) (*WatchHandle, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{
			"user": map[string]any{"xid": "e-user", "xids": map[string]any{"attributes": map[string]any{"name": "a-name"}}},
		}})
	})
	mux.HandleFunc("/data/subscription/set", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/data/events", sseHandler(nil))
	mux.HandleFunc("/history", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		opts, _ := body["opts"].(map[string]any)
		bs.mu.Lock()
		bs.calls = append(bs.calls, opts)
		bs.mu.Unlock()
		bs.called <- struct{}{}
		select {
		case <-bs.release:
		case <-r.Context().Done():
			return
		}
		if bs.fail {
			w.WriteHeader(500)
			return
		}
		xid, _ := opts["record-xid"].(string)
		rows := []map[string]any{
			{"record-xid": xid, "op": "change", "ts": "2026-01-01T00:00:03Z", "attribute-xid": "a-name", "value": "B-" + xid},
			{"record-xid": xid, "op": "insert", "ts": "2026-01-01T00:00:02Z", "attribute-xid": "a-name", "value": "A-" + xid},
			{"record-xid": xid, "op": "insert", "ts": "2026-01-01T00:00:01Z", "attribute-xid": "a-name", "value": "seen"},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": rows})
	})
	srv := httptest.NewServer(mux)
	c, _ := New(Config{Endpoint: srv.URL, Token: "t"})
	w, err := c.Watch(context.Background(), interest, Backfill(true), BackfillLimit(50))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.mux.resolver.ensureLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w, func() { w.Close(); c.Close(); srv.Close() }
}

func waitHistory(t *testing.T, bs *backfillServer) {
	t.Helper()
	select {
	case <-bs.called:
	case <-time.After(3 * time.Second):
		t.Fatal("no /history call")
	}
}

func nextEvent(t *testing.T, ch <-chan WatchEvent) WatchEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	return WatchEvent{}
}

func TestBackfillReplaysTheGapThenResumedThenHeldEvents(t *testing.T) {
	bs := &backfillServer{release: make(chan struct{}), called: make(chan struct{}, 4)}
	w, done := newBackfillWatch(t, bs, WatchInterest{Records: []string{"u-1", "u-2"}, Ops: []string{"insert", "delete"}})
	defer done()
	events := w.Events()

	w.pushEvent(WatchEvent{Type: "record/update", Record: "u-1", Ts: "2026-01-01T00:00:01Z"})
	if ev := nextEvent(t, events); ev.Ts != "2026-01-01T00:00:01Z" {
		t.Fatalf("live event = %+v", ev)
	}
	w.pushSentinel(WatchEvent{Type: "connection/resumed"})
	waitHistory(t, bs)
	waitHistory(t, bs)
	w.pushEvent(WatchEvent{Type: "record/update", Record: "u-9", Ts: "2026-01-01T00:00:09Z"})
	w.pushSentinel(WatchEvent{Type: "schema/changed"})
	close(bs.release)

	var got []WatchEvent
	for len(got) < 5 {
		got = append(got, nextEvent(t, events))
	}
	for i, rec := range []string{"u-1", "u-2"} {
		ev := got[i]
		if ev.Type != "record/insert" || !ev.FromBackfill || ev.After["name"] != "A-"+ev.Record || ev.Ts != "2026-01-01T00:00:02Z" {
			t.Fatalf("backfill %d (%s) = %+v", i, rec, ev)
		}
	}
	if got[0].Record == got[1].Record {
		t.Fatalf("want one replayed insert per record, got %+v", got[:2])
	}
	if got[2].Type != "connection/resumed" || got[3].Record != "u-9" || got[4].Type != "schema/changed" {
		t.Fatalf("after the replay: %+v", got[2:])
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	for _, opts := range bs.calls {
		between, _ := opts["between"].([]any)
		if opts["track"] != "entity" || opts["limit"] != 50.0 || len(between) != 2 || between[0] != "2026-01-01T00:00:01Z" {
			t.Fatalf("history opts = %v", opts)
		}
	}
}

func TestAFailedBackfillStillDeliversResumedAndHeldEvents(t *testing.T) {
	bs := &backfillServer{release: make(chan struct{}), called: make(chan struct{}, 4), fail: true}
	w, done := newBackfillWatch(t, bs, WatchInterest{Records: []string{"u-1"}})
	defer done()
	events := w.Events()
	w.pushEvent(WatchEvent{Type: "record/update", Record: "u-1", Ts: "2026-01-01T00:00:01Z"})
	nextEvent(t, events)
	w.pushSentinel(WatchEvent{Type: "connection/resumed"})
	waitHistory(t, bs)
	w.pushEvent(WatchEvent{Type: "record/update", Record: "u-1", Ts: "2026-01-01T00:00:09Z"})
	close(bs.release)
	if ev := nextEvent(t, events); ev.Type != "connection/resumed" {
		t.Fatalf("first after a failed replay = %+v", ev)
	}
	if ev := nextEvent(t, events); ev.Ts != "2026-01-01T00:00:09Z" || ev.FromBackfill {
		t.Fatalf("held event = %+v", ev)
	}
}
