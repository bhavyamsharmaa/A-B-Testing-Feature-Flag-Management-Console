package scheduling

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/flags"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/controlplane/webhooks"
	"helios/backend/internal/platform/events"
)

// Config tunes the Runner. The zero value means the defaults.
type Config struct {
	// Grace is how late a change may run. A change overdue by more than this
	// (the worker was down) is marked "missed" instead of being applied at a
	// time nobody expects. Default 15 minutes.
	Grace time.Duration
	// Lease is how long a claim holds a row. A worker that dies mid-run leaves
	// the row "running" until this passes, then another worker takes it over.
	// Default 2 minutes: far longer than one execution takes.
	Lease time.Duration
	// BatchSize is how many rows one claim takes; MaxRows how many one Run
	// handles at most. Defaults 20 and 200.
	BatchSize, MaxRows int
	// MaxAttempts is how many times a row is claimed before it is given up on
	// (retries after a transient error, or takeovers after a crash). Default 5.
	MaxAttempts int
	// Backoff is the wait before the retry that follows attempt n (1-based).
	// Default 30s, 1m, 2m, 4m: together under the default grace, so a retry
	// never lands at a surprising time.
	Backoff func(attempt int) time.Duration
	// WorkerID names this worker in the lease. Default: host, pid and random.
	WorkerID string
}

func (c Config) withDefaults() Config {
	if c.Grace <= 0 {
		c.Grace = 15 * time.Minute
	}
	if c.Lease <= 0 {
		c.Lease = 2 * time.Minute
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 20
	}
	if c.MaxRows <= 0 {
		c.MaxRows = 200
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.Backoff == nil {
		c.Backoff = func(attempt int) time.Duration { return 30 * time.Second << max(attempt-1, 0) }
	}
	if c.WorkerID == "" {
		host, _ := os.Hostname()
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		c.WorkerID = fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(b))
	}
	return c
}

// Stats are the counts of one Run. They are all it ever reports.
type Stats struct {
	Missed    int `json:"missed"`
	Claimed   int `json:"claimed"`
	Reclaimed int `json:"reclaimed"` // claimed from a worker whose lease had expired (included in Claimed)
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Retried   int `json:"retried"`
	Skipped   int `json:"skipped"`   // claimed, then cancelled or taken over before it ran
	Cancelled int `json:"cancelled"` // cancelled because the API token that scheduled them is revoked or expired
	Released  int `json:"released"`  // claimed, then given back unexecuted because the run was asked to stop
}

// Runner executes due scheduled changes. It holds no state between calls and
// keeps no process alive: each Run claims, executes and returns, so it can be
// called from a CLI, a Cloud Scheduler job or an HTTP request, from many
// instances at once.
//
// How rows are claimed. One statement moves due rows from pending to running
// and stamps a lease (owner and expiry), selecting them FOR UPDATE SKIP LOCKED:
// a row another worker is claiming at that instant is skipped, never waited
// for, so two workers cannot claim the same row and neither blocks the other.
// The claim is committed at once, and the change is then executed in a
// separate transaction. The alternative, holding the row lock from claim to
// completion, was rejected: it would pin a connection and a transaction for the
// whole execution and, worse, a crash or error would roll back the claim along
// with the attempt count, so a change that crashes its worker could be retried
// forever. With a lease the attempt is recorded at once, a dead worker's row is
// recognisable by its expired lease, and retries are bounded.
type Runner struct {
	pool  *pgxpool.Pool
	pub   events.Publisher
	clock Clock
	cfg   Config
}

func NewRunner(pool *pgxpool.Pool, pub events.Publisher, clock Clock, cfg Config) *Runner {
	if pub == nil {
		pub = events.NoopPublisher{}
	}
	return &Runner{pool: pool, pub: pub, clock: clock, cfg: cfg.withDefaults()}
}

