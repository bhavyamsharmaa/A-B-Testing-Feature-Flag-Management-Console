package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/outbox"
)

// ---- helpers -------------------------------------------------------------------

type outboxWorld struct {
	*tenants
	clock *fakeClock
}

func newOutboxWorld(t *testing.T) *outboxWorld {
	t.Helper()
	return &outboxWorld{tenants: newTenants(t), clock: &fakeClock{t: time.Now().UTC()}}
}

func (w *outboxWorld) endpoint(t *testing.T, ws string) string {
	t.Helper()
	var id string
	err := w.h.pool.QueryRow(context.Background(), `
		INSERT INTO webhook_endpoints (workspace_id, url, event_types, secret_enc, secret_hint)
		VALUES ($1::uuid, 'https://example.com/hook', ARRAY['flag.updated'], '\x00', 'abcd') RETURNING id::text`, ws).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (w *outboxWorld) enqueue(t *testing.T, ws, endpoint string) string {
	t.Helper()
	var d string
	err := pgx.BeginFunc(context.Background(), w.h.pool, func(tx pgx.Tx) error {
		var err error
		d, err = outbox.Enqueue(context.Background(), tx, outbox.Item{
			WorkspaceID: ws, Kind: outbox.KindWebhook, EventType: "flag.updated", EndpointID: endpoint, TargetKey: endpoint,
			Payload: map[string]any{"hello": "world"},
		}, w.clock.now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// fakeSender records what it was asked to deliver and answers with a canned result.
type fakeSender struct {
	mu      sync.Mutex
	seen    []outbox.Delivery
	result  func(outbox.Delivery) outbox.Result
	during  func(outbox.Delivery) // runs inside Send, with no transaction of ours open
	finish  func(outbox.Delivery, outbox.Outcome)
	inSend  atomic.Int64
	maxSend atomic.Int64
}

func (f *fakeSender) Send(_ context.Context, d outbox.Delivery) outbox.Result {
	n := f.inSend.Add(1)
	for {
		m := f.maxSend.Load()
		if n <= m || f.maxSend.CompareAndSwap(m, n) {
			break
		}
	}
	defer f.inSend.Add(-1)
	f.mu.Lock()
	f.seen = append(f.seen, d)
	f.mu.Unlock()
	if f.during != nil {
		f.during(d)
	}
	if f.result != nil {
		return f.result(d)
	}
	return outbox.Result{Delivered: true, HTTPStatus: 200}
}

func (f *fakeSender) deliveries() []outbox.Delivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]outbox.Delivery{}, f.seen...)
}

func (w *outboxWorld) worker(id string, s outbox.Sender, cfg outbox.Config) *outbox.Worker {
	cfg.WorkerID, cfg.Now = id, w.clock.now
	if cfg.Jitter == nil {
		cfg.Jitter = func() float64 { return 1 }
	}
	return outbox.NewWorker(w.h.pool, map[outbox.Kind]outbox.Sender{outbox.KindWebhook: s}, cfg)
}

func (w *outboxWorld) state(id string) (status string, attempts int, next time.Time, lastErr string) {
	var e *string
	err := w.h.pool.QueryRow(context.Background(), `SELECT status, attempts, next_attempt_at, last_error FROM outbox WHERE delivery_id = $1::uuid`, id).Scan(&status, &attempts, &next, &e)
	if err != nil {
		w.h.t.Fatal(err)
	}
	if e != nil {
		lastErr = *e
	}
	return
}

// ---- tests -----------------------------------------------------------------------

func TestAnOutboxRowIsWrittenInTheSameTransactionAsTheChange(t *testing.T) {
	w := newOutboxWorld(t)
	ep := w.endpoint(t, w.wsA.ID)
	boom := errors.New("the change fails after the row was queued")
	err := pgx.BeginFunc(context.Background(), w.h.pool, func(tx pgx.Tx) error {
		if _, err := outbox.Enqueue(context.Background(), tx, outbox.Item{WorkspaceID: w.wsA.ID, Kind: outbox.KindWebhook, EventType: "flag.updated", EndpointID: ep, TargetKey: ep, Payload: map[string]any{}}, w.clock.now()); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if n := w.h.count(`SELECT count(*) FROM outbox`); n != 0 {
		t.Fatalf("a rolled-back change left %d row(s)", n)
	}
	w.enqueue(t, w.wsA.ID, ep)
	if n := w.h.count(`SELECT count(*) FROM outbox`); n != 1 {
		t.Fatalf("a committed change left %d row(s)", n)
	}
}

func TestTwoWorkersNeverClaimTheSameRow(t *testing.T) {
	w := newOutboxWorld(t)
	for i := 0; i < 10; i++ {
		ep := w.endpoint(t, w.wsA.ID)
		for j := 0; j < 8; j++ {
			w.enqueue(t, w.wsA.ID, ep)
		}
	}
	s := &fakeSender{}
	cfg := outbox.Config{PerTarget: 100, BatchSize: 7}
	a, b := w.worker("worker-a", s, cfg), w.worker("worker-b", s, cfg)
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for _, wk := range []*outbox.Worker{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				got, err := wk.Claim(context.Background(), 7)
				if err != nil {
					t.Error(err)
					return
				}
				if len(got) == 0 {
					return
				}
				mu.Lock()
				for _, d := range got {
					seen[d.DeliveryID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != 80 {
		t.Fatalf("claimed %d distinct rows of 80", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %s claimed %d times", id, n)
		}
	}
}

func TestAStaleLeaseIsReclaimedAndTheOldWorkerCannotRecordAResult(t *testing.T) {
	w := newOutboxWorld(t)
	ep := w.endpoint(t, w.wsA.ID)
	id := w.enqueue(t, w.wsA.ID, ep)
	s := &fakeSender{}
	a := w.worker("worker-a", s, outbox.Config{})
	b := w.worker("worker-b", s, outbox.Config{})
	claimedA, err := a.Claim(context.Background(), 5)
	if err != nil || len(claimedA) != 1 {
		t.Fatalf("%v %v", claimedA, err)
	}
	// a live lease cannot be taken
	if got, _ := b.Claim(context.Background(), 5); len(got) != 0 {
		t.Fatalf("a row under a live lease was claimed")
	}
	w.clock.advance(3 * time.Minute) // past the 2-minute lease
	claimedB, err := b.Claim(context.Background(), 5)
	if err != nil || len(claimedB) != 1 || !claimedB[0].Reclaimed || claimedB[0].Attempt != 2 || claimedB[0].DeliveryID != id {
		t.Fatalf("reclaim: %+v %v", claimedB, err)
	}
	// worker A, still thinking it holds the row, finishes: nothing is recorded
	var st outbox.Stats
	if err := a.Deliver(context.Background(), claimedA[0], &st); err != nil || st.LeaseLost != 1 || st.Delivered != 0 {
		t.Fatalf("stale worker: %+v %v", st, err)
	}
	if status, _, _, _ := w.state(id); status != "delivering" {
		t.Fatalf("status %s after the stale worker", status)
	}
	var st2 outbox.Stats
	if err := b.Deliver(context.Background(), claimedB[0], &st2); err != nil || st2.Delivered != 1 {
		t.Fatalf("holder: %+v %v", st2, err)
	}
	if status, attempts, _, _ := w.state(id); status != "delivered" || attempts != 2 {
		t.Errorf("%s %d", status, attempts)
	}
}

// The network call is made with no transaction open, after the claim has committed.
func TestTheNetworkCallHappensOutsideAnyTransaction(t *testing.T) {
	w := newOutboxWorld(t)
	ep := w.endpoint(t, w.wsA.ID)
	id := w.enqueue(t, w.wsA.ID, ep)
	var inTx, visible int
	s := &fakeSender{during: func(d outbox.Delivery) {
		ctx := context.Background()
		_ = w.h.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state LIKE 'idle in transaction%'`).Scan(&inTx)
		_ = w.h.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE delivery_id = $1::uuid AND status = 'delivering' AND lease_owner = 'worker-a'`, d.DeliveryID).Scan(&visible)
	}}
	if _, err := w.worker("worker-a", s, outbox.Config{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.deliveries()) != 1 {
		t.Fatal("not delivered")
	}
	if inTx != 0 {
		t.Errorf("%d transaction(s) were open during the network call", inTx)
	}
	if visible != 1 {
		t.Errorf("the claim was not committed before the network call (visible=%d)", visible)
	}
	if status, _, _, _ := w.state(id); status != "delivered" {
		t.Errorf("status %s", status)
	}
}

func TestBackoffThenDeadAfterTheLastAttempt(t *testing.T) {
	w := newOutboxWorld(t)
	ep := w.endpoint(t, w.wsA.ID)
	id := w.enqueue(t, w.wsA.ID, ep)
	s := &fakeSender{result: func(outbox.Delivery) outbox.Result {
		return outbox.Result{HTTPStatus: 500, Err: "receiver answered 500"}
	}}
	wk := w.worker("w", s, outbox.Config{})
	want := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour}
	var total time.Duration
	for i := 1; i <= 8; i++ {
		before := w.clock.now()
		st, err := wk.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		status, attempts, next, _ := w.state(id)
		if attempts != i {
			t.Fatalf("attempt %d: attempts=%d", i, attempts)
		}
		if i < 8 {
			if status != "failed" || st.Retrying != 1 {
				t.Fatalf("attempt %d: %s %+v", i, status, st)
			}
			if got := next.Sub(before); got != want[i-1] {
				t.Errorf("after attempt %d the next try is in %v, want %v", i, got, want[i-1])
			}
			// not due before its time
			if st2, _ := wk.Run(context.Background()); st2.Claimed != 0 {
				t.Fatalf("claimed before it was due")
			}
			total += want[i-1]
			w.clock.advance(want[i-1])
		} else if status != "dead" || st.Dead != 1 {
			t.Fatalf("after attempt 8: %s %+v", status, st)
		}
	}
	if total < 20*time.Hour || total > 26*time.Hour {
		t.Errorf("retries span %v, want about a day", total)
	}
	// dead is final: no further claim
	w.clock.advance(48 * time.Hour)
	if st, _ := wk.Run(context.Background()); st.Claimed != 0 {
		t.Errorf("a dead row was claimed")
	}
	if len(s.deliveries()) != 8 {
		t.Errorf("%d attempts", len(s.deliveries()))
	}
}

func TestJitterStaysWithinTwentyPercent(t *testing.T) {
	w := newOutboxWorld(t)
	ep := w.endpoint(t, w.wsA.ID)
	id := w.enqueue(t, w.wsA.ID, ep)
	s := &fakeSender{result: func(outbox.Delivery) outbox.Result { return outbox.Result{Err: "x"} }}
	cfg := outbox.Config{WorkerID: "j", Now: w.clock.now}
	wk := outbox.NewWorker(w.h.pool, map[outbox.Kind]outbox.Sender{outbox.KindWebhook: s}, cfg)
	lo, hi := time.Hour, time.Duration(0)
	for i := 0; i < 1; i++ {
		_, _ = wk.Run(context.Background())
		_, _, next, _ := w.state(id)
		d := next.Sub(w.clock.now())
		lo, hi = min(lo, d), max(hi, d)
	}
	if lo < 48*time.Second || hi > 72*time.Second {
		t.Errorf("first retry in %v..%v, want within 60s +-20%%", lo, hi)
	}
}

func TestACrashedDeliveryIsRetriedWithTheSameDeliveryID(t *testing.T) {
	w := newOutboxWorld(t)
	ep := w.endpoint(t, w.wsA.ID)
	id := w.enqueue(t, w.wsA.ID, ep)
	s := &fakeSender{}
	dying := w.worker("dying", s, outbox.Config{})
	claimed, err := dying.Claim(context.Background(), 1)
	if err != nil || len(claimed) != 1 {
		t.Fatal(err)
	}
	// the request goes out, the receiver processes it, and the worker dies before recording anything
	s.Send(context.Background(), claimed[0])
	w.clock.advance(3 * time.Minute)
	if _, err := w.worker("survivor", s, outbox.Config{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	seen := s.deliveries()
	if len(seen) != 2 {
		t.Fatalf("%d deliveries, want the original and one retry", len(seen))
	}
	if seen[0].DeliveryID != id || seen[1].DeliveryID != id || seen[0].EventID != seen[1].EventID {
		t.Errorf("delivery ids differ across the retry: %s vs %s", seen[0].DeliveryID, seen[1].DeliveryID)
	}
	if seen[1].Attempt != 2 || !seen[1].Reclaimed {
		t.Errorf("retry: %+v", seen[1])
	}
	if status, _, _, _ := w.state(id); status != "delivered" {
		t.Errorf("status %s", status)
	}
}

func TestAnOutboxRowThatKeepsKillingItsWorkerStopsAtTheBound(t *testing.T) {
	w := newOutboxWorld(t)
	ep := w.endpoint(t, w.wsA.ID)
	id := w.enqueue(t, w.wsA.ID, ep)
	s := &fakeSender{}
	for i := 0; i < outbox.MaxAttempts; i++ {
		got, err := w.worker(fmt.Sprintf("dies-%d", i), s, outbox.Config{}).Claim(context.Background(), 1)
		if err != nil || len(got) != 1 {
			t.Fatalf("claim %d: %v %v", i, got, err)
		}
		w.clock.advance(3 * time.Minute)
	}
	if _, err := w.worker("last", s, outbox.Config{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, _, _, msg := w.state(id); status != "dead" || !strings.Contains(msg, "given up") {
		t.Errorf("%s %q", status, msg)
	}
	if len(s.deliveries()) != 0 {
		t.Errorf("a row past its attempts was delivered")
	}
}

func TestOneSlowTargetCannotTakeEveryWorker(t *testing.T) {
	w := newOutboxWorld(t)
	slow, other := w.endpoint(t, w.wsA.ID), w.endpoint(t, w.wsA.ID)
	for i := 0; i < 10; i++ {
		w.enqueue(t, w.wsA.ID, slow)
	}
	for i := 0; i < 3; i++ {
		w.enqueue(t, w.wsA.ID, other)
	}
	s := &fakeSender{}
	wk := w.worker("w", s, outbox.Config{PerTarget: 2})
	first, err := wk.Claim(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	per := map[string]int{}
	for _, d := range first {
		per[d.TargetKey]++
	}
	if per[slow] != 2 || per[other] != 2 || len(first) != 4 {
		t.Fatalf("first claim per target: %v", per)
	}
	// while those are in flight (live leases), neither target gets more, from this worker or another
	for _, who := range []*outbox.Worker{wk, w.worker("other-worker", s, outbox.Config{PerTarget: 2})} {
		if more, _ := who.Claim(context.Background(), 50); len(more) != 0 {
			t.Fatalf("%d more claimed while both targets were at their cap", len(more))
		}
	}
	// finish the slow target's two: it may take two more
	for _, d := range first {
		if d.TargetKey == slow {
			var st outbox.Stats
			if err := wk.Deliver(context.Background(), d, &st); err != nil {
				t.Fatal(err)
			}
		}
	}
	more, _ := wk.Claim(context.Background(), 50)
	if len(more) != 2 || more[0].TargetKey != slow {
		t.Errorf("after finishing: %d more", len(more))
	}
}

func TestConcurrentDeliveriesInOneRunAreBoundedPerTargetAndOverall(t *testing.T) {
	w := newOutboxWorld(t)
	for i := 0; i < 6; i++ {
		ep := w.endpoint(t, w.wsA.ID)
		for j := 0; j < 3; j++ {
			w.enqueue(t, w.wsA.ID, ep)
		}
	}
	s := &fakeSender{during: func(outbox.Delivery) { time.Sleep(30 * time.Millisecond) }}
	st, err := w.worker("w", s, outbox.Config{Concurrency: 3, PerTarget: 1, BatchSize: 18}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Delivered != 18 {
		t.Fatalf("%+v", st)
	}
	if m := s.maxSend.Load(); m > 3 {
		t.Errorf("%d deliveries in flight, limit 3", m)
	}
}

func TestTheQueueCapDropsTheOldestWebhooksAndNeverBlocksTheChange(t *testing.T) {
	w := newOutboxWorld(t)
	ep := w.endpoint(t, w.wsA.ID)
	_, err := w.h.pool.Exec(context.Background(), `
		INSERT INTO outbox (workspace_id, kind, event_type, event_id, endpoint_id, target_key, payload, created_at)
		SELECT $1::uuid, 'webhook', 'flag.updated', gen_random_uuid(), $2::uuid, $2, '{}'::jsonb, now() - (g || ' seconds')::interval
		FROM generate_series(1, $3) g`, w.wsA.ID, ep, outbox.MaxQueuedWebhooksPerWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	var oldest string
	_ = w.h.pool.QueryRow(context.Background(), `SELECT delivery_id::text FROM outbox ORDER BY created_at LIMIT 1`).Scan(&oldest)
	// a user's change that queues one more still succeeds
	newest := w.enqueue(t, w.wsA.ID, ep)
	if status, _, _, msg := w.state(oldest); status != "dead" || !strings.Contains(msg, "dropped") {
		t.Errorf("the oldest was not dropped: %s %q", status, msg)
	}
	if status, _, _, _ := w.state(newest); status != "pending" {
		t.Errorf("the new one is %s", status)
	}
	queued := w.h.count(`SELECT count(*) FROM outbox WHERE workspace_id = $1::uuid AND status IN ('pending','failed','delivering')`, w.wsA.ID)
	if queued != outbox.MaxQueuedWebhooksPerWorkspace {
		t.Errorf("%d queued, want the cap %d", queued, outbox.MaxQueuedWebhooksPerWorkspace)
	}
	if n := w.h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'outbox.overflow'`, w.wsA.ID); n != 1 {
		t.Errorf("overflow audit entries: %d", n)
	}
	w.enqueue(t, w.wsA.ID, ep) // again: dropped again, but the audit entry is throttled
	if n := w.h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'outbox.overflow'`, w.wsA.ID); n != 1 {
		t.Errorf("overflow audit entries after a second drop: %d, want 1 (throttled)", n)
	}
	// another workspace is unaffected
	epB := w.endpoint(t, w.wsB.ID)
	w.enqueue(t, w.wsB.ID, epB)
	if n := w.h.count(`SELECT count(*) FROM outbox WHERE workspace_id = $1::uuid AND status = 'dead'`, w.wsB.ID); n != 0 {
		t.Errorf("B lost %d", n)
	}
}

func TestPurgeRemovesOnlyOldFinishedRows(t *testing.T) {
	w := newOutboxWorld(t)
	ep := w.endpoint(t, w.wsA.ID)
	mk := func(status string, age time.Duration) string {
		id := w.enqueue(t, w.wsA.ID, ep)
		_, err := w.h.pool.Exec(context.Background(), `UPDATE outbox SET status = $2, updated_at = $3 WHERE delivery_id = $1::uuid`, id, status, w.clock.now().Add(-age))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldDelivered := mk("delivered", 15*24*time.Hour)
	newDelivered := mk("delivered", 13*24*time.Hour)
	oldDead := mk("dead", 31*24*time.Hour)
	newDead := mk("dead", 20*24*time.Hour) // dead rows are kept longer than delivered ones
	oldPending := mk("pending", 90*24*time.Hour)
	n, err := outbox.Purge(context.Background(), w.h.pool, w.clock.now())
	if err != nil || n != 2 {
		t.Fatalf("purged %d, %v", n, err)
	}
	for id, want := range map[string]int{oldDelivered: 0, oldDead: 0, newDelivered: 1, newDead: 1, oldPending: 1} {
		if got := w.h.count(`SELECT count(*) FROM outbox WHERE delivery_id = $1::uuid`, id); got != want {
			t.Errorf("row %s: %d, want %d", id, got, want)
		}
	}
}

func TestErrorsAreStoredShortAndPrintable(t *testing.T) {
	w := newOutboxWorld(t)
	ep := w.endpoint(t, w.wsA.ID)
	id := w.enqueue(t, w.wsA.ID, ep)
	s := &fakeSender{result: func(outbox.Delivery) outbox.Result {
		return outbox.Result{Err: "line one\r\nline two\x00\x1b[31m" + strings.Repeat("x", 2000)}
	}}
	if _, err := w.worker("w", s, outbox.Config{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, _, msg := w.state(id)
	if len(msg) > 300 || strings.ContainsAny(msg, "\r\n\x00\x1b") {
		t.Errorf("stored %q (len %d)", msg[:min(len(msg), 60)], len(msg))
	}
}
