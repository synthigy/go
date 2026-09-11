package synthigy

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// History is the temporal query surface over the audit plug (POST
// /history). Obtain it via Client.History(). All ops return HISTORY_UNAVAILABLE
// when no audit provider is configured on the server.
type HistoryAPI struct {
	c *Client
}

// History returns the temporal query accessor.
func (c *Client) History() *HistoryAPI { return &HistoryAPI{c: c} }

// post sends a {op, opts} body to /history and returns the decoded result.
func (h *HistoryAPI) post(ctx context.Context, op string, hopts map[string]any) (json.RawMessage, error) {
	data, _ := json.Marshal(map[string]any{"op": op, "opts": hopts})
	resp, err := h.c.fetchAuth(ctx, http.MethodPost, h.c.endpoint+"/history", data,
		map[string]string{"Content-Type": "application/json"}, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw := readBody(resp)
	if resp.StatusCode == http.StatusNotFound {
		return nil, newError(
			"History endpoint unavailable — no audit provider configured on the server",
			"HISTORY_UNAVAILABLE")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError(raw, resp.StatusCode, "History op '"+op+"' failed")
	}
	var out struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, newError("failed to decode history result: "+err.Error(), "INTERNAL_ERROR")
	}
	return out.Result, nil
}

// GetAt returns the state of a record at timestamp `at` as an attribute map.
// Use Tenant(...) and IncludeDeleted(...) options.
func (h *HistoryAPI) GetAt(ctx context.Context, recordXID, at string, opts ...Opt) (json.RawMessage, error) {
	o := applyOpts(opts)
	hopts := map[string]any{"record-xid": recordXID, "at": at}
	if o.tenant != nil {
		hopts["tenant"] = o.tenant
	}
	if o.includeDeleted != nil {
		hopts["include-deleted?"] = *o.includeDeleted
	}
	return h.post(ctx, "get-at", hopts)
}

// Events returns events for a record (or any record) over a time range. The
// server requires an upper time bound; when Between is omitted it defaults to
// [nil, now]. Use Track(...), Limit(...), Tenant(...). Pass the record xid via
// the recordXID arg (empty for any record).
func (h *HistoryAPI) Events(ctx context.Context, recordXID string, opts ...Opt) (json.RawMessage, error) {
	o := applyOpts(opts)
	hopts := map[string]any{"between": o.betweenOrNow()}
	if recordXID != "" {
		hopts["record-xid"] = recordXID
	}
	if o.tenant != nil {
		hopts["tenant"] = o.tenant
	}
	if o.limit != nil {
		hopts["limit"] = *o.limit
	}
	if o.track != "" {
		hopts["track"] = o.track
	}
	return h.post(ctx, "events", hopts)
}

// Diff returns the difference in a record's state between two timestamps.
func (h *HistoryAPI) Diff(ctx context.Context, recordXID, fromTs, toTs string, opts ...Opt) (json.RawMessage, error) {
	o := applyOpts(opts)
	hopts := map[string]any{"record-xid": recordXID, "from-ts": fromTs, "to-ts": toTs}
	if o.tenant != nil {
		hopts["tenant"] = o.tenant
	}
	return h.post(ctx, "diff", hopts)
}

// Timeline returns events grouped by ":request", ":actor", or ":scope" (via
// GroupBy). Between defaults to [nil, now].
func (h *HistoryAPI) Timeline(ctx context.Context, opts ...Opt) (json.RawMessage, error) {
	o := applyOpts(opts)
	hopts := map[string]any{"between": o.betweenOrNow()}
	if o.groupBy != "" {
		hopts["group-by"] = o.groupBy
	}
	if o.tenant != nil {
		hopts["tenant"] = o.tenant
	}
	if o.limit != nil {
		hopts["limit"] = *o.limit
	}
	return h.post(ctx, "timeline", hopts)
}

// Since returns events strictly after the Cursor timestamp, oldest-first.
func (h *HistoryAPI) Since(ctx context.Context, opts ...Opt) (json.RawMessage, error) {
	o := applyOpts(opts)
	hopts := map[string]any{}
	if o.cursor != nil {
		hopts["cursor"] = o.cursor
	}
	if o.tenant != nil {
		hopts["tenant"] = o.tenant
	}
	if o.limit != nil {
		hopts["limit"] = *o.limit
	}
	if o.track != "" {
		hopts["track"] = o.track
	}
	return h.post(ctx, "since", hopts)
}

// betweenOrNow returns the configured [from, to] bounds, defaulting to
// [nil, now] when unset (matching the JS SDK).
func (o callOptions) betweenOrNow() []any {
	if o.between != nil {
		return o.between
	}
	return []any{nil, time.Now().UTC().Format(time.RFC3339)}
}
