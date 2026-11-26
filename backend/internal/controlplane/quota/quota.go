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
