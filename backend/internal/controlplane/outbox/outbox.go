// Package outbox is the shared queue for everything the system sends to the
// outside world: webhook deliveries and invite emails. See docs/OUTBOX.md.
//
// WRITING. Enqueue is called with the caller's TRANSACTION, in the same
// transaction as the change that caused the delivery. A change that rolls back
// leaves no row (nothing is sent about something that did not happen); a change
// that commits has its row (nothing is lost if the process dies right after).
//
// CLAIMING. The worker's claim is the scheduler's pattern: one statement moves
// due rows to 'delivering' and stamps a lease (owner and expiry), selecting them
// FOR UPDATE SKIP LOCKED, so two workers never claim the same row and neither
// waits for the other. It commits at once. The network call is then made with NO
// transaction open (a slow customer must not pin a connection and a lock), and
// the result is recorded in a short second transaction guarded by the lease. A
// delivery whose lease expired (the worker died, or was too slow) is claimable
// again.
//
// GUARANTEE: AT LEAST ONCE. If a worker dies after the receiver processed a
// request and before the result was recorded, the row is delivered again. Every
// attempt of one delivery carries the same delivery_id (an HTTP header for
// webhooks), so receivers can drop duplicates by it. Exactly-once is not
// attempted: it is not achievable across a network call.
//
// FAIRNESS. At most PerTarget rows of one target (one endpoint, one recipient)
// are in flight at once, including across workers, so one slow customer cannot
// take every worker.
//
// RETRIES. Exponential backoff with jitter: after attempt n fails the next one is
// due after 1m, 5m, 15m, 1h, 3h, 6h, 12h (each +-20%); after the 8th failure the
// row is 'dead' (about 22 hours in all). Dead rows are kept for inspection and
// purged after a retention period.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/audit"
)

type Kind string

const (
	KindWebhook Kind = "webhook"
	KindEmail   Kind = "email"
)

const (
	StatusPending    = "pending"
	StatusDelivering = "delivering"
	StatusDelivered  = "delivered"
	StatusFailed     = "failed"
	StatusDead       = "dead"
)

// Limits.
const (
	// MaxQueuedWebhooksPerWorkspace caps webhook rows waiting to be delivered (pending, failed or
	// delivering) per workspace. Over it, the OLDEST are dropped (marked dead) and audited: a user's
	// change is never blocked or failed by a full queue.
	MaxQueuedWebhooksPerWorkspace = 1000
	// MaxPayloadBytes bounds one payload (the database CHECK is the backstop).
	MaxPayloadBytes = 32 << 10
	// MaxAttempts is the number of tries before a row is dead.
	MaxAttempts = 8
	// RetentionDelivered and RetentionDead: how long finished rows are kept (the delivery log).
	RetentionDelivered = 14 * 24 * time.Hour
	RetentionDead      = 30 * 24 * time.Hour
	// OverflowAuditEvery: the overflow audit entry is written at most this often per workspace.
	OverflowAuditEvery = 10 * time.Minute
)

// Item is a delivery to queue.
type Item struct {
	WorkspaceID string
	Kind        Kind
	EventType   string
	EventID     string // shared by the deliveries of one event; generated if empty
	EndpointID  string // webhook
	InviteID    string // email
	// TargetKey: the endpoint id (webhook) or the lower-case recipient (email).
	TargetKey string
	Payload   any
	// DeliveryID may be set by the caller; otherwise the database generates it.
	NextAttemptAt time.Time
}

// ErrPayloadTooLarge: the payload is over MaxPayloadBytes.
var ErrPayloadTooLarge = errors.New("outbox: payload too large")

// Enqueue writes a row in the caller's transaction and returns its delivery id.
// For webhooks it first enforces the per-workspace queue cap.
func Enqueue(ctx context.Context, tx pgx.Tx, it Item, now time.Time) (string, error) {
	body, err := json.Marshal(it.Payload)
	if err != nil {
		return "", err
	}
	if len(body) > MaxPayloadBytes {
		return "", ErrPayloadTooLarge
	}
	if it.NextAttemptAt.IsZero() {
		it.NextAttemptAt = now
	}
	if it.Kind == KindWebhook {
		if err := enforceCap(ctx, tx, it.WorkspaceID, now); err != nil {
			return "", err
		}
	}
	var delivery string
	err = tx.QueryRow(ctx, `
		INSERT INTO outbox (workspace_id, kind, event_type, event_id, endpoint_id, invite_id, target_key, payload, next_attempt_at, created_at, updated_at)
		VALUES ($1::uuid, $2, $3, COALESCE(NULLIF($4, '')::uuid, gen_random_uuid()), NULLIF($5, '')::uuid, NULLIF($6, '')::uuid, $7, $8::jsonb, $9, $10, $10)
		RETURNING delivery_id::text`,
		it.WorkspaceID, string(it.Kind), it.EventType, it.EventID, it.EndpointID, it.InviteID, it.TargetKey, string(body), it.NextAttemptAt, now).Scan(&delivery)
	return delivery, err
}

// enforceCap makes room for one more queued webhook: if the workspace already has
// MaxQueuedWebhooksPerWorkspace waiting, the oldest waiting rows are marked dead
// ("dropped"), and the drop is audited (at most once per OverflowAuditEvery).
// The count is not serialised across concurrent writers: the cap is soft by a
// handful of rows, which is the price of never making a user's change wait.
func enforceCap(ctx context.Context, tx pgx.Tx, workspaceID string, now time.Time) error {
	var queued int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM outbox WHERE workspace_id = $1::uuid AND kind = 'webhook' AND status IN ('pending', 'failed', 'delivering')`,
		workspaceID).Scan(&queued); err != nil {
		return err
	}
	if queued < MaxQueuedWebhooksPerWorkspace {
		return nil
	}
	over := queued - MaxQueuedWebhooksPerWorkspace + 1
	tag, err := tx.Exec(ctx, `
		UPDATE outbox SET status = 'dead', last_error = 'dropped: the workspace''s webhook queue was full', updated_at = $3,
		       lease_owner = NULL, lease_expires_at = NULL
		WHERE id IN (
			SELECT id FROM outbox
			WHERE workspace_id = $1::uuid AND kind = 'webhook' AND status IN ('pending', 'failed')
			ORDER BY created_at, id LIMIT $2)`, workspaceID, over, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	var recent bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'outbox.overflow' AND created_at > $2)`,
		workspaceID, now.Add(-OverflowAuditEvery)).Scan(&recent); err != nil {
		return err
	}
	if recent {
		return nil
	}
	return audit.Write(ctx, tx, audit.Entry{
		WorkspaceID: workspaceID, ActorEmail: "system", Action: "outbox.overflow", ResourceType: "outbox", Severity: audit.SeverityCritical,
		After: map[string]any{"dropped": tag.RowsAffected(), "limit": MaxQueuedWebhooksPerWorkspace, "reason": "webhook queue full: the oldest queued deliveries were dropped"},
	})
}
