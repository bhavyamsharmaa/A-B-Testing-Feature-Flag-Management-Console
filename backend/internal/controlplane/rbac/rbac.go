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
type Access struct {
	User auth.User
	Env  Environment
	Role Role
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

// environmentKeyRE allows at most 32 characters and must start with a
// letter, so no valid key can be 36-character UUID-shaped. ValidEnvironmentKey
// also checks IsUUID explicitly, and 0004 adds the same rule as a CHECK, so
// the {envId} segment is never ambiguous.
var environmentKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ValidEnvironmentKey reports whether s may be used as an environment key.
func ValidEnvironmentKey(s string) bool {
	return environmentKeyRE.MatchString(s) && !IsUUID(s)
}

var ErrEnvironmentNotFound = errors.New("environment not found")

// rowQuerier is the part of *pgxpool.Pool (or a pgx.Tx) the guard needs.
type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// resolveEnvironment turns the {envId} path segment into an environment and
// the caller's role in it, in one query. Anything the caller cannot see
// (unknown, another workspace's, or no role there) is ErrEnvironmentNotFound,
// so the response never says which.
//
//   - A UUID segment matches the environment id. The role join means only
//     environments the caller holds a role in can match.
//   - Anything else is treated as an environment KEY and is only looked up
//     inside the caller's own workspace (the workspace_members subquery),
//     never globally: two tenants can both own a "dev".
//
// TODO(stage-2): remove the environment-key branch once the console uses
// environment UUIDs. It exists so the deployed console, which calls
// /environments/dev/..., keeps working after the route change.
func resolveEnvironment(ctx context.Context, db rowQuerier, userID, segment string) (Environment, Role, error) {
	const base = `
		SELECT e.id::text, e.workspace_id::text, e.key, e.is_production, r.role::text
		FROM environments e
		JOIN user_environment_roles r
		  ON r.environment_id = e.id AND r.workspace_id = e.workspace_id
		WHERE r.user_id = $1::uuid AND `
	var row pgx.Row
	if IsUUID(segment) {
		row = db.QueryRow(ctx, base+`e.id = $2::uuid`, userID, segment)
	} else {
		row = db.QueryRow(ctx, base+`e.key = $2
		  AND e.workspace_id = (SELECT m.workspace_id FROM workspace_members m WHERE m.user_id = $1::uuid)`,
			userID, segment)
	}
	var env Environment
	var role Role
	err := row.Scan(&env.ID, &env.WorkspaceID, &env.Key, &env.IsProduction, &role)
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
// auth.Middleware. It resolves the environment through the caller's own role
// rows and rejects with 404 when the caller can't see it, or 403 when they
// can but their role is too low.
func (g *Guard) Require(req Requirement, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		env, role, err := resolveEnvironment(r.Context(), g.db, user.ID, r.PathValue("envId"))
		if errors.Is(err, ErrEnvironmentNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "ENVIRONMENT_NOT_FOUND", "environment not found")
			return
		}
		if err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}

		if need := req(env); !role.AtLeast(need) {
			httpx.WriteError(w, http.StatusForbidden, "FORBIDDEN",
				"requires "+string(need)+" role in "+env.Key+"; you are "+string(role))
			return
		}

		ctx := context.WithValue(r.Context(), accessKey{}, Access{User: user, Env: env, Role: role})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
