package workspaces

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/auth"
)

const inviteTTL = 7 * 24 * time.Hour

// Create makes another workspace owned by user and switches to it. A user
// may own at most quota.MaxOwnedWorkspaces.
func (s *Service) Create(ctx context.Context, user auth.User, rawName string) (Workspace, error) {
	name, err := cleanName(rawName)
	if err != nil {
		return Workspace{}, apiError{400, "INVALID_REQUEST", err.Error()}
	}
	var id string
	err = withSlugRetry(func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if err := lockUser(ctx, tx, user.ID); err != nil {
				return err
			}
			if err := quota.CheckOwnedWorkspaces(ctx, tx, user.ID); err != nil {
				return err
			}
			id, err = createWorkspaceTx(ctx, tx, user, name, false)
			return err
		})
	})
	if err != nil {
		return Workspace{}, err
	}
	list, err := load(ctx, s.pool, user.ID)
	if err != nil {
		return Workspace{}, err
	}
	for _, w := range list {
		if w.ID == id {
			return w, nil
		}
	}
	return Workspace{}, errors.New("workspaces: created workspace not found")
}

// Rename changes a workspace's name (admin+, enforced by the route).
func (s *Service) Rename(ctx context.Context, access rbac.WorkspaceAccess, rawName string) (string, error) {
	name, err := cleanName(rawName)
	if err != nil {
		return "", apiError{400, "INVALID_REQUEST", err.Error()}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var before string
		if err := tx.QueryRow(ctx, `SELECT name FROM workspaces WHERE id = $1::uuid FOR UPDATE`, access.WorkspaceID).Scan(&before); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET name = $2 WHERE id = $1::uuid`, access.WorkspaceID, name); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID:  access.WorkspaceID,
			ActorID:      access.User.ID,
			ActorEmail:   access.User.Email,
			Action:       "workspace.rename",
			ResourceType: "workspace",
			ResourceID:   access.WorkspaceID,
			Before:       map[string]string{"name": before},
			After:        map[string]string{"name": name},
		})
	})
	return name, err
}

// Switch marks a workspace as the caller's active one (the first in /me).
func (s *Service) Switch(ctx context.Context, access rbac.WorkspaceAccess) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workspace_members SET last_active_at = now()
		WHERE user_id = $1::uuid AND workspace_id = $2::uuid`, access.User.ID, access.WorkspaceID)
	return err
}

type Member struct {
	UserID   string
	Email    string
	Role     rbac.WorkspaceRole
	JoinedAt time.Time
}

func (s *Service) Members(ctx context.Context, workspaceID string) ([]Member, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.user_id::text, COALESCE(u.email, ''), m.role::text, m.created_at
		FROM workspace_members m LEFT JOIN auth.users u ON u.id = m.user_id
		WHERE m.workspace_id = $1::uuid
		ORDER BY CASE m.role WHEN 'owner' THEN 1 WHEN 'admin' THEN 2 WHEN 'editor' THEN 3 ELSE 4 END, u.email`, workspaceID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Member, error) {
		var m Member
		err := row.Scan(&m.UserID, &m.Email, &m.Role, &m.JoinedAt)
		return m, err
	})
	if list == nil {
		list = []Member{}
	}
	return list, err
}

// lockMembers locks every membership row of the workspace for the rest of
// the transaction and returns user -> role and the number of owners. Holding
// them stops two admins from concurrently demoting or removing each other
// and leaving the workspace without an owner.
func lockMembers(ctx context.Context, tx pgx.Tx, workspaceID string) (map[string]rbac.WorkspaceRole, int, error) {
	rows, err := tx.Query(ctx, `
		SELECT user_id::text, role::text FROM workspace_members
		WHERE workspace_id = $1::uuid FOR UPDATE`, workspaceID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	roles := map[string]rbac.WorkspaceRole{}
	owners := 0
	for rows.Next() {
		var id string
		var role rbac.WorkspaceRole
		if err := rows.Scan(&id, &role); err != nil {
			return nil, 0, err
		}
		roles[id] = role
		if role == rbac.WorkspaceOwner {
			owners++
		}
	}
	return roles, owners, rows.Err()
}

var (
	errMemberNotFound = apiError{404, "MEMBER_NOT_FOUND", "no such member in this workspace"}
	errLastOwner      = apiError{409, "LAST_OWNER", "a workspace needs at least one owner; make someone else an owner first"}
)

// lastOwnerViolation turns the database's own guarantee (migration 0007: a
// workspace with members keeps an owner) into the same answer the application
// gives when it spots the problem first.
func lastOwnerViolation(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "workspace_keeps_an_owner" {
		return errLastOwner
	}
	return err
}

// ChangeRole moves a member to a new role.
func (s *Service) ChangeRole(ctx context.Context, access rbac.WorkspaceAccess, targetID string, next rbac.WorkspaceRole) error {
	return lastOwnerViolation(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		roles, owners, err := lockMembers(ctx, tx, access.WorkspaceID)
		if err != nil {
			return err
		}
		actor, ok := roles[access.User.ID]
		if !ok {
			return errMemberNotFound
		}
		current, ok := roles[targetID]
		if !ok {
			return errMemberNotFound
		}
		if current == next {
			return nil
		}
		if err := canChangeRole(actor, current, next); err != nil {
			return err
		}
		if wouldLeaveNoOwner(owners, current, next) {
			return errLastOwner
		}
		if _, err := tx.Exec(ctx, `
			UPDATE workspace_members SET role = $3::workspace_role
			WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, access.WorkspaceID, targetID, string(next)); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID:  access.WorkspaceID,
			ActorID:      access.User.ID,
			ActorEmail:   access.User.Email,
			Action:       "member.role_change",
			ResourceType: "member",
			ResourceID:   targetID,
			Before:       map[string]any{"role": current},
			After:        map[string]any{"role": next},
		})
	}))
}

