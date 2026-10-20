package track

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"helios/backend/internal/platform/httpx"
)

// ErrBusy means a batch waited its whole allowance for a database turn.
var ErrBusy = errors.New("track: the database is busy")

// Gate bounds how many /track batches use the database at the same time, and how long one may wait for its turn.
//
// WHY: without it, a burst of batches queues inside the connection pool without limit; every one of them is still
// answered eventually, usually after the SDK has given up, so the server does work nobody is waiting for and the
// evaluate path and the console share a pool that /track has filled. With it, at most Max batches write at once, a
// batch that cannot get a turn within Wait is refused at once with 503 and a Retry-After (nothing was written, and
// events carry ids, so the retry is safe), and the rest of the pool stays available to everything else.
//
// WHAT IT DOES NOT DO: it never acknowledges an event it has not written. An accepted batch holds a turn until its
// INSERT has returned, and only then is it answered.
type Gate struct {
	slots    chan struct{}
	wait     time.Duration
	waiting  atomic.Int64
	refused  atomic.Uint64
	admitted atomic.Uint64
}

// NewGate: max concurrent batches (at least 1) and the longest wait for a turn.
func NewGate(max int, wait time.Duration) *Gate {
	if max < 1 {
		max = 1
	}
	return &Gate{slots: make(chan struct{}, max), wait: wait}
}

// Acquire returns the function that gives the turn back. It fails with ErrBusy after the wait, or with the context's
// error if the caller leaves first.
func (g *Gate) Acquire(ctx context.Context) (func(), error) {
	select {
	case g.slots <- struct{}{}: // a free turn: no timer, no waiting
	default:
		g.waiting.Add(1)
		defer g.waiting.Add(-1)
		t := time.NewTimer(g.wait)
		defer t.Stop()
		select {
		case g.slots <- struct{}{}:
		case <-t.C:
			g.refused.Add(1)
			return nil, ErrBusy
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	g.admitted.Add(1)
	return func() { <-g.slots }, nil
}

// GateStats are counters for tests and logs.
type GateStats struct {
	InUse, Capacity, Waiting int
	Admitted, Refused        uint64
}

func (g *Gate) Stats() GateStats {
	return GateStats{InUse: len(g.slots), Capacity: cap(g.slots), Waiting: int(g.waiting.Load()), Admitted: g.admitted.Load(), Refused: g.refused.Load()}
}

// retryAfterSeconds spreads the retries of a refused burst over 1-3 seconds so they do not return together.
func retryAfterSeconds() string { return strconv.Itoa(1 + rand.IntN(3)) }

// writeBusy answers 503 TRACK_BUSY with Retry-After. Nothing was stored.
func writeBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", retryAfterSeconds())
	httpx.WriteError(w, http.StatusServiceUnavailable, "TRACK_BUSY", "the server is busy; nothing was stored; retry the same batch (same event ids) after the time in Retry-After")
}
