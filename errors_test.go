package synthigy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func TestErrorCategoryAndRetryable(t *testing.T) {
	cases := []struct {
		code      string
		category  string
		retryable bool
	}{
		{"UNAUTHORIZED", "auth", false},
		{"FORBIDDEN", "iam", false},
		{"UNKNOWN_OPERATOR", "validation", false},
		{"UNKNOWN_ENTITY", "not_found", false},
		{"UNIQUE_VIOLATION", "conflict", false},
		{"TIMEOUT", "rate_limit", true},
		{"NETWORK_ERROR", "network", true},
		{"INTERNAL_ERROR", "internal", true},
		{"SOMETHING_NEW", "internal", false}, // unknown code -> internal, not retryable
		{"RELATION_FORBIDDEN", "iam", false},
		{"ATTRIBUTE_FORBIDDEN", "iam", false},
		{"ROW_FORBIDDEN", "iam", false},
		{"CREATE_FORBIDDEN", "iam", false},
		{"DELETE_FORBIDDEN", "iam", false},
		{"SLOT_OCCUPIED", "iam", false},
		{"INVALID_CLIENT", "auth", false},
		// Regression: these two are terminal, NOT retryable. They previously
		// fell through to internal (retryable=true), so a retry-on-Retryable
		// loop would spin forever on a missing audit provider / no client.
		// Must match the Python SDK, which had them right.
		{"HISTORY_UNAVAILABLE", "not_found", false},
		{"NOT_CONNECTED", "validation", false},
	}
	for _, c := range cases {
		e := newError("msg", c.code)
		if e.Category != c.category {
			t.Errorf("%s: category = %q, want %q", c.code, e.Category, c.category)
		}
		if e.Retryable != c.retryable {
			t.Errorf("%s: retryable = %v, want %v", c.code, e.Retryable, c.retryable)
		}
	}
}

func TestServerRetryableFlagWins(t *testing.T) {
	yes, no := true, false
	if e := errorFromServer(&serverError{Code: "DB_UNAVAILABLE", Retryable: &yes}, 200, ""); !e.Retryable {
		t.Error("server-marked unknown code must be retryable")
	}
	if e := errorFromServer(&serverError{Code: "DB_UNAVAILABLE"}, 200, ""); e.Retryable {
		t.Error("unmarked unknown code must not be retryable")
	}
	if e := errorFromServer(&serverError{Code: "INTERNAL_ERROR", Retryable: &no}, 500, ""); e.Retryable {
		t.Error("server retryable=false must override the category")
	}
}

func TestTokenRefusalIsTyped(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   string
		msg    string
	}{
		{401, `{"error":"invalid_client","error_description":"Client authentication failed"}`, "INVALID_CLIENT",
			"Token request refused (401): invalid_client — Client authentication failed"},
		{400, `{"error":"invalid_scope"}`, "INVALID_SCOPE", "Token request refused (400): invalid_scope"},
		{400, `{"error":"invalid_target","error_description":"Client is not authorized for audience x"}`, "INVALID_AUDIENCE",
			"Token request refused (400): invalid_target — Client is not authorized for audience x"},
		{400, `{"error":"unauthorized_client"}`, "UNAUTHORIZED", "Token request refused (400): unauthorized_client"},
		{502, `Bad Gateway`, "UNAUTHORIZED", "Token request failed (502): Bad Gateway"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		}))
		cl, err := New(Config{Endpoint: srv.URL, ClientID: "id", ClientSecret: "wrong"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		_, err = cl.Search(context.Background(), "user", nil, Selection{"x": nil})
		srv.Close()
		var se *Error
		if !errors.As(err, &se) {
			t.Fatalf("%s: expected *Error, got %v", c.code, err)
		}
		if se.Code != c.code || se.Category != "auth" || se.Retryable || se.Status != c.status || se.Message != c.msg {
			t.Errorf("%s: got code=%s category=%s retryable=%v status=%d msg=%q",
				c.code, se.Code, se.Category, se.Retryable, se.Status, se.Message)
		}
		if (se.Hint != "") != (c.code == "INVALID_CLIENT") {
			t.Errorf("%s: hint = %q", c.code, se.Hint)
		}
	}
}

func TestErrorAs(t *testing.T) {
	var err error = newError("nope", "FORBIDDEN")
	var se *Error
	if !errors.As(err, &se) {
		t.Fatal("errors.As failed to match *Error")
	}
	if se.Code != "FORBIDDEN" {
		t.Errorf("code = %q, want FORBIDDEN", se.Code)
	}
}

func TestErrorFromServerExtras(t *testing.T) {
	se := &serverError{
		Message:   "bad operator",
		Code:      "UNKNOWN_OPERATOR",
		Hint:      "did you mean _eq?",
		Available: []string{"_eq", "_neq"},
		Operator:  "_equals",
	}
	e := errorFromServer(se, 400, "req-123")
	if e.Hint != "did you mean _eq?" || e.Operator != "_equals" {
		t.Errorf("extras not carried: %+v", e)
	}
	if len(e.Available) != 2 || e.RequestID != "req-123" || e.Status != 400 {
		t.Errorf("metadata not carried: %+v", e)
	}
	if e.Category != "validation" {
		t.Errorf("category = %q, want validation", e.Category)
	}
}

