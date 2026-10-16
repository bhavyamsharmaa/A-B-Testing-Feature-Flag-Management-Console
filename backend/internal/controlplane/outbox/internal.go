package outbox

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"time"

	"helios/backend/internal/platform/httpx"
)

// SecretHeader carries the shared secret on POST /internal/outbox/run.
const SecretHeader = "X-Helios-Outbox-Secret"

// MinSharedLen is the shortest secret that enables the endpoint.
const MinSharedLen = 32

// InternalHandler serves POST /internal/outbox/run, which a scheduler job calls to
// run the worker once. The same rules as the scheduler's endpoint: with no (or a
// short) secret it does not exist (404); the secret is compared in constant time
// over equal-length digests; a missing or wrong secret, or any request with an
// Origin header (a browser), gets one fixed 401; nothing about the request is
// logged; the answer is the counts. The route's per-IP rate limit runs first.
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
