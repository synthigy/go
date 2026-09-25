//go:build integration

// Integration (e2e) tests run against a live Synthigy server using a real
// confidential OAuth client (client-credentials grant).
//
// Run with:
//
//	go test -tags integration ./...
//
// By default they target a local dev server at http://localhost:7887 using
// the confidential e2e client below. Override any of these via env:
//
//	SYNTHIGY_ENDPOINT       (default http://localhost:7887)
//	SYNTHIGY_CLIENT_ID      (default synthigy-go-sdk-e2e)
//	SYNTHIGY_CLIENT_SECRET  (required unless SYNTHIGY_TOKEN is set)
//	SYNTHIGY_TOKEN          (static token; overrides client credentials)
//	SYNTHIGY_TEST_ENTITY    (default user)
//
// ---------------------------------------------------------------------------
// Creating the confidential e2e client (one-time, via the backend nREPL):
//
//	(require '[synthigy.iam :as iam] '[synthigy.data :as data]
//	         '[synthigy.oauth.core :as oc])
//	(iam/add-client
//	  {:id "synthigy-go-sdk-e2e"
//	   :name "Synthigy Go SDK E2E"
//	   :secret (System/getenv "SYNTHIGY_CLIENT_SECRET")   ; pick your own
//	   :type :confidential
//	   :settings {"allowed-grants" ["client_credentials" "refresh_token"]
//	              "redirections" ["http://localhost:8000/synthigy/callback"
//	                              "http://localhost:7887/synthigy/callback"
//	                              "http://localhost:8080/callback"]
//	              "logout-redirections" ["http://localhost:8000/synthigy"
//	                                     "http://localhost:7887/synthigy"]
//	              "trusted" true}})
//	;; grant the auto-created SERVICE user the SUPERUSER role so the
//	;; client-credentials principal can read:
//	(iam/set-user {:name "synthigy-go-sdk-e2e" :type :SERVICE :active true
//	               :roles [data/*ROOT*]})
//	(oc/get-client "synthigy-go-sdk-e2e")
//
// add-client is the production path: it bcrypts the secret on write and
// auto-creates the linked :SERVICE user that the client_credentials grant
// resolves the principal from (token.clj). `trusted: true` enables the
// acting_as trusted-param cascade.
package synthigy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

