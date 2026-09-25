package synthigy

import (
	"context"
	"encoding/json"
	"errors"
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

func TestCompileReturnsWireOp(t *testing.T) {
	var gotBody map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compile" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"ok": true,
				"operation": map[string]any{
					"op": "search", "entity": "movie",
					"args":       map[string]any{"_limit": 2},
					"selections": map[string]any{"title": nil},
				},
			}},
		})
	}))
	op, err := c.Compile(context.Background(), "movie (limit ?n:int=5)\n  title\n",
		map[string]any{"n": 2})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if op["op"] != "search" || op["entity"] != "movie" {
		t.Fatalf("bad wire op: %+v", op)
	}
	ops, _ := gotBody["operations"].([]any)
	if len(ops) != 1 {
		t.Fatalf("expected one operation in the body, got %v", gotBody)
	}
	sent, _ := ops[0].(map[string]any)
	if sent["op"] != "xsql" {
		t.Errorf("compile must send the xsql DOCUMENT op, got %v", sent["op"])
	}
	if doc, _ := sent["xsql"].(string); !strings.HasPrefix(doc, "@search _q\n") {
		t.Errorf("xsql = %q, want @search _q header", doc)
	}
}

func TestCompileSurfacesPerOpError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"ok": false,
				"error": map[string]any{
					"message": "XSQL parse error: unexpected character",
					"code":    "XSQL_PARSE_ERROR",
				},
			}},
		})
	}))
	_, err := c.Compile(context.Background(), "movie (\n", nil)
	if err == nil {
		t.Fatal("expected an error for a failed compile")
	}
	var se *Error
	if !errors.As(err, &se) || se.Code != "XSQL_PARSE_ERROR" {
		t.Fatalf("want XSQL_PARSE_ERROR, got %v", err)
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
