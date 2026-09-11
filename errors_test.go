package synthigy

import (
	"errors"
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
		{"SOMETHING_NEW", "internal", true}, // unknown code -> internal
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
