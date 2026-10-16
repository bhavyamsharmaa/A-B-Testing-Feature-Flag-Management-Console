// Package quota enforces the per-workspace limits: environments, flags and
// active SDK keys. A breach is reported as an Exceeded error, which the API
// turns into 409 QUOTA_EXCEEDED.
//
// Checks that count rows first lock the workspace row (SELECT ... FOR UPDATE)
// in the caller's transaction, so two concurrent creates can't both read
// "49 flags" and both insert. Lock order: the workspace row is always the
// FIRST lock a transaction takes (flag create, mkkey), so these callers can't
// deadlock with each other or with delete, start or stop, none of which touch
// the workspace row.
package quota

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/platform/httpx"
)

const (
	MaxEnvironments  = 3
	MaxFlags         = 50
	MaxActiveSDKKeys = 10
	// MaxMembers counts members plus open (unexpired, unrevoked) invites.
	MaxMembers = 20
	// MaxOwnedWorkspaces is per user, not per workspace.
	MaxOwnedWorkspaces = 5
	// MaxSegments is per workspace.
	MaxSegments = 20
	// MaxSegmentMembers is the number of explicit member subject keys one
	// segment may hold. Saving a segment is O(members); evaluating one is not
	// (an indexed lookup of the one subject), so this limit is about save time
	// and request size (10,000 keys of up to 256 bytes fit well under the
	// 4 MiB segment request cap), not about /evaluate latency.
	MaxSegmentMembers = 10000
	// MaxSegmentRuleBytes is the largest JSON encoding of a segment's attribute
	// rules, and MaxSegmentRuleClauses the most clauses in them. The database
	// carries the same bounds as a CHECK (16384 bytes, 20 clauses) as a backstop.
	MaxSegmentRuleBytes   = 8192
	MaxSegmentRuleClauses = 20
	// MaxPendingChangesPerFlag and MaxPendingChangesPerWorkspace cap the scheduled
	// changes waiting to run (pending or running): per flag (all environments) and per workspace.
	MaxPendingChangesPerFlag      = 10
	MaxPendingChangesPerWorkspace = 100
	// MaxActiveAPITokensPerWorkspace and MaxActiveAPITokensPerUser cap the API tokens that are neither
	// revoked nor expired: per workspace, and per person who created them (in that workspace).
	MaxActiveAPITokensPerWorkspace = 25
	MaxActiveAPITokensPerUser      = 10
	// MaxWebhookEndpoints is per workspace (enabled or not).
	MaxWebhookEndpoints = 10
	// MaxInviteEmailsPerWorkspacePerDay counts invite emails queued in any 24 hours, resends included.
	MaxInviteEmailsPerWorkspacePerDay = 50
	// MaxInviteEmailsPerRecipientPerDay is per workspace and recipient;
	// MaxInviteEmailsPerRecipientAnywherePerDay is per recipient across every workspace.
	MaxInviteEmailsPerRecipientPerDay         = 3
	MaxInviteEmailsPerRecipientAnywherePerDay = 6
)

// Exceeded says which limit a request would have broken. Per is what the
// limit is counted against ("workspace" unless stated).
type Exceeded struct {
	Resource string
	Limit    int
	Per      string
}

func (e Exceeded) Error() string {
	per := e.Per
	if per == "" {
		per = "workspace"
	}
	return fmt.Sprintf("limit reached: at most %d %s per %s", e.Limit, e.Resource, per)
}

// check reports whether one more of something fits: have is the current count.
func check(resource string, have, limit int) error {
	if have >= limit {
		return Exceeded{Resource: resource, Limit: limit}
	}
	return nil
}

// CheckEnvironments is for provisioning: requested environments in total.
func CheckEnvironments(requested int) error {
	if requested > MaxEnvironments {
		return Exceeded{Resource: "environments", Limit: MaxEnvironments}
	}
	return nil
}

func lockWorkspace(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	var one int
	return tx.QueryRow(ctx, `SELECT 1 FROM workspaces WHERE id = $1::uuid FOR UPDATE`, workspaceID).Scan(&one)
}

