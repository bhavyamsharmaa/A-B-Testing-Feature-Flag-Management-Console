// Package wspurge removes the data of workspaces whose deletion grace period has ended. It runs as
// cmd/purgeworkspaces or behind POST /internal/workspaces/purge, never inside the API's own request path.
//
// SAFETY, in order of what could go wrong:
//
//   - Only a workspace that is scheduled for deletion AND past purge_after is touched; every step re-reads
//     that from the database, so a restore (possible only before purge_after) can never race a purge.
//   - Nothing is one giant transaction or a long table lock. The big tables (events, the outbox, segment
//     members, the audit log) are deleted in batches of BatchSize rows, each its own short transaction that
//     locks only the rows it deletes (FOR UPDATE SKIP LOCKED). The few small tables go with the workspace row,
//     whose cascade then has almost nothing left to remove.
//   - Order is dependency order: children before parents. events (which point at environments and
//     experiments), then the outbox (endpoints, invites), then segment members (segments), then the audit
//     log (which has no foreign key; its delete trigger permits only this transaction, see AuditGateSQL), and last the
//     workspace row, which cascades the rest and is replaced by a tombstone in the same transaction.
//   - RESUMABLE AND IDEMPOTENT. Progress is the data itself: a crash between batches leaves the workspace
//     scheduled with fewer rows, and the next run carries on; a finished workspace is simply not found.
//   - The audit log stays append-only for everyone else (migration 0014): a row can be deleted only when
//     its own workspace is past its purge date.
//
// WHAT REMAINS: a tombstone (workspace id, who asked, when, row counts: no customer data) and a platform
// audit entry under the nil workspace id (see workspaces.PlatformWorkspaceID).
package wspurge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/workspaces"
	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/events"
)

// Batched are deleted in batches, in this order (children first).
var Batched = []string{"events", "outbox", "segment_members", "audit_logs"}

// Cascaded are removed by deleting the workspace row (foreign keys with ON DELETE CASCADE, directly or through
// a parent), after the batches have emptied the big tables.
var Cascaded = []string{
	"api_keys", "api_token_environments", "api_tokens", "environments", "experiment_variations", "experiments",
	"flag_configs", "flags", "ownership_offers", "scheduled_changes", "segments", "user_environment_roles",
	"webhook_endpoints", "workspace_invites", "workspace_members",
}

// Exempt tables have a workspace_id column and keep rows for a purged workspace, on purpose.
var Exempt = map[string]string{
	"workspace_tombstones": "the permanent record that a workspace was purged (id, who asked, when, counts): no customer data",
}

// Covered is every table the purge accounts for. The completeness test compares it with the catalog, so a
// table added later with a workspace_id column fails the build until it is listed in one of the three groups.
func Covered() map[string]string {
	m := map[string]string{}
	for _, t := range Batched {
		m[t] = "batched"
	}
	for _, t := range Cascaded {
		m[t] = "cascade"
	}
	for t, why := range Exempt {
		m[t] = "exempt: " + why
	}
	return m
}

// Config tunes a Purger. The zero value means the defaults.
type Config struct {
	BatchSize     int // rows per batch; default 2000
	MaxWorkspaces int // workspaces per run; default 20
	Pause         time.Duration
	Pub           events.Publisher
	// AfterBatch, if set, is called after every committed batch; an error stops the run there (tests use it to
	// simulate a crash between batches).
	AfterBatch func(table string, deleted int) error
}

func (c Config) withDefaults() Config {
	if c.BatchSize <= 0 {
		c.BatchSize = 2000
	}
	if c.MaxWorkspaces <= 0 {
		c.MaxWorkspaces = 20
	}
	return c
}

type Purger struct {
	pool *pgxpool.Pool
	cfg  Config
}

func New(pool *pgxpool.Pool, cfg Config) *Purger { return &Purger{pool: pool, cfg: cfg.withDefaults()} }

// Stats are counts only.
type Stats struct {
	OffersExpired int `json:"offersExpired"`
	Purged        int `json:"purged"`
	Batches       int `json:"batches"`
	Rows          int `json:"rows"`
}

// Run expires stale ownership offers and purges the workspaces that are due.
func (p *Purger) Run(ctx context.Context) (Stats, error) {
	var st Stats
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		n, err := workspaces.ExpireOffers(ctx, tx, time.Now())
		st.OffersExpired = n
		return err
	})
	if err != nil {
		return st, err
	}
	rows, err := p.pool.Query(ctx, `
		SELECT id::text FROM workspaces WHERE deleted_at IS NOT NULL AND purge_after <= now() ORDER BY purge_after, id LIMIT $1`, p.cfg.MaxWorkspaces)
	if err != nil {
		return st, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return st, err
	}
	for _, id := range ids {
		done, err := p.purgeOne(ctx, id, &st)
		if err != nil {
			return st, fmt.Errorf("workspace %s: %w", id, err) // an id and an error: never a name or a row
		}
		if done {
			st.Purged++
		}
	}
	return st, nil
}

