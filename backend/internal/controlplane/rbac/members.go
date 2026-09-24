package rbac

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/httpx"
)

type MemberHandlers struct {
	pool *pgxpool.Pool
}

func NewMemberHandlers(pool *pgxpool.Pool) *MemberHandlers {
	return &MemberHandlers{pool: pool}
}

type environmentRole struct {
	Environment string `json:"environment"`
	Role        Role   `json:"role"`
}

type meResponse struct {
	ID    string            `json:"id"`
	Email string            `json:"email"`
	Roles []environmentRole `json:"roles"`
}

// Me handles GET /me: the caller plus their role in each environment they
// belong to. Environments they have no role in are omitted.
func (h *MemberHandlers) Me(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	rows, err := h.pool.Query(r.Context(), `
		SELECT e.key, r.role::text
		FROM user_environment_roles r
		JOIN environments e ON e.id = r.environment_id
		WHERE r.user_id = $1::uuid
		ORDER BY e.key`, user.ID)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	roles, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (environmentRole, error) {
		var er environmentRole
		err := row.Scan(&er.Environment, &er.Role)
		return er, err
	})
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	if roles == nil {
		roles = []environmentRole{}
	}
	httpx.WriteJSON(w, http.StatusOK, meResponse{ID: user.ID, Email: user.Email, Roles: roles})
}

type addMemberRequest struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

type memberResponse struct {
	UserID      string `json:"userId"`
	Email       string `json:"email"`
	Environment string `json:"environment"`
	Role        Role   `json:"role"`
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// AddMember handles POST /environments/{env}/members. It grants a role, or
// changes an existing member's role. The user must already have signed up
// through Supabase Auth; identify them by userId or email.
func (h *MemberHandlers) AddMember(w http.ResponseWriter, r *http.Request) {
	access := AccessFrom(r.Context())
	var req addMemberRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	role, ok := ParseRole(req.Role)
	if !ok {
		httpx.BadRequest(w, "role must be one of viewer, editor, approver, admin")
		return
	}
	if (req.UserID == "") == (req.Email == "") {
		httpx.BadRequest(w, "provide exactly one of userId or email")
		return
	}
	if req.UserID != "" && !uuidRE.MatchString(req.UserID) {
		httpx.BadRequest(w, "userId must be a UUID")
		return
	}

	var userID, email string
	err := h.pool.QueryRow(r.Context(), `
		SELECT id::text, COALESCE(email, '') FROM auth.users
		WHERE ($1 <> '' AND id = NULLIF($1, '')::uuid) OR ($2 <> '' AND lower(email) = lower($2))`,
		req.UserID, req.Email,
	).Scan(&userID, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "USER_NOT_FOUND",
			"no Supabase user with that id or email; they must sign up first")
		return
	}
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}

	err = pgx.BeginFunc(r.Context(), h.pool, func(tx pgx.Tx) error {
		admins, err := lockAdmins(r.Context(), tx, access.Env.ID)
		if err != nil {
			return err
		}
		var previous *Role
		err = tx.QueryRow(r.Context(), `
			SELECT role::text FROM user_environment_roles
			WHERE user_id = $1::uuid AND environment_id = $2::uuid`,
			userID, access.Env.ID,
		).Scan(&previous)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if previous != nil && *previous == Admin && role != Admin && admins == 1 {
			return errLastAdmin
		}

		if _, err := tx.Exec(r.Context(), `
			INSERT INTO user_environment_roles (user_id, environment_id, role)
			VALUES ($1::uuid, $2::uuid, $3::role_type)
			ON CONFLICT (user_id, environment_id)
			DO UPDATE SET role = EXCLUDED.role, updated_at = now()`,
			userID, access.Env.ID, string(role),
		); err != nil {
			return err
		}

		var before any
		if previous != nil {
			before = map[string]any{"role": *previous}
		}
		return audit.Write(r.Context(), tx, audit.Entry{
			ActorID:       access.User.ID,
			ActorEmail:    access.User.Email,
			EnvironmentID: access.Env.ID,
			Action:        "member.upsert",
			ResourceType:  "member",
			ResourceID:    userID,
			Before:        before,
			After:         map[string]any{"role": role, "email": email},
		})
	})
	if errors.Is(err, errLastAdmin) {
		writeLastAdmin(w, access.Env.Key)
		return
	}
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, memberResponse{
		UserID: userID, Email: email, Environment: access.Env.Key, Role: role,
	})
}

// RemoveMember handles DELETE /environments/{env}/members/{userId}.
func (h *MemberHandlers) RemoveMember(w http.ResponseWriter, r *http.Request) {
	access := AccessFrom(r.Context())
	userID := strings.ToLower(r.PathValue("userId"))
	if !uuidRE.MatchString(userID) {
		httpx.BadRequest(w, "userId must be a UUID")
		return
	}

	err := pgx.BeginFunc(r.Context(), h.pool, func(tx pgx.Tx) error {
		admins, err := lockAdmins(r.Context(), tx, access.Env.ID)
		if err != nil {
			return err
		}
		var current Role
		err = tx.QueryRow(r.Context(), `
			SELECT role::text FROM user_environment_roles
			WHERE user_id = $1::uuid AND environment_id = $2::uuid
			FOR UPDATE`,
			userID, access.Env.ID,
		).Scan(&current)
		if err != nil {
			return err
		}
		if current == Admin && admins == 1 {
			return errLastAdmin
		}
		if _, err := tx.Exec(r.Context(), `
			DELETE FROM user_environment_roles
			WHERE user_id = $1::uuid AND environment_id = $2::uuid`,
			userID, access.Env.ID,
		); err != nil {
			return err
		}
		return audit.Write(r.Context(), tx, audit.Entry{
			ActorID:       access.User.ID,
			ActorEmail:    access.User.Email,
			EnvironmentID: access.Env.ID,
			Action:        "member.remove",
			ResourceType:  "member",
			ResourceID:    userID,
			Before:        map[string]any{"role": current},
		})
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		httpx.WriteError(w, http.StatusNotFound, "MEMBER_NOT_FOUND", "that user has no role in "+access.Env.Key)
	case errors.Is(err, errLastAdmin):
		writeLastAdmin(w, access.Env.Key)
	case err != nil:
		httpx.WriteInternal(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

var errLastAdmin = errors.New("last admin")

func writeLastAdmin(w http.ResponseWriter, envKey string) {
	httpx.WriteError(w, http.StatusConflict, "LAST_ADMIN",
		"cannot remove or demote the last admin of "+envKey+"; promote another admin first")
}

// lockAdmins row-locks every admin of an environment and returns how many
// there are. Holding the locks for the rest of the transaction stops two
// admins from concurrently demoting each other and leaving none.
func lockAdmins(ctx context.Context, tx pgx.Tx, environmentID string) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT user_id::text FROM user_environment_roles
		WHERE environment_id = $1::uuid AND role = 'admin'
		FOR UPDATE`, environmentID)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return len(ids), err
}
