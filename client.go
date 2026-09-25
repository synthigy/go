package synthigy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Config configures a Client. Provide either Token (static bearer token) or
// ClientID+ClientSecret (OAuth client-credentials). Endpoint defaults to $SYNTHIGY_ENDPOINT.
type Config struct {
	// Endpoint is the Synthigy base URL (e.g. https://synthigy.example.com).
	Endpoint string

	// Client-credentials auth.
	ClientID     string
	ClientSecret string
	Scope        string

	// Audience overrides the audience bound to every mint this Client makes.
	// Leave it empty: it defaults to PlatformAudience, which is what /data,
	// /schema, /history, /logs and subscriptions require.
	Audience string

	// Static bearer token (alternative to client credentials). Empty string
	// is allowed for unauthenticated / dev servers.
	Token string
	// hasToken disambiguates an intentional empty static token from "unset".
	// Set Token and, for the empty-string dev case, also set StaticToken.
	StaticToken bool

	// With none of the above, New also falls back to — when
	// SYNTHIGY_SUPERVISED=1 — a supervised-stdio auth.token ask (the pipe
	// beats the env var: it can refresh mid-run), then the SYNTHIGY_TOKEN
	// env var; with no source at
	// all it returns a *Error{Code: "NO_TOKEN"} whose message teaches the
	// fix.

	// ActingAs is a default impersonation target applied to every request
	// unless overridden per call.
	ActingAs string

	// KeyFormat is the default response key-case ("kebab", "snake", "camel").
	// The server defaults to snake_case when empty.
	KeyFormat string

	// Timeout is the default per-request deadline applied to non-streaming
	// calls when the caller's context has no deadline of its own. Zero means
	// no default timeout.
	Timeout time.Duration

	// HTTPClient overrides the underlying *http.Client. When nil a client
	// with sensible connection pooling is used. SSE streams rely on context
	// cancellation rather than a client-level timeout, so do not set
	// HTTPClient.Timeout if you also use the streaming APIs.
	HTTPClient *http.Client

	// KeepAlive pins the upstream SSE session open across watch churn (for
	// long-lived BFFs / services). Eagerly opens the watch multiplexer.
	KeepAlive bool

	// LoginStore holds in-flight browser logins for LoginStart/LoginComplete.
	// Nil makes them fail with NO_LOGIN_STORE.
	LoginStore LoginStore
}

// Client is a Synthigy /data client. Construct one with New. It is safe for
// concurrent use.
type Client struct {
	endpoint         string
	dataURL          string
	defaultActingAs  string
	defaultKeyFormat string
	defaultAudience  string
	timeout          time.Duration
	keepAlive        bool

	httpClient  *http.Client
	tokenSource tokenSource // nil in static-token mode
	staticToken string
	clientID    string
	// kept outside the auth switch: a static-token client can still be confidential for login
	clientSecret string
	loginStore   LoginStore

	// Local mirror of the session's subscription set (records-only
	// set-replace contract). Guarded by subMu.
	subMu        sync.Mutex
	dataSubs     map[string]descriptor // key -> descriptor
	entitySubs   map[string]struct{}
	relationSubs map[string]struct{}
	modelSubs    map[string]struct{}

	// Watch multiplexer, created lazily (or eagerly when KeepAlive).
	muxMu sync.Mutex
	mux   *watchMultiplexer
}

func newClient(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		cfg.Endpoint = os.Getenv("SYNTHIGY_ENDPOINT")
	}
	if cfg.Endpoint == "" {
		return nil, newError("no endpoint — set Config.Endpoint, or SYNTHIGY_ENDPOINT (run under `synthigy exec`)", "NO_ENDPOINT")
	}
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	// This SDK is the client for the platform API, so that is what it mints
	// for. Nothing to configure, and nowhere to look the value up if there
	// were: the server does not advertise it in discovery. Minting a token for
	// some OTHER API is a per-call Audience option. The env var is an escape
	// hatch, not the normal path.
	if cfg.Audience == "" {
		cfg.Audience = os.Getenv("SYNTHIGY_AUDIENCE")
	}
	if cfg.Audience == "" {
		cfg.Audience = PlatformAudience
	}

	hc := cfg.HTTPClient
	if hc == nil {
		hc = defaultHTTPClient()
	}

	c := &Client{
		endpoint:         endpoint,
		dataURL:          endpoint + "/data",
		defaultActingAs:  cfg.ActingAs,
		defaultKeyFormat: cfg.KeyFormat,
		defaultAudience:  cfg.Audience,
		timeout:          cfg.Timeout,
		keepAlive:        cfg.KeepAlive,
		httpClient:       hc,
		clientID:         cfg.ClientID,
		clientSecret:     cfg.ClientSecret,
		loginStore:       cfg.LoginStore,
		dataSubs:         map[string]descriptor{},
		entitySubs:       map[string]struct{}{},
		relationSubs:     map[string]struct{}{},
		modelSubs:        map[string]struct{}{},
	}

	switch {
	case cfg.Token != "" || cfg.StaticToken:
		// Static-token mode (empty string allowed for dev when StaticToken).
		c.staticToken = cfg.Token
	case cfg.ClientID != "" && cfg.ClientSecret != "":
		c.tokenSource = newTokenManager(endpoint+"/oauth/token",
			cfg.ClientID, cfg.ClientSecret, cfg.Scope, hc)
	case os.Getenv("SYNTHIGY_SUPERVISED") == "1":
		// The pipe beats the env var: exec injects the cached token AND
		// supervises; only the pipe refreshes mid-run.
		c.tokenSource = newSupervisedTokenSource()
	case os.Getenv("SYNTHIGY_TOKEN") != "":
		// A snapshot, not a live source: exec/connect refresh and rewrite
		// the profile's cache on THEIR next run, not this process's.
		c.staticToken = os.Getenv("SYNTHIGY_TOKEN")
	default:
		return nil, noTokenError()
	}

	if c.keepAlive {
		c.getMultiplexer()
	}
	return c, nil
}

