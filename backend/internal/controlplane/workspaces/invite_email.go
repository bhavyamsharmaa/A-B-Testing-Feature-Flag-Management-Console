package workspaces

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/invitemail"
	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/controlplane/rbac"
)

// ResendCooldown is the shortest time between two emails for one invite.
const ResendCooldown = 10 * time.Minute

// emailStatusOf maps an outbox status to what the console shows.
func emailStatusOf(status string) string {
	switch status {
	case "":
		return "none"
	case outbox.StatusDelivered:
		return "sent"
	case outbox.StatusDead:
		return "failed"
	default: // pending, delivering, failed (a retry is scheduled)
		return "queued"
	}
}

// queueInviteEmail writes the outbox row for an invite email, in the caller's
// transaction. The daily limits are applied here (see quota.CheckInviteEmail); an
// email suppressed by the per-address limit is recorded as a dead row, so the
// invite's email status says plainly that it was not sent.
func (s *Service) queueInviteEmail(ctx context.Context, tx pgx.Tx, access rbac.WorkspaceAccess, inv Invite, token string) error {
	now := s.now()
	suppress, err := quota.CheckInviteEmail(ctx, tx, access.WorkspaceID, inv.Email, now)
	if err != nil {
		return err
	}
	var wsName string
	if err := tx.QueryRow(ctx, `SELECT name FROM workspaces WHERE id = $1::uuid`, access.WorkspaceID).Scan(&wsName); err != nil {
		return err
	}
	p := invitemail.Payload{
		To: inv.Email, Token: token, WorkspaceName: wsName, InviterName: access.User.Email,
		Role: string(inv.Role), ExpiresAt: inv.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if suppress {
		p.Token = "" // nothing will be sent, so nothing to protect
	}
	_, err = outbox.Enqueue(ctx, tx, outbox.Item{
		WorkspaceID: access.WorkspaceID, Kind: outbox.KindEmail, EventType: invitemail.EventType,
		InviteID: inv.ID, TargetKey: inv.Email, Payload: p,
	}, now)
	if err != nil || !suppress {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE outbox SET status = 'dead', last_error = 'not sent: too many invite emails to this address today; share the invite link instead', updated_at = $2
		WHERE id = (SELECT id FROM outbox WHERE workspace_id = $1::uuid AND invite_id = $3::uuid ORDER BY created_at DESC, id DESC LIMIT 1)`,
		access.WorkspaceID, now, inv.ID)
	return err
}

// ErrResendCooldown: the last email for this invite was queued less than ResendCooldown ago.
type ErrResendCooldown struct{ RetryAfter time.Duration }

func (e ErrResendCooldown) Error() string {
	return "an email for this invite was sent a moment ago; wait before sending another"
}

// ResendInvite sends the invite email again. The token cannot be recovered (only
// its hash is stored), so a resend issues a NEW token: the link in any earlier email,
// or copied earlier from the console, stops working, and any earlier email still
// queued is cancelled. The response is the same whether or not the address has an
// account, and the invite keeps its expiry.
func (s *Service) ResendInvite(ctx context.Context, access rbac.WorkspaceAccess, inviteID string) (Invite, string, error) {
	if !rbac.IsUUID(inviteID) {
		return Invite{}, "", apiError{404, "INVITE_NOT_FOUND", "no such open invite in this workspace"}
	}
	token, hash, err := newInviteToken()
	if err != nil {
		return Invite{}, "", err
	}
	var inv Invite
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The workspace row first (as every counted limit does), then the invite.
		if err := quota.LockWorkspace(ctx, tx, access.WorkspaceID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
			SELECT id::text, email, role::text, created_at, expires_at FROM workspace_invites
			WHERE id = $1::uuid AND workspace_id = $2::uuid AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
			FOR UPDATE`, inviteID, access.WorkspaceID).Scan(&inv.ID, &inv.Email, &inv.Role, &inv.CreatedAt, &inv.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return apiError{404, "INVITE_NOT_FOUND", "no such open invite in this workspace"}
		}
		if err != nil {
			return err
		}
		inv.InvitedByEmail = access.User.Email
		now := s.now()
		var last *time.Time
		if err := tx.QueryRow(ctx, `
			SELECT max(created_at) FROM outbox WHERE workspace_id = $1::uuid AND invite_id = $2::uuid AND kind = 'email'`,
			access.WorkspaceID, inv.ID).Scan(&last); err != nil {
			return err
		}
		if last != nil && now.Sub(*last) < ResendCooldown {
			return ErrResendCooldown{RetryAfter: ResendCooldown - now.Sub(*last)}
		}
		// Replace the token, and retire what was queued with the old one.
		if _, err := tx.Exec(ctx, `UPDATE workspace_invites SET token_hash = $2 WHERE id = $1::uuid`, inv.ID, hash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE outbox SET status = 'dead', last_error = 'not sent: a newer email replaced this one', payload = payload - 'token', updated_at = $3,
			       lease_owner = NULL, lease_expires_at = NULL
			WHERE workspace_id = $1::uuid AND invite_id = $2::uuid AND kind = 'email' AND status IN ('pending', 'failed')`,
			access.WorkspaceID, inv.ID, now); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: access.WorkspaceID, ActorID: access.User.ID, ActorEmail: access.User.Email,
			Action: "invite.resend", ResourceType: "invite", ResourceID: inv.ID, After: map[string]any{"email": inv.Email},
		}); err != nil {
			return err
		}
		return s.queueInviteEmail(ctx, tx, access, inv, token)
	})
	return inv, token, err
}