// Run does one pass: marks what is overdue as missed, then claims and executes
// due changes until none are left or MaxRows have been handled.
func (r *Runner) Run(ctx context.Context) (Stats, error) {
	var st Stats
	if err := r.sweep(ctx, &st); err != nil {
		return st, err
	}
	if err := r.cancelDeadTokenChanges(ctx, &st); err != nil {
		return st, err
	}
	handled := 0
	for handled < r.cfg.MaxRows {
		batch := min(r.cfg.BatchSize, r.cfg.MaxRows-handled)
		claimed, err := r.Claim(ctx, batch)
		if err != nil {
			return st, err
		}
		if len(claimed) == 0 {
			break
		}
		for i, c := range claimed {
			if ctx.Err() != nil {
				// Asked to stop (shutdown, timeout) between claiming and executing: give the rest back at once, so
				// they are not stranded under a lease until it expires.
				r.release(claimed[i:], &st)
				return st, nil
			}
			st.Claimed++
			if c.Reclaimed {
				st.Reclaimed++
			}
			// A change that has begun is finished even if we are asked to stop: its transaction is short and atomic
			// (it commits whole or not at all), and stopping halfway is what would strand the row.
			ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), executeTimeout)
			err := r.execute(ectx, c, &st)
			cancel()
			if err != nil {
				// the row stays running with its lease; another pass takes it over
				log.Printf("scheduler: change %s: %v", c.ID, err)
				r.release(claimed[i:i+1], &st) // give it back now rather than at lease expiry (a no-op if it finished)
			}
		}
		handled += len(claimed)
		if ctx.Err() != nil {
			break
		}
	}
	return st, nil
}

// executeTimeout bounds one change's transaction once it has begun.
const executeTimeout = 30 * time.Second

// release gives claimed rows back: pending again, lease cleared, the attempt that never happened taken off. It only
// touches rows still running under THIS worker's lease, so a row that finished, was cancelled or was taken over after
// its lease expired is left alone. It uses its own short context because it runs when the caller's is already done.
func (r *Runner) release(rows []Claimed, st *Stats) {
	if len(rows) == 0 {
		return
	}
	ids := make([]string, len(rows))
	for i, c := range rows {
		ids[i] = c.ID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tag, err := r.pool.Exec(ctx, `
		UPDATE scheduled_changes
		SET status = 'pending', lease_owner = NULL, lease_expires_at = NULL, attempts = GREATEST(attempts - 1, 0), updated_at = $3
		WHERE id = ANY($1::uuid[]) AND status = 'running' AND lease_owner = $2`, ids, r.cfg.WorkerID, r.clock.now())
	if err != nil {
		log.Printf("scheduler: could not release %d claimed change(s); their leases will expire: %v", len(ids), err)
		return
	}
	st.Released += int(tag.RowsAffected())
}

// Claimed is a row a worker has leased and not yet executed.
type Claimed struct {
	ID, WorkspaceID string
	Reclaimed       bool
}

// Claim takes up to n due rows: pending ones whose time has come, and running
// ones whose lease has expired (their worker died).
func (r *Runner) Claim(ctx context.Context, n int) ([]Claimed, error) {
	now := r.clock.now()
	rows, err := r.pool.Query(ctx, `
		WITH due AS (
			SELECT id, status AS previous FROM scheduled_changes
			WHERE ((status = 'pending' AND due_at <= $1) OR (status = 'running' AND lease_expires_at <= $1))
			  AND NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.id = scheduled_changes.workspace_id AND w.deleted_at IS NOT NULL)
			ORDER BY run_at, id
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE scheduled_changes c
		SET status = 'running', lease_owner = $3, lease_expires_at = $4, attempts = c.attempts + 1, updated_at = $1
		FROM due WHERE c.id = due.id
		RETURNING c.id::text, c.workspace_id::text, due.previous = 'running'`,
		now, n, r.cfg.WorkerID, now.Add(r.cfg.Lease))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Claimed, error) {
		var c Claimed
		return c, row.Scan(&c.ID, &c.WorkspaceID, &c.Reclaimed)
	})
}

// row is a scheduled change loaded for execution.
type row struct {
	id, workspaceID, flagKey, envID, envKey string
	isProd                                  bool
	kind                                    string
	change, baseConfig                      json.RawMessage
	baseVersion                             int64
	runAt                                   time.Time
	status, leaseOwner                      string
	createdBy                               *string
	createdByEmail                          string
	attempts                                int
	tokenID                                 *string
	tokenRevoked                            bool
	tokenExpires                            *time.Time
}

func (rw row) env() rbac.Environment {
	return rbac.Environment{ID: rw.envID, WorkspaceID: rw.workspaceID, Key: rw.envKey, IsProduction: rw.isProd}
}

func (rw row) actor() flags.Actor {
	a := flags.Actor{Email: rw.createdByEmail, Via: "schedule"}
	if rw.createdBy != nil {
		a.ID = *rw.createdBy
	}
	return a
}

