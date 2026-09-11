// Command bff is a minimal example of using the Synthigy Go SDK from a
// service / BFF: client-credentials auth, a search, a live SQL-template
// dashboard tile, and a record observe loop.
//
// Run with:
//
//	SYNTHIGY_ENDPOINT=https://synthigy.example.com \
//	SYNTHIGY_CLIENT_ID=my-service \
//	SYNTHIGY_CLIENT_SECRET=... \
//	go run ./examples/bff
package main

import (
	"context"
	"log"
	"os"
	"time"

	synthigy "github.com/synthigy/go"
)

func main() {
	endpoint := os.Getenv("SYNTHIGY_ENDPOINT")
	if endpoint == "" {
		log.Fatal("set SYNTHIGY_ENDPOINT (and SYNTHIGY_CLIENT_ID / SYNTHIGY_CLIENT_SECRET or SYNTHIGY_TOKEN)")
	}

	cfg := synthigy.Config{
		Endpoint:     endpoint,
		ClientID:     os.Getenv("SYNTHIGY_CLIENT_ID"),
		ClientSecret: os.Getenv("SYNTHIGY_CLIENT_SECRET"),
		Timeout:      30 * time.Second,
		KeepAlive:    true,
	}
	if tok := os.Getenv("SYNTHIGY_TOKEN"); tok != "" {
		cfg.Token = tok
	}

	// Single-client model: install the process-wide default; every synthigy.*
	// verb operates on it. Per-user identity is multiplexed with ActingAs.
	if err := synthigy.Connect(cfg); err != nil {
		log.Fatal(err)
	}
	defer synthigy.Disconnect()

	ctx := context.Background()

	// 1. A simple RLS-scoped read on behalf of an end user.
	actingAs := os.Getenv("SYNTHIGY_ACTING_AS")
	users, err := synthigy.Search(ctx, "user",
		synthigy.Args{"active": synthigy.Eq(true), "_limit": 5},
		synthigy.Selection{"name": nil},
		synthigy.ActingAs(actingAs),
	)
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	log.Printf("found %d users", len(users))
	for _, u := range users {
		log.Printf("  - %v", u["name"])
	}

	// 2. A live analytics tile via watchSqlTemplate.
	tile, err := synthigy.WatchSqlTemplate(ctx,
		"SELECT count(*) AS n FROM {user}", nil,
		synthigy.Entities("user"))
	if err != nil {
		log.Fatalf("watchSqlTemplate: %v", err)
	}
	defer tile.Close()
	log.Printf("initial user count: %v", tile.First())

	go func() {
		for ev := range tile.Events() {
			if ev.Type == "result/changed" {
				log.Printf("user count changed: %v", tile.First())
			}
		}
	}()

	// 3. Observe changes to specific records (notify-then-refetch).
	if len(users) > 0 {
		xid, _ := users[0]["xid"].(string)
		if xid != "" {
			watchCtx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			stream, err := synthigy.Observe(watchCtx, synthigy.RecordsDesc(xid))
			if err != nil {
				log.Fatalf("observe: %v", err)
			}
			defer stream.Close()
			log.Printf("observing record %s for 60s...", xid)
			for ev := range stream.Events() {
				log.Printf("  event: %s on %s", ev.Type, ev.RecordXID)
			}
		}
	}
}
