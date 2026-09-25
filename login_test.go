package synthigy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingStore struct {
	*MemoryLoginStore
	mu   sync.Mutex
	puts map[string]PendingLogin
}

func newRecordingStore() *recordingStore {
	return &recordingStore{MemoryLoginStore: NewMemoryLoginStore(0), puts: map[string]PendingLogin{}}
}

func (s *recordingStore) Put(ctx context.Context, state string, l PendingLogin) error {
	s.mu.Lock()
	s.puts[state] = l
	s.mu.Unlock()
	return s.MemoryLoginStore.Put(ctx, state, l)
}

func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

type tokenCall struct {
	form   url.Values
	bearer string
}

// tokenServer answers /oauth/token with respond(form).
func tokenServer(t *testing.T, respond func(url.Values) (int, any)) (*httptest.Server, *[]tokenCall) {
	t.Helper()
	var calls []tokenCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		calls = append(calls, tokenCall{form, r.Header.Get("Authorization")})
		status, out := respond(form)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func loginClient(t *testing.T, endpoint string, store LoginStore) *Client {
	t.Helper()
	c, err := New(Config{Endpoint: endpoint, Token: "static", ClientID: "bff",
		ClientSecret: "s3cret", LoginStore: store})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func codeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func startState(t *testing.T, c *Client, o LoginStartOptions) (string, url.Values) {
	t.Helper()
	raw, err := c.LoginStart(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	return q.Get("state"), q
}

func TestLoginGuards(t *testing.T) {
	ctx := context.Background()
	c, _ := New(Config{Endpoint: "http://x", Token: "t"})
	if _, err := c.LoginStart(ctx, LoginStartOptions{RedirectURI: "http://app/cb"}); codeOf(err) != "LOGIN_REQUIRES_CONFIDENTIAL_CLIENT" {
		t.Fatalf("want confidential guard, got %v", err)
	}
	c, _ = New(Config{Endpoint: "http://x", Token: "t", ClientID: "bff", ClientSecret: "s"})
	if _, err := c.LoginStart(ctx, LoginStartOptions{RedirectURI: "http://app/cb"}); codeOf(err) != "NO_LOGIN_STORE" {
		t.Fatalf("want NO_LOGIN_STORE, got %v", err)
	}
	if _, _, err := c.LoginCancel(ctx, "s"); codeOf(err) != "NO_LOGIN_STORE" {
		t.Fatalf("cancel: want NO_LOGIN_STORE, got %v", err)
	}
}

func TestLoginStartURL(t *testing.T) {
	store := newRecordingStore()
	c := loginClient(t, "http://engine:7887", store)
	raw, err := c.LoginStart(context.Background(), LoginStartOptions{
		RedirectURI: "http://app/cb", ReturnTo: "/movies", PublicEndpoint: "https://id.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "https://id.example.com/oauth/authorize?") {
		t.Fatalf("url = %s", raw)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	for k, want := range map[string]string{"response_type": "code", "client_id": "bff",
		"redirect_uri": "http://app/cb", "scope": "openid", "code_challenge_method": "S256"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	p := store.puts[q.Get("state")]
	if p.Nonce != q.Get("nonce") || p.ReturnTo != "/movies" {
		t.Fatalf("stored %+v", p)
	}
	sum := sha256.Sum256([]byte(p.CodeVerifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("challenge is not S256 of the stored verifier")
	}
}

func TestLoginCompleteHappyPathViaGlobal(t *testing.T) {
	store := newRecordingStore()
	srv, calls := tokenServer(t, func(f url.Values) (int, any) {
		var nonce string
		for _, p := range store.puts {
			if p.CodeVerifier == f.Get("code_verifier") {
				nonce = p.Nonce
			}
		}
		return 200, map[string]any{"access_token": "at", "scope": "openid profile",
			"id_token": fakeJWT(map[string]any{"sub": "alice", "xid": "x-alice", "nonce": nonce})}
	})
	Disconnect()
	defer Disconnect()
	if err := Connect(Config{Endpoint: srv.URL, Token: "static", ClientID: "bff",
		ClientSecret: "s3cret", LoginStore: store}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	raw, err := LoginStart(ctx, LoginStartOptions{RedirectURI: "http://app/cb"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	state := u.Query().Get("state")
	res, err := LoginComplete(ctx, "the-code", state, "http://app/cb")
	if err != nil {
		t.Fatal(err)
	}
	if res.User.XID != "x-alice" || res.User.Name != "alice" ||
		strings.Join(res.User.Scopes, ",") != "openid,profile" || res.ReturnTo != "/" ||
		res.Tokens["access_token"] != "at" {
		t.Fatalf("result %+v", res)
	}
	call := (*calls)[0]
	for k, want := range map[string]string{"grant_type": "authorization_code", "code": "the-code",
		"redirect_uri": "http://app/cb", "client_id": "bff", "client_secret": "s3cret"} {
		if call.form.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, call.form.Get(k), want)
		}
	}
	if call.bearer != "" {
		t.Error("the exchange must carry no bearer")
	}
	if _, err := LoginComplete(ctx, "the-code", state, "http://app/cb"); codeOf(err) != "LOGIN_STATE_UNKNOWN" {
		t.Fatalf("replay: want LOGIN_STATE_UNKNOWN, got %v", err)
	}
	if len(*calls) != 1 {
		t.Fatal("a replayed state must not reach the IdP")
	}
}

func TestLoginCompleteFailures(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		status int
		body   any
		code   string
	}{
		{"nonce mismatch", 200, map[string]any{"id_token": fakeJWT(map[string]any{"xid": "x", "nonce": "wrong"})}, "LOGIN_NONCE_MISMATCH"},
		{"non-2xx", 400, map[string]any{"error": "invalid_grant"}, "LOGIN_EXCHANGE_FAILED"},
		{"no id_token", 200, map[string]any{"access_token": "at"}, "LOGIN_EXCHANGE_FAILED"},
		{"no xid", 200, map[string]any{"id_token": fakeJWT(map[string]any{"sub": "a"})}, "LOGIN_EXCHANGE_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := tokenServer(t, func(url.Values) (int, any) { return tc.status, tc.body })
			c := loginClient(t, srv.URL, NewMemoryLoginStore(0))
			state, _ := startState(t, c, LoginStartOptions{RedirectURI: "http://app/cb"})
			_, err := c.LoginComplete(ctx, "c", state, "http://app/cb")
			if codeOf(err) != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
			var e *Error
			if tc.status == 400 && (!errors.As(err, &e) || e.Status != 400) {
				t.Fatalf("want Status 400, got %v", err)
			}
		})
	}
}

func TestLoginCancelAndTTL(t *testing.T) {
	ctx := context.Background()
	c := loginClient(t, "http://x", NewMemoryLoginStore(0))
	state, _ := startState(t, c, LoginStartOptions{RedirectURI: "http://app/cb", ReturnTo: "/back"})
	if to, ok, err := c.LoginCancel(ctx, state); err != nil || !ok || to != "/back" {
		t.Fatalf("cancel = %q %v %v", to, ok, err)
	}
	if _, ok, _ := c.LoginCancel(ctx, state); ok {
		t.Fatal("cancel is one-shot")
	}
	s := NewMemoryLoginStore(time.Millisecond)
	_ = s.Put(ctx, "s", PendingLogin{CreatedAt: time.Now().Add(-time.Second)})
	if p, _ := s.Take(ctx, "s"); p != nil {
		t.Fatal("expired login returned")
	}
}
