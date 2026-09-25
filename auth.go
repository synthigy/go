package synthigy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tokenSource is anything that can produce and invalidate a bearer token.
// tokenManager and supervisedTokenSource both implement it; Client's field
// is this interface (not the concrete *tokenManager) so fetchAuth's 401
// retry works identically regardless of which source is installed.
type tokenSource interface {
	getToken(ctx context.Context, audience string) (string, error)
	clear()
}

// tokenEntry is a cached access token plus its absolute expiry.
type tokenEntry struct {
	token     string
	expiresAt time.Time
}

// tokenManager implements the OAuth client-credentials grant against the
// server's /oauth/token endpoint. Tokens are cached per audience and
// refreshed before expiry.
type tokenManager struct {
	tokenURL     string
	clientID     string
	clientSecret string
	scope        string
	httpClient   *http.Client

	// mu guards tokens and is held across a refresh so concurrent callers
	// don't stampede the token endpoint (single-flight). The JS SDK added an
	// equivalent single-flight guard to fix a token-refresh race.
	mu     sync.Mutex
	tokens map[string]tokenEntry
}

func newTokenManager(tokenURL, clientID, clientSecret, scope string, hc *http.Client) *tokenManager {
	return &tokenManager{
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		scope:        scope,
		httpClient:   hc,
		tokens:       map[string]tokenEntry{},
	}
}

// tokenResponse is the OAuth token endpoint response shape.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// getToken returns a valid access token for the given audience (empty for the
// default/Synthigy audience), refreshing if the cached token is missing or
// within 30s of expiry.
func (tm *tokenManager) getToken(ctx context.Context, audience string) (string, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	key := audience
	if cached, ok := tm.tokens[key]; ok && time.Now().Before(cached.expiresAt.Add(-30*time.Second)) {
		return cached.token, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", tm.clientID)
	form.Set("client_secret", tm.clientSecret)
	if tm.scope != "" {
		form.Set("scope", tm.scope)
	}
	if audience != "" {
		form.Set("audience", audience)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tm.tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", newError(err.Error(), "NETWORK_ERROR")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		return "", newError(err.Error(), "NETWORK_ERROR")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newError(
			fmt.Sprintf("Token request failed (%d): %s", resp.StatusCode, string(body)),
			"UNAUTHORIZED")
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", newError("Invalid token response: "+err.Error(), "INTERNAL_ERROR")
	}
	expiresIn := tr.ExpiresIn
	if expiresIn == 0 {
		expiresIn = 3600
	}
	tm.tokens[key] = tokenEntry{
		token:     tr.AccessToken,
		expiresAt: time.Now().Add(time.Duration(expiresIn) * time.Second),
	}
	return tr.AccessToken, nil
}

// clear drops all cached tokens — used after a 401 so the next request
// fetches a fresh token (the cached one may have been rotated server-side).
func (tm *tokenManager) clear() {
	tm.mu.Lock()
	tm.tokens = map[string]tokenEntry{}
	tm.mu.Unlock()
}

// ============================================================================
// Supervised stdio — SYNTHIGY_SUPERVISED=1. The SDK never mints locally under this mode: the CLI/commander
// is the platform's stdio owner, so a token is asked for over JSON-RPC on
// the process's OWN stdio (supervise grammar) instead. {token, expires_in}
// is deliberately byte-compatible with robotics' request-access-token
// response, so this ask serves both parents (exec locally, the commander
// through a reacher agent in production).
//
// ONE reader goroutine for the whole process lifetime (not one per ask):
// two independent readers on the same stdin would race/steal each other's
// line. A single malformed/undecodable line must never take the loop down
// with it — every future ask would otherwise hang to its own timeout
// forever with no way to ever succeed again. One shared cache + lock too
// (not per-source): every supervisedTokenSource in a process shares the
// one supervising parent, so a second independent ask would just be a
// wasted round trip.
// ============================================================================

const supervisedAskTimeout = 5 * time.Second

type supervisedFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type authTokenResult struct {
	Token     string          `json:"token"`
	ExpiresIn json.RawMessage `json:"expires_in"`
}

// supervisedIO is the process-wide singleton owning the supervised stdio
// channel AND the per-audience token cache. Constructed once, lazily —
// the reader goroutine only starts on the first actual ask, so a process
// that never touches a supervised token never opens stdin.
type supervisedIO struct {
	readerOnce sync.Once

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[int64]chan supervisedFrame
	nextID    int64

	cacheMu sync.Mutex
	tokens  map[string]tokenEntry
}

var globalSupervisedIO = &supervisedIO{
	pending: map[int64]chan supervisedFrame{},
	tokens:  map[string]tokenEntry{},
}

func (s *supervisedIO) ensureReader() {
	s.readerOnce.Do(func() { go s.readLoop() })
}

func (s *supervisedIO) readLoop() {
	r := bufio.NewReader(os.Stdin)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			s.handleLine(line)
		}
		if err != nil {
			// EOF, or any other read error (undecodable bytes, a torn-down
			// stream) — treated the same way: there is no listener anymore.
			// Flush every pending ask so it fails fast instead of blocking
			// to its own timeout.
			s.pendingMu.Lock()
			waiters := s.pending
			s.pending = map[int64]chan supervisedFrame{}
			s.pendingMu.Unlock()
			for _, ch := range waiters {
				close(ch)
			}
			return
		}
	}
}