// RemoveMember removes a member, or lets the caller leave (targetID is their
// own id).
func (s *Service) RemoveMember(ctx context.Context, access rbac.WorkspaceAccess, targetID string) error {
	return lastOwnerViolation(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		roles, owners, err := lockMembers(ctx, tx, access.WorkspaceID)
		if err != nil {
			return err
		}
		actor, ok := roles[access.User.ID]
		if !ok {
			return errMemberNotFound
		}
		target, ok := roles[targetID]
		if !ok {
			return errMemberNotFound
		}
		if err := canRemove(actor, target, targetID == access.User.ID); err != nil {
			return err
		}
		if wouldLeaveNoOwner(owners, target, "") {
			return errLastOwner
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM workspace_members WHERE workspace_id = $1::uuid AND user_id = $2::uuid`,
			access.WorkspaceID, targetID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID:  access.WorkspaceID,
			ActorID:      access.User.ID,
			ActorEmail:   access.User.Email,
			Action:       "member.remove",
			ResourceType: "member",
			ResourceID:   targetID,
			Before:       map[string]any{"role": target},
		})
	}))
}

type Invite struct {
	ID             string
	Email          string
	Role           rbac.WorkspaceRole
	InvitedByEmail string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

func (s *Service) Invites(ctx context.Context, workspaceID string) ([]Invite, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT i.id::text, i.email, i.role::text, COALESCE(u.email, ''), i.created_at, i.expires_at
		FROM workspace_invites i LEFT JOIN auth.users u ON u.id = i.invited_by
		WHERE i.workspace_id = $1::uuid AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now()
		ORDER BY i.created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Invite, error) {
		var i Invite
		err := row.Scan(&i.ID, &i.Email, &i.Role, &i.InvitedByEmail, &i.CreatedAt, &i.ExpiresAt)
		return i, err
	})
	if list == nil {
		list = []Invite{}
	}
	return list, err
}

// CreateInvite records an invite and returns it with its one-time token,
// which is never stored and can't be shown again.
func (s *Service) CreateInvite(ctx context.Context, access rbac.WorkspaceAccess, rawEmail string, role rbac.WorkspaceRole) (Invite, string, error) {
	email, ok := normalizeEmail(rawEmail)
	if !ok {
		return Invite{}, "", apiError{400, "INVALID_REQUEST", "email must be a valid address"}
	}
	if err := canInvite(access.Role, role); err != nil {
		return Invite{}, "", err
	}
	token, hash, err := newInviteToken()
	if err != nil {
		return Invite{}, "", err
	}
	var inv Invite
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Locks the workspace row first (see quota), then counts.
		if err := quota.CheckMember(ctx, tx, access.WorkspaceID); err != nil {
			return err
		}
		var already bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM workspace_members m JOIN auth.users u ON u.id = m.user_id
				WHERE m.workspace_id = $1::uuid AND lower(u.email) = $2)`,
			access.WorkspaceID, email).Scan(&already); err != nil {
			return err
		}
		if already {
			return apiError{409, "ALREADY_MEMBER", "that person is already a member of this workspace"}
		}
		// An expired invite would still occupy the one-open-invite slot.
		if _, err := tx.Exec(ctx, `
			UPDATE workspace_invites SET revoked_at = now()
			WHERE workspace_id = $1::uuid AND email = $2 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at <= now()`,
			access.WorkspaceID, email); err != nil {
			return err
		}
		inv = Invite{Email: email, Role: role, InvitedByEmail: access.User.Email}
		if err := tx.QueryRow(ctx, `
			INSERT INTO workspace_invites (workspace_id, email, role, token_hash, invited_by, expires_at)
			VALUES ($1::uuid, $2, $3::workspace_role, $4, $5::uuid, now() + $6::interval)
			RETURNING id::text, created_at, expires_at`,
			access.WorkspaceID, email, string(role), hash, access.User.ID, inviteTTL.String(),
		).Scan(&inv.ID, &inv.CreatedAt, &inv.ExpiresAt); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID:  access.WorkspaceID,
			ActorID:      access.User.ID,
			ActorEmail:   access.User.Email,
			Action:       "invite.create",
			ResourceType: "invite",
			ResourceID:   inv.ID,
			After:        map[string]any{"email": email, "role": role},
		})
	})
	if isUniqueViolation(err, "one_open_invite_per_email") {
		return Invite{}, "", apiError{409, "INVITE_EXISTS", "there is already an open invite for that email; revoke it to send a new one"}
	}
	return inv, token, err
}