const selectForRun = `
	SELECT sc.id::text, sc.workspace_id::text, f.key, e.id::text, e.key, e.is_production, sc.kind, sc.change::text, sc.base_config::text,
	       sc.base_version, sc.run_at, sc.status, COALESCE(sc.lease_owner, ''), sc.created_by::text, sc.created_by_email, sc.attempts,
	       sc.created_by_token_id::text, COALESCE(t.revoked_at IS NOT NULL, false), t.expires_at
	FROM scheduled_changes sc
	LEFT JOIN api_tokens t ON t.id = sc.created_by_token_id AND t.workspace_id = sc.workspace_id
	JOIN workspaces ws ON ws.id = sc.workspace_id AND ws.deleted_at IS NULL
	JOIN flags f ON f.id = sc.flag_id AND f.workspace_id = sc.workspace_id
	JOIN environments e ON e.id = sc.environment_id AND e.workspace_id = sc.workspace_id
	WHERE sc.id = $1::uuid AND sc.workspace_id = $2::uuid
	FOR UPDATE OF sc`

func loadRow(ctx context.Context, tx pgx.Tx, id, workspaceID string) (row, error) {
	var rw row
	var change, base string
	err := tx.QueryRow(ctx, selectForRun, id, workspaceID).Scan(&rw.id, &rw.workspaceID, &rw.flagKey, &rw.envID, &rw.envKey, &rw.isProd,
		&rw.kind, &change, &base, &rw.baseVersion, &rw.runAt, &rw.status, &rw.leaseOwner, &rw.createdBy, &rw.createdByEmail, &rw.attempts, &rw.tokenID, &rw.tokenRevoked, &rw.tokenExpires)
	rw.change, rw.baseConfig = json.RawMessage(change), json.RawMessage(base)
	return rw, err
}

// audit writes the entry for a state change of a scheduled change: the creator
// as the actor, "via schedule", in the same transaction as the change itself.
func (rw row) audit(ctx context.Context, tx pgx.Tx, action, severity string, extra map[string]any) error {
	after := map[string]any{"flagKey": rw.flagKey, "environment": rw.envKey, "kind": rw.kind, "runAt": rw.runAt.UTC().Format(time.RFC3339), "attempts": rw.attempts}
	for k, v := range extra {
		after[k] = v
	}
	a := rw.actor()
	if err := audit.Write(ctx, tx, audit.Entry{
		WorkspaceID: rw.workspaceID, ActorID: a.ID, ActorEmail: a.Email + " via schedule", EnvironmentID: rw.envID,
		Action: action, ResourceType: "scheduled_change", ResourceID: rw.id, Severity: severity, After: after,
	}); err != nil {
		return err
	}
	return rw.emit(ctx, tx, action, extra)
}

// emit queues the webhook event for a finished scheduled change, in the same transaction: executed
// on success, failed for a failure or a miss. Retries and cancellations are not announced. The data
// carries ids and the result code, never the human message (it can name the creator).
func (rw row) emit(ctx context.Context, tx pgx.Tx, action string, extra map[string]any) error {
	typ := ""
	switch action {
	case "scheduled_change.succeeded":
		typ = webhooks.EventScheduledChangeExecuted
	case "scheduled_change.failed", "scheduled_change.missed":
		typ = webhooks.EventScheduledChangeFailed
	default:
		return nil
	}
	actor := webhooks.Actor{Type: "user", Via: "schedule", ScheduledChangeID: rw.id}
	if rw.createdBy != nil {
		actor.ID = *rw.createdBy
	}
	code, _ := extra["resultCode"].(string)
	return webhooks.Emit(ctx, tx, webhooks.Event{
		Type: typ, WorkspaceID: rw.workspaceID, EnvironmentID: rw.envID, EnvironmentKey: rw.envKey, Actor: actor,
		Data: map[string]any{
			"flag":            map[string]string{"key": rw.flagKey},
			"scheduledChange": map[string]any{"id": rw.id, "kind": rw.kind, "runAt": rw.runAt.UTC().Format(time.RFC3339), "attempts": rw.attempts, "resultCode": code},
		},
	}, time.Now())
}

