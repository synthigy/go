package synthigy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

// historyStub captures the last /history request body and answers with `result`.
func historyStub(t *testing.T, status int, result any) (*Client, *map[string]any) {
	t.Helper()
	var got map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/history" {
			t.Errorf("path = %s, want /history", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(status)
		if status < 300 {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
		}
	}))
	return c, &got
}

func TestHistoryGetAt(t *testing.T) {
	c, got := historyStub(t, 200, map[string]any{"name": "old"})
	raw, err := c.History().GetAt(context.Background(), "r-1", "2026-01-01T00:00:00Z",
		IncludeDeleted(true))
	if err != nil {
		t.Fatalf("GetAt: %v", err)
	}
	var state map[string]any
	_ = json.Unmarshal(raw, &state)
	if state["name"] != "old" {
		t.Fatalf("bad result: %v", state)
	}
	if (*got)["op"] != "get-at" {
		t.Fatalf("op = %v, want get-at", (*got)["op"])
	}
	opts := (*got)["opts"].(map[string]any)
	if opts["record-xid"] != "r-1" || opts["at"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("opts wrong: %v", opts)
	}
	if opts["include-deleted?"] != true {
		t.Fatalf("include-deleted? not forwarded: %v", opts)
	}
}

func TestHistoryEventsDefaultsUpperBound(t *testing.T) {
	c, got := historyStub(t, 200, []any{map[string]any{"op": "u"}})
	_, err := c.History().Events(context.Background(), "r-1", Limit(5), Track("entity"))
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	opts := (*got)["opts"].(map[string]any)
	between, ok := opts["between"].([]any)
	if !ok || len(between) != 2 {
		t.Fatalf("between should default to [nil, now]: %v", opts["between"])
	}
	if between[0] != nil || between[1] == nil {
		t.Fatalf("between bounds wrong: %v", between)
	}
	if opts["limit"].(float64) != 5 || opts["track"] != "entity" || opts["record-xid"] != "r-1" {
		t.Fatalf("opts wrong: %v", opts)
	}
}

func TestHistoryDiffAndTimelineAndSince(t *testing.T) {
	c, got := historyStub(t, 200, map[string]any{})

	_, _ = c.History().Diff(context.Background(), "r-1", "t1", "t2")
	opts := (*got)["opts"].(map[string]any)
	if (*got)["op"] != "diff" || opts["from-ts"] != "t1" || opts["to-ts"] != "t2" {
		t.Fatalf("diff opts wrong: %v / %v", (*got)["op"], opts)
	}

	_, _ = c.History().Timeline(context.Background(), GroupBy("actor"))
	opts = (*got)["opts"].(map[string]any)
	if (*got)["op"] != "timeline" || opts["group-by"] != "actor" {
		t.Fatalf("timeline opts wrong: %v", opts)
	}

	_, _ = c.History().Since(context.Background(), Cursor("c-9"), Track("record"))
	opts = (*got)["opts"].(map[string]any)
	if (*got)["op"] != "since" || opts["cursor"] != "c-9" || opts["track"] != "record" {
		t.Fatalf("since opts wrong: %v", opts)
	}
}

func TestHistoryUnavailableOn404(t *testing.T) {
	c, _ := historyStub(t, 404, nil)
	_, err := c.History().Events(context.Background(), "")
	var se *Error
	if !errors.As(err, &se) || se.Code != "HISTORY_UNAVAILABLE" {
		t.Fatalf("want HISTORY_UNAVAILABLE, got %v", err)
	}
}