// handleLine applies the strict candidate test (first byte '{', then
// parses, then carries jsonrpc:"2.0") — the same rule the parent side
// uses. A malformed candidate line must not take the dispatcher down with
// it, hence the recover: nothing here is expected to panic, but the
// hardening bar is "a garbage line can never kill the reader", not "a
// garbage line can never kill the reader unless our own parsing has a
// bug".
func (s *supervisedIO) handleLine(line string) {
	defer func() { _ = recover() }()

	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed[0] != '{' {
		return
	}
	var frame supervisedFrame
	if err := json.Unmarshal([]byte(trimmed), &frame); err != nil {
		return
	}
	if frame.JSONRPC != "2.0" {
		return
	}

	s.pendingMu.Lock()
	ch, ok := s.pending[frame.ID]
	if ok {
		delete(s.pending, frame.ID)
	}
	s.pendingMu.Unlock()
	if ok {
		ch <- frame
	}
}

// writeFrame is an atomic single write of frame+"\n" (the frame
// rule). A broken pipe (parent already gone) surfaces as a
// plain error from Write — Go never raises SIGPIPE into the process for
// stdout — so the caller falls through to the timeout path uniformly.
func (s *supervisedIO) writeFrame(frame supervisedFrame) bool {
	payload, err := json.Marshal(frame)
	if err != nil {
		return false
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = os.Stdout.Write(append(payload, '\n'))
	return err == nil
}

// ask sends one JSON-RPC request and blocks up to `timeout` for the
// matching response. Returns nil (no error) on timeout, parent EOF, or a
// write failure — the caller maps that uniformly to the teaching throw.
func (s *supervisedIO) ask(method string, params any, timeout time.Duration) *supervisedFrame {
	s.ensureReader()

	s.pendingMu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan supervisedFrame, 1)
	s.pending[id] = ch
	s.pendingMu.Unlock()

	if !s.writeFrame(supervisedFrame{JSONRPC: "2.0", ID: id, Method: method, Params: params}) {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
		return nil
	}

	select {
	case f, ok := <-ch:
		if !ok {
			return nil // reader hit EOF; channel closed instead of delivering
		}
		return &f
	case <-time.After(timeout):
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
		return nil
	}
}

// parseExpiresIn defensively coerces the parent's expires_in — a JSON
// number or a numeric string both work; anything else (missing, null,
// non-numeric garbage) reports !ok so the caller falls back to a safe
// default instead of crashing the cache-expiry computation.
func parseExpiresIn(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if f, err2 := strconv.ParseFloat(s, 64); err2 == nil {
			return f, true
		}
	}
	return 0, false
}

// getToken: per-audience cache with a 30s pre-expiry buffer, matching
// tokenManager's own discipline — extended process-wide (one cache, one
// lock held across the whole ask) since every supervisedTokenSource in a
// process shares one supervising parent.
func (s *supervisedIO) getToken(_ context.Context, audience string) (string, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	if cached, ok := s.tokens[audience]; ok && time.Now().Before(cached.expiresAt.Add(-30*time.Second)) {
		return cached.token, nil
	}

	params := map[string]string{}
	if audience != "" {
		params["audience"] = audience
	}
	frame := s.ask("auth.token", params, supervisedAskTimeout)
	if frame == nil {
		return "", newError(
			"Timed out waiting for auth.token from the supervising parent — "+
				"a hung or missing parent must not hang the bot. Check the "+
				"parent process (synthigy exec/agent, or the robotics "+
				"commander) is still connected.", "NO_TOKEN")
	}
	if frame.Error != nil {
		msg := frame.Error.Message
		if msg == "" {
			msg = "auth.token request denied"
		}
		return "", newError(msg, "NO_TOKEN")
	}

	var result authTokenResult
	if len(frame.Result) > 0 {
		_ = json.Unmarshal(frame.Result, &result)
	}
	if result.Token == "" {
		return "", newError("auth.token response carried no token", "NO_TOKEN")
	}

	expiresIn := 300 * time.Second
	if n, ok := parseExpiresIn(result.ExpiresIn); ok {
		expiresIn = time.Duration(n * float64(time.Second))
	}
	s.tokens[audience] = tokenEntry{token: result.Token, expiresAt: time.Now().Add(expiresIn)}
	return result.Token, nil
}

func (s *supervisedIO) clear() {
	s.cacheMu.Lock()
	s.tokens = map[string]tokenEntry{}
	s.cacheMu.Unlock()
}

// supervisedTokenSource is the tokenSource for SYNTHIGY_SUPERVISED=1 — a
// thin handle onto the process-wide globalSupervisedIO singleton (cache
// and all), so multiple Clients in one process share one cache instead of
// each asking the parent independently.
type supervisedTokenSource struct{}

func newSupervisedTokenSource() *supervisedTokenSource {
	return &supervisedTokenSource{}
}

func (supervisedTokenSource) getToken(ctx context.Context, audience string) (string, error) {
	return globalSupervisedIO.getToken(ctx, audience)
}

func (supervisedTokenSource) clear() {
	globalSupervisedIO.clear()
}

// noTokenError is the teaching throw: the
// error IS the UX, no flag, no silent anonymous fallback.
func noTokenError() *Error {
	return newError(
		"no Synthigy token: set Token, or ClientID + ClientSecret, or set "+
			"SYNTHIGY_TOKEN, or run under `synthigy exec` (or a Synthigy "+
			"agent) with SYNTHIGY_SUPERVISED=1 so a parent can supply one.",
		"NO_TOKEN")
}
