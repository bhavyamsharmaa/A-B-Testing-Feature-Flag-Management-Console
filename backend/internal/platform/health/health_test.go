package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/platform/lifecycle"
)

// A pool that has never been able to connect: pgxpool connects lazily, so creating it needs no server.
func deadPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody:s3cr3t-password@127.0.0.1:1/nodb?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func get(h http.HandlerFunc) (int, string) {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/", nil))
	return rec.Code, rec.Body.String()
}

func TestLivenessChecksNothingAndStaysUpWhileShuttingDown(t *testing.T) {
	// no pool, no Redis, a draining lifecycle: the process can still answer, so it is alive
	if code, body := get(Live()); code != 200 || body != "ok" {
		t.Errorf("%d %q", code, body)
	}
}

func TestReadinessSaysNotReadyWhenTheDatabaseIsDownWithoutLeakingAnything(t *testing.T) {
	code, body := get(Ready(deadPool(t), nil, false, nil))
	if code != 503 {
		t.Fatalf("status %d: %s", code, body)
	}
	var r struct {
		Status string
		Checks map[string]string
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil || r.Status != "unavailable" || r.Checks["database"] != "unavailable" || r.Checks["redis"] != "not_configured" {
		t.Errorf("%s (%v)", body, err)
	}
	for _, leak := range []string{"127.0.0.1", "nobody", "s3cr3t", "nodb", "refused", "connect", "dial", "error"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Errorf("the response contains %q: %s", leak, body)
		}
	}
}

func TestReadinessTimesOutQuicklyOnASlowDependency(t *testing.T) {
	slow := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	start := time.Now()
	code, body := get(Ready(deadPool(t), slow, true, nil))
	if took := time.Since(start); took > CheckTimeout+500*time.Millisecond {
		t.Errorf("took %v, want about %v", took, CheckTimeout)
	}
	if code != 503 {
		t.Errorf("%d %s", code, body)
	}
}

func TestShuttingDownIsNotReadyAtOnce(t *testing.T) {
	life := lifecycle.New()
	h := Ready(deadPool(t), nil, false, life)
	if code, _ := get(h); code != 503 { // (the dead pool makes this 503 anyway; the next assertion is the point)
		t.Fatalf("setup %d", code)
	}
	life.Drain()
	start := time.Now()
	code, body := get(h)
	if code != 503 || !strings.Contains(body, `"shutting_down"`) || strings.Contains(body, "checks") {
		t.Errorf("%d %s", code, body)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Errorf("shutting_down took %v: it must not wait for the dependency checks", time.Since(start))
	}
}

func TestRedisDownIsDegradedNotUnreadyUnlessRequired(t *testing.T) {
	// The database check is the only thing that needs a real server; use the test database when there is one.
	pool := testPool(t)
	down := func(context.Context) error { return errors.New("redis: connection refused 10.0.0.9:6379") }
	up := func(context.Context) error { return nil }
	for name, c := range map[string]struct {
		ping    func(context.Context) error
		require bool
		code    int
		status  string
		redis   string
	}{
		"redis up":                {up, false, 200, "ok", "ok"},
		"redis down":              {down, false, 200, "degraded", "unavailable"},
		"redis down and required": {down, true, 503, "unavailable", "unavailable"},
		"no redis":                {nil, true, 200, "ok", "not_configured"},
	} {
		code, body := get(Ready(pool, c.ping, c.require, nil))
		var r struct {
			Status string
			Checks map[string]string
		}
		_ = json.Unmarshal([]byte(body), &r)
		if code != c.code || r.Status != c.status || r.Checks["redis"] != c.redis || r.Checks["database"] != "ok" {
			t.Errorf("%s: %d %s", name, code, body)
		}
		if strings.Contains(body, "10.0.0.9") || strings.Contains(body, "refused") {
			t.Errorf("%s leaks the Redis error: %s", name, body)
		}
	}
}
