// Package rbac provides server-side role enforcement for the control plane
// (viewer / editor / approver / admin), scoped per environment. The console
// hiding a button is cosmetic; this package is the actual boundary (US-06 AC-1).
package rbac

import (
	"context"
	"errors"
	"net/http"

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

type Environment struct {
	ID           string
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

var ErrEnvironmentNotFound = errors.New("environment not found")

// LoadEnvironment looks up an environment by its key (e.g. "production").
func LoadEnvironment(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, key string) (Environment, error) {
	var env Environment
	err := q.QueryRow(ctx,
		`SELECT id::text, key, is_production FROM environments WHERE key = $1`, key,
	).Scan(&env.ID, &env.Key, &env.IsProduction)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, ErrEnvironmentNotFound
	}
	return env, err
}

type Guard struct {
	pool *pgxpool.Pool
}

func NewGuard(pool *pgxpool.Pool) *Guard {
	return &Guard{pool: pool}
}

// Require wraps a route that has an {env} path parameter. It must run after
// auth.Middleware. It resolves the environment, looks up the caller's role
// there, and rejects with 403 unless that role meets req.
func (g *Guard) Require(req Requirement, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		env, err := LoadEnvironment(r.Context(), g.pool, r.PathValue("env"))
		if errors.Is(err, ErrEnvironmentNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "ENVIRONMENT_NOT_FOUND", "unknown environment "+r.PathValue("env"))
			return
		}
		if err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}

		var role Role
		err = g.pool.QueryRow(r.Context(), `
			SELECT role::text FROM user_environment_roles
			WHERE user_id = $1::uuid AND environment_id = $2::uuid`,
			user.ID, env.ID,
		).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusForbidden, "FORBIDDEN", "you have no role in environment "+env.Key)
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