// LockWorkspace takes the workspace row lock that counted limits serialise on.
func LockWorkspace(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	return lockWorkspace(ctx, tx, workspaceID)
}

// CheckFlag returns Exceeded when the workspace already has MaxFlags flags.
// Call it first in the transaction that inserts the flag.
func CheckFlag(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	var have int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM flags WHERE workspace_id = $1::uuid`, workspaceID).Scan(&have); err != nil {
		return err
	}
	return check("flags", have, MaxFlags)
}

// CheckSDKKey returns Exceeded when the workspace already has
// MaxActiveSDKKeys unrevoked SDK keys across its environments.
func CheckSDKKey(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	var have int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM api_keys k
		JOIN environments e ON e.id = k.environment_id
		WHERE e.workspace_id = $1::uuid AND k.kind = 'sdk' AND k.revoked_at IS NULL`, workspaceID).Scan(&have); err != nil {
		return err
	}
	return check("active SDK keys", have, MaxActiveSDKKeys)
}

// CheckSegment returns Exceeded when the workspace already has MaxSegments
// segments. Call it first in the transaction that inserts the segment.
func CheckSegment(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	var have int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM segments WHERE workspace_id = $1::uuid`, workspaceID).Scan(&have); err != nil {
		return err
	}
	return check("segments", have, MaxSegments)
}

// CheckSegmentMembers returns Exceeded when a segment would hold more than
// MaxSegmentMembers explicit members. It is pure: the caller has already
// worked out the number the segment would end up with (and holds the segment's
// row lock, so that number cannot change underneath it).
func CheckSegmentMembers(total int) error {
	if total > MaxSegmentMembers {
		return Exceeded{Resource: "explicit members", Limit: MaxSegmentMembers, Per: "segment"}
	}
	return nil
}

// CheckSegmentRules returns Exceeded when a segment's rule definition is too
// large: encodedBytes of JSON, or clauses of them.
func CheckSegmentRules(encodedBytes, clauses int) error {
	if clauses > MaxSegmentRuleClauses {
		return Exceeded{Resource: "rule clauses", Limit: MaxSegmentRuleClauses, Per: "segment"}
	}
	if encodedBytes > MaxSegmentRuleBytes {
		return Exceeded{Resource: "bytes of rule definition", Limit: MaxSegmentRuleBytes, Per: "segment"}
	}
	return nil
}

// CheckScheduledChange returns Exceeded when scheduling one more change for the
// flag would pass a cap on pending ones. Call it first in the transaction that
// inserts the change: it takes the workspace row lock, so two concurrent
// schedules cannot both pass.
func CheckScheduledChange(ctx context.Context, tx pgx.Tx, workspaceID, flagID string) error {
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	var perWorkspace, perFlag int
	if err := tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE flag_id = $2::uuid)
		FROM scheduled_changes WHERE workspace_id = $1::uuid AND status IN ('pending', 'running')`,
		workspaceID, flagID).Scan(&perWorkspace, &perFlag); err != nil {
		return err
	}
	if perFlag >= MaxPendingChangesPerFlag {
		return Exceeded{Resource: "pending scheduled changes", Limit: MaxPendingChangesPerFlag, Per: "flag"}
	}
	return check("pending scheduled changes", perWorkspace, MaxPendingChangesPerWorkspace)
}

// CheckAPIToken returns Exceeded when creating one more API token would pass a cap
// on active ones (not revoked, not expired). Call it first in the transaction that
// inserts the token: it takes the workspace row lock, so two concurrent creates
// cannot both pass.
func CheckAPIToken(ctx context.Context, tx pgx.Tx, workspaceID, userID string) error {
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	var perWorkspace, perUser int
	if err := tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE created_by = $2::uuid)
		FROM api_tokens WHERE workspace_id = $1::uuid AND revoked_at IS NULL AND expires_at > now()`,
		workspaceID, userID).Scan(&perWorkspace, &perUser); err != nil {
		return err
	}
	if perUser >= MaxActiveAPITokensPerUser {
		return Exceeded{Resource: "active API tokens", Limit: MaxActiveAPITokensPerUser, Per: "person"}
	}
	return check("active API tokens", perWorkspace, MaxActiveAPITokensPerWorkspace)
}

// CheckWebhookEndpoint returns Exceeded when the workspace already has MaxWebhookEndpoints
// endpoints. It takes the workspace row lock, like the other counted limits.
func CheckWebhookEndpoint(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	var have int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM webhook_endpoints WHERE workspace_id = $1::uuid`, workspaceID).Scan(&have); err != nil {
		return err
	}
	return check("webhook endpoints", have, MaxWebhookEndpoints)
}

