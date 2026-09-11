package synthigy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// connectStub installs a module-default client pointed at a stub /data server
// that answers every request with the given results. Returns the server and a
// restore func that disconnects.
func connectStub(t *testing.T, results []map[string]any) (*httptest.Server, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResults(w, results)
	}))
	if err := Connect(Config{Endpoint: srv.URL, Token: "test-token"}); err != nil {
		srv.Close()
		t.Fatalf("Connect: %v", err)
	}
	return srv, func() {
		Disconnect()
		srv.Close()
	}
}

// captureOp runs fn against a stub that records the first operation of the
// last /data request, and returns it.
func captureOp(t *testing.T, resp []map[string]any, fn func(c *Client)) map[string]any {
	t.Helper()
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		writeResults(w, resp)
	}))
	fn(c)
	ops, _ := gotBody["operations"].([]any)
	if len(ops) == 0 {
		t.Fatalf("no operations captured: %v", gotBody)
	}
	return ops[0].(map[string]any)
}

func TestGetFlatArgsAndNil(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResults(w, []map[string]any{{"ok": true, "data": nil}})
	}))
	rec, err := c.Get(context.Background(), "user", Args{"xid": "u-1"}, Selection{"name": nil})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec != nil {
		t.Fatalf("expected nil record for null data, got %v", rec)
	}
}

func TestGetOpShape(t *testing.T) {
	op := captureOp(t, []map[string]any{{"ok": true, "data": map[string]any{"name": "A"}}},
		func(c *Client) {
			_, _ = c.Get(context.Background(), "user", Args{"xid": "u-1"}, Selection{"name": nil})
		})
	if op["op"] != "get" || op["entity"] != "user" {
		t.Fatalf("bad get op: %v", op)
	}
	args, _ := op["args"].(map[string]any)
	if args["xid"] != "u-1" {
		t.Errorf("flat args not passed through: %v", args)
	}
}

func TestWriteVerbOpShapes(t *testing.T) {
	cases := []struct {
		name string
		want string
		call func(c *Client)
	}{
		{"sync", "sync", func(c *Client) {
			_, _ = c.Sync(context.Background(), "user", map[string]any{"xid": "u-1", "name": "A"})
		}},
		{"stack", "stack", func(c *Client) {
			_, _ = c.Stack(context.Background(), "user", map[string]any{"xid": "u-1"})
		}},
		{"delete", "delete", func(c *Client) {
			_, _ = c.Delete(context.Background(), "user", map[string]any{"xid": "u-1"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := captureOp(t, []map[string]any{{"ok": true, "data": map[string]any{"xid": "u-1"}}}, tc.call)
			if op["op"] != tc.want {
				t.Fatalf("op = %v, want %v", op["op"], tc.want)
			}
		})
	}
}

func TestDeleteDecodesBool(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResults(w, []map[string]any{{"ok": true, "data": true}})
	}))
	ok, err := c.Delete(context.Background(), "user", map[string]any{"xid": "u-1"})
	if err != nil || !ok {
		t.Fatalf("Delete = %v, %v; want true, nil", ok, err)
	}
}

func TestSliceDecodesRelationMap(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResults(w, []map[string]any{{"ok": true, "data": map[string]any{"roles": true}}})
	}))
	out, err := c.Slice(context.Background(), "user", Args{"xid": "u-1"},
		Selection{"roles": Selection{"xid": nil}})
	if err != nil {
		t.Fatalf("Slice: %v", err)
	}
	if !out["roles"] {
		t.Fatalf("expected roles=true, got %v", out)
	}
}

func TestSliceSelectionHasNoJoinSugar(t *testing.T) {
	// No op ever injects _join (flat-LEFT decree: join semantics are the
	// server's); slice pins it since an injected _join once changed what
	// slice deleted.
	op := captureOp(t, []map[string]any{{"ok": true, "data": map[string]any{"roles": true}}},
		func(c *Client) {
			_, _ = c.Slice(context.Background(), "user", Args{"xid": "u-1"},
				Selection{"roles": Selection{"xid": nil}})
		})
	sel, _ := op["selections"].(map[string]any)
	roles, _ := sel["roles"].([]any)
	if len(roles) != 1 {
		t.Fatalf("expected wire-shaped roles selection, got %v", sel)
	}
	entry := roles[0].(map[string]any)
	if _, hasArgs := entry["args"]; hasArgs {
		t.Errorf("slice selection must not carry _join args: %v", entry)
	}
}

func TestPurgeReturnsRawData(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResults(w, []map[string]any{{"ok": true, "data": []map[string]any{{"xid": "u-1"}}}})
	}))
	raw, err := c.Purge(context.Background(), "user", Args{"active": Eq(false)}, Selection{"xid": nil})
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("purge raw decode: %v (%v)", rows, err)
	}
}

func TestSearchTreeComposesForest(t *testing.T) {
	flat := []map[string]any{
		{"xid": "r", "p": nil},
		{"xid": "k", "p": map[string]any{"xid": "r"}},
	}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResults(w, []map[string]any{{"ok": true, "data": flat}})
	}))
	forest, err := c.SearchTree(context.Background(), "human", "p", nil, Selection{"xid": nil})
	if err != nil {
		t.Fatalf("SearchTree: %v", err)
	}
	if len(forest) != 1 || forest[0]["xid"] != "r" {
		t.Fatalf("expected one root 'r', got %v", forest)
	}
	kids, _ := forest[0]["_children"].([]Record)
	if len(kids) != 1 || kids[0]["xid"] != "k" {
		t.Fatalf("expected child 'k', got %v", forest[0]["_children"])
	}
}

func TestGetTreeFlatReturnsRaw(t *testing.T) {
	flat := []map[string]any{{"xid": "r"}, {"xid": "k", "p": map[string]any{"xid": "r"}}}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResults(w, []map[string]any{{"ok": true, "data": flat}})
	}))
	rows, err := c.GetTreeFlat(context.Background(), "human", "r", "p", Selection{"xid": nil})
	if err != nil {
		t.Fatalf("GetTreeFlat: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 flat rows, got %v", rows)
	}
}

// Typed decode helpers (SearchAs / GetAs) run on the module-default client.

type userRow struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

func TestSearchAsAndGetAsDecodeTyped(t *testing.T) {
	Disconnect()
	defer Disconnect()
	srv, restore := connectStub(t, []map[string]any{
		{"ok": true, "data": []map[string]any{{"name": "Alice", "active": true}}},
	})
	defer restore()
	_ = srv

	rows, err := SearchAs[userRow](context.Background(), "user", nil, Selection{"name": nil, "active": nil})
	if err != nil {
		t.Fatalf("SearchAs: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "Alice" || !rows[0].Active {
		t.Fatalf("typed decode wrong: %+v", rows)
	}
}

func TestGetAsNilOnNull(t *testing.T) {
	Disconnect()
	defer Disconnect()
	_, restore := connectStub(t, []map[string]any{{"ok": true, "data": nil}})
	defer restore()

	got, err := GetAs[userRow](context.Background(), "user", Args{"xid": "nope"}, Selection{"name": nil})
	if err != nil {
		t.Fatalf("GetAs: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil pointer for null data, got %+v", got)
	}
}

func TestDecodeSliceEmptyOnNull(t *testing.T) {
	out, err := decodeSlice[userRow](json.RawMessage("null"))
	if err != nil || out == nil || len(out) != 0 {
		t.Fatalf("decodeSlice(null) = %v, %v; want empty non-nil slice", out, err)
	}
}
