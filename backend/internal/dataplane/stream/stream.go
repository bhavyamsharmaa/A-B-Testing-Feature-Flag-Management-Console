// Package stream implements GET /sdk/stream: an SSE feed of flag-change
// notifications for SDKs that want push updates instead of polling.
//
// This is purely a propagation optimization. If it's unavailable (no
// subscriber configured, or the broker is down), it fails fast with 503
// rather than accepting a connection that will never deliver anything —
// /evaluate's correctness never depends on this endpoint working.
package stream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/httpx"
)

// KeyChecker tells a long-lived stream whether its SDK key is still valid.
type KeyChecker interface {
	Active(ctx context.Context, prefix string) (bool, error)
}

// Handler authenticates like /evaluate (SDK key decides the workspace and
// environment, via apikey.Middleware) and streams that environment's
// flag-change channel to the client as Server-Sent Events until the client
// disconnects. A key is only verified when the connection opens, so every
// `recheck` the stream asks the checker whether the key was revoked since and
// closes if it was: a revoked key must not keep receiving flag changes.
func Handler(sub events.Subscriber, checker KeyChecker, recheck time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, ok := apikey.ScopeFrom(r.Context())
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "SDK key required")
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			httpx.WriteInternal(w, r, errors.New("stream: ResponseWriter does not support flushing"))
			return
		}

		msgs, closeStream, err := sub.Subscribe(r.Context(), events.FlagsChannel(scope.WorkspaceID, scope.EnvironmentID))
		if err != nil {
			httpx.WriteError(w, http.StatusServiceUnavailable, "STREAM_UNAVAILABLE",
				"propagation stream unavailable; fall back to polling")
			return
		}
		// The one place this subscription gets released. r.Context() is
		// cancelled when the client disconnects, which unblocks the range
		// below and reaches this defer — no goroutine or subscription leak
		// on a dropped SDK connection.
		defer closeStream()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (e.g. nginx) so events aren't delayed
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		var tick <-chan time.Time
		if checker != nil && recheck > 0 {
			t := time.NewTicker(recheck)
			defer t.Stop()
			tick = t.C
		}

		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick:
				// An error keeps the stream open (a database blip must not cut
				// every SDK off); only a definite "revoked" closes it.
				if active, err := checker.Active(r.Context(), scope.Prefix); err == nil && !active {
					return
				}
			case payload, ok := <-msgs:
				if !ok {
					return
				}
				fmt.Fprintf(w, "event: flag_update\ndata: %s\n\n", payload)
				flusher.Flush()
			}
		}
	}
}
