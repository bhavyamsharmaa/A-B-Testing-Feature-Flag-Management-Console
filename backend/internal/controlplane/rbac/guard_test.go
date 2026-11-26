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
	calls int
	sql   string
	args  []any
	row   fakeRow
}

type fakeRow struct {
	err                  error
	id, workspaceID, key string
	isProduction         bool
	role                 WorkspaceRole
}

func (f *fakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.calls++
	f.sql, f.args = sql, args
	return f.row
}

// Scan fills either the environment query (5 columns) or the workspace
// query (1 column: the role).
func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) == 1 {
		*dest[0].(*WorkspaceRole) = r.role
		return nil
	}
	*dest[0].(*string) = r.id
	*dest[1].(*string) = r.workspaceID
	*dest[2].(*string) = r.key
	*dest[3].(*bool) = r.isProduction
	*dest[4].(*WorkspaceRole) = r.role
	return nil
}

const (
	userA   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	envID   = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	otherID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	wsA     = "11111111-1111-4111-8111-111111111111"
)

func serve(g *Guard, wrap func(http.Handler) http.Handler, pattern, path string, signedIn bool) (*httptest.ResponseRecorder, *Access, *WorkspaceAccess) {
	var gotEnv *Access
	var gotWs *WorkspaceAccess
	h := wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := AccessFrom(r.Context())
		gotEnv = &a
		wa := WorkspaceAccessFrom(r.Context())
		gotWs = &wa
		w.WriteHeader(http.StatusOK)
	}))
	mux := http.NewServeMux()
	mux.Handle("GET "+pattern, h)
	r := httptest.NewRequest("GET", path, nil)
	if signedIn {
		r = r.WithContext(auth.WithUser(r.Context(), auth.User{ID: userA, Email: "a@example.com"}))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec, gotEnv, gotWs
}

func serveEnv(g *Guard, req Requirement, segment string, signedIn bool) (*httptest.ResponseRecorder, *Access) {
	rec, a, _ := serve(g, func(h http.Handler) http.Handler { return g.Require(req, h) },
		"/environments/{envId}/x", "/environments/"+segment+"/x", signedIn)
	return rec, a
}

func serveWs(g *Guard, min WorkspaceRole, segment string, signedIn bool) (*httptest.ResponseRecorder, *WorkspaceAccess) {
	rec, _, w := serve(g, func(h http.Handler) http.Handler { return g.RequireWorkspace(min, h) },
		"/workspaces/{wsId}/x", "/workspaces/"+segment+"/x", signedIn)
	return rec, w
}

