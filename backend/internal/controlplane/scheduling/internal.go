package scheduling

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"time"

	"helios/backend/internal/platform/httpx"
)

// SecretHeader carries the shared secret on POST /internal/scheduler/run.
const SecretHeader = "X-Helios-Scheduler-Secret"

// MinSharedLen is the shortest secret that enables the endpoint: a shorter
// one is treated as not set, because a guessable secret is worse than none.
const MinSharedLen = 32

// InternalHandler serves POST /internal/scheduler/run, which a scheduler job
// (Cloud Scheduler, cron) calls to run the Runner once. It is not part of the
// public API and not for browsers:
//
//   - with no secret configured (or a short one) it does not exist: it answers
//     exactly like any unknown path (404);
//   - the secret is compared in constant time (crypto/subtle on equal-length
//     digests, so neither the value nor its length leaks through timing);
//   - a missing or wrong secret, and any request that carries an Origin header
//     (a browser), get one fixed 401 with no detail;
//   - the secret is never logged, echoed or put in an error, and the handler
//     logs nothing about the request;
//   - the answer is the Runner's counts and nothing else;
//   - the route sits behind a tight per-IP rate limit that applies BEFORE this
//     check (see the router), and the CORS middleware skips /internal/ paths.
func InternalHandler(secret string, run func(context.Context) (Stats, error)) http.Handler {
	enabled := len(secret) >= MinSharedLen
	want := sha256.Sum256([]byte(secret))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !enabled {
			http.NotFound(w, r)
			return
		}
		got := sha256.Sum256([]byte(r.Header.Get(SecretHeader)))
		if r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
		defer cancel()
		st, err := run(ctx)
		if err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteJSON(w, http.StatusOK, st)
	})
}