// RevokeInvite cancels an open invite of this workspace.
func (s *Service) RevokeInvite(ctx context.Context, access rbac.WorkspaceAccess, inviteID string) error {
	if !rbac.IsUUID(inviteID) {
		return apiError{404, "INVITE_NOT_FOUND", "no such open invite in this workspace"}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var email string
		err := tx.QueryRow(ctx, `
			UPDATE workspace_invites SET revoked_at = now()
			WHERE id = $1::uuid AND workspace_id = $2::uuid AND accepted_at IS NULL AND revoked_at IS NULL
			RETURNING email`, inviteID, access.WorkspaceID).Scan(&email)
		if errors.Is(err, pgx.ErrNoRows) {
			return apiError{404, "INVITE_NOT_FOUND", "no such open invite in this workspace"}
		}
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID:  access.WorkspaceID,
			ActorID:      access.User.ID,
			ActorEmail:   access.User.Email,
			Action:       "invite.revoke",
			ResourceType: "invite",
			ResourceID:   inviteID,
			Before:       map[string]any{"email": email},
		})
	})
}

// AcceptInvite adds the caller to the invite's workspace with the invited
// role. The invite is named by its one-time token or by its id (as listed in
// /me for the caller's own email); either way the invite's email must be the
// caller's.
func (s *Service) AcceptInvite(ctx context.Context, user auth.User, token, inviteID string) (string, error) {
	email, ok := normalizeEmail(user.Email)
	if !ok {
		return "", roleError{"your account has no usable email address, so it can't accept invites"}
	}
	var workspaceID string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		var inviteEmail string
		var role rbac.WorkspaceRole
		var expired bool
		var err error
		switch {
		case token != "":
			err = tx.QueryRow(ctx, `
				SELECT id::text, workspace_id::text, email, role::text, expires_at <= now()
				FROM workspace_invites
				WHERE token_hash = $1 AND accepted_at IS NULL AND revoked_at IS NULL FOR UPDATE`,
				hashToken(strings.TrimSpace(token))).Scan(&id, &workspaceID, &inviteEmail, &role, &expired)
		default:
			if !rbac.IsUUID(inviteID) {
				return errInviteNotFound
			}
			// By id: only invites addressed to the caller exist for them.
			err = tx.QueryRow(ctx, `
				SELECT id::text, workspace_id::text, email, role::text, expires_at <= now()
				FROM workspace_invites
				WHERE id = $1::uuid AND email = $2 AND accepted_at IS NULL AND revoked_at IS NULL FOR UPDATE`,
				inviteID, email).Scan(&id, &workspaceID, &inviteEmail, &role, &expired)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return errInviteNotFound
		}
		if err != nil {
			return err
		}
		if inviteEmail != email {
			return roleError{"this invite was sent to a different email address; sign in with the invited address"}
		}
		if expired {
			return apiError{410, "INVITE_EXPIRED", "this invite has expired; ask for a new one"}
		}
		// Already a member (say, via an earlier invite): keep the existing role.
		if _, err := tx.Exec(ctx, `
			INSERT INTO workspace_members (user_id, workspace_id, role, last_active_at)
			VALUES ($1::uuid, $2::uuid, $3::workspace_role, now())
			ON CONFLICT (user_id, workspace_id) DO UPDATE SET last_active_at = now()`,
			user.ID, workspaceID, string(role)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE workspace_invites SET accepted_at = now(), accepted_by = $2::uuid WHERE id = $1::uuid`,
			id, user.ID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID:  workspaceID,
			ActorID:      user.ID,
			ActorEmail:   user.Email,
			Action:       "invite.accept",
			ResourceType: "invite",
			ResourceID:   id,
			After:        map[string]any{"email": email, "role": role},
		})
	})
	return workspaceID, err
}

var errInviteNotFound = apiError{404, "INVITE_NOT_FOUND", "this invite is invalid, already used, or was revoked"}
