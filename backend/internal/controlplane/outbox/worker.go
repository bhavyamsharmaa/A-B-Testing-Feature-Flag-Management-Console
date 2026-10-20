package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Delivery is a claimed row, handed to a Sender.
type Delivery struct {
	ID, WorkspaceID, EventType, EventID, DeliveryID string
	Kind                                            Kind
	EndpointID, InviteID, TargetKey                 string
	Payload                                         json.RawMessage
	// Attempt is this attempt's number, from 1.
	Attempt int
	// Reclaimed: taken over from a worker whose lease expired.
	Reclaimed bool
}

// Result is what a Sender reports about one attempt. A Sender never panics the
// worker with an error value: everything is a Result.
type Result struct {
	Delivered  bool
	HTTPStatus int
	Duration   time.Duration
	// Err is a SHORT description that must never contain a secret, token or
	// response body (the worker truncates and strips it anyway).
	Err string
	// Hint is a truncated, printable piece of the receiver's response.
	Hint string
	// Permanent: do not retry (the target is gone, disabled or no longer relevant).
	Permanent bool
	// Internal: the failure is ours (no signing key, say), not the receiver's, so it
	// does not count against the target's health.
	Internal bool
}

// Sender performs the network call for one kind. It is called with NO
// transaction open and must not hold one.
type Sender interface {
	Send(ctx context.Context, d Delivery) Result
}

// Finisher is an optional Sender hook run in the SAME transaction that records
// the result: webhooks update the endpoint's health, email scrubs the invite token.
type Finisher interface {
	Finish(ctx context.Context, tx pgx.Tx, d Delivery, o Outcome) error
}

// Outcome is the recorded result of an attempt.
type Outcome struct {
	Result
	Status    string // delivered | failed | dead
	NextAt    time.Time
	Now       time.Time
	Exhausted bool
}

// Config tunes a Worker. The zero value means the defaults.
type Config struct {
	Lease       time.Duration // default 2 minutes: well over the 10 s total timeout
	BatchSize   int           // rows per claim; default 20
	MaxRows     int           // rows per Run; default 500
	Concurrency int           // simultaneous deliveries in one Run; default 8
	PerTarget   int           // in flight per target (endpoint, recipient); default 2
	MaxAttempts int           // default MaxAttempts (8)
	// Backoff is the wait after attempt n (1-based) failed, before jitter.
	Backoff func(n int) time.Duration
	// Jitter returns a factor in [0.8, 1.2); tests inject 1.
	Jitter   func() float64
	WorkerID string
	Now      func() time.Time
	// Kinds restricts the claim (default: every kind that has a Sender).
}

var defaultBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour}

// DefaultBackoff is the wait after attempt n failed.
func DefaultBackoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > len(defaultBackoff) {
		n = len(defaultBackoff)
	}
	return defaultBackoff[n-1]
}

func defaultJitter() float64 {
	v, err := rand.Int(rand.Reader, big.NewInt(400))
	if err != nil {
		return 1
	}
	return 0.8 + float64(v.Int64())/1000
}

func (c Config) withDefaults() Config {
	if c.Lease <= 0 {
		c.Lease = 2 * time.Minute
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 20
	}
	if c.MaxRows <= 0 {
		c.MaxRows = 500
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 8
	}
	if c.PerTarget <= 0 {
		c.PerTarget = 2
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = MaxAttempts
	}
	if c.Backoff == nil {
		c.Backoff = DefaultBackoff
	}
	if c.Jitter == nil {
		c.Jitter = defaultJitter
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.WorkerID == "" {
		host, _ := os.Hostname()
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		c.WorkerID = fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(b))
	}
	return c
}

// Stats are the counts of one Run: all it ever reports.
type Stats struct {
	Claimed   int `json:"claimed"`
	Reclaimed int `json:"reclaimed"`
	Delivered int `json:"delivered"`
	Retrying  int `json:"retrying"`
	Dead      int `json:"dead"`
	LeaseLost int `json:"leaseLost"` // delivered or failed, but another worker had taken the row over
	Purged    int `json:"purged"`
	Released  int `json:"released"` // claimed, then given back unsent because the run was asked to stop
}

// Worker claims and delivers. It holds no state between calls.
type Worker struct {
	pool    *pgxpool.Pool
	senders map[Kind]Sender
	cfg     Config
}

func NewWorker(pool *pgxpool.Pool, senders map[Kind]Sender, cfg Config) *Worker {
	return &Worker{pool: pool, senders: senders, cfg: cfg.withDefaults()}
}

