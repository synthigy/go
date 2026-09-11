package synthigy

import (
	"strings"
	"sync"
)

// coalesceBuffer is an ordered event queue with a pump goroutine that
// delivers to an output channel. In "coalesce" mode, pending record/* events
// on the same record collapse (delete supersedes; updates merge keeping the
// earliest before). Otherwise events are appended, dropping the oldest when
// the size bound is exceeded.
type coalesceBuffer struct {
	mu       sync.Mutex
	cond     *sync.Cond
	queue    []WatchEvent
	byRecord map[string]int
	closed   bool
	mode     string
	size     int
	out      chan WatchEvent
	doneCh   chan struct{}
}

func newCoalesceBuffer(mode string, size int) *coalesceBuffer {
	b := &coalesceBuffer{
		mode:     mode,
		size:     size,
		byRecord: map[string]int{},
		out:      make(chan WatchEvent),
		doneCh:   make(chan struct{}),
	}
	b.cond = sync.NewCond(&b.mu)
	go b.pump()
	return b
}

func (b *coalesceBuffer) push(ev WatchEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if b.mode == "coalesce" && strings.HasPrefix(ev.Type, "record/") && ev.Record != "" {
		if idx, ok := b.byRecord[ev.Record]; ok {
			prev := b.queue[idx]
			if ev.Type == "record/delete" {
				b.queue[idx] = ev
				b.cond.Signal()
				return
			}
			if prev.Type == "record/delete" {
				return // delete already wins
			}
			merged := ev
			if prev.Before != nil {
				merged.Before = prev.Before
			}
			merged.Changed = unionStrings(prev.Changed, ev.Changed)
			b.queue[idx] = merged
			b.cond.Signal()
			return
		}
	}
	if len(b.queue) >= b.size && b.mode != "lossless" {
		// Drop oldest (sliding) and reindex.
		b.queue = b.queue[1:]
		b.reindex()
	}
	if strings.HasPrefix(ev.Type, "record/") && ev.Record != "" {
		b.byRecord[ev.Record] = len(b.queue)
	}
	b.queue = append(b.queue, ev)
	b.cond.Signal()
}

func (b *coalesceBuffer) reindex() {
	b.byRecord = map[string]int{}
	for i, e := range b.queue {
		if strings.HasPrefix(e.Type, "record/") && e.Record != "" {
			b.byRecord[e.Record] = i
		}
	}
}

func (b *coalesceBuffer) close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	close(b.doneCh)
	b.cond.Broadcast()
	b.mu.Unlock()
}

func (b *coalesceBuffer) pump() {
	defer close(b.out)
	for {
		b.mu.Lock()
		for len(b.queue) == 0 && !b.closed {
			b.cond.Wait()
		}
		if len(b.queue) == 0 && b.closed {
			b.mu.Unlock()
			return
		}
		ev := b.queue[0]
		b.queue = b.queue[1:]
		b.reindex()
		b.mu.Unlock()

		select {
		case b.out <- ev:
		case <-b.doneCh:
			return
		}
	}
}

func unionStrings(a, b []string) []string {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	set := map[string]struct{}{}
	out := []string{}
	for _, x := range append(append([]string{}, a...), b...) {
		if _, ok := set[x]; ok {
			continue
		}
		set[x] = struct{}{}
		out = append(out, x)
	}
	return out
}
