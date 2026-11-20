package synthigy

import (
	"context"
	"encoding/json"
	"errors"
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

// captureBody runs fn against a stub that records the last /data request body.
// captureOp returns the first operation of the request captureBody records.
func captureOp(t *testing.T, resp []map[string]any, fn func(c *Client)) map[string]any {
	t.Helper()
	gotBody := captureBody(t, resp, fn)
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

func TestDecodeCutsNullIsEmptyAndBadIsAnError(t *testing.T) {
	for _, in := range []string{"", "null"} {
		if m, err := decodeCuts(json.RawMessage(in)); err != nil || m == nil || len(m) != 0 {
			t.Errorf("decodeCuts(%q) = %v, %v; want empty map", in, m, err)
		}
	}
	if m, err := decodeCuts(json.RawMessage(`{"actors":true,"genres":false}`)); err != nil || !m["actors"] || m["genres"] {
		t.Errorf("decodeCuts map = %v, %v", m, err)
	}
	if _, err := decodeCuts(json.RawMessage(`[1]`)); err == nil {
		t.Error("decodeCuts of a list must fail")
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

func TestDeployPostsExportContentsVerbatim(t *testing.T) {
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeResults(w, []map[string]any{{"ok": true, "data": map[string]any{"deployed": true, "version": "0.3", "dataset": "ds-1"}}})
	}))
	ack, err := c.Deploy(context.Background(), `{"~:xid":"v-1"}`)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if ack != (DeployAck{Deployed: true, Version: "0.3", Dataset: "ds-1"}) {
		t.Fatalf("Deploy ack = %+v", ack)
	}
	ops := gotBody["operations"].([]any)
	op := ops[0].(map[string]any)
	if op["op"] != "deploy" || op["data"] != `{"~:xid":"v-1"}` {
		t.Fatalf("wire op = %+v", op)
	}
	if _, hasEntity := op["entity"]; hasEntity {
		t.Fatalf("deploy must not carry an entity")
	}
}

func TestDestroyIsDeleteOnDatasetByXid(t *testing.T) {
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeResults(w, []map[string]any{{"ok": true, "data": true}})
	}))
	ok, err := c.Destroy(context.Background(), "ds-1")
	if err != nil || !ok {
		t.Fatalf("Destroy = %v, %v; want true, nil", ok, err)
	}
	ops := gotBody["operations"].([]any)
	op := ops[0].(map[string]any)
	if op["op"] != "delete" || op["entity"] != "dataset" {
		t.Fatalf("wire op = %+v", op)
	}
	data := op["data"].(map[string]any)
	if data["xid"] != "ds-1" {
		t.Fatalf("data = %+v", data)
	}
}

func TestDestroyAnswersTrueWhateverDataTheEngineSends(t *testing.T) {
	for _, res := range []map[string]any{
		{"ok": true, "data": true},
		{"ok": true, "data": nil},
		{"ok": true},
	} {
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeResults(w, []map[string]any{res})
		}))
		if ok, err := c.Destroy(context.Background(), "ds-1"); err != nil || !ok {
			t.Fatalf("%v: Destroy = %v, %v; want true, nil", res, ok, err)
		}
	}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResults(w, []map[string]any{{"ok": false, "error": map[string]any{"message": "no", "code": "FORBIDDEN"}}})
	}))
	if ok, err := c.Destroy(context.Background(), "ds-1"); ok || err == nil {
		t.Fatalf("refused Destroy = %v, %v; want false, an error", ok, err)
	}
}

