//go:build integration

package synthigy

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// browserLogin plays the browser: authorize → submit credentials → callback params.
func browserLogin(t *testing.T, endpoint, authorizeURL, user, password string) url.Values {
	t.Helper()
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	location := func(resp *http.Response, err error) url.Values {
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		u, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return u.Query()
	}
	page := location(hc.Get(authorizeURL))
	return location(hc.PostForm(endpoint+"/oauth/login", url.Values{
		"username": {user}, "password": {password}, "state": {page.Get("state")}}))
}

func TestLiveLogin(t *testing.T) {
	id, secret := os.Getenv("SYNTHIGY_TEST_LOGIN_CLIENT_ID"), os.Getenv("SYNTHIGY_TEST_LOGIN_CLIENT_SECRET")
	user, password := os.Getenv("SYNTHIGY_TEST_LOGIN_USER"), os.Getenv("SYNTHIGY_TEST_LOGIN_PASSWORD")
	if id == "" || secret == "" || user == "" || password == "" {
		t.Skip("set SYNTHIGY_TEST_LOGIN_CLIENT_ID/SECRET/USER/PASSWORD")
	}
	endpoint := os.Getenv("SYNTHIGY_TEST_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:7887"
	}
	redirect := os.Getenv("SYNTHIGY_TEST_LOGIN_REDIRECT")
	if redirect == "" {
		redirect = "http://127.0.0.1:9/py-sdk/callback"
	}
	ctx := context.Background()
	store := NewMemoryLoginStore(0)
	c, err := New(Config{Endpoint: endpoint, ClientID: id, ClientSecret: secret, LoginStore: store})
	if err != nil {
		t.Fatal(err)
	}
	var xids []string
	for i := 0; i < 2; i++ {
		raw, err := c.LoginStart(ctx, LoginStartOptions{RedirectURI: redirect, ReturnTo: "/after", Scope: "openid profile"})
		if err != nil {
			t.Fatal(err)
		}
		cb := browserLogin(t, endpoint, raw, user, password)
		res, err := c.LoginComplete(ctx, cb.Get("code"), cb.Get("state"), redirect)
		if err != nil {
			t.Fatal(err)
		}
		if res.User.Name != user || res.User.XID == "" || res.ReturnTo != "/after" ||
			!strings.Contains(strings.Join(res.User.Scopes, " "), "openid") || res.Tokens["access_token"] == "" {
			t.Fatalf("result %+v", res)
		}
		xids = append(xids, res.User.XID)
	}
	if xids[0] != xids[1] {
		t.Fatalf("xid changed across logins: %v", xids)
	}

	bad, _ := New(Config{Endpoint: endpoint, ClientID: id, ClientSecret: "wrong", LoginStore: store})
	raw, _ := c.LoginStart(ctx, LoginStartOptions{RedirectURI: redirect})
	cb := browserLogin(t, endpoint, raw, user, password)
	_, err = bad.LoginComplete(ctx, cb.Get("code"), cb.Get("state"), redirect)
	var e *Error
	if codeOf(err) != "LOGIN_EXCHANGE_FAILED" || !errors.As(err, &e) || (e.Status != 400 && e.Status != 401) {
		t.Fatalf("wrong secret: %v", err)
	}
}
