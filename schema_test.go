package synthigy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSchemaGetWithEntities(t *testing.T) {
	var gotPath, gotQuery string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id-key": "xid", "entities": map[string]any{},
		})
	}))
	out, err := c.Schema(context.Background(), "user", "role")
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if out["id-key"] != "xid" {
		t.Fatalf("id-key wrong: %v", out)
	}
	if gotPath != "/schema" {
		t.Errorf("path = %s", gotPath)
	}
	if !strings.Contains(gotQuery, "entities=user%2Crole") {
		t.Errorf("entities query missing/unencoded: %s", gotQuery)
	}
}

func TestSchemaFullNoQuery(t *testing.T) {
	var gotQuery string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{"id-key": "xid", "entities": map[string]any{}})
	}))
	if _, err := c.Schema(context.Background()); err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("no-arg Schema should send no query, got %q", gotQuery)
	}
}

func TestLintReturnsDiagnostics(t *testing.T) {
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/lint" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"diagnostics": []map[string]any{
				{"severity": "error", "message": "unknown attr", "from": 3, "to": 7},
			},
		})
	}))
	diags, err := c.Lint(context.Background(), "user\n  nmae\n", LintEntity("user"))
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if len(diags) != 1 || diags[0].Message != "unknown attr" || diags[0].From != 3 {
		t.Fatalf("bad diagnostics: %+v", diags)
	}
	if gotBody["entity"] != "user" || gotBody["source"] == nil {
		t.Errorf("lint body wrong: %v", gotBody)
	}
}

func TestLintEmptyDiagnosticsIsNonNilSlice(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{}) // no diagnostics key
	}))
	diags, err := c.Lint(context.Background(), "user\n  name\n")
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if diags == nil || len(diags) != 0 {
		t.Fatalf("expected empty non-nil slice, got %v", diags)
	}
}

func TestDeployedAndRuntimeModelReturnRawData(t *testing.T) {
	var lastOp string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		ops := parsed["operations"].([]any)
		lastOp = ops[0].(map[string]any)["op"].(string)
		writeResults(w, []map[string]any{{"ok": true, "data": map[string]any{"version": "1"}}})
	}))
	if _, err := c.DeployedModel(context.Background()); err != nil {
		t.Fatalf("DeployedModel: %v", err)
	}
	if lastOp != "deployed-model" {
		t.Errorf("op = %s, want deployed-model", lastOp)
	}
	if _, err := c.RuntimeModel(context.Background()); err != nil {
		t.Fatalf("RuntimeModel: %v", err)
	}
	if lastOp != "runtime-model" {
		t.Errorf("op = %s, want runtime-model", lastOp)
	}
}
