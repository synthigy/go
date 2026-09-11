package synthigy

import (
	"reflect"
	"sort"
	"sync"
	"testing"
)

// newTestBuffer builds a coalesceBuffer WITHOUT starting its pump goroutine,
// so push() merge logic can be asserted deterministically against the raw
// queue (the real pump drains eagerly, which would race any coalesce test).
func newTestBuffer(mode string, size int) *coalesceBuffer {
	b := &coalesceBuffer{
		mode:     mode,
		size:     size,
		byRecord: map[string]int{},
		out:      make(chan WatchEvent),
		doneCh:   make(chan struct{}),
	}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func TestUnionStrings(t *testing.T) {
	if got := unionStrings(nil, []string{"b"}); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("nil,a → %v", got)
	}
	if got := unionStrings([]string{"a"}, nil); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("a,nil → %v", got)
	}
	got := unionStrings([]string{"a", "b"}, []string{"b", "c"})
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("union dedup preserving order = %v, want [a b c]", got)
	}
}

func TestCoalesceMergesSameRecordUpdates(t *testing.T) {
	b := newTestBuffer("coalesce", 100)
	b.push(WatchEvent{Type: "record/update", Record: "r-1",
		Before: map[string]any{"n": 1}, Changed: []string{"n"}})
	b.push(WatchEvent{Type: "record/update", Record: "r-1",
		Before: map[string]any{"n": 2}, Changed: []string{"m"}})

	if len(b.queue) != 1 {
		t.Fatalf("same-record updates should collapse to 1, got %d", len(b.queue))
	}
	merged := b.queue[0]
	// earliest before is kept; changed is unioned
	if merged.Before["n"] != 1 {
		t.Errorf("earliest before not kept: %v", merged.Before)
	}
	sort.Strings(merged.Changed)
	if !reflect.DeepEqual(merged.Changed, []string{"m", "n"}) {
		t.Errorf("changed not unioned: %v", merged.Changed)
	}
}

func TestCoalesceDeleteSupersedes(t *testing.T) {
	b := newTestBuffer("coalesce", 100)
	b.push(WatchEvent{Type: "record/update", Record: "r-1", Changed: []string{"n"}})
	b.push(WatchEvent{Type: "record/delete", Record: "r-1"})
	b.push(WatchEvent{Type: "record/update", Record: "r-1", Changed: []string{"n"}}) // delete already wins

	if len(b.queue) != 1 {
		t.Fatalf("expected 1 coalesced event, got %d", len(b.queue))
	}
	if b.queue[0].Type != "record/delete" {
		t.Fatalf("delete should supersede, got %v", b.queue[0].Type)
	}
}

func TestCoalesceKeepsDistinctRecords(t *testing.T) {
	b := newTestBuffer("coalesce", 100)
	b.push(WatchEvent{Type: "record/update", Record: "r-1"})
	b.push(WatchEvent{Type: "record/update", Record: "r-2"})
	if len(b.queue) != 2 {
		t.Fatalf("distinct records must not merge, got %d", len(b.queue))
	}
}

func TestCoalesceDoesNotMergeNonRecordEvents(t *testing.T) {
	b := newTestBuffer("coalesce", 100)
	b.push(WatchEvent{Type: "entity/touched", Entity: "movie"})
	b.push(WatchEvent{Type: "entity/touched", Entity: "movie"})
	if len(b.queue) != 2 {
		t.Fatalf("entity pokes have no record key — must not coalesce, got %d", len(b.queue))
	}
}

func TestSlidingDropsOldestWhenFull(t *testing.T) {
	b := newTestBuffer("sliding", 2)
	for i, e := range []string{"e0", "e1", "e2"} {
		b.push(WatchEvent{Type: "entity/touched", Entity: e})
		_ = i
	}
	if len(b.queue) != 2 {
		t.Fatalf("sliding size 2 should cap at 2, got %d", len(b.queue))
	}
	if b.queue[0].Entity != "e1" || b.queue[1].Entity != "e2" {
		t.Fatalf("oldest (e0) should be dropped, got %v/%v", b.queue[0].Entity, b.queue[1].Entity)
	}
}

func TestLosslessNeverDropsOrCoalesces(t *testing.T) {
	b := newTestBuffer("lossless", 2)
	// even past size, and even for same-record events, lossless keeps everything
	b.push(WatchEvent{Type: "record/update", Record: "r-1", After: map[string]any{"n": 1}})
	b.push(WatchEvent{Type: "record/update", Record: "r-1", After: map[string]any{"n": 2}})
	b.push(WatchEvent{Type: "record/update", Record: "r-1", After: map[string]any{"n": 3}})
	if len(b.queue) != 3 {
		t.Fatalf("lossless must keep all 3, got %d", len(b.queue))
	}
	if b.queue[0].After["n"] != 1 || b.queue[2].After["n"] != 3 {
		t.Fatalf("lossless order/content wrong: %v", b.queue)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	b := newCoalesceBuffer("coalesce", 10) // real one with pump
	b.close()
	b.close() // must not panic (double close of doneCh)
	// pushing after close is a no-op
	b.push(WatchEvent{Type: "record/update", Record: "r-1"})
}
