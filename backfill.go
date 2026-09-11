package synthigy

import (
	"context"
	"encoding/json"
	"time"
)

// backfillObserve replays /history events missed during an Observe
// disconnect, folding attribute-primary audit rows into observe-shaped
// events (one per record+op) and emitting them. It returns the updated
// lastSeenTs. Best-effort: history errors are swallowed.
//
// Backfill rows are attribute-xid-keyed (the server hasn't translated them to
// attribute names like live events). Folded events carry fromBackfill=true in
// Raw so consumers can tell them apart and translate if needed.
func (c *Client) backfillObserve(ctx context.Context, nd descriptor, lastSeenTs string, limit int, emit func(Event) bool) string {
	now := time.Now().UTC().Format(time.RFC3339)
	raw, err := c.History().Events(ctx, "",
		Between(lastSeenTs, now), Track("entity"), Limit(limit))
	if err != nil {
		return lastSeenTs
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return lastSeenTs
	}

	type folded struct {
		ev  Event
		max string
	}
	byKey := map[string]*folded{}
	order := []string{}

	for _, r := range rows {
		xid := firstStr(r, "recordXid", "record-xid")
		if xid == "" {
			continue
		}
		if _, ok := nd.records[xid]; !ok {
			continue
		}
		op := firstStr(r, "op")
		foldKey := xid + "::" + op
		channelOp := op
		if op == "change" {
			channelOp = "update"
		}
		f := byKey[foldKey]
		if f == nil {
			f = &folded{ev: Event{
				Type:      "record/" + channelOp,
				RecordXID: xid,
				Ts:        firstStr(r, "ts"),
				Txid:      firstStr(r, "txid"),
				Actor:     firstStr(r, "actorXid", "actor-xid"),
				Request:   firstStr(r, "requestId", "request-id"),
				Scope:     firstStr(r, "scopeXid", "scope-xid"),
				After:     map[string]any{},
				Raw:       map[string]any{"fromBackfill": true},
			}}
			byKey[foldKey] = f
			order = append(order, foldKey)
		}
		attrXid := firstStr(r, "attributeXid", "attribute-xid")
		if attrXid != "" && attrXid != "__delete__" {
			f.ev.After[attrXid] = r["value"]
		}
		if ts := firstStr(r, "ts"); ts > f.max {
			f.max = ts
			f.ev.Ts = ts
		}
	}

	for _, k := range order {
		f := byKey[k]
		if f.ev.Type == "record/delete" {
			f.ev.After = nil
		}
		if f.ev.Ts != "" {
			lastSeenTs = f.ev.Ts
		}
		if !emit(f.ev) {
			return lastSeenTs
		}
	}
	return lastSeenTs
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
