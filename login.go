package synthigy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PendingLogin is the in-flight state of one login, from LoginStart until the
// callback. It round-trips through a LoginStore, so it carries JSON tags.
type PendingLogin struct {
	CodeVerifier string    `json:"code_verifier"`
	Nonce        string    `json:"nonce"`
	ReturnTo     string    `json:"return_to"`
	CreatedAt    time.Time `json:"created_at"`
}

// LoginStore holds in-flight logins between LoginStart and the callback. Take
// is one-shot: it returns the login once and removes it, or (nil, nil) when
// the state is unknown or expired.
type LoginStore interface {
	Put(ctx context.Context, state string, login PendingLogin) error
	Take(ctx context.Context, state string) (*PendingLogin, error)
}

// MemoryLoginStore keeps in-flight logins in memory — SINGLE PROCESS ONLY.
// Behind a load balancer the callback can land on an instance that never saw
// LoginStart and fails with LOGIN_STATE_UNKNOWN.
type MemoryLoginStore struct {
	ttl     time.Duration
	mu      sync.Mutex
	pending map[string]PendingLogin
}

// NewMemoryLoginStore returns a MemoryLoginStore; ttl <= 0 means five minutes.
func NewMemoryLoginStore(ttl time.Duration) *MemoryLoginStore {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &MemoryLoginStore{ttl: ttl, pending: map[string]PendingLogin{}}
}

func (s *MemoryLoginStore) Put(_ context.Context, state string, login PendingLogin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-s.ttl)
	for k, v := range s.pending {
		if !v.CreatedAt.After(cutoff) {
			delete(s.pending, k)
		}
	}
	s.pending[state] = login
	return nil
}

func (s *MemoryLoginStore) Take(_ context.Context, state string) (*PendingLogin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	login, ok := s.pending[state]
	if !ok {
		return nil, nil
	}
	delete(s.pending, state)
	if !login.CreatedAt.After(time.Now().Add(-s.ttl)) {
		return nil, nil
	}
	return &login, nil
}

// LoginStartOptions configures LoginStart. RedirectURI is required.
type LoginStartOptions struct {
	// RedirectURI is the callback on YOUR server, registered on this OAuth client.
	RedirectURI string
	// ReturnTo is handed back by LoginComplete; default "/".
	ReturnTo string
	// Scope defaults to "openid".
	Scope string
	// PublicEndpoint is the browser-facing server URL, when it differs from Endpoint.
	PublicEndpoint string
}

