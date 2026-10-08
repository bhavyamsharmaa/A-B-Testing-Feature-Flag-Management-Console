package rbac

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/platform/auth"
)

// fakeDB scripts the guard's single query and records what was asked.
type fakeDB struct {
	sql  string
	args []any
	row  fakeRow
}

type fakeRow struct {
	err                  error
	id, workspaceID, key string
	isProduction         bool
	role                 Role
}

func (f *fakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.sql, f.args = sql, args
	return f.row
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*string) = r.id
	*dest[1].(*string) = r.workspaceID
	*dest[2].(*string) = r.key
	*dest[3].(*bool) = r.isProduction
	*dest[4].(*Role) = r.role
	return nil
}

const (
	userA   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	envID   = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	otherID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	wsA     = "11111111-1111-4111-8111-111111111111"
)

func serve(g *Guard, req Requirement, segment string, signedIn bool) (*httptest.ResponseRecorder, *Access) {
	var got *Access
	h := g.Require(req, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := AccessFrom(r.Context())
		got = &a
		w.WriteHeader(http.StatusOK)
	}))
	mux := http.NewServeMux()
	mux.Handle("GET /environments/{envId}/x", h)
	r := httptest.NewRequest("GET", "/environments/"+segment+"/x", nil)
	if signedIn {
		r = r.WithContext(auth.WithUser(r.Context(), auth.User{ID: userA, Email: "a@example.com"}))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec, got
}

// Unknown, another tenant's, and role-less environments are indistinguishable.
func TestGuardHidesEnvironmentsTheCallerCannotSee(t *testing.T) {
	var bodies []string
	for _, segment := range []string{otherID, "dev", "production", strings.ToUpper(envID)} {
		g := &Guard{db: &fakeDB{row: fakeRow{err: pgx.ErrNoRows}}}
		rec, got := serve(g, Min(Viewer), segment, true)
		if rec.Code != http.StatusNotFound || got != nil {
			t.Fatalf("%s: status %d, handler ran: %v", segment, rec.Code, got != nil)
		}
		if !strings.Contains(rec.Body.String(), `"code":"ENVIRONMENT_NOT_FOUND"`) {
			t.Errorf("%s: body %s", segment, rec.Body.String())
		}
		bodies = append(bodies, rec.Body.String())
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("responses differ, so they leak which case it was:\n%s\n%s", bodies[0], b)
		}
	}
}

func TestGuardLowRoleIs403NotFound404(t *testing.T) {
	g := &Guard{db: &fakeDB{row: fakeRow{id: envID, workspaceID: wsA, key: "dev", role: Viewer}}}
	rec, got := serve(g, Min(Editor), envID, true)
	if rec.Code != http.StatusForbidden || got != nil {
		t.Fatalf("status %d, handler ran: %v", rec.Code, got != nil)
	}
	if !strings.Contains(rec.Body.String(), "requires editor") {
		t.Errorf("body %s", rec.Body.String())
	}
}

func TestGuardPassesWorkspaceToHandler(t *testing.T) {
	g := &Guard{db: &fakeDB{row: fakeRow{id: envID, workspaceID: wsA, key: "production", isProduction: true, role: Approver}}}
	rec, got := serve(g, FlagWrite, envID, true)
	if rec.Code != http.StatusOK || got == nil {
		t.Fatalf("status %d", rec.Code)
	}
	if got.Env.ID != envID || got.Env.WorkspaceID != wsA || got.Env.Key != "production" || !got.Env.IsProduction || got.Role != Approver || got.User.ID != userA {
		t.Errorf("access = %+v", *got)
	}
}

func TestGuardUnauthenticatedAndDBErrors(t *testing.T) {
	if rec, _ := serve(&Guard{db: &fakeDB{}}, Min(Viewer), envID, false); rec.Code != http.StatusUnauthorized {
		t.Errorf("no user: status %d", rec.Code)
	}
	g := &Guard{db: &fakeDB{row: fakeRow{err: errors.New("connection reset")}}}
	rec, _ := serve(g, Min(Viewer), envID, true)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "connection reset") {
		t.Errorf("db error: status %d body %s", rec.Code, rec.Body.String())
	}
}