// Run does one pass: gives up on rows whose workers kept dying, then claims and
// delivers until nothing is due or MaxRows were handled, then purges old rows.
func (w *Worker) Run(ctx context.Context) (Stats, error) {
	var st Stats
	if err := w.giveUp(ctx, &st); err != nil {
		return st, err
	}
	handled := 0
	for handled < w.cfg.MaxRows && ctx.Err() == nil {
		claimed, err := w.Claim(ctx, min(w.cfg.BatchSize, w.cfg.MaxRows-handled))
		if err != nil {
			return st, err
		}
		if len(claimed) == 0 {
			break
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, w.cfg.Concurrency)
		var unstarted []Delivery // claimed, but we were asked to stop before sending them
		for _, d := range claimed {
			// Wait for a free sender, but not past a request to stop.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				unstarted = append(unstarted, d)
				continue
			}
			if ctx.Err() != nil {
				<-sem
				unstarted = append(unstarted, d)
				continue
			}
			wg.Add(1)
			go func(d Delivery) {
				defer wg.Done()
				defer func() { <-sem }()
				var one Stats
				// A delivery that has begun is finished and recorded even if we are asked to stop: it is bounded by
				// the sender's own timeout, and abandoning it would send the message AND leave the row to be sent again.
				dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliverTimeout)
				defer cancel()
				if err := w.Deliver(dctx, d, &one); err != nil {
					log.Printf("outbox: delivery %s: %v", d.DeliveryID, err) // ids only: never a payload, secret or URL
				}
				mu.Lock()
				st.Claimed++
				if d.Reclaimed {
					st.Reclaimed++
				}
				st.Delivered += one.Delivered
				st.Retrying += one.Retrying
				st.Dead += one.Dead
				st.LeaseLost += one.LeaseLost
				mu.Unlock()
			}(d)
		}
		wg.Wait()
		w.Release(unstarted, &st)
		handled += len(claimed)
	}
	if ctx.Err() != nil {
		return st, nil // asked to stop: nothing is held, and the housekeeping can wait for the next run
	}
	if n, err := Purge(ctx, w.pool, w.cfg.Now()); err != nil {
		log.Printf("outbox: purge: %v", err)
	} else {
		st.Purged = int(n)
	}
	return st, nil
}

// deliverTimeout bounds one delivery once it has begun (the sender has its own, shorter, timeout).
const deliverTimeout = 30 * time.Second