// CheckInviteEmail applies the daily limits on invite emails (resends included), counted
// over the last 24 hours from the outbox. Call it in the transaction that queues the email,
// after the workspace row is locked (CheckMember does that), so concurrent invites cannot
// both pass.
//
// The workspace-wide limit and the per-recipient limit inside the workspace return Exceeded:
// they count only this workspace's own activity, so refusing says nothing about anyone else.
// The per-recipient limit ACROSS workspaces counts other workspaces' activity, so it is not
// reported as an error (that would let one workspace probe for another's invites): it
// returns suppress=true and the caller records the email as not sent.
func CheckInviteEmail(ctx context.Context, tx pgx.Tx, workspaceID, recipient string, now time.Time) (suppress bool, err error) {
	var perWorkspace, perRecipient, anywhere int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE workspace_id = $1::uuid),
		       count(*) FILTER (WHERE workspace_id = $1::uuid AND target_key = $2),
		       count(*) FILTER (WHERE target_key = $2)
		FROM outbox
		WHERE kind = 'email' AND created_at > $3
		  AND (workspace_id = $1::uuid OR target_key = $2)`, workspaceID, recipient, now.Add(-24*time.Hour)).Scan(&perWorkspace, &perRecipient, &anywhere); err != nil {
		return false, err
	}
	if perWorkspace >= MaxInviteEmailsPerWorkspacePerDay {
		return false, Exceeded{Resource: "invite emails", Limit: MaxInviteEmailsPerWorkspacePerDay, Per: "workspace per day"}
	}
	if perRecipient >= MaxInviteEmailsPerRecipientPerDay {
		return false, Exceeded{Resource: "invite emails to one address", Limit: MaxInviteEmailsPerRecipientPerDay, Per: "workspace per day"}
	}
	return anywhere >= MaxInviteEmailsPerRecipientAnywherePerDay, nil
}

// CheckMember returns Exceeded when members plus open invites already reach
// MaxMembers. Call it first in the transaction that adds an invite.
func CheckMember(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	if err := lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	var have int
	if err := tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM workspace_members WHERE workspace_id = $1::uuid)
		     + (SELECT count(*) FROM workspace_invites
		        WHERE workspace_id = $1::uuid AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now())`,
		workspaceID).Scan(&have); err != nil {
		return err
	}
	return check("members and open invites", have, MaxMembers)
}

// CheckOwnedWorkspaces returns Exceeded when the user already owns
// MaxOwnedWorkspaces workspaces. The caller must serialise concurrent
// creates for the same user (the workspaces package takes a per-user
// advisory lock).
func CheckOwnedWorkspaces(ctx context.Context, tx pgx.Tx, userID string) error {
	var have int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM workspaces WHERE owner_id = $1::uuid`, userID).Scan(&have); err != nil {
		return err
	}
	if have >= MaxOwnedWorkspaces {
		return Exceeded{Resource: "workspaces", Limit: MaxOwnedWorkspaces, Per: "user"}
	}
	return nil
}

// WriteError answers 409 QUOTA_EXCEEDED when err is an Exceeded, and reports
// whether it did.
func WriteError(w http.ResponseWriter, err error) bool {
	var exceeded Exceeded
	if !errors.As(err, &exceeded) {
		return false
	}
	httpx.WriteError(w, http.StatusConflict, "QUOTA_EXCEEDED", exceeded.Error())
	return true
}