func finish(ctx context.Context, tx pgx.Tx, id string, status, code, message string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE scheduled_changes
		SET status = $2, result_code = $3, result_message = $4, finished_at = $5, updated_at = $5,
		    lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1::uuid`, id, status, code, message, now)
	return err
}

// sweep marks as missed the changes that are overdue by more than the grace
// window and are not running (the worker was down, or a retry would land too
// late), and as failed those a worker kept dying on past the attempt bound.
// Neither is applied: a stale change is never run at a surprising time.
func (r *Runner) sweep(ctx context.Context, st *Stats) error {
	now := r.clock.now()
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, workspace_id::text FROM scheduled_changes
			WHERE ((status = 'pending' AND run_at < $2)
			   OR (status = 'running' AND lease_expires_at <= $1 AND (run_at < $2 OR attempts >= $3)))
			  AND NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.id = scheduled_changes.workspace_id AND w.deleted_at IS NOT NULL)
			ORDER BY run_at, id
			LIMIT 200
			FOR UPDATE SKIP LOCKED`, now, now.Add(-r.cfg.Grace), r.cfg.MaxAttempts)
		if err != nil {
			return err
		}
		type pair struct{ id, ws string }
		pairs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pair, error) {
			var p pair
			return p, row.Scan(&p.id, &p.ws)
		})
		if err != nil {
			return err
		}
		for _, p := range pairs {
			rw, err := loadRow(ctx, tx, p.id, p.ws)
			if err != nil {
				return err
			}
			overdue := now.Sub(rw.runAt) > r.cfg.Grace
			switch {
			case overdue:
				msg := fmt.Sprintf("not run: it was due %s UTC and the scheduler did not get to it within %s", rw.runAt.UTC().Format("2006-01-02 15:04"), r.cfg.Grace)
				if err := finish(ctx, tx, rw.id, StatusMissed, CodeMissed, msg, now); err != nil {
					return err
				}
				if err := rw.audit(ctx, tx, "scheduled_change.missed", audit.SeverityCritical, map[string]any{"resultCode": CodeMissed}); err != nil {
					return err
				}
				st.Missed++
			case rw.status == StatusRunning && rw.attempts >= r.cfg.MaxAttempts:
				if err := finish(ctx, tx, rw.id, StatusFailed, CodeExhausted, "given up: workers kept failing before finishing it", now); err != nil {
					return err
				}
				if err := rw.audit(ctx, tx, "scheduled_change.failed", audit.SeverityCritical, map[string]any{"resultCode": CodeExhausted}); err != nil {
					return err
				}
				st.Failed++
			}
		}
		return nil
	})
}

// outcome of one execution, for the stats and the post-commit event.
type outcome int

const (
	outSkipped outcome = iota
	outSucceeded
	outFailed
	outRetried
	outMissed
	outCancelled
)

// execute applies one claimed change in its own transaction.
//
// The row is re-read FOR UPDATE and must still be "running" under this
// worker's lease: if it was cancelled, or its lease expired and another
// worker took it over, nothing happens. Then, in order: the grace window, the
// creator's authority NOW, and the change itself inside a savepoint, so a
// refusal rolls back only the attempt and is still recorded, in the same
// transaction as the state change.
func (r *Runner) execute(ctx context.Context, c Claimed, st *Stats) error {
	return r.Execute(ctx, c, st)
}

