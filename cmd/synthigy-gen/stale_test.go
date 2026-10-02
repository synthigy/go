package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWarnStaleSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"datasets":{"A":"2026-09-15T09:47:15.698+02:00","B":"2026-10-01T00:00:00Z"}}`))
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "schema.json")

	write := func(datasets string) {
		if err := os.WriteFile(path, []byte(`{"entities":{},"datasets":`+datasets+`}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(endpoint string) string {
		var out bytes.Buffer
		warnStaleSchema(&out, endpoint, path)
		return out.String()
	}

	write(`{"A":{"deployed-at":"2026-09-15T07:47:15.698Z"},"B":{"deployed-at":"2026-10-01T00:00:00Z"}}`)
	if got := run(srv.URL); got != "" {
		t.Fatalf("current schema must stay silent, got %q", got)
	}
	write(`{"A":{"deployed-at":"2026-09-15T07:47:15.698Z"}}`)
	if got := run(srv.URL); !strings.Contains(got, "stale") || !strings.Contains(got, "1 datasets changed") {
		t.Fatalf("expected a stale warning, got %q", got)
	}
	if got := run(""); got != "" {
		t.Fatalf("no endpoint must stay silent, got %q", got)
	}
}