// Unknown, other tenants' and non-UUID environments are indistinguishable.
func TestGuardHidesEnvironmentsTheCallerIsNotAMemberOf(t *testing.T) {
	var bodies []string
	for _, segment := range []string{otherID, "dev", "production", strings.ToUpper(envID)} {
		g := &Guard{db: &fakeDB{row: fakeRow{err: pgx.ErrNoRows}}}
		rec, got := serveEnv(g, Min(Viewer), segment, true)
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

func TestGuardNonUUIDSegmentNeverReachesTheDatabase(t *testing.T) {
	db := &fakeDB{row: fakeRow{id: envID, workspaceID: wsA, key: "dev", role: WorkspaceOwner}}
	rec, got := serveEnv(&Guard{db: db}, Min(Viewer), "dev", true)
	if rec.Code != http.StatusNotFound || got != nil || db.calls != 0 {
		t.Errorf("status %d, ran %v, db calls %d", rec.Code, got != nil, db.calls)
	}
}

func TestGuardLowRoleIs403NotFound404(t *testing.T) {
	g := &Guard{db: &fakeDB{row: fakeRow{id: envID, workspaceID: wsA, key: "dev", role: WorkspaceViewer}}}
	rec, got := serveEnv(g, Min(Editor), envID, true)
	if rec.Code != http.StatusForbidden || got != nil {
		t.Fatalf("status %d, handler ran: %v", rec.Code, got != nil)
	}
	if !strings.Contains(rec.Body.String(), "requires editor") {
		t.Errorf("body %s", rec.Body.String())
	}
}

func TestGuardPassesWorkspaceAndRolesToHandler(t *testing.T) {
	g := &Guard{db: &fakeDB{row: fakeRow{id: envID, workspaceID: wsA, key: "production", isProduction: true, role: WorkspaceAdmin}}}
	rec, got := serveEnv(g, FlagWrite, envID, true)
	if rec.Code != http.StatusOK || got == nil {
		t.Fatalf("status %d", rec.Code)
	}
	if got.Env.ID != envID || got.Env.WorkspaceID != wsA || got.Env.Key != "production" || !got.Env.IsProduction ||
		got.Role != Admin || got.WorkspaceRole != WorkspaceAdmin || got.User.ID != userA {
		t.Errorf("access = %+v", *got)
	}
}

// Editors may change dev/staging flags but not production; viewers nothing.
func TestWorkspaceRolesGateFlagWritesPerEnvironment(t *testing.T) {
	cases := []struct {
		role WorkspaceRole
		prod bool
		want int
	}{
		{WorkspaceOwner, true, 200}, {WorkspaceAdmin, true, 200},
		{WorkspaceEditor, false, 200}, {WorkspaceEditor, true, 403},
		{WorkspaceViewer, false, 403}, {WorkspaceViewer, true, 403},
	}
	for _, c := range cases {
		g := &Guard{db: &fakeDB{row: fakeRow{id: envID, workspaceID: wsA, key: "x", isProduction: c.prod, role: c.role}}}
		rec, _ := serveEnv(g, FlagWrite, envID, true)
		if rec.Code != c.want {
			t.Errorf("%s prod=%v: status %d, want %d", c.role, c.prod, rec.Code, c.want)
		}
	}
}

func TestGuardUnauthenticatedAndDBErrors(t *testing.T) {
	if rec, _ := serveEnv(&Guard{db: &fakeDB{}}, Min(Viewer), envID, false); rec.Code != http.StatusUnauthorized {
		t.Errorf("no user: status %d", rec.Code)
	}
	g := &Guard{db: &fakeDB{row: fakeRow{err: errors.New("connection reset")}}}
	rec, _ := serveEnv(g, Min(Viewer), envID, true)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "connection reset") {
		t.Errorf("db error: status %d body %s", rec.Code, rec.Body.String())
	}
}

// The environment lookup must go through the caller's own membership.
func TestEnvironmentLookupJoinsMembership(t *testing.T) {
	db := &fakeDB{row: fakeRow{err: pgx.ErrNoRows}}
	_, _, _ = resolveEnvironment(context.Background(), db, userA, envID)
	for _, want := range []string{
		"JOIN workspace_members m ON m.workspace_id = e.workspace_id",
		"m.user_id = $1::uuid",
		"e.id = $2::uuid",
	} {
		if !strings.Contains(db.sql, want) {
			t.Errorf("SQL lacks %q:\n%s", want, db.sql)
		}
	}
	if len(db.args) != 2 || db.args[0] != userA || db.args[1] != envID {
		t.Errorf("args = %v", db.args)
	}
}

func TestRequireWorkspace(t *testing.T) {
	// not a member (or no such workspace), and a non-UUID: both 404, same body
	g := &Guard{db: &fakeDB{row: fakeRow{err: pgx.ErrNoRows}}}
	rec1, w1 := serveWs(g, WorkspaceViewer, wsA, true)
	db := &fakeDB{}
	rec2, w2 := serveWs(&Guard{db: db}, WorkspaceViewer, "not-a-uuid", true)
	if rec1.Code != 404 || rec2.Code != 404 || w1 != nil || w2 != nil || db.calls != 0 || rec1.Body.String() != rec2.Body.String() {
		t.Errorf("non-member %d, non-uuid %d (db calls %d)", rec1.Code, rec2.Code, db.calls)
	}
	if !strings.Contains(rec1.Body.String(), `"code":"WORKSPACE_NOT_FOUND"`) {
		t.Errorf("body %s", rec1.Body.String())
	}

	// member with a low role: 403; with enough: handler runs with the role
	g = &Guard{db: &fakeDB{row: fakeRow{role: WorkspaceEditor}}}
	if rec, w := serveWs(g, WorkspaceAdmin, wsA, true); rec.Code != 403 || w != nil {
		t.Errorf("editor on admin route: %d", rec.Code)
	}
	rec, w := serveWs(g, WorkspaceEditor, strings.ToUpper(wsA), true)
	if rec.Code != 200 || w == nil || w.WorkspaceID != wsA || w.Role != WorkspaceEditor || w.User.ID != userA {
		t.Errorf("status %d access %+v", rec.Code, w)
	}
}

func TestWorkspaceRoles(t *testing.T) {
	order := []WorkspaceRole{WorkspaceViewer, WorkspaceEditor, WorkspaceAdmin, WorkspaceOwner}
	for i, a := range order {
		for j, b := range order {
			if got := a.AtLeast(b); got != (i >= j) {
				t.Errorf("%s.AtLeast(%s) = %v", a, b, got)
			}
		}
	}
	if WorkspaceRole("").AtLeast(WorkspaceViewer) || WorkspaceRole("root").AtLeast(WorkspaceViewer) {
		t.Error("unknown roles must grant nothing")
	}
	for in, want := range map[WorkspaceRole]Role{
		WorkspaceOwner: Admin, WorkspaceAdmin: Admin, WorkspaceEditor: Editor, WorkspaceViewer: Viewer, "": Viewer,
	} {
		if got := in.EnvRole(); got != want {
			t.Errorf("%q.EnvRole() = %s, want %s", in, got, want)
		}
	}
	for _, ok := range []string{"owner", "admin", "editor", "viewer"} {
		if _, valid := ParseWorkspaceRole(ok); !valid {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "Owner", "approver", "root"} {
		if _, valid := ParseWorkspaceRole(bad); valid {
			t.Errorf("%q accepted", bad)
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

// No string can be both a valid environment key and UUID-shaped.
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
		if rng.Intn(3) == 0 && n >= 36 {
			s = s[:8] + "-" + s[9:13] + "-" + s[14:18] + "-" + s[19:23] + "-" + s[24:36]
		}
		if ValidEnvironmentKey(s) && IsUUID(s) {
			t.Fatalf("%q is both a valid key and a UUID", s)
		}
	}
}
