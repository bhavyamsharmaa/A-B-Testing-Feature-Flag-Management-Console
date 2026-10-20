// Package graceful serves an http.Server until told to stop, then shuts it down in a fixed order inside a fixed budget.
// The platform sends SIGTERM and, after a grace period (Render and Cloud Run both default to about 10 seconds), SIGKILL;
// everything here exists so that the process has finished, cleanly, before that.
//
// The order, and why:
//
//  1. Drain the lifecycle. /readyz answers "not ready" from this instant, new streams are refused, open streams are
//     sent a reconnect event and closed, and job endpoints stop claiming work. The platform's router stops sending
//     us new requests because of the first of those; clients reconnect elsewhere because of the second.
//  2. Wait ReadyDelay (default 1 s) so a load balancer that polls readiness has seen it. Capped at a quarter of the budget.
//  3. Shutdown: stop accepting, let in-flight requests finish, until the budget is spent.
//  4. If the budget is spent first, close what is left. The caller then closes the database pool.
//
// Whatever a killed request had claimed is recovered by lease expiry (docs/OPERATIONS.md); the point of 1-3 is that,
// normally, nothing is left to recover.
package graceful

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	"helios/backend/internal/platform/lifecycle"
)

// ErrBudget means requests were still running when the budget ended and were cut off.
var ErrBudget = errors.New("graceful: the shutdown budget ended with requests still in flight; they were closed")

type Options struct {
	Life       *lifecycle.State
	Timeout    time.Duration // the whole shutdown, from the signal; default 8 s
	ReadyDelay time.Duration // step 2; default 1 s
}

// Serve runs srv on ln until it fails or ctx ends (the signal), then shuts down. A nil error means a clean, in-budget exit.
func Serve(ctx context.Context, srv *http.Server, ln net.Listener, o Options) error {
	if o.Timeout <= 0 {
		o.Timeout = 8 * time.Second
	}
	if o.ReadyDelay < 0 {
		o.ReadyDelay = 0
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	start := time.Now()
	log.Printf("shutdown: signal received; not ready from now on, streams told to reconnect")
	o.Life.Drain()
	if d := min(o.ReadyDelay, o.Timeout/4); d > 0 {
		time.Sleep(d)
	}
	sctx, cancel := context.WithDeadline(context.Background(), start.Add(o.Timeout))
	defer cancel()
	err := srv.Shutdown(sctx)
	if err != nil {
		_ = srv.Close() // the budget is spent: cut off what is left
		<-errc
		log.Printf("shutdown: budget of %s ended with requests in flight; closed them", o.Timeout)
		return ErrBudget
	}
	<-errc
	log.Printf("shutdown: drained in %s", time.Since(start).Round(time.Millisecond))
	return nil
}
