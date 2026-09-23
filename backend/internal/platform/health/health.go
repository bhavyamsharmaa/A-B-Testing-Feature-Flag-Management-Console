// Package health provides an HTTP handler that reports whether the backend
// can actually reach its database, not just whether the process is running.
package health

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler returns 200 with "ok" when the database is reachable, or 503 with
// "db unreachable" when it isn't. A 200 here is the thing worth curling
// after a deploy — the process being up is necessary but not sufficient.
func Handler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		if err := pool.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("db unreachable"))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}