// Execute applies one claimed change (see Claim). Run is Claim followed by
// Execute for each row; they are exported separately so a test can stop
// between the two, which is exactly where a crash or a cancel can land.
func (r *Runner) Execute(ctx context.Context, c Claimed, st *Stats) error {
	var rw row
	var view flags.View
	var out outcome
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		rw, err = loadRow(ctx, tx, c.ID, c.WorkspaceID)
		if errors.Is(err, pgx.ErrNoRows) {
			out = outSkipped // the flag or environment went away, taking the row with it
			return nil
		}
		if err != nil {
			return err
		}
		if rw.status != StatusRunning || rw.leaseOwner != r.cfg.WorkerID {
			out = outSkipped
			return nil
		}
		now := r.clock.now()

		if now.Sub(rw.runAt) > r.cfg.Grace {
			msg := fmt.Sprintf("not run: it was due %s UTC and ran too late (grace %s)", rw.runAt.UTC().Format("2006-01-02 15:04"), r.cfg.Grace)
			out = outMissed
			if err := finish(ctx, tx, rw.id, StatusMissed, CodeMissed, msg, now); err != nil {
				return err
			}
			return rw.audit(ctx, tx, "scheduled_change.missed", audit.SeverityCritical, map[string]any{"resultCode": CodeMissed})
		}

		// A change scheduled through an API token runs only while that token is
		// still valid, even if the cancellation made at revocation was missed.
		if rw.tokenGone(now) {
			out = outCancelled
			if err := finish(ctx, tx, rw.id, StatusCancelled, CodeTokenInvalid, tokenGoneMessage, now); err != nil {
				return err
			}
			return rw.audit(ctx, tx, "scheduled_change.cancel", audit.SeverityCritical, map[string]any{"resultCode": CodeTokenInvalid, "reason": tokenGoneMessage})
		}

		// Authority as of now, from the creator's current membership: someone
		// removed or demoted since scheduling must not act through the schedule.
		if code, msg := r.authority(ctx, tx, rw); code != "" {
			out = outFailed
			if err := finish(ctx, tx, rw.id, StatusFailed, code, msg, now); err != nil {
				return err
			}
			return rw.audit(ctx, tx, "scheduled_change.failed", audit.SeverityCritical, map[string]any{"resultCode": code, "reason": msg})
		}

		sp, err := tx.Begin(ctx) // savepoint
		if err != nil {
			return err
		}
		view, err = r.effect(ctx, sp, rw)
		if err == nil {
			if err := sp.Commit(ctx); err != nil {
				return err
			}
			out = outSucceeded
			if err := finish(ctx, tx, rw.id, StatusSucceeded, CodeApplied, "applied", now); err != nil {
				return err
			}
			return rw.audit(ctx, tx, "scheduled_change.succeeded", audit.SeverityInfo, map[string]any{"resultCode": CodeApplied, "version": view.Config.Version})
		}
		_ = sp.Rollback(ctx)

		code, msg, transient := classify(err)
		if transient && rw.attempts < r.cfg.MaxAttempts {
			next := now.Add(r.cfg.Backoff(rw.attempts))
			out = outRetried
			if _, err := tx.Exec(ctx, `
				UPDATE scheduled_changes
				SET status = 'pending', due_at = $2, result_code = $3, result_message = $4, updated_at = $5,
				    lease_owner = NULL, lease_expires_at = NULL
				WHERE id = $1::uuid`, rw.id, next, CodeRetrying, msg, now); err != nil {
				return err
			}
			return rw.audit(ctx, tx, "scheduled_change.retry", audit.SeverityInfo, map[string]any{"resultCode": CodeRetrying, "nextAttemptAt": next.UTC().Format(time.RFC3339)})
		}
		if transient {
			code, msg = CodeExhausted, "given up after "+fmt.Sprint(rw.attempts)+" attempts: "+msg
		}
		out = outFailed
		if err := finish(ctx, tx, rw.id, StatusFailed, code, msg, now); err != nil {
			return err
		}
		return rw.audit(ctx, tx, "scheduled_change.failed", audit.SeverityCritical, map[string]any{"resultCode": code, "reason": msg})
	})
	if err != nil {
		return err
	}
	switch out {
	case outSkipped:
		st.Skipped++
	case outSucceeded:
		st.Succeeded++
		action := "updated"
		if rw.kind == KindKill {
			action = "killed"
		}
		flags.NewHandlers(r.pool, r.pub).PublishChange(ctx, rw.env(), action, view)
	case outFailed, outMissed:
		if out == outFailed {
			st.Failed++
		} else {
			st.Missed++
		}
	case outRetried:
		st.Retried++
	case outCancelled:
		st.Cancelled++
	}
	return nil
}

const tokenGoneMessage = "cancelled: the API token that scheduled this change was revoked or has expired"

// tokenGone: the change came from an API token that is no longer valid.
func (rw row) tokenGone(now time.Time) bool {
	return rw.tokenID != nil && (rw.tokenRevoked || rw.tokenExpires == nil || !now.Before(*rw.tokenExpires))
}

