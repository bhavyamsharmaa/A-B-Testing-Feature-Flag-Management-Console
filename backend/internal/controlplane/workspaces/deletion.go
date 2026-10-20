package workspaces

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/httpx"
)

// DeletionGrace is how long a workspace scheduled for deletion can still be restored.
const DeletionGrace = 14 * 24 * time.Hour

// PlatformWorkspaceID is the "workspace" under which lifecycle events about workspaces that no longer exist
// are audited. It is the nil UUID, which no workspace has, so no tenant can ever read these entries, and a
// purged workspace's own id appears only as the entry's resource id.
const PlatformWorkspaceID = "00000000-0000-0000-0000-000000000000"

// WORKSPACE DELETION is two steps, and only the first happens in the API.
//
// 1. ScheduleDeletion (an owner, session only, after typing the workspace's name) sets deleted_at and
//    purge_after = deleted_at + 14 days. From that instant the workspace is invisible: the two rbac membership
//    queries and workspaces.load all require deleted_at IS NULL, so every route answers 404 to every member and
//    every API token exactly as to a stranger; SDK keys stop verifying and their streams close (the cache is
//    dropped on this instance and announced to the others); the scheduler and the outbox skip it; invites for
//    it cannot be accepted. NOTHING is deleted or changed: the owner can Restore until purge_after.
// 2. cmd/purgeworkspaces (or POST /internal/workspaces/purge), run by a scheduler, removes the data of workspaces
//    past purge_after, in bounded batches. Never in the API process.
//
// A workspace pending deletion still counts toward its owner's owned-workspaces limit until it is purged: it can
// come back, and letting its owner create five more in the meantime would make Restore fail on the limit.

// WorkspaceDropper drops what this instance has cached about a workspace (SDK key verifications, streams).
type WorkspaceDropper interface{ InvalidateWorkspace(workspaceID string) }

// WithRuntime gives the service what it needs to drop caches when a workspace is scheduled for deletion: the
// local SDK key verifier, and the publisher that tells the other instances.
func (s *Service) WithRuntime(d WorkspaceDropper, pub events.Publisher) *Service {
	s.drop, s.pub = d, pub
	return s
}

func (s *Service) announceDrop(ctx context.Context, workspaceID string) {
	if s.drop != nil {
		s.drop.InvalidateWorkspace(workspaceID) // this instance, at once
	}
	if s.pub != nil { // every other instance
		if err := s.pub.Publish(ctx, events.RevocationChannel, []byte(apikey.WorkspaceMessage(workspaceID))); err != nil {
			log.Printf("workspaces: could not announce a workspace drop (other instances honour it within their cache TTL and the streams' re-check): %v", err)
		}
	}
}

type PendingDeletion struct {
	ID         string
	Name       string
	DeletedAt  time.Time
	PurgeAfter time.Time
}

var (
	errNameMismatch  = apiError{400, "NAME_MISMATCH", "type the workspace's exact name to confirm"}
	errNotRestorable = apiError{404, "WORKSPACE_NOT_FOUND", "workspace not found"}
)

