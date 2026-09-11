package synthigy

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
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

func TestBackfillObserveFoldsPerRecordOp(t *testing.T) {
	// /history returns attribute-primary rows; backfill folds them into one
	// observe event per (record, op), attribute-xid-keyed, with fromBackfill.
	rows := []map[string]any{
		{"record-xid": "u-1", "op": "change", "ts": "t1", "attribute-xid": "a-name", "value": "A"},
		{"record-xid": "u-1", "op": "change", "ts": "t2", "attribute-xid": "a-age", "value": 30},
		{"record-xid": "outside", "op": "change", "ts": "t1", "attribute-xid": "a-x", "value": 1},
		{"record-xid": "u-2", "op": "delete", "ts": "t3", "attribute-xid": "__delete__"},
	}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": rows})
	}))

	nd := descriptor{records: map[string]struct{}{"u-1": {}, "u-2": {}}}
	var got []Event
	last := c.backfillObserve(context.Background(), nd, "t0", 100, func(e Event) bool {
		got = append(got, e)
		return true
	})

	if len(got) != 2 {
		t.Fatalf("expected 2 folded events (u-1 update, u-2 delete), got %d: %+v", len(got), got)
	}
	upd := got[0]
	if upd.Type != "record/update" || upd.RecordXID != "u-1" {
		t.Fatalf("first event wrong: %+v", upd)
	}
	if upd.After["a-name"] != "A" {
		t.Errorf("attribute a-name not folded: %v", upd.After)
	}
	if upd.After["a-age"] != float64(30) { // JSON numbers decode to float64
		t.Errorf("attribute a-age not folded: %v", upd.After)
	}
	if upd.Ts != "t2" {
		t.Errorf("ts should be the max within the fold, got %q", upd.Ts)
	}
	if upd.Raw["fromBackfill"] != true {
		t.Errorf("fromBackfill flag missing: %v", upd.Raw)
	}
	del := got[1]
	if del.Type != "record/delete" || del.After != nil {
		t.Errorf("delete event should have nil After: %+v", del)
	}
	if last != "t3" {
		t.Errorf("lastSeenTs should advance to newest folded ts, got %q", last)
	}
}

func TestBackfillObserveSwallowsHistoryErrors(t *testing.T) {
	// 404 (no audit provider) → HISTORY_UNAVAILABLE inside; backfill swallows
	// it and returns the input lastSeenTs unchanged, emitting nothing.
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	nd := descriptor{records: map[string]struct{}{"u-1": {}}}
	emitted := 0
	last := c.backfillObserve(context.Background(), nd, "t5", 10, func(Event) bool {
		emitted++
		return true
	})
	if emitted != 0 {
		t.Errorf("history error should emit nothing, emitted %d", emitted)
	}
	if last != "t5" {
		t.Errorf("lastSeenTs should be unchanged on error, got %q", last)
	}
}
