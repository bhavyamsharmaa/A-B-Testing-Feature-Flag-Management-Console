package workspaces

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/httpx"
)

// OfferTTL is how long an ownership offer stays open.
const OfferTTL = 7 * 24 * time.Hour

// OWNERSHIP TRANSFER
//
// An owner offers ownership to an existing admin of the same workspace; the admin accepts or
// declines, the owner can cancel, and an offer nobody answers expires after OfferTTL. Only one offer
// per workspace is open at a time (a partial unique index).
//
// ACCEPTANCE IS ONE TRANSACTION and its order is what keeps the workspace owned at every instant:
//
//  1. the workspace row is locked, then the per-user advisory lock of the new owner (so two
//     workspaces cannot both hand the same person a fifth workspace at once), then every membership
//     row of the workspace (the same locks ChangeRole and RemoveMember take, so a concurrent
//     demotion or removal queues behind this transaction and then sees the new state);
//  2. everything is checked again against the locked rows: the offer is still open and not expired,
//     the workspace is not pending deletion, the offerer is still an owner, the target is still a
//     member AND still an admin, and the target is under the owned-workspaces limit;
//  3. the target is promoted to owner FIRST, then the offerer is demoted to admin, then
//     workspaces.owner_id moves. Promote-then-demote means the owner count is never below one at any
//     statement, and the deferred last-owner trigger of 0007 (checked at commit) sees two owners
//     become one. The reverse order would also pass the trigger, because it is deferred, but a reader
//     or an error between the statements would see an ownerless workspace, so we never write it.

type OwnershipOffer struct {
	ID            string
	WorkspaceID   string
	WorkspaceName string
	FromUserID    string
	FromEmail     string
	ToUserID      string
	ToEmail       string
	Status        string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

var (
	errOfferNotFound = apiError{404, "OFFER_NOT_FOUND", "no such open ownership offer"}
	errOfferExists   = apiError{409, "OFFER_EXISTS", "this workspace already has an open ownership offer; cancel it to make another"}
)

const selectOffer = `
	SELECT o.id::text, o.workspace_id::text, w.name, o.from_user_id::text, COALESCE(f.email, ''), o.to_user_id::text, COALESCE(t.email, ''),
	       o.status, o.created_at, o.expires_at
	FROM ownership_offers o
	JOIN workspaces w ON w.id = o.workspace_id
	LEFT JOIN auth.users f ON f.id = o.from_user_id
	LEFT JOIN auth.users t ON t.id = o.to_user_id`

func scanOffer(row pgx.Row) (OwnershipOffer, error) {
	var o OwnershipOffer
	err := row.Scan(&o.ID, &o.WorkspaceID, &o.WorkspaceName, &o.FromUserID, &o.FromEmail, &o.ToUserID, &o.ToEmail, &o.Status, &o.CreatedAt, &o.ExpiresAt)
	return o, err
}

// expireStale marks the workspace's pending offer expired if its time has passed, with an audit entry.
// Called inside the transactions that create or read offers, so an expired offer is never treated as open.
func expireStale(ctx context.Context, tx pgx.Tx, workspaceID string, now time.Time) error {
	rows, err := tx.Query(ctx, `
		UPDATE ownership_offers SET status = 'expired', resolved_at = $2
		WHERE workspace_id = $1::uuid AND status = 'pending' AND expires_at <= $2
		RETURNING id::text, from_user_id::text, to_user_id::text`, workspaceID, now)
	if err != nil {
		return err
	}
	type e struct{ id, from, to string }
	expired, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (e, error) {
		var x e
		return x, r.Scan(&x.id, &x.from, &x.to)
	})
	if err != nil {
		return err
	}
	for _, x := range expired {
		if err := audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: workspaceID, ActorEmail: "system", Action: "ownership.expire", ResourceType: "ownership_offer", ResourceID: x.id,
			After: map[string]any{"fromUserId": x.from, "toUserId": x.to},
		}); err != nil {
			return err
		}
	}
	return nil
}

