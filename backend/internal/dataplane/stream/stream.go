// Package stream implements GET /sdk/stream: an SSE feed of flag-change
// notifications for SDKs that want push updates instead of polling.
//
// This is purely a propagation optimization. If it's unavailable (no
// subscriber configured, or the broker is down), it fails fast with 503
// rather than accepting a connection that will never deliver anything —
// /evaluate's correctness never depends on this endpoint working.
package stream

import (
	"errors"
	"fmt"
	"net/http"

	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/httpx"
)

// Handler authenticates like /evaluate (SDK key decides the environment,
// via apikey.Middleware) and streams that environment's flag-change channel
// to the client as Server-Sent Events until the client disconnects.
func Handler(sub events.Subscriber) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		envID, ok := apikey.EnvironmentID(r.Context())
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "SDK key required")
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			httpx.WriteInternal(w, r, errors.New("stream: ResponseWriter does not support flushing"))
			return
		}

		msgs, closeStream, err := sub.Subscribe(r.Context(), events.FlagsChannel(envID))
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

		for {
			select {
			case <-r.Context().Done():
				return
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