var stampError = map[string]any{
	"message": "'modified_by' is set by the system on Movie", "code": "ATTRIBUTE_FORBIDDEN",
	"hint": "leave it out of data", "rule": "stamp", "doc": "errors/stamp",
	"entity": "Movie", "attribute": "modified_by",
}

func keepsStamp(t *testing.T, where string, err error) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("%s: expected *Error, got %v", where, err)
	}
	got := []string{e.Code, e.Hint, e.Rule, e.Doc, e.Entity, e.Attribute}
	want := []string{"ATTRIBUTE_FORBIDDEN", "leave it out of data", "stamp", "errors/stamp", "Movie", "modified_by"}
	if !slices.Equal(got, want) {
		t.Errorf("%s: got %q, want %q", where, got, want)
	}
}

// answering returns a client whose server answers every request with status and body.
func answering(t *testing.T, status int, body any) *Client {
	t.Helper()
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	return c
}

func TestErrorKeepsDocRuleAttribute(t *testing.T) {
	top := map[string]any{"ok": false, "error": stampError}
	for _, c := range []struct {
		where  string
		status int
		body   any
	}{
		{"per-op", 200, map[string]any{"results": []any{map[string]any{"ok": false, "error": stampError}}}},
		{"top-level", 200, top},
		{"top-level 400", 400, top},
		{"top-level 403", 403, top},
	} {
		_, err := answering(t, c.status, c.body).Sync(context.Background(), "movie", map[string]any{"modified_by": "x"})
		keepsStamp(t, c.where, err)
	}
}

func TestErrorWithoutDocRuleAttributeLeavesThemZeroAndOmitted(t *testing.T) {
	notFound := map[string]any{"message": "Not found", "code": "NOT_FOUND"}
	_, err := answering(t, 200, map[string]any{"results": []any{map[string]any{"ok": false, "error": notFound}}}).
		Get(context.Background(), "movie", Args{"xid": "x"}, Selection{"title": nil})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("expected *Error, got %v", err)
	}
	if e.Hint != "" || e.Rule != "" || e.Doc != "" || e.Attribute != "" {
		t.Errorf("absent fields set: %+v", e)
	}
	raw, _ := json.Marshal(e)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"hint", "rule", "doc", "attribute"} {
		if _, has := m[k]; has {
			t.Errorf("%s in JSON: %s", k, raw)
		}
	}
}

func TestEveryEndpointKeepsDocRuleAttribute(t *testing.T) {
	ctx := context.Background()
	refused := map[string]any{"error": stampError}
	perOp := map[string]any{"results": []any{map[string]any{"ok": false, "error": stampError}}}
	query := func(c *Client) (OpResult, error) {
		rs, err := c.Exec(ctx, []Op{OpQuery("movie\n  title\n", nil, "")})
		if err != nil {
			return OpResult{}, err
		}
		return rs[0], nil
	}
	for _, c := range []struct {
		where  string
		status int
		body   any
		call   func(*Client) error
	}{
		{"schema", 400, refused, func(c *Client) error { _, err := c.Schema(ctx); return err }},
		{"lint", 400, refused, func(c *Client) error { _, err := c.Lint(ctx, "movie\n  title\n"); return err }},
		{"compile", 400, refused, func(c *Client) error { _, err := c.Compile(ctx, "movie\n  title\n", nil); return err }},
		{"compile per-op", 200, perOp, func(c *Client) error { _, err := c.Compile(ctx, "movie\n  title\n", nil); return err }},
		{"history", 400, refused, func(c *Client) error { _, err := c.History().Events(ctx, ""); return err }},
		{"subscription set", 400, refused, func(c *Client) error { return c.ClearSubscriptions(ctx) }},
		{"subscription status", 400, refused, func(c *Client) error { _, err := c.Subscriptions(ctx); return err }},
		{"files", 400, refused, func(c *Client) error { _, err := c.Files().Info(ctx, "/site/logo.png"); return err }},
		{"deploy", 200, perOp, func(c *Client) error { _, err := c.Deploy(ctx, "{}"); return err }},
		{"ResultAs", 200, perOp, func(c *Client) error {
			r, err := query(c)
			if err != nil {
				return err
			}
			_, err = ResultAs[map[string]any](r)
			return err
		}},
		{"ResultOneAs", 200, perOp, func(c *Client) error {
			r, err := query(c)
			if err != nil {
				return err
			}
			_, err = ResultOneAs[map[string]any](r)
			return err
		}},
	} {
		keepsStamp(t, c.where, c.call(answering(t, c.status, c.body)))
	}

	forbidden := map[string]any{}
	for k, v := range stampError {
		forbidden[k] = v
	}
	forbidden["code"] = "FORBIDDEN"
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s := answering(t, 403, map[string]any{"error": forbidden}).Listen(lctx)
	for range s.Events() {
	}
	var e *Error
	if !errors.As(s.Err(), &e) || e.Code != "FORBIDDEN" || e.Status != 403 ||
		e.Rule != "stamp" || e.Doc != "errors/stamp" || e.Attribute != "modified_by" {
		t.Errorf("listen: got %+v", s.Err())
	}
}

func TestGenerateRequestID(t *testing.T) {
	a := generateRequestID()
	b := generateRequestID()
	if a == "" || a == b {
		t.Errorf("request ids should be non-empty and unique: %q %q", a, b)
	}
}