// LoginUser is the person a login authenticated. XID comes from the id_token.
type LoginUser struct {
	XID    string   `json:"xid"`
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

// LoginResult is what LoginComplete returns.
type LoginResult struct {
	User     LoginUser      `json:"user"`
	Tokens   map[string]any `json:"tokens"`
	ReturnTo string         `json:"return_to"`
}

func noLoginStoreError() *Error {
	return newError("no login store: set Config.LoginStore — a LoginStore backed by "+
		"whatever already holds your sessions. For a single-process dev server, "+
		"pass synthigy.NewMemoryLoginStore(0).", "NO_LOGIN_STORE")
}

func randomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("synthigy: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func pkcePair() (verifier, challenge string) {
	verifier = randomToken(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// decodeJWTPayload reads a JWT's claims unverified — only for a token from our own authenticated token POST.
func decodeJWTPayload(jwt string) map[string]any {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return nil
	}
	return claims
}

func (c *Client) requireLogin() error {
	if c.clientID == "" || c.clientSecret == "" {
		return newError("login needs ClientID + ClientSecret: the code exchange "+
			"authenticates this process as a confidential client. A browser or "+
			"native app is a public client and drives the same flow with a PKCE "+
			"library of its own.", "LOGIN_REQUIRES_CONFIDENTIAL_CLIENT")
	}
	if c.loginStore == nil {
		return noLoginStoreError()
	}
	return nil
}

// LoginStart begins a person's login (OIDC authorization code + PKCE) and
// returns the URL to redirect their browser to.
func (c *Client) LoginStart(ctx context.Context, o LoginStartOptions) (string, error) {
	if err := c.requireLogin(); err != nil {
		return "", err
	}
	if o.RedirectURI == "" {
		return "", newError("LoginStart needs RedirectURI — the URL on YOUR server the "+
			"IdP sends the browser back to, registered on this OAuth client.",
			"LOGIN_REQUIRES_CONFIDENTIAL_CLIENT")
	}
	if o.ReturnTo == "" {
		o.ReturnTo = "/"
	}
	if o.Scope == "" {
		o.Scope = "openid"
	}
	state, nonce := randomToken(24), randomToken(24)
	verifier, challenge := pkcePair()
	// stored BEFORE the URL is handed out — a fast browser would come back to nothing
	if err := c.loginStore.Put(ctx, state, PendingLogin{
		CodeVerifier: verifier, Nonce: nonce, ReturnTo: o.ReturnTo, CreatedAt: time.Now(),
	}); err != nil {
		return "", err
	}
	base := c.endpoint
	if o.PublicEndpoint != "" {
		base = strings.TrimRight(o.PublicEndpoint, "/")
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.clientID},
		"redirect_uri":          {o.RedirectURI},
		"scope":                 {o.Scope},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"nonce":                 {nonce},
	}
	return base + "/oauth/authorize?" + q.Encode(), nil
}

// LoginComplete exchanges the callback's code for tokens and the user behind
// them. Fails with LOGIN_STATE_UNKNOWN, LOGIN_NONCE_MISMATCH or
// LOGIN_EXCHANGE_FAILED.
func (c *Client) LoginComplete(ctx context.Context, code, state, redirectURI string) (*LoginResult, error) {
	if err := c.requireLogin(); err != nil {
		return nil, err
	}
	var pending *PendingLogin
	if state != "" {
		p, err := c.loginStore.Take(ctx, state)
		if err != nil {
			return nil, err
		}
		pending = p
	}
	if pending == nil {
		return nil, newError("unknown or expired login state: the store never saw it, "+
			"it was already consumed, or it timed out. A callback landing on a "+
			"different instance than the one that served /login does this.",
			"LOGIN_STATE_UNKNOWN")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code_verifier": {pending.CodeVerifier},
	}
	if c.timeout > 0 {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.timeout)
			defer cancel()
		}
	}
	// never fetchAuth — its 401 retry would replay a burned one-shot code
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, newError(err.Error(), "NETWORK_ERROR")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, newError(err.Error(), "NETWORK_ERROR")
	}
	defer resp.Body.Close()
	raw := readBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e := newError("authorization-code exchange failed ("+strconv.Itoa(resp.StatusCode)+")",
			"LOGIN_EXCHANGE_FAILED")
		e.Status = resp.StatusCode
		e.Details = string(raw)
		return nil, e
	}
	var tokens map[string]any
	if err := json.Unmarshal(raw, &tokens); err != nil {
		return nil, newError("token response is not JSON: "+err.Error(), "LOGIN_EXCHANGE_FAILED")
	}
	idToken, _ := tokens["id_token"].(string)
	claims := decodeJWTPayload(idToken)
	if claims == nil {
		return nil, newError("token response carried no readable id_token — request "+
			"the openid scope, which is what names the user.", "LOGIN_EXCHANGE_FAILED")
	}
	xid, _ := claims["xid"].(string)
	if xid == "" {
		return nil, newError("id_token carries no xid claim — without it the user can't "+
			"be named in ActingAs, and calls would silently run as this service.",
			"LOGIN_EXCHANGE_FAILED")
	}
	if n, _ := claims["nonce"].(string); n != pending.Nonce {
		return nil, newError("id_token nonce does not match the one this login started with",
			"LOGIN_NONCE_MISMATCH")
	}
	scope, _ := tokens["scope"].(string)
	if scope == "" {
		scope, _ = claims["scope"].(string)
	}
	name, _ := claims["sub"].(string)
	return &LoginResult{
		User:     LoginUser{XID: xid, Name: name, Scopes: strings.Fields(scope)},
		Tokens:   tokens,
		ReturnTo: pending.ReturnTo,
	}, nil
}

// LoginCancel discards an in-flight login (the callback came back with
// `error`) and returns where the visitor was headed; ok is false for an
// unknown state.
func (c *Client) LoginCancel(ctx context.Context, state string) (returnTo string, ok bool, err error) {
	if c.loginStore == nil {
		return "", false, noLoginStoreError()
	}
	if state == "" {
		return "", false, nil
	}
	p, err := c.loginStore.Take(ctx, state)
	if err != nil || p == nil {
		return "", false, err
	}
	return p.ReturnTo, true, nil
}
