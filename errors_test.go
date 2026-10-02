package synthigy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestGenerateRequestID(t *testing.T) {
	a := generateRequestID()
	b := generateRequestID()
	if a == "" || a == b {
		t.Errorf("request ids should be non-empty and unique: %q %q", a, b)
	}
}
