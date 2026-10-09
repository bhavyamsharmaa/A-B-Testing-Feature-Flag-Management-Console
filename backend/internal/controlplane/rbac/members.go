package rbac

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/platform/httpx"
)

type MemberHandlers struct {
	pool *pgxpool.Pool
}

func NewMemberHandlers(pool *pgxpool.Pool) *MemberHandlers {
	return &MemberHandlers{pool: pool}
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

// AddMember handles POST /environments/{envId}/members. It grants a role, or
// changes an existing member's role. The user must already have signed up
// through Supabase Auth; identify them by userId or email.
//
// A user belongs to exactly one workspace (see decideMembership):
//   - already in the caller's workspace: their role can be changed, naming
//     them by userId or email;
//   - no workspace yet (they have never opened the console): they can be
//     added, but ONLY by userId. An email alone would let an admin probe
//     which addresses have signed up, so that case answers like an unknown
//     user;
//   - in ANOTHER workspace: the same 404 USER_NOT_FOUND as an unknown user,
//     so a tenant can't learn who else uses Helios.
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
		writeUserNotFound(w)
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
		var memberOf *string
		err = tx.QueryRow(r.Context(), `SELECT workspace_id::text FROM workspace_members WHERE user_id = $1::uuid`, userID).Scan(&memberOf)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch decideMembership(memberOf, access.Env.WorkspaceID, req.UserID != "") {
		case membershipDeny:
			return errUserUnavailable
		case membershipAdopt:
			if _, err := tx.Exec(r.Context(), `
				INSERT INTO workspace_members (user_id, workspace_id) VALUES ($1::uuid, $2::uuid)`,
				userID, access.Env.WorkspaceID,
			); err != nil {
				return err
			}
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
			INSERT INTO user_environment_roles (user_id, environment_id, workspace_id, role)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::role_type)
			ON CONFLICT (user_id, environment_id)
			DO UPDATE SET role = EXCLUDED.role, updated_at = now()`,
			userID, access.Env.ID, access.Env.WorkspaceID, string(role),
		); err != nil {
			return err
		}

		var before any
		if previous != nil {
			before = map[string]any{"role": *previous}
		}
		return audit.Write(r.Context(), tx, audit.Entry{
			WorkspaceID:   access.Env.WorkspaceID,
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
	// The user signed in and got their own workspace between the checks above
	// and the insert: the primary key on workspace_members.user_id fired.
	if errors.Is(err, errUserUnavailable) || isUniqueViolation(err, "workspace_members_pkey") {
		writeUserNotFound(w)
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

// RemoveMember handles DELETE /environments/{envId}/members/{userId}. When it
// removes the user's last role in the workspace, their membership goes too,
// so they are not stranded without a workspace of their own.
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
		if _, err := tx.Exec(r.Context(), `
			DELETE FROM workspace_members m
			WHERE m.user_id = $1::uuid AND m.workspace_id = $2::uuid
			  AND NOT EXISTS (
			    SELECT 1 FROM user_environment_roles r
			    WHERE r.user_id = m.user_id AND r.workspace_id = m.workspace_id)`,
			userID, access.Env.WorkspaceID,
		); err != nil {
			return err
		}
		return audit.Write(r.Context(), tx, audit.Entry{
			WorkspaceID:   access.Env.WorkspaceID,
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

// errUserUnavailable: the target can't be added by this caller (another
// workspace, or no workspace and identified by email only). Reported exactly
// like an unknown user.
var errUserUnavailable = errors.New("user not available to add")

type membershipDecision int

const (
	membershipAllow membershipDecision = iota // already in the caller's workspace
	membershipAdopt                           // no workspace: add them to the caller's
	membershipDeny                            // not addable by this caller
)

// decideMembership is the whole policy for whether AddMember may proceed.
// memberOf is the target's current workspace id (nil when they have none);
// byUserID is whether the request named them by userId rather than email.
func decideMembership(memberOf *string, callerWorkspaceID string, byUserID bool) membershipDecision {
	switch {
	case memberOf == nil:
		if byUserID {
			return membershipAdopt
		}
		return membershipDeny
	case *memberOf == callerWorkspaceID:
		return membershipAllow
	default:
		return membershipDeny
	}
}

func writeUserNotFound(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusNotFound, "USER_NOT_FOUND",
		"no user with that id or email is available to add; they must sign up first")
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

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