// The environment-key shim (TODO stage 2) may only ever look inside the
// caller's own workspace; a UUID may only match an environment the caller
// holds a role in.
func TestKeySegmentIsConfinedToTheCallersWorkspace(t *testing.T) {
	db := &fakeDB{row: fakeRow{err: pgx.ErrNoRows}}
	_, _, _ = resolveEnvironment(context.Background(), db, userA, "dev")
	for _, want := range []string{
		"e.key = $2",
		"e.workspace_id = (SELECT m.workspace_id FROM workspace_members m WHERE m.user_id = $1::uuid)",
		"r.user_id = $1::uuid",
		"r.workspace_id = e.workspace_id",
	} {
		if !strings.Contains(db.sql, want) {
			t.Errorf("key lookup SQL lacks %q:\n%s", want, db.sql)
		}
	}
	if len(db.args) != 2 || db.args[0] != userA || db.args[1] != "dev" {
		t.Errorf("args = %v", db.args)
	}

	_, _, _ = resolveEnvironment(context.Background(), db, userA, envID)
	if strings.Contains(db.sql, "e.key = $2") || strings.Contains(db.sql, "workspace_members") || !strings.Contains(db.sql, "e.id = $2::uuid") || !strings.Contains(db.sql, "r.user_id = $1::uuid") {
		t.Errorf("UUID lookup SQL is wrong:\n%s", db.sql)
	}
}

func TestSegmentRoutingByShape(t *testing.T) {
	for _, uuid := range []string{envID, strings.ToUpper(envID), "01234567-89ab-cdef-0123-456789abcdef"} {
		db := &fakeDB{row: fakeRow{err: pgx.ErrNoRows}}
		_, _, _ = resolveEnvironment(context.Background(), db, userA, uuid)
		if strings.Contains(db.sql, "e.key = $2") {
			t.Errorf("%s was treated as a key", uuid)
		}
	}
	for _, key := range []string{"dev", "staging", "production", "qa-2", "e_1"} {
		db := &fakeDB{row: fakeRow{err: pgx.ErrNoRows}}
		_, _, _ = resolveEnvironment(context.Background(), db, userA, key)
		if !strings.Contains(db.sql, "e.key = $2") {
			t.Errorf("%s was not treated as a key", key)
		}
	}
}

func TestValidEnvironmentKey(t *testing.T) {
	for _, ok := range []string{"dev", "staging", "production", "qa-2", "a", "e_1", strings.Repeat("a", 32)} {
		if !ValidEnvironmentKey(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{
		"", "Dev", "1dev", "-dev", "dev env", "a/b", "dev.x", strings.Repeat("a", 33),
		"11111111-1111-1111-1111-111111111111", "ABCDEF12-3456-7890-ABCD-EF1234567890", "abcdef12-3456-7890-abcd-ef1234567890",
	} {
		if ValidEnvironmentKey(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// No string can be both a valid environment key and UUID-shaped, so the
// {envId} segment always takes exactly one of the two lookup paths.
func TestNoKeyIsEverUUIDShaped(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	const alphabet = "abcdef0123456789-_"
	for i := 0; i < 20000; i++ {
		n := 1 + rng.Intn(40)
		b := make([]byte, n)
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		s := string(b)
		if rng.Intn(3) == 0 && n >= 36 { // force the UUID shape often
			s = s[:8] + "-" + s[9:13] + "-" + s[14:18] + "-" + s[19:23] + "-" + s[24:36]
		}
		if ValidEnvironmentKey(s) && IsUUID(s) {
			t.Fatalf("%q is both a valid key and a UUID", s)
		}
	}
}
