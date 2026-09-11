package synthigy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGlobalDelegatesRouteToDefaultClient proves the module-level verbs
// delegate to the Connect-installed default client and thread the op through
// unchanged. A thin layer, but a whole one — one table covers it.
func TestGlobalDelegatesRouteToDefaultClient(t *testing.T) {
	Disconnect()
	defer Disconnect()

	var lastOp map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		var data any = map[string]any{"xid": "x"}
		if ops, _ := parsed["operations"].([]any); len(ops) > 0 {
			lastOp = ops[0].(map[string]any)
			switch lastOp["op"] {
			case "search":
				data = []map[string]any{{"xid": "x"}}
			case "delete":
				data = true
			}
		}
		writeResults(w, []map[string]any{{"ok": true, "data": data}})
	}))
	defer srv.Close()
	if err := Connect(Config{Endpoint: srv.URL, Token: "t"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	ctx := context.Background()
	cases := []struct {
		name   string
		wantOp string
		run    func() error
	}{
		{"Search", "search", func() error {
			_, err := Search(ctx, "user", nil, Selection{"xid": nil})
			return err
		}},
		{"Get", "get", func() error {
			_, err := Get(ctx, "user", Args{"xid": "x"}, Selection{"xid": nil})
			return err
		}},
		{"Sync", "sync", func() error {
			_, err := Sync(ctx, "user", map[string]any{"xid": "x"})
			return err
		}},
		{"Stack", "stack", func() error {
			_, err := Stack(ctx, "user", map[string]any{"xid": "x"})
			return err
		}},
		{"Delete", "delete", func() error {
			ok, err := Delete(ctx, "user", map[string]any{"xid": "x"})
			if err == nil && !ok {
				t.Error("Delete decoded false from a true response")
			}
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lastOp = nil
			if err := tc.run(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if lastOp == nil || lastOp["op"] != tc.wantOp {
				t.Fatalf("%s routed op = %v, want %v", tc.name, lastOp, tc.wantOp)
			}
		})
	}
}

func TestGlobalExecBatchesOps(t *testing.T) {
	Disconnect()
	defer Disconnect()
	var gotOps []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		gotOps, _ = parsed["operations"].([]any)
		writeResults(w, []map[string]any{
			{"ok": true, "data": map[string]any{"xid": "a"}},
			{"ok": true, "data": true},
		})
	}))
	defer srv.Close()
	if err := Connect(Config{Endpoint: srv.URL, Token: "t"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	results, err := Exec(context.Background(), []Op{
		OpStack("user", map[string]any{"xid": "a"}),
		OpDelete("user", map[string]any{"xid": "b"}),
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(gotOps) != 2 {
		t.Fatalf("expected 2 ops in one request, got %d", len(gotOps))
	}
	if len(results) != 2 || !results[0].OK {
		t.Fatalf("unexpected results: %+v", results)
	}
}
