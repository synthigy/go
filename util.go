package synthigy

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// readBody reads and returns the full response body, ignoring read errors
// (the caller has already decided to inspect status/body).
func readBody(resp *http.Response) []byte {
	raw, _ := io.ReadAll(resp.Body)
	return raw
}

// httpError maps a non-2xx body to an *Error: an embedded {error:{...}}
// when present, otherwise a generic HTTP_ERROR carrying the raw body.
func httpError(raw []byte, status int, fallbackMsg string) *Error {
	if se := parseServerError(raw); se != nil {
		return errorFromServer(se, status, "")
	}
	e := newError(fallbackMsg, "HTTP_ERROR")
	e.Status = status
	e.Details = string(raw)
	return e
}

// parseServerError extracts an {error: {...}} object from a response body.
// Returns nil if the body isn't JSON or has no error field.
func parseServerError(raw []byte) *serverError {
	if len(raw) == 0 {
		return nil
	}
	var env struct {
		Error *serverError `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	return env.Error
}

// asRecords decodes a json.RawMessage into a slice of records. A null/empty
// payload decodes to an empty slice.
func asRecords(data json.RawMessage) ([]Record, error) {
	if len(data) == 0 || string(data) == "null" {
		return []Record{}, nil
	}
	var out []Record
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, newError("failed to decode result: "+err.Error(), "INTERNAL_ERROR")
	}
	return out, nil
}

// asRecord decodes a json.RawMessage into a single record (nil for null).
func asRecord(data json.RawMessage) (Record, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var out Record
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, newError("failed to decode result: "+err.Error(), "INTERNAL_ERROR")
	}
	return out, nil
}

// xsqlDocument ensures an XSQL operation DOCUMENT (STRICT wire: XSQL
// travels only as {op: "xsql", xsql: <document>}). Sources already starting
// with `@` pass through — their @verb is authoritative; bare rooted bodies
// get a synthetic `@<op> _q` header.
func xsqlDocument(source, op string) string {
	if strings.HasPrefix(strings.TrimLeft(source, " \t\n"), "@") {
		return source
	}
	return "@" + op + " _q\n" + source
}
