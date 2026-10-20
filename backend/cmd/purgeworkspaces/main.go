// Command purgeworkspaces removes the data of workspaces whose 14-day deletion grace period has ended, in bounded
// batches, and expires stale ownership offers. Like cmd/scheduler and cmd/outbox it keeps no process alive: a cron
// entry or a person runs it (hourly is plenty), from as many places as they like; it is safe to repeat, to run
// concurrently, and to kill half way (the next run carries on).
//
//	DATABASE_URL=... go run ./cmd/purgeworkspaces
//	DATABASE_URL=... REDIS_URL=... go run ./cmd/purgeworkspaces -batch 1000 -pause 100ms
//
// It prints counts only: never a workspace's name or content. See docs/WORKSPACE_DELETION.md.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"helios/backend/internal/controlplane/wspurge"
	"helios/backend/internal/platform/config"
	"helios/backend/internal/platform/db"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/redisx"
)

func main() {
	batch := flag.Int("batch", 2000, "rows deleted per statement")
	max := flag.Int("max-workspaces", 20, "purge at most this many workspaces in one run")
	pause := flag.Duration("pause", 0, "wait between batches, to leave room for other writers")
	timeout := flag.Duration("timeout", 30*time.Minute, "give up after this long")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	cfg := config.Load()
	config.MustBeSafe(cfg, config.RoleJob)
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	var pub events.Publisher = events.NoopPublisher{}
	if cfg.RedisURL != "" {
		if client, err := redisx.Connect(ctx, cfg.RedisURL); err != nil {
			log.Printf("purgeworkspaces: Redis unavailable, instances will drop the workspace within their cache TTL: %v", err)
		} else {
			defer client.Close()
			pub = redisx.Publisher{Client: client}
		}
	}

	st, err := wspurge.New(pool, wspurge.Config{BatchSize: *batch, MaxWorkspaces: *max, Pause: *pause, Pub: pub}).Run(ctx)
	log.Printf("purgeworkspaces: offers expired %d, workspaces purged %d, batches %d, rows %d", st.OffersExpired, st.Purged, st.Batches, st.Rows)
	if err != nil {
		log.Fatalf("purgeworkspaces: stopped early: %v", err) // an id and a database error: no names, no content
	}
}
