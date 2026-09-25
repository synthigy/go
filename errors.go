package synthigy

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// errorCategories maps a stable error code to its category. Codes not in the
// table fall back to "internal". Categories are stable enums consumers can
// branch on:
//
//   - auth        — token / session / IdP issues. Re-authenticate.
//   - iam         — RBAC/RLS denial. Caller lacks permission.
//   - validation  — caller's request shape is invalid.
//   - not_found   — referenced entity/relation/record doesn't exist.
//   - conflict    — constraint violation, optimistic-concurrency, etc.
//   - rate_limit  — too many requests.
//   - network     — transport failure (DNS, TCP, timeout, abort).
//   - internal    — unexpected server error. Retry-safe; check logs.
var errorCategories = map[string]string{
	// auth
	"UNAUTHORIZED":            "auth",
	"CLIENT_NOT_FOUND":        "auth",
	"CLIENT_INACTIVE":         "auth",
	"PUBLIC_CLIENT_FORBIDDEN": "auth",
	"NOT_TRUSTED":             "auth",
	"USER_NOT_FOUND":          "auth",
	"USER_INACTIVE":           "auth",
	"PROVISION_FORBIDDEN":     "auth",
	"CLAIM_INVALID":           "auth",
	"NO_LOGIN_STORE":          "auth",
	"LOGIN_NONCE_MISMATCH":    "auth",
	"LOGIN_EXCHANGE_FAILED":   "auth",
	"NO_TOKEN":                "auth",       // client-side: no token source configured — retrying can't fix it
	"NO_ENDPOINT":             "validation", // client-side: no endpoint configured — retrying can't fix it
	// iam
	"FORBIDDEN":             "iam",
	"FORBIDDEN_OP":          "iam",
	"ENTITY_FORBIDDEN":      "iam",
	"ENTITY_NOT_READABLE":   "iam",
	"RELATION_NOT_READABLE": "iam",
	// validation
	"INVALID_BODY":                       "validation",
	"NO_OPERATIONS":                      "validation",
	"UNKNOWN_OP":                         "validation",
	"UNKNOWN_OPERATOR":                   "validation",
	"MISSING_ON":                         "validation",
	"MISSING_ROOT":                       "validation",
	"MISSING_RECORDS":                    "validation",
	"EMPTY_RECORDS":                      "validation",
	"MISSING_ENTITIES":                   "validation",
	"EMPTY_ENTITIES":                     "validation",
	"MISSING_RELATIONS":                  "validation",
	"EMPTY_RELATIONS":                    "validation",
	"INVALID_RELATION_NAME":              "validation",
	"INVALID_SUBSCRIPTION":               "validation",
	"INVALID_OPERATIONS":                 "validation",
	"INVALID_INTEREST":                   "validation",
	"EMPTY_INTEREST":                     "validation",
	"UNSUPPORTED_TYPE":                   "validation",
	"XSQL_PARSE_ERROR":                   "validation",
	"TEMPLATE_BAD_CTE":                   "validation",
	"TEMPLATE_UNBALANCED_PARENS":         "validation",
	"TEMPLATE_ERROR":                     "validation",
	"TEMPLATE_PARAM_ERROR":               "validation",
	"QUERY_NOT_SELECT":                   "validation",
	"PARAM_MISSING":                      "validation",
	"PARAM_TYPE_MISMATCH":                "validation",
	"NOT_CONNECTED":                      "validation", // client-side guard — retrying without connecting can't fix it
	"XID_REQUIRED":                       "validation",
	"NAMESPACE_REQUIRED":                 "validation",
	"DUPLICATE_OPERATION":                "validation",
	"BATCH_MEMBER_UNRESOLVED":            "validation",
	"CLAIM_METHOD_NOT_ALLOWED":           "validation",
	"PASSWORD_TOO_WEAK":                  "validation",
	"RETURN_URL_NOT_REGISTERED":          "validation",
	"LOGIN_STATE_UNKNOWN":                "validation",
	"LOGIN_REQUIRES_CONFIDENTIAL_CLIENT": "validation",
	// not_found
	"UNKNOWN_ENTITY":            "not_found",
	"UNKNOWN_RELATION":          "not_found",
	"UNKNOWN_TEMPLATE_RELATION": "not_found",
	"HISTORY_UNAVAILABLE":       "not_found", // no audit provider — retrying can't fix it
	// conflict
	"FK_VIOLATION":       "conflict",
	"UNIQUE_VIOLATION":   "conflict",
	"CHECK_VIOLATION":    "conflict",
	"NOT_NULL_VIOLATION": "conflict",
	// rate / capacity
	"TIMEOUT": "rate_limit",
	// network
	"NETWORK_ERROR": "network",
	// internal — fallback
	"INTERNAL_ERROR":  "internal",
	"OPERATION_ERROR": "internal",
	"HTTP_ERROR":      "internal",
}

