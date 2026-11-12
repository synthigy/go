//go:build integration

// Live: a trusted client's live XSQL list acting as a user sees that user's rows
// only (RLS), on the snapshot and on every refetch. Runs with the options
// generated Watch* ops pass: ActingAs + WireOp + Entity.
//
// Needs the guarded Project Management model (examples/datasets/
// project-management@0.1.2 + seed/) and a trusted superuser client:
//
//	SYNTHIGY_RLS_TEST_ENDPOINT
//	SYNTHIGY_RLS_TEST_CLIENT_ID  SYNTHIGY_RLS_TEST_CLIENT_SECRET
//	SYNTHIGY_RLS_TEST_ALICE      SYNTHIGY_RLS_TEST_BOB   (user xids)
package synthigy

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"
)

const rlsList = "@search list\nproject\n  xid\n  name\n  description"

func rlsNames(rows []Record) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if n, ok := r["name"].(string); ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func rlsFind(rows []Record, name string) Record {
	for _, r := range rows {
		if r["name"] == name {
			return r
		}
	}
	return nil
}

func TestWatchActingAsLive(t *testing.T) {
	endpoint := os.Getenv("SYNTHIGY_RLS_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set SYNTHIGY_RLS_TEST_ENDPOINT, the client and the ALICE/BOB xids")
	}
	alice, bob := os.Getenv("SYNTHIGY_RLS_TEST_ALICE"), os.Getenv("SYNTHIGY_RLS_TEST_BOB")
	c, err := New(Config{Endpoint: endpoint, ClientID: os.Getenv("SYNTHIGY_RLS_TEST_CLIENT_ID"),
		ClientSecret: os.Getenv("SYNTHIGY_RLS_TEST_CLIENT_SECRET"), Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	plain, err := c.QueryRecords(ctx, rlsList, nil, ActingAs(alice))
	if err != nil {
		t.Fatal(err)
	}
	alpha, beta := rlsFind(plain, "Project Alpha"), rlsFind(plain, "Project Beta")
	if alpha == nil || beta != nil {
		t.Fatalf("plain query as alice = %v", rlsNames(plain))
	}
	bobs, err := c.QueryRecords(ctx, rlsList, nil, ActingAs(bob))
	if err != nil {
		t.Fatal(err)
	}
	if beta = rlsFind(bobs, "Project Beta"); beta == nil {
		t.Fatalf("bob does not see his project: %v", rlsNames(bobs))
	}

	w, err := c.WatchQueryXSQL(ctx, rlsList, nil, ActingAs(alice), WireOp("search"), Entity("project"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if got, want := rlsNames(w.List()), rlsNames(plain); !equalStrings(got, want) {
		t.Fatalf("watch snapshot = %v, plain query = %v", got, want)
	}

	run := "go-" + NewXID()
	if _, err := c.Sync(ctx, "project", map[string]any{"xid": alpha["xid"], "description": "alice " + run}, ActingAs(alice)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sync(ctx, "project", map[string]any{"xid": beta["xid"], "description": "bob " + run}, ActingAs(bob)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if r := rlsFind(w.List(), "Project Alpha"); r != nil && r["description"] == "alice "+run {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("alice's change never reached her watch: %v", w.List())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got, want := rlsNames(w.List()), rlsNames(plain); !equalStrings(got, want) {
		t.Fatalf("after refetch = %v, want alice's rows %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
