// Command outbox delivers what is waiting in the outbox (webhook deliveries and invite
// emails) once, and exits. Like cmd/scheduler it keeps no process alive: a Cloud Run
// Job, a cron entry or a person runs it, as often as they like (every minute is typical),
// from as many places as they like at once; the claim is a lease, so two runs never
// deliver the same row at the same moment. See docs/OUTBOX.md.
//
//	DATABASE_URL=... WEBHOOK_SECRET_KEY=... go run ./cmd/outbox
//	DATABASE_URL=... go run ./cmd/outbox -purge-only
//	DATABASE_URL=... WEBHOOK_SECRET_KEY=... WEBHOOK_SECRET_KEY_PREVIOUS=... go run ./cmd/outbox -rekey
//
// It prints counts only: never a payload, a URL, an address or a secret.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/controlplane/webhooks"
	"helios/backend/internal/delivery"
	"helios/backend/internal/platform/config"
	"helios/backend/internal/platform/db"
)

func main() {
	maxRows := flag.Int("max-rows", 500, "handle at most this many deliveries in one run")
	concurrency := flag.Int("concurrency", 8, "simultaneous deliveries")
	perTarget := flag.Int("per-target", 2, "simultaneous deliveries to one endpoint or recipient")
	timeout := flag.Duration("timeout", 5*time.Minute, "give up after this long")
	purgeOnly := flag.Bool("purge-only", false, "only delete finished rows past their retention")
	rekey := flag.Bool("rekey", false, "re-encrypt every webhook secret under the current WEBHOOK_SECRET_KEY, then exit")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	cfg := config.Load()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	opts, err := delivery.FromConfig(cfg)
	if err != nil {
		log.Fatalf("outbox: %v", err)
	}
	opts.Outbox = outbox.Config{MaxRows: *maxRows, Concurrency: *concurrency, PerTarget: *perTarget}

	switch {
	case *rekey:
		n, err := webhooks.Rekey(ctx, pool, opts.Box)
		log.Printf("outbox: re-encrypted %d webhook secret(s) under the current key", n)
		if err != nil {
			log.Fatalf("outbox: rekey stopped: %v", err)
		}
	case *purgeOnly:
		n, err := outbox.Purge(ctx, pool, time.Now())
		log.Printf("outbox: purged %d finished row(s)", n)
		if err != nil {
			log.Fatalf("outbox: purge stopped: %v", err)
		}
	default:
		if !opts.Box.Available() {
			log.Printf("outbox: WEBHOOK_SECRET_KEY is not set: webhook deliveries will wait; invite email is unaffected")
		}
		st, err := delivery.NewWorker(pool, opts).Run(ctx)
		log.Printf("outbox: claimed %d (reclaimed %d), delivered %d, retrying %d, dead %d, lease lost %d, purged %d",
			st.Claimed, st.Reclaimed, st.Delivered, st.Retrying, st.Dead, st.LeaseLost, st.Purged)
		if err != nil {
			log.Fatalf("outbox: stopped early: %v", err)
		}
	}
}
