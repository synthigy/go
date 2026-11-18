package synthigy

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// backfillConcurrency bounds the /history calls one replay runs at once.
const backfillConcurrency = 8

// heldEvent is a live event or sentinel held while a watch replays its gap.
type heldEvent struct {
	ev       WatchEvent
	sentinel bool
}

// replayGap pushes the record events missed since the last one seen, from
// /history, then the resumed sentinel, then the live events held meanwhile.
func (w *WatchHandle) replayGap(ctx context.Context, sentinel WatchEvent, from string, records, ops []string) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	var (
		mu   sync.Mutex
		rows []map[string]any
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, backfillConcurrency)
	for _, xid := range records {
		wg.Add(1)
		sem <- struct{}{}
		go func(xid string) {
			defer func() { <-sem; wg.Done() }()
			raw, err := w.mux.client.History().Events(ctx, xid,
				Between(from, now), Track("entity"), Limit(w.backfill))
			if err != nil {
				return
			}
			var page []map[string]any
			if json.Unmarshal(raw, &page) != nil {
				return
			}
			mu.Lock()
			rows = append(rows, page...)
			mu.Unlock()
		}(xid)
	}
	wg.Wait()

	set := make(map[string]struct{}, len(records))
	for _, x := range records {
		set[x] = struct{}{}
	}
	for _, ev := range foldHistory(rows, set, from) {
		if !opMatch(ops, ev.Type) {
			continue
		}
		if ev.After != nil && w.mux.resolver != nil {
			named := make(map[string]any, len(ev.After))
			for k, v := range ev.After {
				named[w.mux.resolver.attrName(k)] = v
			}
			ev.After = named
		}
		w.deliver(ev)
	}
	w.deliver(sentinel)
	w.drainHeld()
}

// drainHeld delivers the events held during a replay in order, handing over to
// a new replay when a held connection/resumed starts one.
func (w *WatchHandle) drainHeld() {
	for {
		w.mu.Lock()
		if len(w.held) == 0 {
			w.replaying = false
			w.mu.Unlock()
			return
		}
		h := w.held[0]
		w.held = w.held[1:]
		if h.sentinel {
			if ctx, from, records, ops, ok := w.startReplayLocked(h.ev); ok {
				w.mu.Unlock()
				go w.replayGap(ctx, h.ev, from, records, ops)
				return
			}
		}
		w.mu.Unlock()
		w.deliver(h.ev)
	}
}

// startReplayLocked reports whether sentinel starts a backfill and returns what
// the replay needs. Caller holds w.mu.
func (w *WatchHandle) startReplayLocked(sentinel WatchEvent) (context.Context, string, []string, []string, bool) {
	if sentinel.Type != "connection/resumed" || w.backfill <= 0 || w.lastTs == "" || len(w.intr.Records) == 0 || w.closed {
		return nil, "", nil, nil, false
	}
	w.replaying = true
	return w.ctx, w.lastTs, append([]string(nil), w.intr.Records...), w.intr.Ops, true
}

// foldHistory folds attribute-level /history rows into one record event per
// (record, op) after from, sorted by ts and marked FromBackfill.
func foldHistory(rows []map[string]any, records map[string]struct{}, from string) []WatchEvent {
	byKey := map[string]*WatchEvent{}
	for _, r := range rows {
		xid := firstStr(r, "recordXid", "record-xid")
		ts := firstStr(r, "ts")
		if _, ok := records[xid]; !ok || xid == "" || !(ts > from) {
			continue
		}
		op := firstStr(r, "op")
		if op == "change" {
			op = "update"
		}
		typ := "record/" + op
		key := xid + "::" + typ
		ev := byKey[key]
		if ev == nil {
			ev = &WatchEvent{
				Type:         typ,
				Record:       xid,
				Ts:           ts,
				Txid:         firstStr(r, "txid"),
				Actor:        firstStr(r, "actorXid", "actor-xid"),
				Request:      firstStr(r, "requestId", "request-id"),
				Scope:        firstStr(r, "scopeXid", "scope-xid"),
				After:        map[string]any{},
				FromBackfill: true,
			}
			byKey[key] = ev
		}
		if attr := firstStr(r, "attributeXid", "attribute-xid"); attr != "" && attr != "__delete__" {
			ev.After[attr] = r["value"]
		}
		if ts > ev.Ts {
			ev.Ts = ts
		}
	}
	out := make([]WatchEvent, 0, len(byKey))
	for _, ev := range byKey {
		if ev.Type == "record/delete" {
			ev.After = nil
		}
		out = append(out, *ev)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Ts != out[j].Ts {
			return out[i].Ts < out[j].Ts
		}
		return out[i].Record+out[i].Type < out[j].Record+out[j].Type
	})
	return out
}

// firstStr returns the first present string-valued key from m.
func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}