// Release gives claimed rows back unsent: pending again, lease cleared, the attempt that never happened taken off. It
// only touches rows still delivering under THIS worker's lease. Its own short context, because the caller's is done.
func (w *Worker) Release(rows []Delivery, st *Stats) {
	if len(rows) == 0 {
		return
	}
	ids := make([]string, len(rows))
	for i, d := range rows {
		ids[i] = d.ID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tag, err := w.pool.Exec(ctx, `
		UPDATE outbox
		SET status = 'pending', lease_owner = NULL, lease_expires_at = NULL, attempts = GREATEST(attempts - 1, 0), updated_at = $3
		WHERE id = ANY($1::uuid[]) AND status = 'delivering' AND lease_owner = $2`, ids, w.cfg.WorkerID, w.cfg.Now())
	if err != nil {
		log.Printf("outbox: could not release %d claimed row(s); their leases will expire: %v", len(ids), err)
		return
	}
	st.Released += int(tag.RowsAffected())
}

func (w *Worker) kinds() []string {
	var ks []string
	for k := range w.senders {
		ks = append(ks, string(k))
	}
	return ks
}

// giveUp marks dead the rows whose lease expired after the last permitted attempt:
// a delivery that keeps killing its worker must stop.
func (w *Worker) giveUp(ctx context.Context, st *Stats) error {
	now := w.cfg.Now()
	tag, err := w.pool.Exec(ctx, `
		UPDATE outbox SET status = 'dead', last_error = 'given up: workers kept failing before finishing it', updated_at = $1,
		       lease_owner = NULL, lease_expires_at = NULL
		WHERE status = 'delivering' AND lease_expires_at <= $1 AND attempts >= $2`, now, w.cfg.MaxAttempts)
	if err != nil {
		return err
	}
	st.Dead += int(tag.RowsAffected())
	return nil
}

// Claim takes up to n due rows: pending or failed ones whose time has come, and
// delivering ones whose lease has expired. The claim commits before returning.
//
// PerTarget is enforced here: candidates are ranked per target, a target already
// at its in-flight cap (rows under a live lease) gets none, and FOR UPDATE ... SKIP
// LOCKED is on the final selection only (it cannot be combined with the window).
func (w *Worker) Claim(ctx context.Context, n int) ([]Delivery, error) {
	now := w.cfg.Now()
	rows, err := w.pool.Query(ctx, `
		WITH cand AS (
			SELECT id, target_key,
			       row_number() OVER (PARTITION BY target_key ORDER BY next_attempt_at, id) AS rn
			FROM outbox
			WHERE kind = ANY($1)
			  AND ((status IN ('pending', 'failed') AND next_attempt_at <= $2)
			    OR (status = 'delivering' AND lease_expires_at <= $2))
			  -- nothing is sent for a workspace that is scheduled for deletion (it resumes if it is restored)
			  AND NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.id = outbox.workspace_id AND w.deleted_at IS NOT NULL)
		), busy AS (
			SELECT target_key, count(*) AS n FROM outbox
			WHERE status = 'delivering' AND lease_expires_at > $2 GROUP BY target_key
		), pick AS (
			SELECT o.id, o.status AS previous FROM outbox o
			JOIN cand c ON c.id = o.id
			LEFT JOIN busy b ON b.target_key = o.target_key
			WHERE c.rn <= GREATEST($3 - COALESCE(b.n, 0), 0)
			  -- Claimability is asserted AGAIN here, on the row being locked: the candidates were read from
			  -- a snapshot, and if another worker claimed a row after that, the lock waits for the claim to
			  -- commit and PostgreSQL re-evaluates THIS level's conditions on the new row version. Without
			  -- it two workers could claim the same row.
			  AND ((o.status IN ('pending', 'failed') AND o.next_attempt_at <= $2)
			    OR (o.status = 'delivering' AND o.lease_expires_at <= $2))
			ORDER BY o.next_attempt_at, o.id
			LIMIT $4
			FOR UPDATE OF o SKIP LOCKED
		)
		UPDATE outbox o
		SET status = 'delivering', lease_owner = $5, lease_expires_at = $6, attempts = o.attempts + 1, last_attempt_at = $2, updated_at = $2
		FROM pick WHERE o.id = pick.id
		RETURNING o.id::text, o.workspace_id::text, o.kind, o.event_type, o.event_id::text, o.delivery_id::text,
		          COALESCE(o.endpoint_id::text, ''), COALESCE(o.invite_id::text, ''), o.target_key, o.payload::text, o.attempts,
		          pick.previous = 'delivering'`,
		w.kinds(), now, w.cfg.PerTarget, n, w.cfg.WorkerID, now.Add(w.cfg.Lease))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Delivery, error) {
		var d Delivery
		var kind, payload string
		err := row.Scan(&d.ID, &d.WorkspaceID, &kind, &d.EventType, &d.EventID, &d.DeliveryID, &d.EndpointID, &d.InviteID, &d.TargetKey, &payload, &d.Attempt, &d.Reclaimed)
		d.Kind, d.Payload = Kind(kind), json.RawMessage(payload)
		return d, err
	})
}

// Deliver sends one claimed row and records the result. Run is Claim followed by
// Deliver for each row; they are exported separately so a test can stop between
// the two, where a crash lands.
func (w *Worker) Deliver(ctx context.Context, d Delivery, st *Stats) error {
	sender := w.senders[d.Kind]
	if sender == nil {
		return fmt.Errorf("no sender for kind %q", d.Kind)
	}
	// No transaction is open here: this is the network call.
	res := sender.Send(ctx, d)
	res.Err = sanitize(res.Err, 300)
	res.Hint = sanitize(res.Hint, 200)

	now := w.cfg.Now()
	o := Outcome{Result: res, Now: now}
	switch {
	case res.Delivered:
		o.Status = StatusDelivered
	case res.Permanent || d.Attempt >= w.cfg.MaxAttempts:
		o.Status, o.Exhausted = StatusDead, !res.Permanent
	default:
		o.Status = StatusFailed
		wait := time.Duration(float64(w.cfg.Backoff(d.Attempt)) * w.cfg.Jitter())
		o.NextAt = now.Add(wait)
	}
	return pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		next := o.NextAt
		if next.IsZero() {
			next = now
		}
		var delivered any
		if o.Status == StatusDelivered {
			delivered = now
		}
		tag, err := tx.Exec(ctx, `
			UPDATE outbox
			SET status = $3, next_attempt_at = $4, last_error = NULLIF($5, ''), last_http_status = NULLIF($6, 0), last_duration_ms = $7,
			    last_response_hint = NULLIF($8, ''), delivered_at = $9, updated_at = $10, lease_owner = NULL, lease_expires_at = NULL
			WHERE id = $1::uuid AND workspace_id = $2::uuid AND status = 'delivering' AND lease_owner = $11`,
			d.ID, d.WorkspaceID, o.Status, next, o.Err, o.HTTPStatus, res.Duration.Milliseconds(), o.Hint, delivered, now, w.cfg.WorkerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// The lease was lost: another worker owns the row now. The call was made (at least once);
			// recording nothing is correct, and the other worker decides what happens next.
			st.LeaseLost++
			return nil
		}
		if f, ok := sender.(Finisher); ok {
			if err := f.Finish(ctx, tx, d, o); err != nil {
				return err
			}
		}
		switch o.Status {
		case StatusDelivered:
			st.Delivered++
		case StatusFailed:
			st.Retrying++
		default:
			st.Dead++
		}
		return nil
	})
}