// cancelDeadTokenChanges cancels pending changes whose API token has expired
// (revocation cancels in its own transaction). Nothing else announces an expiry.
func (r *Runner) cancelDeadTokenChanges(ctx context.Context, st *Stats) error {
	now := r.clock.now()
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT sc.id::text, sc.workspace_id::text FROM scheduled_changes sc
			JOIN api_tokens t ON t.id = sc.created_by_token_id AND t.workspace_id = sc.workspace_id
			WHERE sc.status = 'pending' AND (t.revoked_at IS NOT NULL OR t.expires_at <= $1)
			  AND NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.id = sc.workspace_id AND w.deleted_at IS NOT NULL)
			ORDER BY sc.run_at, sc.id LIMIT 200
			FOR UPDATE OF sc SKIP LOCKED`, now)
		if err != nil {
			return err
		}
		type pair struct{ id, ws string }
		pairs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pair, error) {
			var p pair
			return p, row.Scan(&p.id, &p.ws)
		})
		if err != nil {
			return err
		}
		for _, p := range pairs {
			rw, err := loadRow(ctx, tx, p.id, p.ws)
			if err != nil {
				return err
			}
			if err := finish(ctx, tx, rw.id, StatusCancelled, CodeTokenInvalid, tokenGoneMessage, now); err != nil {
				return err
			}
			if err := rw.audit(ctx, tx, "scheduled_change.cancel", audit.SeverityCritical, map[string]any{"resultCode": CodeTokenInvalid, "reason": tokenGoneMessage}); err != nil {
				return err
			}
			st.Cancelled++
		}
		return nil
	})
}

// authority re-checks the creator's right to make this change, from the
// membership table as it is now. It returns a result code and reason when the
// creator may not.
func (r *Runner) authority(ctx context.Context, tx pgx.Tx, rw row) (code, msg string) {
	if rw.createdBy == nil {
		return CodeCreatorGone, "the person who scheduled this change no longer has an account"
	}
	var role rbac.WorkspaceRole
	err := tx.QueryRow(ctx, `SELECT role::text FROM workspace_members WHERE user_id = $1::uuid AND workspace_id = $2::uuid`, *rw.createdBy, rw.workspaceID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return CodeCreatorGone, rw.createdByEmail + " is no longer a member of this workspace, so the change was not made"
	}
	if err != nil {
		return "", "" // a database error is not a verdict: the attempt below will hit the same error and be retried
	}
	need := RequirementFor(rw.kind)(rw.env())
	if !role.EnvRole().AtLeast(need) {
		return CodeNotAuthorized, fmt.Sprintf("%s is now %s, and this change needs the %s role in %s, so it was not made", rw.createdByEmail, role, need, rw.envKey)
	}
	return "", ""
}

// errConflict: the flag changed since scheduling in the parts a patch touches.
type errConflict struct{ part string }

func (e errConflict) Error() string {
	return "the flag's " + e.part + " changed after this was scheduled, so the patch no longer applies cleanly and was not made"
}

// effect makes the change, through the ordinary edit code.
func (r *Runner) effect(ctx context.Context, tx pgx.Tx, rw row) (flags.View, error) {
	actor := rw.actor()
	switch rw.kind {
	case KindKill:
		return flags.KillFlag(ctx, tx, rw.env(), rw.flagKey, actor, rw.id)
	case KindEnable, KindDisable:
		on := rw.kind == KindEnable
		return flags.ApplyConfigChange(ctx, tx, rw.env(), rw.flagKey, actor, flags.Change{Enabled: &on}, flags.ApplyOptions{ScheduledChangeID: rw.id})
	}
	var p patchBody
	if err := json.Unmarshal(rw.change, &p); err != nil {
		return flags.View{}, flags.Invalid("the stored change cannot be read: " + err.Error())
	}
	ch := p.change()
	in, err := flags.Inspect(ctx, tx, rw.env(), rw.flagKey, ch, false)
	if err != nil {
		return flags.View{}, err
	}
	if in.Version != rw.baseVersion {
		// The configuration moved. A patch still applies if the parts it touches
		// are exactly as they were when it was scheduled; otherwise guessing
		// would overwrite someone's edit.
		var base map[string]json.RawMessage
		_ = json.Unmarshal(rw.baseConfig, &base)
		for part, then := range base {
			if string(in.Touched[part]) != string(then) {
				return flags.View{}, errConflict{part: partName(part)}
			}
		}
	}
	return flags.ApplyConfigChange(ctx, tx, rw.env(), rw.flagKey, actor, ch, flags.ApplyOptions{ScheduledChangeID: rw.id})
}

func partName(p string) string {
	switch p {
	case "targetingRules":
		return "targeting rules"
	case "fallthroughVariationId":
		return "fallthrough variation"
	}
	return p
}

// classify turns an error into a result code and message, and says whether it
// is worth retrying. A refusal of the change itself (invalid, conflicting, the
// flag gone) never gets better; anything else (a database error, a timeout, a
// deadlock) is treated as transient and retried within the attempt bound.
func classify(err error) (code, message string, transient bool) {
	var conflict errConflict
	switch {
	case errors.As(err, &conflict):
		return CodeConflict, conflict.Error(), false
	case flags.IsValidation(err):
		return CodeInvalid, err.Error(), false
	case errors.Is(err, pgx.ErrNoRows):
		return CodeFlagGone, "the flag no longer exists", false
	}
	log.Printf("scheduler: transient error: %v", err)
	return CodeRetrying, "a temporary error; it will be retried", true
}