// ExpireOffers expires every pending offer past its time, across workspaces (the purge job's step).
func ExpireOffers(ctx context.Context, tx pgx.Tx, now time.Time) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT workspace_id::text FROM ownership_offers WHERE status = 'pending' AND expires_at <= $1 LIMIT 500`, now)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := expireStale(ctx, tx, id, now); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// OfferOwnership creates the offer. Only an owner may, to an admin of the same workspace.
func (s *Service) OfferOwnership(ctx context.Context, access rbac.WorkspaceAccess, targetID string) (OwnershipOffer, error) {
	if !rbac.IsUUID(targetID) {
		return OwnershipOffer{}, apiError{400, "INVALID_REQUEST", "userId must be the id of a member of this workspace"}
	}
	var out OwnershipOffer
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := quota.LockWorkspace(ctx, tx, access.WorkspaceID); err != nil {
			return err
		}
		roles, _, err := lockMembers(ctx, tx, access.WorkspaceID)
		if err != nil {
			return err
		}
		if roles[access.User.ID] != rbac.WorkspaceOwner {
			return roleError{"only an owner can offer ownership"}
		}
		if targetID == access.User.ID {
			return apiError{400, "INVALID_REQUEST", "you already own this workspace"}
		}
		role, member := roles[targetID]
		switch {
		case !member:
			return errMemberNotFound
		case role == rbac.WorkspaceOwner:
			return apiError{409, "ALREADY_OWNER", "that person is already an owner"}
		case role != rbac.WorkspaceAdmin:
			return apiError{409, "TARGET_NOT_ADMIN", "ownership can only be offered to an admin; make them an admin first"}
		}
		if err := expireStale(ctx, tx, access.WorkspaceID, s.now()); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO ownership_offers (workspace_id, from_user_id, to_user_id, created_at, expires_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5) RETURNING id::text`,
			access.WorkspaceID, access.User.ID, targetID, s.now(), s.now().Add(OfferTTL)).Scan(&id); err != nil {
			return err
		}
		if out, err = scanOffer(tx.QueryRow(ctx, selectOffer+` WHERE o.id = $1::uuid`, id)); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: access.WorkspaceID, ActorID: access.User.ID, ActorEmail: access.User.Email,
			Action: "ownership.offer", ResourceType: "ownership_offer", ResourceID: id, Severity: audit.SeverityCritical,
			After: map[string]any{"toUserId": targetID, "toEmail": out.ToEmail, "expiresAt": out.ExpiresAt.UTC().Format(time.RFC3339)},
		})
	})
	if isUniqueViolation(err, "one_pending_offer_per_workspace") {
		return OwnershipOffer{}, errOfferExists
	}
	return out, err
}

// OpenOffer is the workspace's open offer, if any (expired ones are not open).
func (s *Service) OpenOffer(ctx context.Context, workspaceID string) (*OwnershipOffer, error) {
	o, err := scanOffer(s.pool.QueryRow(ctx, selectOffer+` WHERE o.workspace_id = $1::uuid AND o.status = 'pending' AND o.expires_at > $2`, workspaceID, s.now()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &o, err
}

// CancelOffer withdraws the workspace's open offer (owner).
func (s *Service) CancelOffer(ctx context.Context, access rbac.WorkspaceAccess, offerID string) error {
	if !rbac.IsUUID(offerID) {
		return errOfferNotFound
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var to string
		err := tx.QueryRow(ctx, `
			UPDATE ownership_offers SET status = 'cancelled', resolved_at = $3
			WHERE id = $1::uuid AND workspace_id = $2::uuid AND status = 'pending' RETURNING to_user_id::text`,
			offerID, access.WorkspaceID, s.now()).Scan(&to)
		if errors.Is(err, pgx.ErrNoRows) {
			return errOfferNotFound
		}
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: access.WorkspaceID, ActorID: access.User.ID, ActorEmail: access.User.Email,
			Action: "ownership.cancel", ResourceType: "ownership_offer", ResourceID: offerID, After: map[string]any{"toUserId": to},
		})
	})
}

