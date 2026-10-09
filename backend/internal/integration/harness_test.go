// Package integration tests the whole backend (real routes, guards and SQL)
// against a throwaway Postgres. It is skipped unless HELIOS_TEST_DATABASE_URL
// points at a LOCAL server it may create databases on, e.g.
//
//	HELIOS_TEST_DATABASE_URL='postgres://helios@127.0.0.1:55432/postgres?sslmode=disable' go test ./internal/integration
//
// Each test creates its own database, applies migrations 0001-0006, and
// drops it afterwards. It refuses any non-local host.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/db"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/server"
)

type harness struct {
	mu     sync.Mutex
	bodies [][]byte // every response body h.call has seen
	t      *testing.T
	srv    *httptest.Server
	pool   *pgxpool.Pool
	bus    *events.MemoryBus
	routes []server.Route
	deps   server.Deps
}

type user struct {
	id, email string
	status    string // "" (confirmed), "unconfirmed" or "unknown": what the token says about the email
}

func newHarness(t *testing.T) *harness { return newHarnessWith(t, nil) }

// newHarnessWith lets a test adjust the server's dependencies (rate limits,
// email policy, ...) before the router is built.
func newHarnessWith(t *testing.T, adjust func(*server.Deps)) *harness {
	t.Helper()
	adminURL := os.Getenv("HELIOS_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("HELIOS_TEST_DATABASE_URL not set; skipping database-backed tests")
	}
	u, err := url.Parse(adminURL)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatalf("HELIOS_TEST_DATABASE_URL must point at a local server, got host %q", u.Hostname())
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	name := fmt.Sprintf("helios_it_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	u.Path = "/" + name
	pool, err := db.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	files, err := filepath.Glob(filepath.Join("..", "..", "db", "migrations", "000[1-6]_*.sql"))
	if err != nil || len(files) != 6 {
		t.Fatalf("expected migrations 0001-0006, found %v (%v)", files, err)
	}
	sort.Strings(files)
	for _, f := range files {
		sqlText, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sqlText)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}

	// The test authenticator trusts "X-Test-User: <id>|<email>": the guards,
	// roles and SQL under test are the real ones; only JWT checking is stubbed.
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, rest, ok := strings.Cut(r.Header.Get("X-Test-User"), "|")
			if !ok || id == "" {
				http.Error(w, `{"code":"UNAUTHORIZED","message":"no test user"}`, http.StatusUnauthorized)
				return
			}
			// "id|email" or "id|email|confirmed|unconfirmed|unknown"; confirmed by default.
			email, status, _ := strings.Cut(rest, "|")
			if status == "" {
				status = string(auth.EmailConfirmed)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), auth.User{ID: id, Email: email, EmailStatus: auth.EmailStatus(status)})))
		})
	}
	bus := events.NewMemoryBus()
	deps := server.Deps{
		Pool: pool, Authn: authn,
		Publisher: bus, Subscriber: bus,
		// A long cache TTL on purpose: revocation must not depend on the cache expiring.
		SDKKeyCacheTTL: time.Hour,
		StreamRecheck:  150 * time.Millisecond,
		Email:          auth.NewEmailPolicy(auth.EmailEnforce, "", "", nil),
	}
	if adjust != nil {
		adjust(&deps)
	}
	router := server.New(deps)
	srv := httptest.NewServer(router.Mux)
	t.Cleanup(srv.Close)
	return &harness{t: t, srv: srv, pool: pool, bus: bus, routes: router.Routes, deps: deps}
}

// newUser inserts a user into the (stand-in) auth.users table.
func (h *harness) newUser(name string) user {
	h.t.Helper()
	email := name + "@example.com"
	var id string
	if err := h.pool.QueryRow(context.Background(),
		`INSERT INTO auth.users (email) VALUES ($1) RETURNING id::text`, email).Scan(&id); err != nil {
		h.t.Fatalf("insert user %s: %v", name, err)
	}
	return user{id: id, email: email}
}

// call performs a request as u and returns the status and raw body.
func (h *harness) call(u user, method, path string, body any) (int, []byte) {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rdr)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if u.id != "" {
		req.Header.Set("X-Test-User", u.header())
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	h.mu.Lock()
	h.bodies = append(h.bodies, b)
	h.mu.Unlock()
	return res.StatusCode, b
}

// expect fails the test unless the call returns want, and returns the body.
func (h *harness) expect(want int, u user, method, path string, body any) []byte {
	h.t.Helper()
	status, b := h.call(u, method, path, body)
	if status != want {
		h.t.Fatalf("%s %s as %s: status %d, want %d; body %s", method, path, u.email, status, want, b)
	}
	return b
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

type meEnv struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

type meWorkspace struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Role         string  `json:"role"`
	Environments []meEnv `json:"environments"`
}

type meResp struct {
	ID                string        `json:"id"`
	ActiveWorkspaceID string        `json:"activeWorkspaceId"`
	Workspaces        []meWorkspace `json:"workspaces"`
	Invites           []struct {
		ID, WorkspaceID, WorkspaceName, Role string
	} `json:"invites"`
}

func (h *harness) me(u user) meResp {
	h.t.Helper()
	return decode[meResp](h.t, h.expect(200, u, "GET", "/me", nil))
}

// envID returns the id of the environment with the given key in a workspace.
func envID(t *testing.T, w meWorkspace, key string) string {
	t.Helper()
	for _, e := range w.Environments {
		if e.Key == key {
			return e.ID
		}
	}
	t.Fatalf("workspace %s has no %s environment", w.ID, key)
	return ""
}

func boolFlag(key string) map[string]any {
	return map[string]any{
		"key": key, "name": key, "variationType": "boolean",
		"variations": []map[string]any{{"id": "on", "value": true}, {"id": "off", "value": false}},
	}
}

func (h *harness) count(query string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// slowDown makes every INSERT/DELETE/UPDATE (event) on table pause for d inside
// its transaction, in THIS test's database only. It widens the window between
// "read" and "commit" so that missing locks and constraints show up reliably
// instead of once in a thousand runs.
func (h *harness) slowDown(table, event string, d time.Duration) {
	h.t.Helper()
	fn := "slow_" + table + "_" + strings.ToLower(event)
	_, err := h.pool.Exec(context.Background(), fmt.Sprintf(`
		CREATE FUNCTION %[1]s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(%[4]f); RETURN COALESCE(NEW, OLD); END $$;
		CREATE TRIGGER %[1]s BEFORE %[3]s ON %[2]s FOR EACH ROW EXECUTE FUNCTION %[1]s();`,
		fn, table, event, d.Seconds()))
	if err != nil {
		h.t.Fatalf("slowDown: %v", err)
	}
}

// syncBuffer is a bytes.Buffer safe for the server goroutines to log into.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs redirects the standard logger (what the handlers use) into a
// buffer for the rest of the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

// header is the stub authenticator's view of the user.
func (u user) header() string {
	h := u.id + "|" + u.email
	if u.status != "" {
		h += "|" + u.status
	}
	return h
}
