// Package health provides the two probes an orchestrator needs, and keeps them apart:
//
//   - /healthz: the process is alive. It checks NO dependency, so a database blip never gets a healthy process
//     killed and restarted (which would only add a thundering herd of reconnects to the outage).
//   - /readyz: this instance should receive traffic. It checks the database and Redis with short timeouts, and says
//     "not ready" first thing when the process is shutting down, before the platform stops routing to it.
//
// Neither response carries a hostname, an address, a version, an error text or a credential: only fixed words.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/platform/lifecycle"
)

// CheckTimeout bounds each dependency check.
const CheckTimeout = time.Second

// Live is /healthz: 200 as long as the process can answer.
func Live() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}

// Ready is /readyz. redisPing nil means Redis is not configured (reported as "not_configured", never a failure).
// With requireRedis false (the default) a Redis that is down makes the instance "degraded" but still ready: Redis is a
// propagation optimization and /evaluate works without it, so taking every instance out of rotation over it would turn
// a degradation into an outage. requireRedis true makes it fatal.
func Ready(pool *pgxpool.Pool, redisPing func(context.Context) error, requireRedis bool, life *lifecycle.State) http.HandlerFunc {
	type result struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks,omitempty"`
	}
	write := func(w http.ResponseWriter, code int, res result) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(res)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if life.Draining() {
			write(w, http.StatusServiceUnavailable, result{Status: "shutting_down"})
			return
		}
		checks := map[string]string{"database": "ok", "redis": "not_configured"}
		var wg sync.WaitGroup
		var dbErr, redisErr error
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), CheckTimeout)
			defer cancel()
			dbErr = pool.Ping(ctx)
		}()
		if redisPing != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(r.Context(), CheckTimeout)
				defer cancel()
				redisErr = redisPing(ctx)
			}()
		}
		wg.Wait()
		status := "ok"
		if dbErr != nil {
			checks["database"], status = "unavailable", "unavailable"
		}
		if redisPing != nil {
			if redisErr != nil {
				checks["redis"] = "unavailable"
				switch {
				case requireRedis:
					status = "unavailable"
				case status == "ok":
					status = "degraded"
				}
			} else {
				checks["redis"] = "ok"
			}
		}
		code := http.StatusOK
		if status == "unavailable" {
			code = http.StatusServiceUnavailable
		}
		write(w, code, result{Status: status, Checks: checks})
	}
}