// DeclineOffer is the target saying no.
func (s *Service) DeclineOffer(ctx context.Context, user auth.User, offerID string) error {
	if !rbac.IsUUID(offerID) {
		return errOfferNotFound
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ws string
		err := tx.QueryRow(ctx, `
			UPDATE ownership_offers o SET status = 'declined', resolved_at = $3
			WHERE o.id = $1::uuid AND o.to_user_id = $2::uuid AND o.status = 'pending'
			  AND EXISTS (SELECT 1 FROM workspaces w WHERE w.id = o.workspace_id AND w.deleted_at IS NULL)
			RETURNING o.workspace_id::text`, offerID, user.ID, s.now()).Scan(&ws)
		if errors.Is(err, pgx.ErrNoRows) {
			return errOfferNotFound
		}
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: ws, ActorID: user.ID, ActorEmail: user.Email,
			Action: "ownership.decline", ResourceType: "ownership_offer", ResourceID: offerID,
		})
	})
}

// AcceptOffer makes the caller the owner and the offerer an admin, in one transaction (see the top of this file).
func (s *Service) AcceptOffer(ctx context.Context, user auth.User, offerID string) (string, error) {
	if !rbac.IsUUID(offerID) {
		return "", errOfferNotFound
	}
	var workspaceID string
	expired := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var from, status string
		var expiresAt time.Time
		// Only the person it is addressed to can even see it.
		err := tx.QueryRow(ctx, `
			SELECT workspace_id::text, from_user_id::text, status, expires_at FROM ownership_offers
			WHERE id = $1::uuid AND to_user_id = $2::uuid`, offerID, user.ID).Scan(&workspaceID, &from, &status, &expiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return errOfferNotFound
		}
		if err != nil {
			return err
		}
		// The locks, in the order every other membership change takes them.
		if err := quota.LockWorkspace(ctx, tx, workspaceID); err != nil {
			return err
		}
		if err := lockUser(ctx, tx, user.ID); err != nil {
			return err
		}
		roles, _, err := lockMembers(ctx, tx, workspaceID)
		if err != nil {
			return err
		}
		// Re-read the offer under the locks: it may have been cancelled while we waited.
		if err := tx.QueryRow(ctx, `SELECT status, expires_at FROM ownership_offers WHERE id = $1::uuid FOR UPDATE`, offerID).Scan(&status, &expiresAt); err != nil {
			return err
		}
		if status != "pending" {
			return apiError{409, "OFFER_NOT_OPEN", "this offer is no longer open (it was " + status + ")"}
		}
		now := s.now()
		if !now.Before(expiresAt) {
			expired = true
			return apiError{410, "OFFER_EXPIRED", "this offer expired; ask the owner to make a new one"}
		}
		var deleted bool
		if err := tx.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM workspaces WHERE id = $1::uuid`, workspaceID).Scan(&deleted); err != nil {
			return err
		}
		if deleted {
			return errOfferNotFound // a workspace scheduled for deletion has no offers to anyone
		}
		if roles[from] != rbac.WorkspaceOwner {
			return apiError{409, "OFFER_NO_LONGER_VALID", "the person who made this offer is no longer an owner of the workspace"}
		}
		if role, member := roles[user.ID]; !member || role != rbac.WorkspaceAdmin {
			return apiError{409, "OFFER_NO_LONGER_VALID", "you are no longer an admin of this workspace, so this offer cannot be accepted"}
		}
		if err := quota.CheckOwnedWorkspaces(ctx, tx, user.ID); err != nil {
			var ex quota.Exceeded
			if errors.As(err, &ex) {
				return apiError{409, "QUOTA_EXCEEDED", "you already own the most workspaces allowed (" + itoa(ex.Limit) + "); delete or hand over one of yours before accepting this"}
			}
			return err
		}
		// Promote first, then demote: there are never zero owners at any statement.
		if _, err := tx.Exec(ctx, `UPDATE workspace_members SET role = 'owner' WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, workspaceID, user.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE workspace_members SET role = 'admin' WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, workspaceID, from); err != nil {
			return err
		}
		// (A personal workspace stops being personal when it changes hands: a person has at most one, their own.)
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET owner_id = $2::uuid, is_personal = false WHERE id = $1::uuid`, workspaceID, user.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE ownership_offers SET status = 'accepted', resolved_at = $2 WHERE id = $1::uuid`, offerID, now); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: workspaceID, ActorID: user.ID, ActorEmail: user.Email,
			Action: "ownership.accept", ResourceType: "ownership_offer", ResourceID: offerID, Severity: audit.SeverityCritical,
			Before: map[string]any{"owner": from, "newOwnerRole": "admin"},
			After:  map[string]any{"owner": user.ID, "previousOwnerRole": "admin"},
		})
	})
	if expired {
		// The refusal rolled the transaction back; record the expiry on its own.
		_ = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return expireStale(ctx, tx, workspaceID, s.now()) })
	}
	return workspaceID, lastOwnerViolation(err)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// OffersFor lists the open offers addressed to the user (shown as a banner in the console).