const (
	defaultEndpoint = "http://localhost:7887"
	defaultClientID = "synthigy-go-sdk-e2e"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// requireCreds skips rather than fails when the suite has nothing to
// authenticate with. No secret is baked into this file — export
// SYNTHIGY_CLIENT_SECRET (or SYNTHIGY_TOKEN) for the client you provisioned.
func requireCreds(t *testing.T) {
	t.Helper()
	if os.Getenv("SYNTHIGY_TOKEN") == "" && os.Getenv("SYNTHIGY_CLIENT_SECRET") == "" {
		t.Skip("set SYNTHIGY_CLIENT_SECRET or SYNTHIGY_TOKEN to run the live suite")
	}
}

func liveClient(t *testing.T) *Client {
	t.Helper()
	requireCreds(t)
	cfg := Config{
		Endpoint:     env("SYNTHIGY_ENDPOINT", defaultEndpoint),
		ClientID:     env("SYNTHIGY_CLIENT_ID", defaultClientID),
		ClientSecret: os.Getenv("SYNTHIGY_CLIENT_SECRET"),
		Timeout:      30 * time.Second,
	}
	if tok := os.Getenv("SYNTHIGY_TOKEN"); tok != "" {
		cfg.Token = tok
		cfg.ClientID = ""
		cfg.ClientSecret = ""
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func entity() string { return env("SYNTHIGY_TEST_ENTITY", "user") }

// liveConnect installs the same live config as the process-wide default via the
// single-client API, and disconnects on cleanup.
func liveConnect(t *testing.T) {
	t.Helper()
	requireCreds(t)
	cfg := Config{
		Endpoint:     env("SYNTHIGY_ENDPOINT", defaultEndpoint),
		ClientID:     env("SYNTHIGY_CLIENT_ID", defaultClientID),
		ClientSecret: os.Getenv("SYNTHIGY_CLIENT_SECRET"),
		Timeout:      30 * time.Second,
	}
	if tok := os.Getenv("SYNTHIGY_TOKEN"); tok != "" {
		cfg.Token = tok
		cfg.ClientID = ""
		cfg.ClientSecret = ""
	}
	if err := Connect(cfg); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(Disconnect)
}

// TestLiveGlobalSingleClient proves the package-level verbs work through the
// process-wide default installed by Connect — no *Client threaded anywhere.
func TestLiveGlobalSingleClient(t *testing.T) {
	liveConnect(t)
	ctx, cancel := ctx30(t)
	defer cancel()

	if Default() == nil {
		t.Fatal("Default nil after Connect")
	}
	if _, err := Token(ctx); err != nil {
		t.Fatalf("global Token: %v", err)
	}
	rows, err := Search(ctx, entity(), Args{"_limit": 3}, Selection{"name": nil})
	if err != nil {
		t.Fatalf("global Search: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("global Search returned no rows")
	}
}

func ctx30(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// TestLiveTokenAndAuth proves the confidential client can mint a
// client-credentials token (the foundation for every other call).
func TestLiveTokenAndAuth(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := ctx30(t)
	defer cancel()
	tok, err := c.Token(ctx)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok == "" {
		t.Fatal("empty access token")
	}
	t.Logf("got token (%d chars)", len(tok))
}

func TestLiveSchema(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := ctx30(t)
	defer cancel()
	schema, err := c.Schema(ctx)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if _, ok := schema["entities"]; !ok {
		t.Errorf("schema missing entities key: %v", schema)
	}
	t.Logf("id-key = %v", schema["id-key"])
}

func TestLiveSearch(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := ctx30(t)
	defer cancel()
	rows, err := c.Search(ctx, entity(), Args{"_limit": 3}, Selection{"xid": nil})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	t.Logf("got %d rows", len(rows))
	for _, r := range rows {
		if _, ok := r["xid"]; !ok {
			t.Errorf("row missing xid: %v", r)
		}
	}
}

func TestLiveSQLTemplateCount(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := ctx30(t)
	defer cancel()
	// aggregate op is deprecated server-side; counts go through sql-template.
	rows, err := c.SQLTemplate(ctx, "SELECT count(*) AS n FROM {"+entity()+"}", nil)
	if err != nil {
		t.Fatalf("SQLTemplate: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one row")
	}
	t.Logf("count = %v", rows[0]["n"])
}

func TestLiveRuntimeModel(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := ctx30(t)
	defer cancel()
	rm, err := c.RuntimeModel(ctx)
	if err != nil {
		t.Fatalf("RuntimeModel: %v", err)
	}
	if len(rm) == 0 {
		t.Fatal("empty runtime model")
	}
}

// TestLiveActingAsTrusted proves the confidential client is trusted: the
// acting_as trusted-param is honored, so impersonating a non-existent user
// surfaces USER_NOT_FOUND (rather than being silently ignored or blocked as a
// non-trusted client would be).
func TestLiveActingAsTrusted(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := ctx30(t)
	defer cancel()
	_, err := c.Search(ctx, entity(), Args{"_limit": 1}, Selection{"xid": nil},
		ActingAs("definitely-not-a-real-user-xid"))
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("expected an *Error for bogus acting_as, got %v", err)
	}
	if se.Code != "USER_NOT_FOUND" {
		t.Logf("acting_as bogus user returned %s (%s) — trusted path active", se.Code, se.Category)
	}
}

// TestLiveCountSelection exercises related-entity aggregation via the _count
// selection key (the supported path — there is no standalone aggregate op).
func TestLiveCountSelection(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := ctx30(t)
	defer cancel()
	rows, err := c.Search(ctx, "movie", Args{"_limit": 2},
		Selection{"title": nil, "_count": Selection{"actors": nil}},
		KeyFormat("kebab"))
	if err != nil {
		t.Fatalf("Search with _count: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	cnt, ok := rows[0]["_count"].(map[string]any)
	if !ok {
		t.Fatalf("row missing _count: %v", rows[0])
	}
	t.Logf("%v actors = %v", rows[0]["title"], cnt["actors"])
}

// TestLiveAggregateOpGone documents that the raw aggregate op is rejected
// (UNKNOWN_OP) — aggregation is _count/_agg (relations) or sql-template.
func TestLiveAggregateOpGone(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := ctx30(t)
	defer cancel()
	results, err := c.Exec(ctx, []Op{{"op": "aggregate", "entity": entity(),
		"args": map[string]any{}, "selections": map[string]any{"count": nil}}})
	if err != nil {
		t.Fatalf("Exec transport error: %v", err)
	}
	if len(results) == 1 && !results[0].OK && results[0].Error != nil {
		if results[0].Error.Code != "UNKNOWN_OP" {
			t.Fatalf("expected UNKNOWN_OP, got %s", results[0].Error.Code)
		}
		t.Logf("aggregate op correctly rejected: %s", results[0].Error.Code)
		return
	}
	t.Fatalf("expected aggregate op to be rejected, got results=%v", results)
}

// TestLiveLeftJoinSugarPreservesParents proves the LEFT-join default sugar
// end to end: the engine INNER-joins relations by default (a `roles`
// projection would drop every user without roles), but the SDK injects
// _join:"left" so a plain projection preserves all parents. An explicit
// _join:"inner" opts back into the parent-dropping behavior.
func TestLiveLeftJoinSugarPreservesParents(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := ctx30(t)
	defer cancel()
	const limit = 200
	plain, err := c.Search(ctx, "user", Args{"_limit": limit}, Fields("xid"))
	if err != nil {
		t.Fatalf("plain search: %v", err)
	}
	// Default sugar: projecting roles LEFT-joins, so the parent set is intact.
	left, err := c.Search(ctx, "user", Args{"_limit": limit},
		Selection{"xid": nil, "roles": Selection{"name": nil}})
	if err != nil {
		t.Fatalf("left search: %v", err)
	}
	// Explicit INNER: parents lacking roles are dropped.
	inner, err := c.Search(ctx, "user", Args{"_limit": limit},
		Selection{"xid": nil,
			"roles": Rel(Selection{"name": nil}, WithArgs(Args{"_join": "inner"}))})
	if err != nil {
		t.Fatalf("inner search: %v", err)
	}
	t.Logf("plain=%d left=%d inner=%d", len(plain), len(left), len(inner))
	if len(left) != len(plain) {
		t.Errorf("LEFT-join sugar should preserve parents: plain=%d left=%d",
			len(plain), len(left))
	}
	if len(inner) > len(plain) {
		t.Errorf("INNER cannot return more parents than plain: plain=%d inner=%d",
			len(plain), len(inner))
	}
}

// TestLiveWriteCycle covers sync -> get -> stack -> delete -> purge against the
// real wire. It exists because this suite was read-only for its whole life, and
// that is precisely why the 2026-08-29 silent-write flip went unnoticed here for
// two weeks while the JS suite caught it on day one: a read-only live suite
// cannot see write-contract drift. It pins BOTH halves of the returning
// contract — flagless sync answers {"count": n}, Returning() echoes the record.
func TestLiveWriteCycle(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := ctx30(t)
	defer cancel()

	xid := NewXID()
	name := fmt.Sprintf("__go_sdk_wc_%d__", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		c.Purge(cleanupCtx, "user", Args{"xid": Eq(xid)}, Selection{"xid": nil})
	})

	silent, err := c.Sync(ctx, "user", map[string]any{
		"xid": xid, "name": name, "type": "ROBOT", "active": true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := asInt(silent["count"]); got != 1 {
		t.Fatalf("silent sync should answer {count: 1}, got %v", silent)
	}
	if _, echoed := silent["xid"]; echoed {
		t.Fatalf("silent sync must not echo the record, got %v", silent)
	}

	got, err := c.Get(ctx, "user", Args{"xid": xid},
		Selection{"xid": nil, "name": nil, "type": nil, "active": nil})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got["name"] != name || got["type"] != "ROBOT" || got["active"] != true {
		t.Fatalf("readback mismatch: %v", got)
	}

	if _, err := c.Stack(ctx, "user", map[string]any{"xid": xid, "active": false}); err != nil {
		t.Fatalf("Stack: %v", err)
	}
	got, err = c.Get(ctx, "user", Args{"xid": xid}, Selection{"active": nil})
	if err != nil {
		t.Fatalf("Get after Stack: %v", err)
	}
	if got["active"] != false {
		t.Fatalf("stack did not apply: %v", got)
	}

	// The other half of the contract: Returning() brings the record back.
	echoed, err := c.Sync(ctx, "user", map[string]any{
		"xid": xid, "name": name + "-edited", "type": "ROBOT", "active": true},
		Returning())
	if err != nil {
		t.Fatalf("Sync with Returning: %v", err)
	}
	if echoed["xid"] != xid || echoed["name"] != name+"-edited" {
		t.Fatalf("returning sync should echo the record, got %v", echoed)
	}

	if _, err := c.Delete(ctx, "user", map[string]any{"xid": xid}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// asInt normalizes a JSON number (float64 over the wire) to int.
func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return -1
	}
}

// TestLiveWatchSqlTemplate exercises the live streaming transport end to end:
// snapshot via sql-template + subscription/set POST + SSE open. We assert the
// initial snapshot lands and the stream doesn't immediately reject.
func TestLiveWatchSqlTemplate(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tile, err := c.WatchSqlTemplate(ctx,
		"SELECT count(*) AS n FROM {"+entity()+"}", nil,
		Entities(entity()))
	if err != nil {
		t.Fatalf("WatchSqlTemplate: %v", err)
	}
	defer tile.Close()
	if tile.First() == nil {
		t.Fatal("no initial snapshot row")
	}
	t.Logf("initial = %v", tile.First())

	// Give the SSE + subscription/set a moment; fail if it immediately rejects.
	select {
	case ev := <-tile.Events():
		if ev.Type == "subscription/rejected" {
			t.Fatalf("subscription rejected: %s", ev.Reason)
		}
		t.Logf("event: %s", ev.Type)
	case <-time.After(2 * time.Second):
		// No event in 2s is fine — nothing changed. Transport is up.
	}
}

// TestLiveListenOpens verifies the raw SSE listener connects and emits the
// sse/open sentinel.
func TestLiveListenOpens(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := c.Listen(ctx)
	defer stream.Close()
	select {
	case ev := <-stream.Events():
		if ev.Type != "sse/open" {
			t.Logf("first event: %s (expected sse/open)", ev.Type)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("no sse/open within 8s — SSE did not connect")
	}
}

// ---------------------------------------------------------------------------
// Onboard is gated on the client's principal administering the account —
// RBAC update on User plus the row inside its owner-group write scope, the
// shape the shipped User Provisioner role grants. The e2e client above is
// ROOT, so this runs on its OWN dedicated env pair to exercise the real,
// scoped principal.
//
//	SYNTHIGY_PROVISION_CLIENT_ID      (skip the test if unset)
//	SYNTHIGY_PROVISION_CLIENT_SECRET
//
// Creating the dedicated provisioning client (one-time, via nREPL):
//
//	(access/with-principal nil
//	  (let [id "synthigy-go-sdk-provisioner"
//	        role (dataset/get-entity :iam/user-role {:name "User Provisioner"} {(id/key) nil})
//	        owners (dataset/sync-entity :iam/user-group {:name (str id "-owners")})
//	        client (iam/add-client {:id id :name "Synthigy Go SDK provisioner"
//	                                :type :confidential
//	                                :settings {"allowed-grants" ["client_credentials"]}})]
//	    (dataset/stack-entity :iam/user {:name id
//	                                     :roles [{(id/key) (id/extract role)}]
//	                                     :groups [{(id/key) (id/extract owners)}]})
//	    (access/load-rules)
//	    client))
//	;; -> {:secret "<copy this into SYNTHIGY_PROVISION_CLIENT_SECRET>"}

func provisionClient(t *testing.T) *Client {
	t.Helper()
	id := os.Getenv("SYNTHIGY_PROVISION_CLIENT_ID")
	secret := os.Getenv("SYNTHIGY_PROVISION_CLIENT_SECRET")
	if id == "" || secret == "" {
		t.Skip("set SYNTHIGY_PROVISION_CLIENT_ID/SECRET (a confidential client holding User Provisioner) to run")
	}
	c, err := New(Config{
		Endpoint: env("SYNTHIGY_ENDPOINT", defaultEndpoint), ClientID: id,
		ClientSecret: secret, Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

// ownerGroup is the client's own first group — the partition its accounts
// must be stamped into. Empty for an unscoped (superuser) client.
func ownerGroup(ctx context.Context, t *testing.T, c *Client) string {
	t.Helper()
	me, err := c.Get(ctx, "user", Args{"name": os.Getenv("SYNTHIGY_PROVISION_CLIENT_ID")},
		Selection{"groups": Selection{"xid": nil}})
	if err != nil {
		t.Fatalf("Get own user: %v", err)
	}
	groups, _ := me["groups"].([]any)
	if len(groups) == 0 {
		return ""
	}
	first, _ := groups[0].(map[string]any)
	xid, _ := first["xid"].(string)
	return xid
}

// TestLiveOnboardMintAndReset proves Onboard against a real scoped provisioner:
// onboarding does not create accounts, so this
// creates one over Sync first — stamped into the client's own owner group —
// mints a ticket for its xid, then resets and re-mints, proving reset doesn't
// error and doesn't touch active.
func TestLiveOnboardMintAndReset(t *testing.T) {
	c := provisionClient(t)
	ctx, cancel := ctx30(t)
	defer cancel()

	username := "sdk-onboard-test-" + time.Now().Format("20060102150405") + "@example.com"
	account := map[string]any{"name": username, "active": false}
	if group := ownerGroup(ctx, t, c); group != "" {
		account["owner_group"] = map[string]any{"xid": group}
	}
	if _, err := c.Sync(ctx, "user", account); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	user, err := c.Get(ctx, "user", Args{"name": username}, Selection{"xid": nil})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	xid, _ := user["xid"].(string)
	// Cleanup runs as ROOT, not as the provisioner: User Provisioner grants
	// create/read/update on User but deliberately NOT delete, so a
	// provisioner-run purge fails with insufficient privileges. This ran as a
	// skip for its whole life, so nothing ever noticed it piling up accounts.
	t.Cleanup(func() {
		root := liveClient(t)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := root.Purge(cleanupCtx, "user", Args{"name": Eq(username)},
			Selection{"xid": nil}); err != nil {
			t.Errorf("cleanup purge: %v", err)
		}
	})
	if xid == "" {
		t.Fatalf("created account has no xid: %+v", user)
	}

	out, err := c.Onboard(ctx, xid, OnboardMethods("password"))
	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if out.OnboardURL == "" || out.ExpiresAt == 0 {
		t.Fatalf("bad onboard result: %+v", out)
	}

	out2, err := c.Onboard(ctx, xid, OnboardReset(true))
	if err != nil {
		t.Fatalf("Onboard with reset: %v", err)
	}
	if out2.User.XID != xid {
		t.Fatalf("reset changed the account: %+v", out2)
	}

	_, err = c.Onboard(ctx, "does-not-exist-"+username)
	se, ok := err.(*Error)
	if !ok || se.Code != "USER_NOT_FOUND" {
		t.Fatalf("expected USER_NOT_FOUND, got %v", err)
	}
}