// sanitize makes a message safe to store and show: printable characters only,
// one line, truncated. It is a second line of defence: Senders are written not to
// put secrets or bodies in Err in the first place.
func sanitize(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsPrint(r):
			b.WriteRune(r)
		}
		if b.Len() >= max {
			break
		}
	}
	out := b.String()
	if len(out) > max {
		out = out[:max]
	}
	return strings.TrimSpace(out)
}

// Purge deletes finished rows past their retention (delivered: 14 days, dead: 30),
// in batches, safe to run concurrently. It returns how many it deleted.
func Purge(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int64, error) {
	var total int64
	for i := 0; i < 100; i++ { // bounded: 100 batches of 1000 per run
		tag, err := pool.Exec(ctx, `
			DELETE FROM outbox WHERE id IN (
				SELECT id FROM outbox
				WHERE (status = 'delivered' AND updated_at < $1) OR (status = 'dead' AND updated_at < $2)
				LIMIT 1000
				FOR UPDATE SKIP LOCKED)`, now.Add(-RetentionDelivered), now.Add(-RetentionDead))
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < 1000 {
			break
		}
	}
	return total, nil
}

// ClaimOne leases one specific row now if it is claimable (pending or failed):
// a test ping or a resend that should be sent at once rather than at the next
// worker pass. ok is false when the row is not in this workspace, is delivered,
// dead-and-not-reset, or is already leased.
func (w *Worker) ClaimOne(ctx context.Context, workspaceID, deliveryID string) (Delivery, bool, error) {
	now := w.cfg.Now()
	rows, err := w.pool.Query(ctx, `
		UPDATE outbox SET status = 'delivering', lease_owner = $3, lease_expires_at = $4, attempts = attempts + 1, last_attempt_at = $5, updated_at = $5
		WHERE delivery_id = $1::uuid AND workspace_id = $2::uuid AND status IN ('pending', 'failed')
		RETURNING id::text, workspace_id::text, kind, event_type, event_id::text, delivery_id::text,
		          COALESCE(endpoint_id::text, ''), COALESCE(invite_id::text, ''), target_key, payload::text, attempts, false`,
		deliveryID, workspaceID, w.cfg.WorkerID, now.Add(w.cfg.Lease), now)
	if err != nil {
		return Delivery{}, false, err
	}
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Delivery, error) {
		var d Delivery
		var kind, payload string
		err := row.Scan(&d.ID, &d.WorkspaceID, &kind, &d.EventType, &d.EventID, &d.DeliveryID, &d.EndpointID, &d.InviteID, &d.TargetKey, &payload, &d.Attempt, &d.Reclaimed)
		d.Kind, d.Payload = Kind(kind), json.RawMessage(payload)
		return d, err
	})
	if err != nil || len(got) == 0 {
		return Delivery{}, false, err
	}
	return got[0], true, nil
}

// SendNow claims one row and delivers it in this call, returning the stats.
func (w *Worker) SendNow(ctx context.Context, workspaceID, deliveryID string) (Stats, bool, error) {
	var st Stats
	d, ok, err := w.ClaimOne(ctx, workspaceID, deliveryID)
	if err != nil || !ok {
		return st, false, err
	}
	return st, true, w.Deliver(ctx, d, &st)
}
