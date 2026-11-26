// Package rbac provides server-side role enforcement for the control plane
// (viewer / editor / approver / admin), scoped per environment, and the
// tenant boundary: an {envId} path segment only resolves to an environment
// the caller holds a role in, so another workspace's environments are
// indistinguishable from ones that don't exist. The console hiding a button
// is cosmetic; this package is the actual boundary (US-06 AC-1).
package rbac

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/httpx"
)

type Role string

const (
	Viewer   Role = "viewer"
	Editor   Role = "editor"
	Approver Role = "approver"
	Admin    Role = "admin"
)

var rank = map[Role]int{Viewer: 1, Editor: 2, Approver: 3, Admin: 4}

// ParseRole validates a role name from a request body.
func ParseRole(s string) (Role, bool) {
	r := Role(s)
	_, ok := rank[r]
	return r, ok
}

// AtLeast reports whether r grants everything min does.
func (r Role) AtLeast(min Role) bool {
	return rank[r] >= rank[min]
}

// Environment is an environment as the guard resolved it. WorkspaceID is the
// tenant it belongs to; every query a handler runs must be scoped by it.
type Environment struct {
	ID           string
	WorkspaceID  string
	Key          string
	IsProduction bool
}

// Requirement is the minimum role an action needs in a given environment.
type Requirement func(Environment) Role

// Min is a Requirement that doesn't vary by environment.
func Min(r Role) Requirement {
	return func(Environment) Role { return r }
}

// FlagWrite: editors may change flags in dev and staging, but a normal
// production change needs an approver. The kill switch deliberately doesn't
// use this — see its route in cmd/api.
func FlagWrite(env Environment) Role {
	if env.IsProduction {
		return Approver
	}
	return Editor
}

// Access is what Guard established about the caller for this request.
// Role is what the caller may do inside the environment; it is derived from
// WorkspaceRole, their role in the environment's workspace.
type Access struct {
	User          auth.User
	Env           Environment
	Role          Role
	WorkspaceRole WorkspaceRole
}

type accessKey struct{}

// AccessFrom returns the Access stored by Guard.Require.
func AccessFrom(ctx context.Context) Access {
	a, _ := ctx.Value(accessKey{}).(Access)
	return a
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s is shaped like a UUID.
func IsUUID(s string) bool { return uuidRE.MatchString(s) }

// environmentKeyRE allows at most 32 characters and must start with a letter,
// so no valid key can be 36-character UUID-shaped.
var environmentKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ValidEnvironmentKey reports whether s may be used as an environment key.
func ValidEnvironmentKey(s string) bool {
	return environmentKeyRE.MatchString(s) && !IsUUID(s)
}

var (
	ErrEnvironmentNotFound = errors.New("environment not found")
	ErrWorkspaceNotFound   = errors.New("workspace not found")
)

// rowQuerier is the part of *pgxpool.Pool (or a pgx.Tx) the guard needs.
type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// resolveEnvironment turns the {envId} path segment into an environment and
// the caller's role in its workspace, in one query. The join on
// workspace_members is what makes this the tenant boundary: an environment
// the caller is not a member of (unknown, or another workspace's) matches no
// row, and the response never says which. A segment that is not a UUID can't
// match anything and never reaches the database.
func resolveEnvironment(ctx context.Context, db rowQuerier, userID, segment string) (Environment, WorkspaceRole, error) {
	if !IsUUID(segment) {
		return Environment{}, "", ErrEnvironmentNotFound
	}
	var env Environment
	var role WorkspaceRole
	err := db.QueryRow(ctx, `
		SELECT e.id::text, e.workspace_id::text, e.key, e.is_production, m.role::text
		FROM environments e
		JOIN workspace_members m ON m.workspace_id = e.workspace_id
		WHERE e.id = $2::uuid AND m.user_id = $1::uuid`,
		userID, segment,
	).Scan(&env.ID, &env.WorkspaceID, &env.Key, &env.IsProduction, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, "", ErrEnvironmentNotFound
	}
	return env, role, err
}

// LoadEnvironmentInWorkspace looks an environment up by key inside one
// workspace, for tools like mkkey that run outside a request.
func LoadEnvironmentInWorkspace(ctx context.Context, db rowQuerier, workspaceID, key string) (Environment, error) {
	var env Environment
	err := db.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, key, is_production
		FROM environments WHERE workspace_id = $1::uuid AND key = $2`, workspaceID, key,
	).Scan(&env.ID, &env.WorkspaceID, &env.Key, &env.IsProduction)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, ErrEnvironmentNotFound
	}
	return env, err
}

type Guard struct {
	db rowQuerier
}

func NewGuard(pool *pgxpool.Pool) *Guard {
	return &Guard{db: pool}
}

// Require wraps a route that has an {envId} path parameter. It must run after
// auth.Middleware. It resolves the environment through the caller's own
// workspace membership and rejects with 404 when the caller can't see it, or
// 403 when they can but their role is too low.
func (g *Guard) Require(req Requirement, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		env, wsRole, err := resolveEnvironment(r.Context(), g.db, user.ID, r.PathValue("envId"))
		if errors.Is(err, ErrEnvironmentNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "ENVIRONMENT_NOT_FOUND", "environment not found")
			return
		}
		if err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}

		role := wsRole.EnvRole()
		if need := req(env); !role.AtLeast(need) {
			httpx.WriteError(w, http.StatusForbidden, "FORBIDDEN",
				"requires "+string(need)+" access in "+env.Key+"; your workspace role is "+string(wsRole))
			return
		}

		ctx := context.WithValue(r.Context(), accessKey{}, Access{User: user, Env: env, Role: role, WorkspaceRole: wsRole})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// WorkspaceAccess is what RequireWorkspace established for a {wsId} route.
type WorkspaceAccess struct {
	User        auth.User
	WorkspaceID string
	Role        WorkspaceRole
}

type workspaceAccessKey struct{}

// WorkspaceAccessFrom returns the WorkspaceAccess stored by RequireWorkspace.
func WorkspaceAccessFrom(ctx context.Context) WorkspaceAccess {
	a, _ := ctx.Value(workspaceAccessKey{}).(WorkspaceAccess)
	return a
}

func resolveWorkspace(ctx context.Context, db rowQuerier, userID, segment string) (WorkspaceRole, error) {
	if !IsUUID(segment) {
		return "", ErrWorkspaceNotFound
	}
	var role WorkspaceRole
	err := db.QueryRow(ctx, `
		SELECT m.role::text FROM workspace_members m
		WHERE m.user_id = $1::uuid AND m.workspace_id = $2::uuid`, userID, segment).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrWorkspaceNotFound
	}
	return role, err
}

// RequireWorkspace wraps a route with a {wsId} path parameter. The workspace
// comes from the path but is only honoured when the caller is a member:
// anyone else gets the same 404 as for a workspace that doesn't exist.
func (g *Guard) RequireWorkspace(min WorkspaceRole, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		wsID := strings.ToLower(r.PathValue("wsId"))
		role, err := resolveWorkspace(r.Context(), g.db, user.ID, wsID)
		if errors.Is(err, ErrWorkspaceNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "WORKSPACE_NOT_FOUND", "workspace not found")
			return
		}
		if err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
		if !role.AtLeast(min) {
			httpx.WriteError(w, http.StatusForbidden, "FORBIDDEN",
				"requires the "+string(min)+" role in this workspace; you are "+string(role))
			return
		}
		ctx := context.WithValue(r.Context(), workspaceAccessKey{}, WorkspaceAccess{User: user, WorkspaceID: wsID, Role: role})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
