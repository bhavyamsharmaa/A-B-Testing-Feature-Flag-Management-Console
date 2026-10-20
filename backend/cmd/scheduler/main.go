// Command scheduler runs the scheduled changes that are due, once, and exits.
// It is how scheduled changes are executed without any long-lived process: a
// Cloud Run Job, a cron entry or a person runs it, as often as they like (every
// minute is typical), from as many places as they like at once. See
// docs/SCHEDULING.md.
//
//	DATABASE_URL=... go run ./cmd/scheduler
//	DATABASE_URL=... REDIS_URL=... go run ./cmd/scheduler -grace 10m
//
// Each run: marks changes overdue by more than the grace window as missed,
// claims the due ones (a lease, FOR UPDATE SKIP LOCKED), applies each in its own
// transaction through the ordinary edit code (approval gate, audit, version bump)
// after re-checking the creator's role as it is now, and, with REDIS_URL set,
// tells the SDK streams. Without REDIS_URL the changes still apply; streams just
// learn at their next poll. It prints counts only.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"helios/backend/internal/controlplane/scheduling"
	"helios/backend/internal/platform/config"
	"helios/backend/internal/platform/db"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/redisx"
)

func main() {
	grace := flag.Duration("grace", envDuration("SCHEDULER_GRACE", 15*time.Minute), "a change overdue by more than this is marked missed, not applied (env SCHEDULER_GRACE)")
	maxRows := flag.Int("max-rows", 200, "handle at most this many changes in one run")
	timeout := flag.Duration("timeout", 5*time.Minute, "give up after this long")
	flag.Parse()
	if *grace < time.Minute || *grace > 24*time.Hour {
		log.Fatalf("scheduler: -grace must be between 1m and 24h")
	}

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
		client, err := redisx.Connect(ctx, cfg.RedisURL)
		if err != nil {
			log.Printf("scheduler: Redis unavailable, changes will apply without telling the streams: %v", err)
		} else {
			defer client.Close()
			pub = redisx.Publisher{Client: client}
		}
	}

	st, err := scheduling.NewRunner(pool, pub, nil, scheduling.Config{Grace: *grace, MaxRows: *maxRows}).Run(ctx)
	log.Printf("scheduler: claimed %d (reclaimed %d), succeeded %d, failed %d, retrying %d, missed %d, skipped %d, released %d",
		st.Claimed, st.Reclaimed, st.Succeeded, st.Failed, st.Retried, st.Missed, st.Skipped, st.Released)
	if err != nil {
		log.Fatalf("scheduler: stopped early: %v", err) // the pgx error carries no flag or rule content
	}
}

func envDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Fatalf("scheduler: %s is not a duration: %q", name, v)
		}
		return d
	}
	return def
}
