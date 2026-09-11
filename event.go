package synthigy

import "encoding/json"

// Event is a change-notification envelope delivered over SSE (the records-only
// channel envelope). Record events carry Before/After attribute maps (keyed by
// attribute name); relation events carry a Data tuple [subscribed-xid,
// other-xid]. Provenance fields (Actor/Txid/Request/Scope/Tenant) are xids.
//
// Raw holds the full decoded payload for forward-compatibility — read fields
// the struct doesn't model directly from there.
type Event struct {
	Type      string         `json:"type"`
	RecordXID string         `json:"record-xid"`
	Before    map[string]any `json:"before"`
	After     map[string]any `json:"after"`
	Data      []string       `json:"data"`
	Actor     string         `json:"actor"`
	Txid      string         `json:"txid"`
	Request   string         `json:"request"`
	Scope     string         `json:"scope"`
	Tenant    any            `json:"tenant"`
	Ts        string         `json:"ts"`

	// Raw is the full decoded envelope.
	Raw map[string]any `json:"-"`

	// sseEvent / sseID are SSE frame metadata, not part of the payload.
	sseEvent string
	sseID    string
}

// parseEvent decodes an SSE data payload into an Event, retaining the raw map.
func parseEvent(payload []byte) (Event, error) {
	var ev Event
	if err := json.Unmarshal(payload, &ev); err != nil {
		return Event{}, err
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err == nil {
		ev.Raw = raw
	}
	return ev, nil
}

// matchesDescriptor reports whether a channel event matches the descriptor.
// Record events match by record-xid; relation events match on Data[0] (the
// server rotates so position 0 is the subscribed-perspective endpoint).
func (ev Event) matchesDescriptor(d descriptor) bool {
	if len(d.records) == 0 {
		return false
	}
	if ev.RecordXID != "" {
		_, ok := d.records[ev.RecordXID]
		return ok
	}
	if len(ev.Data) >= 2 {
		_, ok := d.records[ev.Data[0]]
		return ok
	}
	return false
}
