package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	synthigy "github.com/synthigy/go"
)

// Seed fixtures live in ../datasets/seed/ (committed JSON, exported from the
// reference model). They carry both euuid + xid so /data sync never drops a
// relation ref. Order matters: role before users (users reference it), entities
// before the rows that link them.
var seedFixtures = []struct {
	file   string
	entity string
	array  bool
}{
	{"role.json", "user_role", false},
	{"users.json", "user", true},
	{"group.json", "user_group", false},
	{"projects.json", "project", true},
	{"tasks.json", "project_task", true},
	{"allocations.json", "allocation", true},
}

// seedFromFixtures upserts the demo's committed JSON seed over /data as the BFF
// service user (full write visibility). `sync` upserts by xid, so a re-run
// resets to the pristine seeded state — no duplicates, no REPL, no Clojure.
func (s *server) seedFromFixtures(ctx context.Context) error {
	for _, f := range seedFixtures {
		raw := readFirst(
			"../datasets/seed/"+f.file,
			"sdk/go/examples/rls-demo/datasets/seed/"+f.file,
		)
		if raw == "" {
			return fmt.Errorf("seed fixture not found: datasets/seed/%s", f.file)
		}
		recs, err := decodeFixture(raw, f.array)
		if err != nil {
			return fmt.Errorf("%s: %w", f.file, err)
		}
		for _, rec := range recs {
			if _, err := synthigy.Sync(ctx, f.entity, rec); err != nil {
				return fmt.Errorf("sync %s: %w", f.entity, err)
			}
		}
	}
	return nil
}

func decodeFixture(raw string, array bool) ([]map[string]any, error) {
	if array {
		var recs []map[string]any
		return recs, json.Unmarshal([]byte(raw), &recs)
	}
	var one map[string]any
	if err := json.Unmarshal([]byte(raw), &one); err != nil {
		return nil, err
	}
	return []map[string]any{one}, nil
}

// ensureSeeded seeds the demo only if it looks empty (the roster's first user
// can't be resolved) — so a cold start against a fresh server self-seeds.
func (s *server) ensureSeeded(ctx context.Context) {
	u, err := synthigy.Get(ctx, "user",
		synthigy.Args{"xid": roster[0].XID},
		synthigy.Selection{"xid": nil})
	if err == nil && u != nil {
		return // already seeded
	}
	if err := s.seedFromFixtures(ctx); err != nil {
		log.Printf("startup seed failed (continuing; use /reseed): %v", err)
		return
	}
	log.Printf("cold start: seeded demo data from ../datasets/seed over /data")
}