// due re-reads the only fact that permits a purge.
func (p *Purger) due(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := p.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workspaces WHERE id = $1::uuid AND deleted_at IS NOT NULL AND purge_after <= now())`, id).Scan(&ok)
	return ok, err
}

func (p *Purger) purgeOne(ctx context.Context, id string, st *Stats) (bool, error) {
	counts := map[string]int{}
	// Tell every instance again that this workspace is gone: a stream or a cached key must not outlive its data.
	if p.cfg.Pub != nil {
		_ = p.cfg.Pub.Publish(ctx, events.RevocationChannel, []byte(apikey.WorkspaceMessage(id)))
	}
	for _, table := range Batched {
		for {
			if ok, err := p.due(ctx, id); err != nil || !ok {
				return false, err // restored (or already gone): stop touching it
			}
			n, err := p.deleteBatch(ctx, table, id)
			if err != nil {
				return false, err
			}
			if n > 0 {
				st.Batches++
				st.Rows += n
				counts[table] += n
				if p.cfg.AfterBatch != nil {
					if err := p.cfg.AfterBatch(table, n); err != nil {
						return false, err
					}
				}
				if p.cfg.Pause > 0 {
					select {
					case <-ctx.Done():
						return false, ctx.Err()
					case <-time.After(p.cfg.Pause):
					}
				}
			}
			if n < p.cfg.BatchSize {
				break
			}
		}
	}
	return p.finish(ctx, id, counts)
}

// deleteBatch deletes up to BatchSize of one table's rows for the workspace, in its own short transaction, locking
// only those rows and skipping any that another transaction holds.
func (p *Purger) deleteBatch(ctx context.Context, table, id string) (int, error) {
	for _, t := range Batched {
		if t == table {
			goto known
		}
	}
	return 0, errors.New("not a batched table")
known:
	var n int
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if table == "audit_logs" {
			// The audit log's delete trigger (migration 0015) lets a row go only inside a transaction that has
			// declared, with this very statement, the one workspace it is purging and its own id. Nothing else
			// in the code base runs it (TestOnlyThePurgeOpensTheAuditGate).
			if _, err := tx.Exec(ctx, AuditGateSQL, id); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, fmt.Sprintf(`
			DELETE FROM %[1]s WHERE ctid IN (
				SELECT ctid FROM %[1]s WHERE workspace_id = $1::uuid LIMIT $2 FOR UPDATE SKIP LOCKED)`, table), id, p.cfg.BatchSize)
		n = int(tag.RowsAffected())
		return err
	})
	return n, err
}

// AuditGateSQL declares, for the current transaction only (is_local), the one workspace whose audit rows may be
// deleted. The value is bound to the transaction id, so it cannot be carried into another transaction.
const AuditGateSQL = `SELECT set_config('helios.audit_purge', $1::text || ':' || txid_current()::text, true)`

// finish removes the workspace row (cascading the small tables), and writes the tombstone and the platform audit
// entry in the same transaction.
func (p *Purger) finish(ctx context.Context, id string, counts map[string]int) (bool, error) {
	done := false
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		var requestedBy *string
		var requestedAt time.Time
		err := tx.QueryRow(ctx, `
			SELECT deleted_by::text, deleted_at FROM workspaces
			WHERE id = $1::uuid AND deleted_at IS NOT NULL AND purge_after <= now() FOR UPDATE`, id).Scan(&requestedBy, &requestedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // restored, or purged by another run
		}
		if err != nil {
			return err
		}
		// The small tables, counted for the tombstone before they go.
		for _, t := range []string{"flags", "experiments", "segments", "environments", "workspace_members"} {
			var n int
			if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE workspace_id = $1::uuid`, t), id).Scan(&n); err != nil {
				return err
			}
			counts[t] = n
		}
		summary, _ := json.Marshal(counts)
		if _, err := tx.Exec(ctx, `DELETE FROM workspaces WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO workspace_tombstones (workspace_id, requested_by, requested_at, summary)
			VALUES ($1::uuid, $2::uuid, $3, $4::jsonb) ON CONFLICT (workspace_id) DO NOTHING`, id, requestedBy, requestedAt, string(summary)); err != nil {
			return err
		}
		actor := ""
		if requestedBy != nil {
			actor = *requestedBy
		}
		done = true
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: workspaces.PlatformWorkspaceID, ActorID: actor, ActorEmail: "(workspace lifecycle)",
			Action: "workspace.purged", ResourceType: "workspace", ResourceID: id, Severity: audit.SeverityCritical, After: counts,
		})
	})
	if err != nil {
		return false, err
	}
	if done {
		log.Printf("wspurge: purged workspace %s", id)
	}
	return done, nil
}