// Close tears the client down — closes every live watch, releases the
// keepAlive hold, and closes the SSE. Use when a long-lived service shuts
// down so the plug session is dropped cleanly server-side.
func (c *Client) Close() {
	c.muxMu.Lock()
	mux := c.mux
	c.muxMu.Unlock()
	if mux != nil {
		mux.close()
	}
}

// token returns a bearer token for the given audience.
func (c *Client) token(ctx context.Context, audience string) (string, error) {
	if c.tokenSource == nil {
		return c.staticToken, nil
	}
	return c.tokenSource.getToken(ctx, audience)
}

// Token returns an access token for a specific audience (via the Audience
// option), falling back to the Client's configured Audience. Synthigy acts as
// the IdP, so use this to get tokens for external services that trust it.
func (c *Client) Token(ctx context.Context, opts ...Opt) (string, error) {
	o := applyOpts(opts)
	if o.audience == "" {
		o.audience = c.defaultAudience
	}
	return c.token(ctx, o.audience)
}

// fetchAuth performs an authenticated request, retrying once on 401 (the
// cached token may have rotated). It returns the live *http.Response; the
// caller must close resp.Body. On a second 401 it returns an UNAUTHORIZED
// Error. applyTimeout=false is used for SSE streams (no deadline).
func (c *Client) fetchAuth(ctx context.Context, method, urlStr string, body []byte,
	headers map[string]string, applyTimeout bool) (*http.Response, error) {

	resp, err := c.attempt(ctx, method, urlStr, body, headers, applyTimeout)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && c.tokenSource != nil {
		resp.Body.Close()
		c.tokenSource.clear()
		resp, err = c.attempt(ctx, method, urlStr, body, headers, applyTimeout)
		if err != nil {
			return nil, err
		}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		rid := resp.Header.Get("X-Request-Id")
		resp.Body.Close()
		e := newError("Unauthorized", "UNAUTHORIZED")
		e.RequestID = rid
		e.Status = 401
		return nil, e
	}
	return resp, nil
}

func (c *Client) attempt(ctx context.Context, method, urlStr string, body []byte,
	headers map[string]string, applyTimeout bool) (*http.Response, error) {

	tok, err := c.token(ctx, c.defaultAudience)
	if err != nil {
		return nil, err
	}

	reqCtx := ctx
	var cancel context.CancelFunc
	if applyTimeout && c.timeout > 0 {
		if _, has := ctx.Deadline(); !has {
			reqCtx, cancel = context.WithTimeout(ctx, c.timeout)
		}
	}

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, urlStr, rdr)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, newError(err.Error(), "NETWORK_ERROR")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if req.Header.Get("X-Request-Id") == "" {
		req.Header.Set("X-Request-Id", generateRequestID())
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		// Distinguish a deadline/cancel from a transport failure.
		if ctx.Err() != nil {
			return nil, newError(ctx.Err().Error(), "TIMEOUT")
		}
		return nil, newError(err.Error(), "NETWORK_ERROR")
	}
	if cancel != nil {
		// Tie the derived context's lifetime to the response body so the
		// deadline stays armed while the caller reads, and is released on
		// Close. (Without this the timeout would fire mid-read.)
		resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	}
	return resp, nil
}

// cancelOnClose calls cancel when the wrapped body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// post POSTs a JSON body to /data and returns the raw response body plus the
// X-Request-Id. It handles 403 and non-2xx by returning an *Error.
func (c *Client) post(ctx context.Context, body any) ([]byte, string, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, "", newError("failed to encode request: "+err.Error(), "INVALID_BODY")
	}
	resp, err := c.fetchAuth(ctx, http.MethodPost, c.dataURL, data,
		map[string]string{"Content-Type": "application/json"}, true)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	rid := resp.Header.Get("X-Request-Id")
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusForbidden {
		se := parseServerError(raw)
		if se == nil {
			se = &serverError{Message: "Forbidden", Code: "FORBIDDEN"}
		}
		return nil, rid, errorFromServer(se, 403, rid)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if se := parseServerError(raw); se != nil {
			return nil, rid, errorFromServer(se, resp.StatusCode, rid)
		}
		e := newError("Request failed", "HTTP_ERROR")
		e.Details = string(raw)
		e.Status = resp.StatusCode
		e.RequestID = rid
		return nil, rid, e
	}
	return raw, rid, nil
}

// fetchFn returns a transport closure suitable for buffer-style consumers:
// it takes a body, attaches auth, POSTs to /data, and returns the parsed
// {results: [...]} envelope. It does not modify the body.
func (c *Client) FetchFn() func(ctx context.Context, body any) (map[string]any, error) {
	return func(ctx context.Context, body any) (map[string]any, error) {
		raw, _, err := c.post(ctx, body)
		if err != nil {
			return nil, err
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, newError("failed to decode response: "+err.Error(), "INTERNAL_ERROR")
		}
		return out, nil
	}
}

func defaultHTTPClient() *http.Client {
	tr := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     30 * time.Second,
	}
	return &http.Client{Transport: tr}
}