func (s *Service) OffersFor(ctx context.Context, userID string) ([]OwnershipOffer, error) {
	rows, err := s.pool.Query(ctx, selectOffer+`
		WHERE o.to_user_id = $1::uuid AND o.status = 'pending' AND o.expires_at > $2 AND w.deleted_at IS NULL
		ORDER BY o.created_at DESC`, userID, s.now())
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (OwnershipOffer, error) { return scanOffer(r) })
	if list == nil {
		list = []OwnershipOffer{}
	}
	return list, err
}

// ---- handlers -----------------------------------------------------------------------------------

type offerView struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspaceId"`
	WorkspaceName string    `json:"workspaceName"`
	FromEmail     string    `json:"fromEmail"`
	ToUserID      string    `json:"toUserId"`
	ToEmail       string    `json:"toEmail"`
	CreatedAt     time.Time `json:"createdAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

func offerOf(o OwnershipOffer) offerView {
	return offerView{ID: o.ID, WorkspaceID: o.WorkspaceID, WorkspaceName: o.WorkspaceName, FromEmail: o.FromEmail, ToUserID: o.ToUserID, ToEmail: o.ToEmail, CreatedAt: o.CreatedAt, ExpiresAt: o.ExpiresAt}
}

// OfferOwnership handles POST /workspaces/{wsId}/ownership-offers (owner, session only).
func (h *Handlers) OfferOwnership(w http.ResponseWriter, r *http.Request) {
	access := rbac.WorkspaceAccessFrom(r.Context())
	var req struct {
		UserID string `json:"userId"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	o, err := h.svc.OfferOwnership(r.Context(), access, req.UserID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"offer": offerOf(o)})
}

// OpenOffer handles GET /workspaces/{wsId}/ownership-offers (owner): the open offer or null.
func (h *Handlers) OpenOffer(w http.ResponseWriter, r *http.Request) {
	access := rbac.WorkspaceAccessFrom(r.Context())
	o, err := h.svc.OpenOffer(r.Context(), access.WorkspaceID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var v *offerView
	if o != nil {
		x := offerOf(*o)
		v = &x
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"offer": v})
}

// CancelOffer handles DELETE /workspaces/{wsId}/ownership-offers/{offerId} (owner).
func (h *Handlers) CancelOffer(w http.ResponseWriter, r *http.Request) {
	access := rbac.WorkspaceAccessFrom(r.Context())
	if err := h.svc.CancelOffer(r.Context(), access, pathID(r, "offerId")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AcceptOffer handles POST /ownership-offers/{offerId}/accept (the person it is addressed to).
func (h *Handlers) AcceptOffer(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	ws, err := h.svc.AcceptOffer(r.Context(), user, pathID(r, "offerId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"workspaceId": ws})
}

// DeclineOffer handles POST /ownership-offers/{offerId}/decline.
func (h *Handlers) DeclineOffer(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	if err := h.svc.DeclineOffer(r.Context(), user, pathID(r, "offerId")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
