package synthigy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
)

// subStub records every /data/subscription/set body it receives.
func subStub(t *testing.T) (*Client, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/data/subscription/set" {
			body, _ := io.ReadAll(r.Body)
			var parsed map[string]any
			_ = json.Unmarshal(body, &parsed)
			mu.Lock()
			bodies = append(bodies, parsed)
			mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	return c, &bodies
}

func lastItems(t *testing.T, bodies *[]map[string]any) []any {
	t.Helper()
	if len(*bodies) == 0 {
		t.Fatal("no subscription/set POST captured")
	}
	last := (*bodies)[len(*bodies)-1]
	items, _ := last["subscriptions"].([]any)
	return items
}

// NB: Subscribe wire shape + EMPTY_RECORDS rejection are covered by
// TestSubscribeSetBody / TestSubscribeRejectsEmptyRecords in client_test.go.
// These add the rest of the subscription surface.

func TestUnsubscribeUnknownIsNoOp(t *testing.T) {
	c, bodies := subStub(t)
	if err := c.Unsubscribe(context.Background(), "no-such-key"); err != nil {
		t.Fatalf("Unsubscribe unknown: %v", err)
	}
	if len(*bodies) != 0 {
		t.Fatalf("unknown unsubscribe should not POST, got %d posts", len(*bodies))
	}
}

func TestSubscribeThenUnsubscribeEmptiesSet(t *testing.T) {
	c, bodies := subStub(t)
	key, _ := c.Subscribe(context.Background(), RecordsDesc("a-1"))
	if err := c.Unsubscribe(context.Background(), key); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if got := lastItems(t, bodies); len(got) != 0 {
		t.Fatalf("expected empty set after unsubscribe, got %v", got)
	}
}

func TestModelSubscribeVariants(t *testing.T) {
	c, bodies := subStub(t)
	if err := c.SubscribeModel(context.Background(), false); err != nil {
		t.Fatalf("SubscribeModel: %v", err)
	}
	items := lastItems(t, bodies)
	if items[0].(map[string]any)["type"] != "runtime-model" {
		t.Fatalf("expected runtime-model, got %v", items)
	}
	if err := c.SubscribeModel(context.Background(), true); err != nil {
		t.Fatalf("SubscribeModel raw: %v", err)
	}
	types := map[string]bool{}
	for _, it := range lastItems(t, bodies) {
		types[it.(map[string]any)["type"].(string)] = true
	}
	if !types["runtime-model"] || !types["deployed-model"] {
		t.Fatalf("both model types should be present: %v", types)
	}
}

func TestSetAndClearSubscriptions(t *testing.T) {
	c, bodies := subStub(t)
	err := c.SetSubscriptions(context.Background(), []Descriptor{
		RecordsDesc("r-1"),
		RecordsDesc("r-2"),
	})
	if err != nil {
		t.Fatalf("SetSubscriptions: %v", err)
	}
	if got := lastItems(t, bodies); len(got) != 2 {
		t.Fatalf("expected 2 data subs, got %v", got)
	}
	if err := c.ClearSubscriptions(context.Background()); err != nil {
		t.Fatalf("ClearSubscriptions: %v", err)
	}
	if got := lastItems(t, bodies); len(got) != 0 {
		t.Fatalf("expected empty set after clear, got %v", got)
	}
}

func TestSubscribeWithOperations(t *testing.T) {
	c, bodies := subStub(t)
	_, err := c.Subscribe(context.Background(),
		Descriptor{Records: []string{"r-1"}, Operations: []string{"update", "insert"}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	first := lastItems(t, bodies)[0].(map[string]any)
	ops, _ := first["operations"].([]any)
	if len(ops) != 2 || ops[0] != "insert" || ops[1] != "update" {
		t.Fatalf("operations not sorted/present: %v", first)
	}
}

func TestSubscriptionsStatusGet(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/data/subscription/status" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"subscriptions": []any{}})
	}))
	out, err := c.Subscriptions(context.Background())
	if err != nil {
		t.Fatalf("Subscriptions: %v", err)
	}
	if _, ok := out["subscriptions"]; !ok {
		t.Fatalf("missing subscriptions key: %v", out)
	}
}