// Bulk import: one operation carries every record, silent by default.
func TestSyncManySendsOneOpWithAllRecords(t *testing.T) {
	var ops []map[string]any
	returning := false
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Operations []map[string]any `json:"operations"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ops = body.Operations
		if returning {
			writeResults(w, []map[string]any{{"ok": true, "data": []map[string]any{{"xid": "a"}, {"xid": "b"}}}})
			return
		}
		writeResults(w, []map[string]any{{"ok": true, "data": map[string]any{"count": 2}}})
	}))
	ctx := context.Background()
	res, err := c.SyncMany(ctx, "movie", []map[string]any{{"title": "A"}, {"title": "B"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0]["op"] != "sync" || len(ops[0]["data"].([]any)) != 2 || ops[0]["returning"] != false {
		t.Fatalf("wire ops = %v", ops)
	}
	if res.Count != 2 || res.Records != nil {
		t.Fatalf("silent result = %+v", res)
	}

	returning = true
	res, err = c.StackMany(ctx, "movie", []map[string]any{{"xid": "a"}, {"xid": "b"}}, Returning())
	if err != nil {
		t.Fatal(err)
	}
	if ops[0]["op"] != "stack" || ops[0]["returning"] != true {
		t.Fatalf("wire ops = %v", ops)
	}
	if res.Count != 2 || len(res.Records) != 2 || res.Records[1]["xid"] != "b" {
		t.Fatalf("returning result = %+v", res)
	}
}

func captureBody(t *testing.T, resp []map[string]any, fn func(c *Client)) map[string]any {
	t.Helper()
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		writeResults(w, resp)
	}))
	fn(c)
	return gotBody
}

func TestClaimSendsArgsSelectionAndActingAs(t *testing.T) {
	var rows []Record
	body := captureBody(t, []map[string]any{{"ok": true, "data": []map[string]any{{"xid": "j-1"}}}}, func(c *Client) {
		var err error
		rows, err = c.Claim(context.Background(), "Job", Args{"status": Eq("open"), "_limit": 2}, Selection{"xid": nil},
			ActingAs("u-1"), KeyFormat("camel"))
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
	})
	if body["acting_as"] != "u-1" || body["key_format"] != "camel" {
		t.Errorf("acting_as/key_format not forwarded: %v", body)
	}
	op := body["operations"].([]any)[0].(map[string]any)
	if op["op"] != "claim" || op["entity"] != "Job" || op["args"].(map[string]any)["_limit"] != float64(2) {
		t.Errorf("claim op = %v", op)
	}
	if len(rows) != 1 || rows[0]["xid"] != "j-1" {
		t.Errorf("rows = %v", rows)
	}
}

func TestRenewReclaimsXidsAndSkipsNone(t *testing.T) {
	body := captureBody(t, []map[string]any{{"ok": true, "data": []map[string]any{{"xid": "j-1"}}}}, func(c *Client) {
		if _, err := c.Renew(context.Background(), "Job", []string{"j-1", "j-2"}, nil, ActingAs("u-1")); err != nil {
			t.Fatalf("Renew: %v", err)
		}
	})
	op := body["operations"].([]any)[0].(map[string]any)
	args := op["args"].(map[string]any)
	in := args["xid"].(map[string]any)["_in"].([]any)
	if body["acting_as"] != "u-1" || op["op"] != "claim" || args["_limit"] != float64(2) || len(in) != 2 {
		t.Errorf("renew op = %v", body)
	}
	if _, ok := op["selections"].(map[string]any)["xid"]; !ok {
		t.Errorf("renew selects xid by default: %v", op)
	}
	calls := 0
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	rows, err := c.Renew(context.Background(), "Job", nil, nil)
	if err != nil || len(rows) != 0 || calls != 0 {
		t.Errorf("empty renew: rows=%v err=%v calls=%d", rows, err, calls)
	}
}

func TestReleaseCountsAndSurfacesRecordLocked(t *testing.T) {
	var n int
	body := captureBody(t, []map[string]any{{"ok": true, "data": 1}}, func(c *Client) {
		var err error
		if n, err = c.Release(context.Background(), "Job", []string{"j-1"}, ActingAs("u-1")); err != nil {
			t.Fatalf("Release: %v", err)
		}
	})
	op := body["operations"].([]any)[0].(map[string]any)
	if n != 1 || body["acting_as"] != "u-1" || op["op"] != "release" || len(op["data"].([]any)) != 1 {
		t.Errorf("release: n=%d body=%v", n, body)
	}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResults(w, []map[string]any{{"ok": false, "error": map[string]any{"code": "RECORD_LOCKED", "message": "held"}}})
	}))
	_, err := c.Release(context.Background(), "Job", []string{"j-1"})
	var e *Error
	if !errors.As(err, &e) || e.Code != "RECORD_LOCKED" {
		t.Errorf("want RECORD_LOCKED, got %v", err)
	}
}
