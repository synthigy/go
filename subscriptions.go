package synthigy

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
)

// Descriptor identifies which records to subscribe to. Records is required
// and non-empty; Operations optionally narrows to a vocab subset (e.g.
// "update", "link").
type Descriptor struct {
	Records    []string
	Operations []string
}

// RecordsDesc is a shorthand for a record-only Descriptor.
func RecordsDesc(xids ...string) Descriptor { return Descriptor{Records: xids} }

// descriptor is the internal set-based form.
type descriptor struct {
	records    map[string]struct{}
	operations map[string]struct{} // nil if unspecified
}

func normalizeDescriptor(d Descriptor) (descriptor, error) {
	if len(d.Records) == 0 {
		return descriptor{}, newError(
			"records must be a non-empty array of xid strings", "EMPTY_RECORDS")
	}
	out := descriptor{records: map[string]struct{}{}}
	for _, r := range d.Records {
		out.records[r] = struct{}{}
	}
	if d.Operations != nil {
		out.operations = map[string]struct{}{}
		for _, o := range d.Operations {
			out.operations[o] = struct{}{}
		}
	}
	return out, nil
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// descriptorKey is a stable hash of a descriptor (sorted records + sorted
// operations, JSON-encoded), used as the local-mirror key when the caller
// doesn't supply one. Re-subscribing with the same records+operations is
// idempotent.
func descriptorKey(d descriptor) string {
	payload := struct {
		Records    []string `json:"records"`
		Operations []string `json:"operations"`
	}{Records: sortedKeys(d.records)}
	if d.operations != nil {
		payload.Operations = sortedKeys(d.operations)
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

// buildSubscriptionsBody composes the wire subscriptions array from local
// state. Caller holds subMu.
func (c *Client) buildSubscriptionsBody() map[string]any {
	items := []map[string]any{}
	// Deterministic order over data subs for stable requests.
	keys := make([]string, 0, len(c.dataSubs))
	for k := range c.dataSubs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d := c.dataSubs[k]
		item := map[string]any{"type": "data", "records": sortedKeys(d.records)}
		if d.operations != nil {
			item["operations"] = sortedKeys(d.operations)
		}
		items = append(items, item)
	}
	if len(c.entitySubs) > 0 {
		items = append(items, map[string]any{"type": "entity", "entities": sortedKeys(c.entitySubs)})
	}
	if len(c.relationSubs) > 0 {
		items = append(items, map[string]any{"type": "relation", "relations": sortedKeys(c.relationSubs)})
	}
	for _, t := range sortedKeys(c.modelSubs) {
		items = append(items, map[string]any{"type": t})
	}
	return map[string]any{"subscriptions": items}
}

// flushSubscriptions POSTs the current local subscription state to the
// server (full set-replace).
func (c *Client) flushSubscriptions(ctx context.Context) error {
	c.subMu.Lock()
	body := c.buildSubscriptionsBody()
	c.subMu.Unlock()

	data, _ := json.Marshal(body)
	resp, err := c.fetchAuth(ctx, http.MethodPost, c.endpoint+"/data/subscription/set", data,
		map[string]string{"Content-Type": "application/json"}, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw := readBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpError(raw, resp.StatusCode, "Subscription set failed")
	}
	return nil
}

// Subscribe adds a record descriptor to the subscription set and POSTs the
// full set. Returns the local handle (the SubKey option, or the descriptor's
// stable hash) for later Unsubscribe.
func (c *Client) Subscribe(ctx context.Context, d Descriptor, opts ...Opt) (string, error) {
	o := applyOpts(opts)
	nd, err := normalizeDescriptor(d)
	if err != nil {
		return "", err
	}
	key := o.key
	if key == "" {
		key = descriptorKey(nd)
	}
	c.subMu.Lock()
	c.dataSubs[key] = nd
	c.subMu.Unlock()
	return key, c.flushSubscriptions(ctx)
}

// Unsubscribe removes a data subscription by its handle (the key returned
// from Subscribe). Unknown handles are a no-op.
func (c *Client) Unsubscribe(ctx context.Context, handle string) error {
	c.subMu.Lock()
	_, existed := c.dataSubs[handle]
	if existed {
		delete(c.dataSubs, handle)
	}
	c.subMu.Unlock()
	if !existed {
		return nil
	}
	return c.flushSubscriptions(ctx)
}

// SubscribeModel subscribes to model deploy notifications. raw=false (default)
// watches the runtime model; raw=true watches the raw deployed model.
func (c *Client) SubscribeModel(ctx context.Context, raw bool) error {
	t := "runtime-model"
	if raw {
		t = "deployed-model"
	}
	c.subMu.Lock()
	c.modelSubs[t] = struct{}{}
	c.subMu.Unlock()
	return c.flushSubscriptions(ctx)
}

// UnsubscribeModel removes a model subscription.
func (c *Client) UnsubscribeModel(ctx context.Context, raw bool) error {
	t := "runtime-model"
	if raw {
		t = "deployed-model"
	}
	c.subMu.Lock()
	_, existed := c.modelSubs[t]
	delete(c.modelSubs, t)
	c.subMu.Unlock()
	if !existed {
		return nil
	}
	return c.flushSubscriptions(ctx)
}

// SetSubscriptions replaces the entire subscription set in one call. Each
// Descriptor must carry non-empty Records; use SubKey on the descriptor via
// the keys slice (parallel to items) for stable handles, or leave nil to use
// hashes.
func (c *Client) SetSubscriptions(ctx context.Context, items []Descriptor, opts ...Opt) error {
	normalized := make(map[string]descriptor, len(items))
	for _, it := range items {
		nd, err := normalizeDescriptor(it)
		if err != nil {
			return err
		}
		normalized[descriptorKey(nd)] = nd
	}
	c.subMu.Lock()
	c.dataSubs = normalized
	c.entitySubs = map[string]struct{}{}
	c.relationSubs = map[string]struct{}{}
	c.modelSubs = map[string]struct{}{}
	c.subMu.Unlock()
	return c.flushSubscriptions(ctx)
}

// ClearSubscriptions removes all subscriptions for this session.
func (c *Client) ClearSubscriptions(ctx context.Context) error {
	c.subMu.Lock()
	c.dataSubs = map[string]descriptor{}
	c.entitySubs = map[string]struct{}{}
	c.relationSubs = map[string]struct{}{}
	c.modelSubs = map[string]struct{}{}
	c.subMu.Unlock()
	return c.flushSubscriptions(ctx)
}

// Subscriptions fetches the current subscription status from the server.
func (c *Client) Subscriptions(ctx context.Context) (map[string]any, error) {
	raw, err := c.getJSON(ctx, c.endpoint+"/data/subscription/status")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, newError("failed to decode subscription status: "+err.Error(), "INTERNAL_ERROR")
	}
	return out, nil
}

// setSubscriptionsItems replaces the local mirror from a list of wire items
// (mixed tracks: data / entity / relation / runtime-model) and flushes. Used
// by the watch multiplexer, which computes a consolidated union. Mirrors the
// JS setSubscriptions(items) item parsing.
func (c *Client) setSubscriptionsItems(ctx context.Context, items []map[string]any) error {
	c.subMu.Lock()
	c.dataSubs = map[string]descriptor{}
	c.entitySubs = map[string]struct{}{}
	c.relationSubs = map[string]struct{}{}
	c.modelSubs = map[string]struct{}{}
	for _, it := range items {
		t, _ := it["type"].(string)
		if t == "" {
			t = "data"
		}
		switch t {
		case "data":
			d := Descriptor{Records: anyToStrings(it["records"])}
			if ops, ok := it["operations"]; ok {
				d.Operations = anyToStrings(ops)
			}
			nd, err := normalizeDescriptor(d)
			if err != nil {
				c.subMu.Unlock()
				return err
			}
			c.dataSubs[descriptorKey(nd)] = nd
		case "entity":
			for _, e := range anyToStrings(it["entities"]) {
				c.entitySubs[e] = struct{}{}
			}
		case "relation":
			for _, r := range anyToStrings(it["relations"]) {
				c.relationSubs[r] = struct{}{}
			}
		case "deployed-model", "runtime-model":
			c.modelSubs[t] = struct{}{}
		}
	}
	c.subMu.Unlock()
	return c.flushSubscriptions(ctx)
}

func anyToStrings(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, x := range s {
			if str, ok := x.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// addDataSub / removeDataSub are internal helpers used by Observe to manage
// the local mirror around each SSE session.
func (c *Client) addDataSub(key string, d descriptor) {
	c.subMu.Lock()
	c.dataSubs[key] = d
	c.subMu.Unlock()
}

func (c *Client) removeDataSub(key string) bool {
	c.subMu.Lock()
	_, existed := c.dataSubs[key]
	delete(c.dataSubs, key)
	c.subMu.Unlock()
	return existed
}