// ScheduleDeletion hides the workspace and starts the grace period.
func (s *Service) ScheduleDeletion(ctx context.Context, access rbac.WorkspaceAccess, confirmName string) (PendingDeletion, error) {
	var out PendingDeletion
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := quota.LockWorkspace(ctx, tx, access.WorkspaceID); err != nil {
			return err
		}
		roles, _, err := lockMembers(ctx, tx, access.WorkspaceID)
		if err != nil {
			return err
		}
		if roles[access.User.ID] != rbac.WorkspaceOwner {
			return roleError{"only an owner can delete a workspace"}
		}
		var name string
		var already bool
		if err := tx.QueryRow(ctx, `SELECT name, deleted_at IS NOT NULL FROM workspaces WHERE id = $1::uuid`, access.WorkspaceID).Scan(&name, &already); err != nil {
			return err
		}
		if already {
			return errNotRestorable
		}
		if strings.TrimSpace(confirmName) != name {
			return errNameMismatch
		}
		now := s.now()
		out = PendingDeletion{ID: access.WorkspaceID, Name: name, DeletedAt: now, PurgeAfter: now.Add(DeletionGrace)}
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET deleted_at = $2, deleted_by = $3::uuid, purge_after = $4 WHERE id = $1::uuid`,
			access.WorkspaceID, out.DeletedAt, access.User.ID, out.PurgeAfter); err != nil {
			return err
		}
		// An open ownership offer cannot be answered for a workspace that is going away.
		if _, err := tx.Exec(ctx, `UPDATE ownership_offers SET status = 'cancelled', resolved_at = $2 WHERE workspace_id = $1::uuid AND status = 'pending'`, access.WorkspaceID, now); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: access.WorkspaceID, ActorID: access.User.ID, ActorEmail: access.User.Email,
			Action: "workspace.delete_scheduled", ResourceType: "workspace", ResourceID: access.WorkspaceID, Severity: audit.SeverityCritical,
			After: map[string]any{"purgeAfter": out.PurgeAfter.UTC().Format(time.RFC3339)},
		}); err != nil {
			return err
		}
		return platformAudit(ctx, tx, "workspace.delete_scheduled", access.WorkspaceID, access.User.ID, map[string]any{"purgeAfter": out.PurgeAfter.UTC().Format(time.RFC3339)})
	})
	if err != nil {
		return PendingDeletion{}, err
	}
	s.announceDrop(ctx, out.ID)
	return out, nil
}

// platformAudit writes an entry under PlatformWorkspaceID: the workspace's id and the actor's id, counts and
// dates, never a name, an email or any content.
func platformAudit(ctx context.Context, tx pgx.Tx, action, workspaceID, actorID string, after map[string]any) error {
	return audit.Write(ctx, tx, audit.Entry{
		WorkspaceID: PlatformWorkspaceID, ActorID: actorID, ActorEmail: "(workspace lifecycle)",
		Action: action, ResourceType: "workspace", ResourceID: workspaceID, Severity: audit.SeverityCritical, After: after,
	})
}

// Restore brings a workspace back during its grace period. Only an owner of it can. Nothing was deleted, so
// nothing is rebuilt: the three columns are cleared.
func (s *Service) Restore(ctx context.Context, user auth.User, workspaceID string) error {
	if !rbac.IsUUID(workspaceID) {
		return errNotRestorable
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var graceOver bool
		err := tx.QueryRow(ctx, `
			SELECT w.purge_after <= now() FROM workspaces w
			JOIN workspace_members m ON m.workspace_id = w.id AND m.user_id = $2::uuid AND m.role = 'owner'
			WHERE w.id = $1::uuid AND w.deleted_at IS NOT NULL FOR UPDATE OF w`, workspaceID, user.ID).Scan(&graceOver)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotRestorable // not an owner of it, not deleted, or not there: all the same answer
		}
		if err != nil {
			return err
		}
		if graceOver { // judged by the database clock, the one the purge and the audit trigger use
			return apiError{410, "GRACE_PERIOD_ENDED", "the grace period for this workspace has ended; its data is being removed and it can no longer be restored"}
		}
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET deleted_at = NULL, deleted_by = NULL, purge_after = NULL WHERE id = $1::uuid`, workspaceID); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: workspaceID, ActorID: user.ID, ActorEmail: user.Email,
			Action: "workspace.restored", ResourceType: "workspace", ResourceID: workspaceID, Severity: audit.SeverityCritical,
		}); err != nil {
			return err
		}
		return platformAudit(ctx, tx, "workspace.restored", workspaceID, user.ID, nil)
	})
}

// PendingFor lists the workspaces of which the user is an owner that are scheduled for deletion and can still
// be restored (shown in the console's workspace list).
func (s *Service) PendingFor(ctx context.Context, userID string) ([]PendingDeletion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT w.id::text, w.name, w.deleted_at, w.purge_after
		FROM workspaces w JOIN workspace_members m ON m.workspace_id = w.id AND m.user_id = $1::uuid AND m.role = 'owner'
		WHERE w.deleted_at IS NOT NULL AND w.purge_after > now()
		ORDER BY w.deleted_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (PendingDeletion, error) {
		var p PendingDeletion
		return p, r.Scan(&p.ID, &p.Name, &p.DeletedAt, &p.PurgeAfter)
	})
	if list == nil {
		list = []PendingDeletion{}
	}
	return list, err
}

type pendingView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	DeletedAt  time.Time `json:"deletedAt"`
	PurgeAfter time.Time `json:"purgeAfter"`
}

func pendingOf(p PendingDeletion) pendingView {
	return pendingView{ID: p.ID, Name: p.Name, DeletedAt: p.DeletedAt, PurgeAfter: p.PurgeAfter}
}

// ScheduleDeletion handles POST /workspaces/{wsId}/schedule-deletion (owner, session only).
func (h *Handlers) ScheduleDeletion(w http.ResponseWriter, r *http.Request) {
	access := rbac.WorkspaceAccessFrom(r.Context())
	var req struct {
		ConfirmName string `json:"confirmName"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	p, err := h.svc.ScheduleDeletion(r.Context(), access, req.ConfirmName)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"workspace": pendingOf(p)})
}

// Restore handles POST /workspaces/restore {"workspaceId"} (an owner of a workspace scheduled for deletion).
func (h *Handlers) Restore(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	var req struct {
		WorkspaceID string `json:"workspaceId"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.Restore(r.Context(), user, strings.ToLower(req.WorkspaceID)); err != nil {
		writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"workspaceId": strings.ToLower(req.WorkspaceID)})
}
