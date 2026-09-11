package synthigy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// Diagnostic is one lint result with byte offsets and 1-based positions for
// editor integration.
type Diagnostic struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	From     int    `json:"from"`
	To       int    `json:"to"`
	Start    *Pos   `json:"start"`
	End      *Pos   `json:"end"`
}

// Schema fetches the IAM-filtered model schema via GET /schema. Pass
// kebab-case entity names to limit the result, or none for the full schema.
// Returns the decoded JSON shape (id-key + entities map).
func (c *Client) Schema(ctx context.Context, entities ...string) (map[string]any, error) {
	u, err := url.Parse(c.endpoint + "/schema")
	if err != nil {
		return nil, newError("invalid endpoint: "+err.Error(), "INVALID_BODY")
	}
	if len(entities) > 0 {
		q := u.Query()
		q.Set("entities", strings.Join(entities, ","))
		u.RawQuery = q.Encode()
	}
	raw, err := c.getJSON(ctx, u.String())
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, newError("failed to decode schema: "+err.Error(), "INTERNAL_ERROR")
	}
	return out, nil
}

// Lint checks an XSQL source string against the IAM-projected schema and
// returns diagnostics. Use LintEntity(name) for schema-aware checks and
// LintOp(op) to set the wire op.
func (c *Client) Lint(ctx context.Context, source string, opts ...Opt) ([]Diagnostic, error) {
	o := applyOpts(opts)
	body := map[string]any{"source": source}
	if o.lintEntity != "" {
		body["entity"] = o.lintEntity
	}
	if o.lintOp != "" {
		body["op"] = o.lintOp
	}
	data, _ := json.Marshal(body)
	resp, err := c.fetchAuth(ctx, http.MethodPost, c.endpoint+"/lint", data,
		map[string]string{"Content-Type": "application/json"}, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw := readBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError(raw, resp.StatusCode, "Lint request failed")
	}
	var out struct {
		Diagnostics []Diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, newError("failed to decode lint result: "+err.Error(), "INTERNAL_ERROR")
	}
	if out.Diagnostics == nil {
		return []Diagnostic{}, nil
	}
	return out.Diagnostics, nil
}

// DeployedModel fetches the raw deployed ERD model (modeler-authored shape).
// Most consumers want RuntimeModel instead. Requires dataset:load scope. The
// server currently returns this as a transit-encoded JSON string.
func (c *Client) DeployedModel(ctx context.Context, opts ...Opt) (json.RawMessage, error) {
	r, err := c.execOne(ctx, OpDeployedModel(), opts...)
	if err != nil {
		return nil, err
	}
	return r.Data, nil
}

// RuntimeModel fetches the runtime ERD model — the deployed model augmented
// with identity attrs, audit attrs, and reference-as-relation expansion.
// Requires dataset:load scope.
func (c *Client) RuntimeModel(ctx context.Context, opts ...Opt) (json.RawMessage, error) {
	r, err := c.execOne(ctx, OpRuntimeModel(), opts...)
	if err != nil {
		return nil, err
	}
	return r.Data, nil
}

// getJSON performs an authenticated GET and returns the body, mapping
// non-2xx to an *Error.
func (c *Client) getJSON(ctx context.Context, urlStr string) ([]byte, error) {
	resp, err := c.fetchAuth(ctx, http.MethodGet, urlStr, nil, nil, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw := readBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError(raw, resp.StatusCode, "Request failed")
	}
	return raw, nil
}