// retryableCategories are the categories whose codes are automatically
// retryable. Any other code is one-shot: the caller must fix something
// before retrying.
var retryableCategories = map[string]bool{
	"network":    true,
	"rate_limit": true,
	"internal":   true,
}

// Pos is a 1-based source position, used by XSQL_PARSE_ERROR and TEMPLATE_*
// errors to point an editor at the offending token.
type Pos struct {
	Line int `json:"line"`
	Col  int `json:"col"`
}

// Error is the error type every SDK call returns. It implements the error
// interface; inspect it with errors.As(err, &target).
//
// Always present: Message, Code, Category, Retryable. The remaining fields
// are populated only when the server (or transport) includes them.
type Error struct {
	Message   string `json:"message"`
	Code      string `json:"code"`
	Category  string `json:"category"`
	Retryable bool   `json:"retryable"`

	Details   any      `json:"details,omitempty"`
	Hint      string   `json:"hint,omitempty"`
	Available []string `json:"available,omitempty"`
	Path      any      `json:"path,omitempty"`
	Entity    string   `json:"entity,omitempty"`
	Relation  string   `json:"relation,omitempty"`
	Operator  string   `json:"operator,omitempty"`
	RequestID string   `json:"requestId,omitempty"`
	Status    int      `json:"status,omitempty"`

	// Source-position fields for XSQL_PARSE_ERROR and TEMPLATE_* errors.
	Line        int   `json:"line,omitempty"`
	Col         int   `json:"col,omitempty"`
	Start       *Pos  `json:"start,omitempty"`
	End         *Pos  `json:"end,omitempty"`
	Diagnostics []any `json:"diagnostics,omitempty"`
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("synthigy: %s (%s)", e.Message, e.Code)
	}
	return "synthigy: " + e.Message
}

// newError builds an Error, deriving category and retryability from the code.
func newError(message, code string) *Error {
	cat := errorCategories[code]
	if cat == "" {
		cat = "internal"
	}
	return &Error{
		Message:   message,
		Code:      code,
		Category:  cat,
		Retryable: retryableCategories[cat],
	}
}

// serverError is the wire shape of an error object returned by the server,
// either at the top level of a /data response or inside a per-op result.
type serverError struct {
	Message     string   `json:"message"`
	Code        string   `json:"code"`
	Details     any      `json:"details"`
	Hint        string   `json:"hint"`
	Available   []string `json:"available"`
	Path        any      `json:"path"`
	Entity      string   `json:"entity"`
	Relation    string   `json:"relation"`
	Operator    string   `json:"operator"`
	Line        int      `json:"line"`
	Col         int      `json:"col"`
	Start       *Pos     `json:"start"`
	End         *Pos     `json:"end"`
	Diagnostics []any    `json:"diagnostics"`
}

// errorFromServer builds an Error from a server-returned error object, the
// HTTP status, and the request id for cross-correlation.
func errorFromServer(se *serverError, status int, requestID string) *Error {
	if se == nil {
		se = &serverError{Message: "Unknown error", Code: "INTERNAL_ERROR"}
	}
	e := newError(se.Message, se.Code)
	e.Details = se.Details
	e.Hint = se.Hint
	e.Available = se.Available
	e.Path = se.Path
	e.Entity = se.Entity
	e.Relation = se.Relation
	e.Operator = se.Operator
	e.Line = se.Line
	e.Col = se.Col
	e.Start = se.Start
	e.End = se.End
	e.Diagnostics = se.Diagnostics
	e.RequestID = requestID
	e.Status = status
	return e
}

// generateRequestID returns 16 chars of URL-safe random for X-Request-Id —
// enough entropy to not collide in a single client's lifetime, short enough
// to read in logs. Mirrors the JS SDK's _generateRequestId (12 random bytes,
// base64url).
func generateRequestID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		// rand.Read effectively never fails on supported platforms; fall
		// back to a fixed marker rather than panicking in a client library.
		return "reqid-fallback"
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
