package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPullNamesWhereTheDescribeFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/schema":
			w.Write([]byte(`{}`))
		case "/data":
			w.Write([]byte(`{"results":[{"ok":false,"error":{"code":"XSQL_PARSE_ERROR","message":"bad token",` +
				`"line":3,"col":5,"hint":"close the brace","doc":"errors/xsql_parse"}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("SYNTHIGY_CLIENT_ID", "")
	t.Setenv("SYNTHIGY_TOKEN", "t")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ops.xsql"), []byte("@search movies\nMovie { title }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := doPull(srv.URL, dir, filepath.Join(dir, "schema.json"), filepath.Join(dir, "ops.ir.json"))
	want := "describe failed: XSQL_PARSE_ERROR: bad token\n" +
		"  at line 3, col 5\n" +
		"  hint: close the brace\n" +
		"  docs: " + srv.URL + "/docs/errors/xsql_parse"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v\nwant %s", err, want)
	}
}
