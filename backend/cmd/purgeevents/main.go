// Command purgeevents deletes events older than the retention window. It is
// an admin tool run by hand or by a scheduler; there is no HTTP route for it.
//
//	DATABASE_URL=... go run ./cmd/purgeevents                  # 90 days
//	DATABASE_URL=... go run ./cmd/purgeevents -dry-run
//	HELIOS_EVENT_RETENTION_DAYS=60 DATABASE_URL=... go run ./cmd/purgeevents
//
// Events of experiments that are running, or stopped inside the window, are
// never deleted (see internal/platform/eventpurge). It prints counts only. It
// is safe to run repeatedly and concurrently.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"time"

	"helios/backend/internal/platform/config"
	"helios/backend/internal/platform/db"
	"helios/backend/internal/platform/eventpurge"
)

func main() {
	days := flag.Int("retention-days", envInt("HELIOS_EVENT_RETENTION_DAYS", 90), "keep events newer than this many days (minimum 7); env HELIOS_EVENT_RETENTION_DAYS")
	batch := flag.Int("batch", eventpurge.DefaultBatchSize, "rows deleted per statement")
	maxBatches := flag.Int("max-batches", 0, "stop after this many batches (0: until done)")
	pause := flag.Duration("pause", 0, "wait between batches")
	timeout := flag.Duration("timeout", 30*time.Minute, "give up after this long")
	dry := flag.Bool("dry-run", false, "count what would be deleted, delete nothing")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	pool, err := db.Connect(ctx, config.Load().DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	st, err := eventpurge.Purge(ctx, pool, eventpurge.Options{
		Retention: time.Duration(*days) * 24 * time.Hour, BatchSize: *batch,
		MaxBatches: *maxBatches, Pause: *pause, DryRun: *dry,
	})
	verb := "deleted"
	if *dry {
		verb = "would delete"
	}
	log.Printf("purgeevents: %s %d events older than %s across %d environments in %d batches (truncated: %v)",
		verb, st.Deleted, st.Cutoff.UTC().Format(time.RFC3339), st.Environments, st.Batches, st.Truncated)
	if err != nil {
		log.Fatalf("purgeevents: stopped early: %v", err) // the pgx error carries no event content
	}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}
