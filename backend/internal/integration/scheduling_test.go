package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/controlplane/scheduling"
	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/platform/redisx"
	"helios/backend/internal/server"
)

// fakeClock is the time source of the API and the runner in these tests: no
// test sleeps to make a change come due, it moves the clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type schedWorld struct {
	*tenants
	clock   *fakeClock
	staging string
	prod    string
}

// newSchedWorld: two tenants, a controllable clock, rate limits out of the way
// (these tests make many writes), and A's flag "sched" configured in every environment.
func newSchedWorld(t *testing.T) *schedWorld {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)}
	x := newTenantsWith(t, func(d *server.Deps) {
		d.Clock = clock.now
		d.Limiter = ratelimit.New(ratelimit.Config{Rules: map[string]ratelimit.Rule{
			ratelimit.ClassScheduleWrite: {PerSecond: 1000, Burst: 1000},
			ratelimit.ClassSegmentWrite:  {PerSecond: 1000, Burst: 1000},
		}, StreamsPerKey: 100})
	})
	w := &schedWorld{tenants: x, clock: clock, staging: envID(t, x.wsA, "staging"), prod: envID(t, x.wsA, "production")}
	x.h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", boolFlag("sched"))
	return w
}

func (w *schedWorld) runner(id string, cfg scheduling.Config) *scheduling.Runner {
	cfg.WorkerID = id
	return scheduling.NewRunner(w.h.pool, w.h.bus, w.clock.now, cfg)
}

func (w *schedWorld) run(t *testing.T) scheduling.Stats {
	t.Helper()
	st, err := w.runner("test-worker", scheduling.Config{}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

type schedView struct {
	ID, FlagKey, Environment, Kind, Status, IdempotencyKey, CreatedBy string
	ResultCode, ResultMessage                                         *string
	Attempts                                                          int
	BaseVersion                                                       int64
	RunAt                                                             time.Time
}

func (w *schedWorld) in(d time.Duration) string { return w.clock.now().Add(d).Format(time.RFC3339) }

func (w *schedWorld) schedule(u user, env, flag string, body map[string]any) (int, []byte) {
	return w.h.call(u, "POST", "/environments/"+env+"/flags/"+flag+"/scheduled-changes", body)
}

func (w *schedWorld) scheduleOK(t *testing.T, u user, env, flag, key, kind string, in time.Duration, change map[string]any) schedView {
	t.Helper()
	body := map[string]any{"idempotencyKey": key, "kind": kind, "runAt": w.in(in)}
	if change != nil {
		body["change"] = change
	}
	st, b := w.schedule(u, env, flag, body)
	if st != 201 {
		t.Fatalf("schedule %s %s: status %d, body %s", kind, key, st, b)
	}
	return decode[schedView](t, b)
}

func (w *schedWorld) get(t *testing.T, u user, env, id string) schedView {
	t.Helper()
	return decode[schedView](t, w.h.expect(200, u, "GET", "/environments/"+env+"/scheduled-changes/"+id, nil))
}

func (w *schedWorld) flagCfg(t *testing.T, env, flag string) (enabled bool, version int64, fallthrough_ string) {
	t.Helper()
	c := decode[struct {
		Config struct {
			Enabled                bool
			Version                int64
			FallthroughVariationID string
		}
	}](t, w.h.expect(200, w.a, "GET", "/environments/"+env+"/flags/"+flag, nil)).Config
	return c.Enabled, c.Version, c.FallthroughVariationID
}

func (w *schedWorld) auditRows(action string) int {
	return w.h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = $2`, w.wsA.ID, action)
}

// ---------------------------------------------------------------------------

func TestScheduledEnableRunsOnThroughTheEditPath(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	stream := h.openStream(h.newSDKKey(w.a, w.devA))
	c := w.scheduleOK(t, w.a, w.devA, "sched", "k1", "enable", 10*time.Minute, nil)
	if c.Status != "pending" || c.FlagKey != "sched" || c.Environment != "dev" || c.CreatedBy != w.a.email || c.BaseVersion != 1 {
		t.Fatalf("created = %+v", c)
	}
	// not due: nothing happens
	if st := w.run(t); st.Claimed != 0 {
		t.Fatalf("a change that is not due was claimed: %+v", st)
	}
	if en, _, _ := w.flagCfg(t, w.devA, "sched"); en {
		t.Fatal("enabled early")
	}
	w.clock.advance(10 * time.Minute)
	st := w.run(t)
	if st.Claimed != 1 || st.Succeeded != 1 {
		t.Fatalf("run = %+v", st)
	}
	en, version, _ := w.flagCfg(t, w.devA, "sched")
	if !en || version != 2 {
		t.Errorf("flag enabled=%v version=%d, want enabled at version 2", en, version)
	}
	// The ordinary edit's audit entry, with the creator as the actor "via schedule".
	var actor, after string
	if err := h.pool.QueryRow(t.Context(), `SELECT actor_email, diff_after::text FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'flag.update' AND resource_id = 'sched' AND diff_after::text LIKE '%scheduledChangeId%'`, w.wsA.ID).Scan(&actor, &after); err != nil {
		t.Fatalf("no flag.update audit row from the schedule: %v", err)
	}
	if actor != w.a.email+" via schedule" || !strings.Contains(after, c.ID) || !strings.Contains(after, `"via": "schedule"`) {
		t.Errorf("audit: actor %q after %s", actor, after)
	}
	var who string
	_ = h.pool.QueryRow(t.Context(), `SELECT actor_id::text FROM audit_logs WHERE action = 'scheduled_change.succeeded' AND resource_id = $1`, c.ID).Scan(&who)
	if who != w.a.id {
		t.Errorf("scheduled_change.succeeded is recorded for %q, want the creator", who)
	}
	if msg := stream.next(2 * time.Second); !strings.Contains(msg, `"flagKey":"sched"`) {
		t.Errorf("no flag event: %q", msg)
	}
	got := w.get(t, w.a, w.devA, c.ID)
	if got.Status != "succeeded" || got.Attempts != 1 || got.ResultCode == nil || *got.ResultCode != "APPLIED" {
		t.Errorf("after: %+v", got)
	}
	// once only
	if st := w.run(t); st.Claimed != 0 {
		t.Errorf("a finished change was claimed again: %+v", st)
	}
	if w.auditRows("scheduled_change.create") != 1 {
		t.Error("creation not audited")
	}
}

func TestEveryKindRunsThroughTheNormalPath(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/sched", map[string]any{"enabled": true})
	// disable
	w.scheduleOK(t, w.a, w.devA, "sched", "d", "disable", 5*time.Minute, nil)
	w.clock.advance(5 * time.Minute)
	w.run(t)
	if en, _, _ := w.flagCfg(t, w.devA, "sched"); en {
		t.Error("disable did not disable")
	}
	// kill: critical, even though already off
	w.scheduleOK(t, w.a, w.devA, "sched", "kill", "kill", 5*time.Minute, nil)
	w.clock.advance(5 * time.Minute)
	if st := w.run(t); st.Succeeded != 1 {
		t.Fatalf("kill: %+v", st)
	}
	var sev, actor string
	if err := h.pool.QueryRow(t.Context(), `SELECT severity, actor_email FROM audit_logs WHERE action = 'flag.kill' AND workspace_id = $1::uuid`, w.wsA.ID).Scan(&sev, &actor); err != nil || sev != "critical" || actor != w.a.email+" via schedule" {
		t.Errorf("kill audit: %v severity=%q actor=%q", err, sev, actor)
	}
	// patch: rollout and fallthrough
	w.scheduleOK(t, w.a, w.devA, "sched", "p", "patch", 5*time.Minute, map[string]any{"rollout": map[string]any{"on": 20000, "off": 80000}, "fallthroughVariationId": "off"})
	w.clock.advance(5 * time.Minute)
	if st := w.run(t); st.Succeeded != 1 {
		t.Fatalf("patch: %+v", st)
	}
	if _, _, ft := w.flagCfg(t, w.devA, "sched"); ft != "off" {
		t.Error("patch did not change the fallthrough")
	}
	if got := w.h.count(`SELECT count(*) FROM flag_configs WHERE rollout = '{"on":20000,"off":80000}'::jsonb AND workspace_id = $1::uuid`, w.wsA.ID); got != 1 {
		t.Error("patch did not change the rollout")
	}
}

func TestTwoWorkersExecuteARowOnce(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	const n = 16
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("race-flag-%02d", i)
		h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags", boolFlag(key))
		w.scheduleOK(t, w.a, w.devA, key, "race-"+key, "enable", 2*time.Minute, nil)
	}
	h.slowDown("flag_configs", "UPDATE", 15*time.Millisecond) // widen the window between claim and finish
	w.clock.advance(3 * time.Minute)

	var wg sync.WaitGroup
	var mu sync.Mutex
	total := scheduling.Stats{}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := w.runner(fmt.Sprintf("worker-%d", i), scheduling.Config{BatchSize: 3}).Run(context.Background())
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			total.Claimed += st.Claimed
			total.Succeeded += st.Succeeded
			total.Failed += st.Failed
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total.Claimed != n || total.Succeeded != n || total.Failed != 0 {
		t.Errorf("4 workers over %d rows: %+v (each row must be claimed and executed exactly once)", n, total)
	}
	// each flag was edited exactly once: version 2, one flag.update audit row each
	if got := h.count(`SELECT count(*) FROM flag_configs fc JOIN flags f ON f.id = fc.flag_id WHERE f.key LIKE 'race-flag-%' AND fc.environment_id = $1::uuid AND fc.version = 2 AND fc.enabled`, w.devA); got != n {
		t.Errorf("%d of %d flags are enabled at version 2", got, n)
	}
	if got := h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'flag.update' AND resource_id LIKE 'race-flag-%'`, w.wsA.ID); got != n {
		t.Errorf("%d flag.update audit rows, want %d", got, n)
	}
	if got := h.count(`SELECT count(*) FROM scheduled_changes WHERE workspace_id = $1::uuid AND status = 'succeeded' AND attempts = 1`, w.wsA.ID); got != n {
		t.Errorf("%d rows succeeded with a single attempt, want %d", got, n)
	}
}

// A worker that dies after claiming leaves a running row with a lease. Until the lease runs out nobody
// else touches it; afterwards it is taken over and runs once.
func TestACrashBetweenClaimAndCompletionIsRecovered(t *testing.T) {
	w := newSchedWorld(t)
	c := w.scheduleOK(t, w.a, w.devA, "sched", "crash", "enable", 5*time.Minute, nil)
	w.clock.advance(5 * time.Minute)
	dying := w.runner("dying-worker", scheduling.Config{})
	claimed, err := dying.Claim(context.Background(), 5)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	// ...and the worker dies here: it never executes. The row is running under its lease.
	if got := w.get(t, w.a, w.devA, c.ID); got.Status != "running" || got.Attempts != 1 {
		t.Fatalf("claimed row: %+v", got)
	}
	if st := w.run(t); st.Claimed != 0 {
		t.Fatalf("a row under a live lease was taken over: %+v", st)
	}
	w.clock.advance(3 * time.Minute) // past the 2-minute lease, within the grace
	st := w.run(t)
	if st.Claimed != 1 || st.Reclaimed != 1 || st.Succeeded != 1 {
		t.Fatalf("recovery run: %+v", st)
	}
	if got := w.get(t, w.a, w.devA, c.ID); got.Status != "succeeded" || got.Attempts != 2 {
		t.Errorf("after recovery: %+v", got)
	}
	if en, version, _ := w.flagCfg(t, w.devA, "sched"); !en || version != 2 {
		t.Errorf("flag enabled=%v version=%d: applied once, not twice", en, version)
	}
	// the dead worker comes back and tries to finish: its lease is gone, nothing happens
	var late scheduling.Stats
	if err := dying.Execute(context.Background(), claimed[0], &late); err != nil || late.Skipped != 1 || late.Succeeded != 0 {
		t.Errorf("a worker without the lease executed: %+v %v", late, err)
	}
	if _, version, _ := w.flagCfg(t, w.devA, "sched"); version != 2 {
		t.Errorf("version %d after the stale worker came back", version)
	}
}

// The takeover itself: B takes the row over from A while it is still "running" (not yet executed by B), and
// then A, which still thinks it holds it, tries to execute. Only the holder of the lease may.
func TestAStaleWorkerCannotExecuteARowThatWasTakenOver(t *testing.T) {
	w := newSchedWorld(t)
	c := w.scheduleOK(t, w.a, w.devA, "sched", "takeover", "enable", 5*time.Minute, nil)
	w.clock.advance(5 * time.Minute)
	a := w.runner("worker-a", scheduling.Config{})
	claimedA, err := a.Claim(context.Background(), 5)
	if err != nil || len(claimedA) != 1 {
		t.Fatal(claimedA, err)
	}
	w.clock.advance(3 * time.Minute) // A's lease runs out
	b := w.runner("worker-b", scheduling.Config{})
	claimedB, err := b.Claim(context.Background(), 5)
	if err != nil || len(claimedB) != 1 || !claimedB[0].Reclaimed {
		t.Fatalf("B did not take over: %v %v", claimedB, err)
	}
	var stA, stB scheduling.Stats
	if err := a.Execute(context.Background(), claimedA[0], &stA); err != nil || stA.Skipped != 1 || stA.Succeeded != 0 {
		t.Fatalf("the stale worker executed a row it no longer holds: %+v %v", stA, err)
	}
	if en, _, _ := w.flagCfg(t, w.devA, "sched"); en {
		t.Fatal("applied by the stale worker")
	}
	if err := b.Execute(context.Background(), claimedB[0], &stB); err != nil || stB.Succeeded != 1 {
		t.Fatalf("the holder of the lease could not execute: %+v %v", stB, err)
	}
	if got := w.get(t, w.a, w.devA, c.ID); got.Status != "succeeded" || got.Attempts != 2 {
		t.Errorf("final: %+v", got)
	}
	if _, version, _ := w.flagCfg(t, w.devA, "sched"); version != 2 {
		t.Errorf("version %d: applied more than once", version)
	}
}

func TestAWorkerThatKeepsDyingStopsAtTheBound(t *testing.T) {
	w := newSchedWorld(t)
	c := w.scheduleOK(t, w.a, w.devA, "sched", "poison", "enable", 2*time.Minute, nil)
	w.clock.advance(2 * time.Minute)
	for i := 0; i < 5; i++ { // five workers claim it and die
		if got, err := w.runner(fmt.Sprintf("dying-%d", i), scheduling.Config{}).Claim(context.Background(), 5); err != nil || len(got) != 1 {
			t.Fatalf("claim %d: %v %v", i, got, err)
		}
		w.clock.advance(3 * time.Minute)
	}
	st := w.run(t)
	if st.Failed != 1 || st.Claimed != 0 {
		t.Fatalf("run after five deaths: %+v", st)
	}
	// 5 attempts x 3 minutes is past the 15-minute grace as well; either way it is not applied
	got := w.get(t, w.a, w.devA, c.ID)
	if got.Status != "failed" && got.Status != "missed" {
		t.Errorf("status %q", got.Status)
	}
	if en, _, _ := w.flagCfg(t, w.devA, "sched"); en {
		t.Error("a change that killed five workers was applied")
	}
}

func TestOverdueBeyondGraceIsMissedNotApplied(t *testing.T) {
	w := newSchedWorld(t)
	late := w.scheduleOK(t, w.a, w.devA, "sched", "late", "enable", 5*time.Minute, nil)
	w.clock.advance(5*time.Minute + 16*time.Minute) // worker was down: 16 minutes late, grace is 15
	st := w.run(t)
	if st.Missed != 1 || st.Claimed != 0 || st.Succeeded != 0 {
		t.Fatalf("run = %+v", st)
	}
	got := w.get(t, w.a, w.devA, late.ID)
	if got.Status != "missed" || got.ResultCode == nil || *got.ResultCode != "MISSED" || !strings.Contains(*got.ResultMessage, "not run") {
		t.Errorf("late = %+v", got)
	}
	if en, version, _ := w.flagCfg(t, w.devA, "sched"); en || version != 1 {
		t.Errorf("a missed change was applied: enabled=%v version=%d", en, version)
	}
	var actor, sev string
	if err := w.h.pool.QueryRow(t.Context(), `SELECT actor_email, severity FROM audit_logs WHERE action = 'scheduled_change.missed' AND resource_id = $1`, late.ID).Scan(&actor, &sev); err != nil || actor != w.a.email+" via schedule" || sev != "critical" {
		t.Errorf("missed audit: %v actor=%q severity=%q", err, actor, sev)
	}
	// 14 minutes late is inside the grace window and runs; the window is configurable
	w.scheduleOK(t, w.a, w.devA, "sched", "just-in-time", "enable", 5*time.Minute, nil)
	w.clock.advance(5*time.Minute + 14*time.Minute)
	if st := w.run(t); st.Succeeded != 1 || st.Missed != 0 {
		t.Errorf("14 minutes late: %+v", st)
	}
	w.scheduleOK(t, w.a, w.devA, "sched", "tight", "disable", 5*time.Minute, nil)
	w.clock.advance(5*time.Minute + 2*time.Minute)
	st, err := w.runner("tight-worker", scheduling.Config{Grace: time.Minute}).Run(context.Background())
	if err != nil || st.Missed != 1 {
		t.Errorf("with a 1-minute grace, 2 minutes late: %+v %v", st, err)
	}
}

// A cancel that lands after the claim but before the execution wins and nothing is applied; a cancel that
// arrives while the change is being applied waits for it and is refused. Never half.
func TestCancelRacingWithExecutionNeverHalfApplies(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	// 1. claimed, then cancelled, then the claiming worker executes: it must do nothing
	a := w.scheduleOK(t, w.a, w.devA, "sched", "cancel-first", "enable", 5*time.Minute, nil)
	w.clock.advance(5 * time.Minute)
	r := w.runner("racer", scheduling.Config{})
	claimed, err := r.Claim(context.Background(), 5)
	if err != nil || len(claimed) != 1 {
		t.Fatal(claimed, err)
	}
	cancelled := decode[schedView](t, h.expect(200, w.a, "POST", "/environments/"+w.devA+"/scheduled-changes/"+a.ID+"/cancel", nil))
	if cancelled.Status != "cancelled" {
		t.Fatalf("cancel of a running change: %+v", cancelled)
	}
	var st scheduling.Stats
	if err := r.Execute(context.Background(), claimed[0], &st); err != nil || st.Skipped != 1 || st.Succeeded != 0 {
		t.Fatalf("the executor ran a cancelled change: %+v %v", st, err)
	}
	if en, version, _ := w.flagCfg(t, w.devA, "sched"); en || version != 1 {
		t.Errorf("a cancelled change was applied: enabled=%v version=%d", en, version)
	}

	// 2. the cancel arrives while the change is being applied (the database is slowed so the window is wide):
	//    it waits for the row, then finds a finished change and is refused
	b := w.scheduleOK(t, w.a, w.devA, "sched", "cancel-late", "disable", 5*time.Minute, nil)
	h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/sched", map[string]any{"enabled": true})
	h.slowDown("flag_configs", "UPDATE", 300*time.Millisecond)
	w.clock.advance(5 * time.Minute)
	done := make(chan scheduling.Stats)
	go func() { done <- w.run(t) }()
	deadline := time.Now().Add(10 * time.Second)
	for h.count(`SELECT count(*) FROM scheduled_changes WHERE id = $1::uuid AND status = 'running'`, b.ID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the change never started running")
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, body := h.call(w.a, "POST", "/environments/"+w.devA+"/scheduled-changes/"+b.ID+"/cancel", nil)
	if status != 409 || errCode(t, body) != "NOT_CANCELLABLE" {
		t.Errorf("cancel during execution: %d %s", status, body)
	}
	if st := <-done; st.Succeeded != 1 {
		t.Errorf("run = %+v", st)
	}
	if en, _, _ := w.flagCfg(t, w.devA, "sched"); en {
		t.Error("the change was refused to be cancelled but not applied")
	}
	if got := w.get(t, w.a, w.devA, b.ID); got.Status != "succeeded" {
		t.Errorf("final: %+v", got)
	}
	// cancelling twice, or a finished one, is a clear refusal
	h.expect(409, w.a, "POST", "/environments/"+w.devA+"/scheduled-changes/"+a.ID+"/cancel", nil)
}

func TestIdempotencyKeys(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	body := map[string]any{"idempotencyKey": "same", "kind": "enable", "runAt": w.in(time.Hour)}
	first := decode[schedView](t, h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags/sched/scheduled-changes", body))
	st, b := w.schedule(w.a, w.devA, "sched", body)
	replay := decode[schedView](t, b)
	if st != 200 || replay.ID != first.ID {
		t.Fatalf("a retry of the same request: status %d, %+v", st, replay)
	}
	if n := h.count(`SELECT count(*) FROM scheduled_changes WHERE workspace_id = $1::uuid AND idempotency_key = 'same'`, w.wsA.ID); n != 1 {
		t.Errorf("%d rows for one key", n)
	}
	if n := w.auditRows("scheduled_change.create"); n != 1 {
		t.Errorf("a replay was audited as a new schedule (%d rows)", n)
	}
	// the same key for a different change is refused
	for name, other := range map[string]map[string]any{
		"another kind": {"idempotencyKey": "same", "kind": "disable", "runAt": w.in(time.Hour)},
		"another time": {"idempotencyKey": "same", "kind": "enable", "runAt": w.in(2 * time.Hour)},
	} {
		if b := h.expect(409, w.a, "POST", "/environments/"+w.devA+"/flags/sched/scheduled-changes", other); errCode(t, b) != "IDEMPOTENCY_CONFLICT" {
			t.Errorf("%s: %s", name, b)
		}
	}
	if st, b := w.schedule(w.a, w.staging, "sched", body); st != 409 || errCode(t, b) != "IDEMPOTENCY_CONFLICT" {
		t.Errorf("another environment: %d %s", st, b)
	}
	// keys are per workspace: B can use the same one
	h.expect(201, w.b, "POST", "/environments/"+w.devB+"/flags", boolFlag("sched"))
	h.expect(201, w.b, "POST", "/environments/"+w.devB+"/flags/sched/scheduled-changes", body)
	// a key is required
	h.expect(400, w.a, "POST", "/environments/"+w.devA+"/flags/sched/scheduled-changes", map[string]any{"kind": "enable", "runAt": w.in(time.Hour)})
	// at the cap a replay still answers (it is not a new change)
	for i := 0; i < quota.MaxPendingChangesPerFlag-1; i++ {
		w.scheduleOK(t, w.a, w.devA, "sched", fmt.Sprintf("fill-%d", i), "enable", time.Hour, nil)
	}
	h.expect(409, w.a, "POST", "/environments/"+w.devA+"/flags/sched/scheduled-changes", map[string]any{"idempotencyKey": "over", "kind": "enable", "runAt": w.in(time.Hour)})
	h.expect(200, w.a, "POST", "/environments/"+w.devA+"/flags/sched/scheduled-changes", body)
}

func TestRetriesStopAtTheBound(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	c := w.scheduleOK(t, w.a, w.devA, "sched", "flaky", "enable", 2*time.Minute, nil)
	// make every attempt fail with a database error
	if _, err := h.pool.Exec(t.Context(), `
		CREATE FUNCTION fail_updates() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'simulated outage'; END $$;
		CREATE TRIGGER fail_updates BEFORE UPDATE ON flag_configs FOR EACH ROW EXECUTE FUNCTION fail_updates();`); err != nil {
		t.Fatal(err)
	}
	w.clock.advance(2 * time.Minute)
	wait := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute}
	for attempt := 1; attempt <= 4; attempt++ {
		st := w.run(t)
		if st.Retried != 1 {
			t.Fatalf("attempt %d: %+v", attempt, st)
		}
		got := w.get(t, w.a, w.devA, c.ID)
		if got.Status != "pending" || got.Attempts != attempt || got.ResultCode == nil || *got.ResultCode != "RETRYING" {
			t.Fatalf("after attempt %d: %+v", attempt, got)
		}
		// not due again until the backoff has passed
		w.clock.advance(wait[attempt-1] - time.Second)
		if st := w.run(t); st.Claimed != 0 {
			t.Fatalf("retried before the backoff of %v: %+v", wait[attempt-1], st)
		}
		w.clock.advance(time.Second)
	}
	st := w.run(t) // the fifth and last attempt
	if st.Failed != 1 || st.Retried != 0 {
		t.Fatalf("attempt 5: %+v", st)
	}
	got := w.get(t, w.a, w.devA, c.ID)
	if got.Status != "failed" || got.Attempts != 5 || got.ResultCode == nil || *got.ResultCode != "RETRIES_EXHAUSTED" {
		t.Errorf("final: %+v", got)
	}
	// nothing more happens, even once the outage is over
	h.pool.Exec(t.Context(), `DROP TRIGGER fail_updates ON flag_configs`)
	w.clock.advance(time.Hour)
	if st := w.run(t); st.Claimed != 0 {
		t.Errorf("a failed change was tried again: %+v", st)
	}
	if en, _, _ := w.flagCfg(t, w.devA, "sched"); en {
		t.Error("applied despite failing")
	}
	// retry and failure are audited
	if w.auditRows("scheduled_change.retry") != 4 || w.auditRows("scheduled_change.failed") != 1 {
		t.Errorf("audit: retry=%d failed=%d", w.auditRows("scheduled_change.retry"), w.auditRows("scheduled_change.failed"))
	}
	// the retry waits fit inside the grace window, so a retry is never "late"
	var total time.Duration
	for _, d := range wait {
		total += d
	}
	if total >= 15*time.Minute {
		t.Errorf("backoffs add up to %v, past the default grace", total)
	}
}

// ---- permissions ---------------------------------------------------------------------

func TestSchedulingNeedsTheRoleOfTheChange(t *testing.T) {
	w := newSchedWorld(t)
	editor := w.joinAs("eddie", "editor")
	viewer := w.joinAs("vera", "viewer")
	body := func(kind, key string) map[string]any {
		return map[string]any{"idempotencyKey": key, "kind": kind, "runAt": w.in(time.Hour)}
	}
	// an editor may schedule outside production...
	if st, b := w.schedule(editor, w.staging, "sched", body("enable", "e1")); st != 201 {
		t.Errorf("editor, staging enable: %d %s", st, b)
	}
	// ...but not an approval-gated change in production: the same as making it now
	for _, kind := range []string{"enable", "disable", "patch"} {
		b := body(kind, "prod-"+kind)
		if kind == "patch" {
			b["change"] = map[string]any{"fallthroughVariationId": "off"}
		}
		if st, resp := w.schedule(editor, w.prod, "sched", b); st != 403 || errCode(t, resp) != "FORBIDDEN" || !strings.Contains(errMessage(resp), "approver") {
			t.Errorf("editor, production %s: %d %s", kind, st, resp)
		}
	}
	w.h.expect(403, editor, "PATCH", "/environments/"+w.prod+"/flags/sched", map[string]any{"enabled": true}) // "now" is refused too
	// a kill is allowed for editors everywhere, as an immediate kill is
	if st, b := w.schedule(editor, w.prod, "sched", body("kill", "prod-kill")); st != 201 {
		t.Errorf("editor, production kill: %d %s", st, b)
	}
	// a viewer cannot schedule at all
	if st, _ := w.schedule(viewer, w.staging, "sched", body("enable", "v1")); st != 403 {
		t.Errorf("viewer: %d", st)
	}
	// the owner (an approver) can schedule production changes
	if st, b := w.schedule(w.a, w.prod, "sched", body("enable", "prod-ok")); st != 201 {
		t.Errorf("owner, production: %d %s", st, b)
	}
	if n := w.h.count(`SELECT count(*) FROM scheduled_changes WHERE workspace_id = $1::uuid AND idempotency_key LIKE 'prod-%' AND kind <> 'kill'`, w.wsA.ID); n != 1 {
		t.Errorf("%d production changes stored, want only the owner's", n)
	}
	// viewers read the list; cancel needs the creator or the role of the change
	w.h.expect(200, viewer, "GET", "/environments/"+w.staging+"/scheduled-changes", nil)
	prodChange := decode[struct{ ScheduledChanges []schedView }](t, w.h.expect(200, viewer, "GET", "/environments/"+w.prod+"/scheduled-changes?status=pending", nil)).ScheduledChanges
	for _, c := range prodChange {
		if c.Kind == "enable" { // the owner's
			w.h.expect(403, editor, "POST", "/environments/"+w.prod+"/scheduled-changes/"+c.ID+"/cancel", nil)
			w.h.expect(200, w.a, "POST", "/environments/"+w.prod+"/scheduled-changes/"+c.ID+"/cancel", nil)
		}
		if c.Kind == "kill" { // the editor's own: they can take it back
			w.h.expect(403, viewer, "POST", "/environments/"+w.prod+"/scheduled-changes/"+c.ID+"/cancel", nil)
			w.h.expect(200, editor, "POST", "/environments/"+w.prod+"/scheduled-changes/"+c.ID+"/cancel", nil)
		}
	}
}

// The creator's authority is checked again when the change runs: someone demoted or removed after scheduling
// cannot act through the schedule.
func TestTheCreatorsRoleIsRecheckedAtExecution(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	admin := w.joinAs("adam", "admin")
	setRole := func(u user, role string) {
		h.expect(200, w.a, "PATCH", "/workspaces/"+w.wsA.ID+"/members/"+u.id, map[string]any{"role": role})
	}
	enableProd := w.scheduleOK(t, admin, w.prod, "sched", "prod-enable", "enable", 5*time.Minute, nil)
	killProd := w.scheduleOK(t, admin, w.prod, "sched", "prod-kill", "kill", 5*time.Minute, nil)
	stagingEnable := w.scheduleOK(t, admin, w.staging, "sched", "staging-enable", "enable", 5*time.Minute, nil)
	setRole(admin, "editor") // demoted before it runs
	w.clock.advance(5 * time.Minute)
	st := w.run(t)
	if st.Failed != 1 || st.Succeeded != 2 {
		t.Fatalf("run = %+v (an editor may still kill, and enable in staging; not enable in production)", st)
	}
	got := w.get(t, w.a, w.prod, enableProd.ID)
	if got.Status != "failed" || *got.ResultCode != "CREATOR_NOT_AUTHORIZED" || !strings.Contains(*got.ResultMessage, "approver") {
		t.Errorf("production enable: %+v", got)
	}
	if en, _, _ := w.flagCfg(t, w.prod, "sched"); en {
		t.Error("a demoted creator enabled a flag in production through the schedule")
	}
	if got := w.get(t, w.a, w.prod, killProd.ID); got.Status != "succeeded" {
		t.Errorf("an editor's scheduled kill: %+v", got)
	}
	if got := w.get(t, w.a, w.staging, stagingEnable.ID); got.Status != "succeeded" {
		t.Errorf("an editor's scheduled staging enable: %+v", got)
	}
	var actor, sev, reason string
	if err := h.pool.QueryRow(t.Context(), `SELECT actor_email, severity, diff_after->>'reason' FROM audit_logs WHERE action = 'scheduled_change.failed' AND resource_id = $1`, enableProd.ID).Scan(&actor, &sev, &reason); err != nil {
		t.Fatal(err)
	}
	if actor != admin.email+" via schedule" || sev != "critical" || !strings.Contains(reason, "editor") {
		t.Errorf("failure audit: actor=%q severity=%q reason=%q", actor, sev, reason)
	}
	if h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'flag.update' AND resource_id = 'sched' AND environment_id = $2::uuid`, w.wsA.ID, w.prod) != 0 {
		t.Error("an edit was recorded for the refused change")
	}

	// removed from the workspace: also refused
	gone := w.joinAs("gail", "admin")
	c := w.scheduleOK(t, gone, w.prod, "sched", "gail-prod", "enable", 5*time.Minute, nil)
	h.expect(204, w.a, "DELETE", "/workspaces/"+w.wsA.ID+"/members/"+gone.id, nil)
	w.clock.advance(5 * time.Minute)
	w.run(t)
	if got := w.get(t, w.a, w.prod, c.ID); got.Status != "failed" || *got.ResultCode != "CREATOR_REMOVED" || !strings.Contains(*got.ResultMessage, gone.email) {
		t.Errorf("removed creator: %+v", got)
	}
	// and a creator whose account is gone altogether
	again := w.joinAs("hugh", "admin")
	c2 := w.scheduleOK(t, again, w.staging, "sched", "hugh-staging", "disable", 5*time.Minute, nil)
	h.pool.Exec(t.Context(), `UPDATE scheduled_changes SET created_by = NULL WHERE id = $1::uuid`, c2.ID)
	w.clock.advance(5 * time.Minute)
	w.run(t)
	if got := w.get(t, w.a, w.staging, c2.ID); got.Status != "failed" || *got.ResultCode != "CREATOR_REMOVED" {
		t.Errorf("creator with no account: %+v", got)
	}
	// a promoted creator is fine: authority is whatever it is NOW
	editor := w.joinAs("ed", "editor")
	c3 := w.scheduleOK(t, editor, w.staging, "sched", "ed-staging", "disable", 5*time.Minute, nil)
	w.clock.advance(5 * time.Minute)
	if st := w.run(t); st.Succeeded != 1 {
		t.Errorf("an unchanged editor's staging change: %+v (%v)", st, c3.ID)
	}
}

// ---- patches -----------------------------------------------------------------------------

func TestAPatchThatNoLongerAppliesCleanlyFailsWithAConflict(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/sched", map[string]any{"rollout": map[string]any{"on": 10000, "off": 90000}})
	touching := w.scheduleOK(t, w.a, w.devA, "sched", "touch-rollout", "patch", 5*time.Minute, map[string]any{"rollout": map[string]any{"on": 50000, "off": 50000}})
	other := w.scheduleOK(t, w.a, w.devA, "sched", "touch-fallthrough", "patch", 5*time.Minute, map[string]any{"fallthroughVariationId": "off"})
	// someone edits the rollout, and flips enabled, after scheduling
	h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/sched", map[string]any{"rollout": map[string]any{"on": 70000, "off": 30000}, "enabled": true})
	w.clock.advance(5 * time.Minute)
	st := w.run(t)
	if st.Failed != 1 || st.Succeeded != 1 {
		t.Fatalf("run = %+v", st)
	}
	got := w.get(t, w.a, w.devA, touching.ID)
	if got.Status != "failed" || *got.ResultCode != "CONFLICT" || !strings.Contains(*got.ResultMessage, "rollout") {
		t.Errorf("conflicting patch: %+v", got)
	}
	// the other person's edit stands
	if n := h.count(`SELECT count(*) FROM flag_configs WHERE rollout = '{"on":70000,"off":30000}'::jsonb AND workspace_id = $1::uuid`, w.wsA.ID); n != 1 {
		t.Error("the later edit was overwritten by the stale patch")
	}
	// a patch to a part nobody touched still applies, though the version moved (enabled changed)
	if got := w.get(t, w.a, w.devA, other.ID); got.Status != "succeeded" {
		t.Errorf("a patch to an untouched part: %+v", got)
	}
	if _, _, ft := w.flagCfg(t, w.devA, "sched"); ft != "off" {
		t.Error("the clean patch did not apply")
	}
}

func TestSchedulingValidatesLikeAnEdit(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	post := func(want int, body map[string]any) []byte {
		body["idempotencyKey"] = fmt.Sprintf("v-%d", time.Now().UnixNano())
		if _, ok := body["runAt"]; !ok {
			body["runAt"] = w.in(time.Hour)
		}
		return h.expect(want, w.a, "POST", "/environments/"+w.devA+"/flags/sched/scheduled-changes", body)
	}
	post(400, map[string]any{"kind": "teleport"})
	post(400, map[string]any{"kind": "patch"})                                                                                                                                   // no change
	post(400, map[string]any{"kind": "patch", "change": map[string]any{}})                                                                                                       // empty change
	post(400, map[string]any{"kind": "patch", "change": map[string]any{"enabled": true}})                                                                                        // enabled is enable/disable
	post(400, map[string]any{"kind": "patch", "change": map[string]any{"rollout": map[string]any{"on": 1, "off": 2}}})                                                           // does not sum
	post(400, map[string]any{"kind": "patch", "change": map[string]any{"fallthroughVariationId": "nope"}})                                                                       // not a variation
	post(400, map[string]any{"kind": "patch", "change": map[string]any{"targetingRules": []map[string]any{{"clauses": []map[string]any{inSeg("ghost")}, "variationId": "on"}}}}) // unknown segment
	post(400, map[string]any{"kind": "enable", "change": map[string]any{"rollout": map[string]any{"on": 100000}}})                                                               // enable takes no change
	post(400, map[string]any{"kind": "enable", "runAt": "tomorrow"})
	post(400, map[string]any{"kind": "enable", "runAt": w.in(30 * time.Second)}) // under the minimum lead
	post(400, map[string]any{"kind": "enable", "runAt": w.in(-time.Hour)})
	post(400, map[string]any{"kind": "enable", "runAt": w.in(scheduling.MaxHorizon + time.Hour)})
	// the boundaries themselves are accepted
	post(201, map[string]any{"kind": "enable", "runAt": w.in(scheduling.MinLead + time.Second)})
	post(201, map[string]any{"kind": "enable", "runAt": w.in(scheduling.MaxHorizon - time.Hour)})
	// a time with an offset is stored as the same instant in UTC
	c := decode[schedView](t, post(201, map[string]any{"kind": "disable", "runAt": w.clock.now().Add(2 * time.Hour).In(time.FixedZone("IST", 5*3600+1800)).Format(time.RFC3339)}))
	if !c.RunAt.Equal(w.clock.now().Add(2 * time.Hour)) {
		t.Errorf("runAt = %v", c.RunAt)
	}
	h.expect(404, w.a, "POST", "/environments/"+w.devA+"/flags/nope/scheduled-changes", map[string]any{"idempotencyKey": "x", "kind": "enable", "runAt": w.in(time.Hour)})
}

// ---- tenants --------------------------------------------------------------------------

func TestScheduledChangesAreTenantScoped(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	h.expect(201, w.b, "POST", "/environments/"+w.devB+"/flags", boolFlag("sched")) // B has a flag of the same key
	mine := w.scheduleOK(t, w.a, w.devA, "sched", "acme-secret-change", "enable", 5*time.Minute, nil)

	// B cannot see, fetch or cancel A's change: through A's environment (not B's), or through its own
	for _, c := range []struct{ method, path string }{
		{"GET", "/environments/" + w.devA + "/scheduled-changes"},
		{"GET", "/environments/" + w.devA + "/scheduled-changes/" + mine.ID},
		{"POST", "/environments/" + w.devA + "/scheduled-changes/" + mine.ID + "/cancel"},
		{"POST", "/environments/" + w.devA + "/flags/sched/scheduled-changes"},
	} {
		st, body := h.call(w.b, c.method, c.path, map[string]any{"idempotencyKey": "x", "kind": "enable", "runAt": w.in(time.Hour)})
		if st != 404 || errCode(t, body) != "ENVIRONMENT_NOT_FOUND" || strings.Contains(string(body), "acme") {
			t.Errorf("B %s %s: %d %s", c.method, c.path, st, body)
		}
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/environments/" + w.devB + "/scheduled-changes/" + mine.ID},
		{"POST", "/environments/" + w.devB + "/scheduled-changes/" + mine.ID + "/cancel"},
	} {
		if st, body := h.call(w.b, c.method, c.path, nil); st != 404 || errCode(t, body) != "SCHEDULED_CHANGE_NOT_FOUND" {
			t.Errorf("B %s %s: %d %s", c.method, c.path, st, body)
		}
	}
	if list := decode[struct{ ScheduledChanges []schedView }](t, h.expect(200, w.b, "GET", "/environments/"+w.devB+"/scheduled-changes", nil)); len(list.ScheduledChanges) != 0 {
		t.Errorf("B lists %d of A's changes", len(list.ScheduledChanges))
	}
	// A's change is untouched, and still pending
	if got := w.get(t, w.a, w.devA, mine.ID); got.Status != "pending" {
		t.Errorf("A's change: %+v", got)
	}
	// B's change with the same idempotency key is its own; running A's does not touch B's flag of the same key
	bc := decode[schedView](t, h.expect(201, w.b, "POST", "/environments/"+w.devB+"/flags/sched/scheduled-changes",
		map[string]any{"idempotencyKey": "acme-secret-change", "kind": "disable", "runAt": w.in(time.Hour)}))
	if bc.ID == mine.ID {
		t.Error("the same key in two workspaces is the same change")
	}
	w.clock.advance(5 * time.Minute)
	w.run(t)
	enB := decode[struct {
		Config struct {
			Enabled bool
			Version int64
		}
	}](t, h.expect(200, w.b, "GET", "/environments/"+w.devB+"/flags/sched", nil)).Config
	if enB.Enabled || enB.Version != 1 {
		t.Errorf("A's scheduled enable reached B's flag: %+v", enB)
	}
	if en, _, _ := w.flagCfg(t, w.devA, "sched"); !en {
		t.Error("A's change did not run")
	}
	// B's audit log shows none of A's scheduled-change rows
	_, logs := h.call(w.b, "GET", "/environments/"+w.devB+"/audit-logs", nil)
	if bytes.Contains(logs, []byte("acme-secret-change")) || bytes.Contains(logs, []byte(mine.ID)) || bytes.Contains(logs, []byte("via schedule")) && !bytes.Contains(logs, []byte(w.b.email)) {
		t.Errorf("B's audit log shows A's schedule: %s", logs)
	}
	// the database itself refuses a row that mixes workspaces
	var flagA string
	if err := h.pool.QueryRow(t.Context(), `SELECT id::text FROM flags WHERE key = 'sched' AND workspace_id = $1::uuid`, w.wsA.ID).Scan(&flagA); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(t.Context(), `INSERT INTO scheduled_changes (workspace_id, flag_id, environment_id, kind, base_version, run_at, due_at, idempotency_key, created_by_email)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'enable', 1, now(), now(), 'mixed', 'x')`, w.wsB.ID, flagA, w.devB); err == nil {
		t.Error("the database accepted a scheduled change in workspace B for a flag of workspace A")
	}
	if _, err := h.pool.Exec(t.Context(), `INSERT INTO scheduled_changes (workspace_id, flag_id, environment_id, kind, base_version, run_at, due_at, idempotency_key, created_by_email)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'enable', 1, now(), now(), 'mixed2', 'x')`, w.wsA.ID, flagA, w.devB); err == nil {
		t.Error("the database accepted a scheduled change for another workspace's environment")
	}
}

func TestSchedulingCaps(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	over := func(key, flag string) (int, []byte) {
		return w.schedule(w.a, w.devA, flag, map[string]any{"idempotencyKey": key, "kind": "enable", "runAt": w.in(time.Hour)})
	}
	var ids []string
	for i := 0; i < quota.MaxPendingChangesPerFlag; i++ {
		ids = append(ids, w.scheduleOK(t, w.a, w.devA, "sched", fmt.Sprintf("a-%d", i), "enable", time.Hour, nil).ID)
	}
	st, body := over("a-over", "sched")
	if st != 409 || errCode(t, body) != "QUOTA_EXCEEDED" || !strings.Contains(errMessage(body), "per flag") {
		t.Fatalf("per-flag cap: %d %s", st, body)
	}
	// cancelling frees a slot
	h.expect(200, w.a, "POST", "/environments/"+w.devA+"/scheduled-changes/"+ids[0]+"/cancel", nil)
	if st, b := over("a-again", "sched"); st != 201 {
		t.Errorf("after a cancel: %d %s", st, b)
	}
	// the workspace cap: ten flags of ten changes each is the limit
	for f := 0; f < quota.MaxPendingChangesPerWorkspace/quota.MaxPendingChangesPerFlag-1; f++ {
		key := fmt.Sprintf("cap-flag-%d", f)
		h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags", boolFlag(key))
		for i := 0; i < quota.MaxPendingChangesPerFlag; i++ {
			w.scheduleOK(t, w.a, w.devA, key, fmt.Sprintf("%s-%d", key, i), "enable", time.Hour, nil)
		}
	}
	h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags", boolFlag("one-more-flag"))
	st, body = over("ws-over", "one-more-flag")
	if st != 409 || !strings.Contains(errMessage(body), "per workspace") {
		t.Errorf("per-workspace cap: %d %s", st, body)
	}
	// another workspace is not affected
	h.expect(201, w.b, "POST", "/environments/"+w.devB+"/flags", boolFlag("sched"))
	h.expect(201, w.b, "POST", "/environments/"+w.devB+"/flags/sched/scheduled-changes", map[string]any{"idempotencyKey": "b", "kind": "enable", "runAt": w.in(time.Hour)})
	// finished changes do not count against the cap
	w.clock.advance(time.Hour)
	w.run(t)
	if st, b := over("after-run", "one-more-flag"); st != 201 {
		t.Errorf("after the pending ones ran: %d %s", st, b)
	}
}

// Parallel schedules cannot overshoot a cap: the workspace row lock serialises the count and the insert.
func TestSchedulingCapHoldsUnderConcurrency(t *testing.T) {
	w := newSchedWorld(t)
	var wg sync.WaitGroup
	statuses := make([]int, 16)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], _ = w.schedule(w.a, w.devA, "sched", map[string]any{"idempotencyKey": fmt.Sprintf("par-%d", i), "kind": "enable", "runAt": w.in(time.Hour)})
		}()
	}
	wg.Wait()
	created := 0
	for _, s := range statuses {
		if s == 201 {
			created++
		} else if s != 409 {
			t.Errorf("status %d", s)
		}
	}
	if created != quota.MaxPendingChangesPerFlag {
		t.Errorf("%d created in parallel, want exactly %d", created, quota.MaxPendingChangesPerFlag)
	}
}

func TestScheduledChangeListAndStatusFilter(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	soon := w.scheduleOK(t, w.a, w.devA, "sched", "soon", "enable", 5*time.Minute, nil)
	later := w.scheduleOK(t, w.a, w.devA, "sched", "later", "disable", 3*time.Hour, nil)
	w.scheduleOK(t, w.a, w.staging, "sched", "elsewhere", "enable", time.Hour, nil)
	w.clock.advance(5 * time.Minute)
	w.run(t)
	list := decode[struct{ ScheduledChanges []schedView }](t, h.expect(200, w.a, "GET", "/environments/"+w.devA+"/scheduled-changes", nil)).ScheduledChanges
	if len(list) != 2 || list[0].ID != later.ID || list[1].ID != soon.ID {
		t.Errorf("order: pending first, then history: %+v", list)
	}
	only := decode[struct{ ScheduledChanges []schedView }](t, h.expect(200, w.a, "GET", "/environments/"+w.devA+"/scheduled-changes?status=succeeded", nil)).ScheduledChanges
	if len(only) != 1 || only[0].ID != soon.ID {
		t.Errorf("status filter: %+v", only)
	}
	h.expect(400, w.a, "GET", "/environments/"+w.devA+"/scheduled-changes?status=bogus", nil)
	h.expect(404, w.a, "GET", "/environments/"+w.devA+"/scheduled-changes/not-a-uuid", nil)
}

// cmd/scheduler is a one-shot command: it runs what is due and exits, printing counts only.
func TestSchedulerCommandRunsDueChangesOnce(t *testing.T) {
	w := newSchedWorld(t)
	w.scheduleOK(t, w.a, w.devA, "sched", "cli", "enable", time.Minute+time.Second, nil)
	// the command uses the real clock: backdate the change instead of moving a clock it cannot see
	w.h.pool.Exec(t.Context(), `UPDATE scheduled_changes SET run_at = now() - interval '1 minute', due_at = now() - interval '1 minute' WHERE idempotency_key = 'cli'`)
	cmd := exec.Command("go", "run", "../../cmd/scheduler")
	cmd.Env = append(os.Environ(), "DATABASE_URL="+w.h.dbURL, "REDIS_URL=", "SCHEDULER_GRACE=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cmd/scheduler: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "claimed 1 (reclaimed 0), succeeded 1, failed 0, retrying 0, missed 0, skipped 0") {
		t.Errorf("output: %s", out)
	}
	if strings.Contains(string(out), "sched") && strings.Contains(string(out), w.a.email) {
		t.Errorf("the command printed more than counts: %s", out)
	}
	if en, _, _ := w.flagCfg(t, w.devA, "sched"); !en {
		t.Error("the command did not apply the change")
	}
	out, err = exec.Command("go", "run", "../../cmd/scheduler", "-grace", "10s").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "-grace must be between 1m and 24h") {
		t.Errorf("a silly grace window was accepted: %v %s", err, out)
	}
}

// With a real Redis the scheduler's event crosses it and reaches only the right stream.
func TestRealRedisCarriesScheduledChangeEvents(t *testing.T) {
	url := os.Getenv("HELIOS_TEST_REDIS_URL")
	if url == "" {
		t.Skip("HELIOS_TEST_REDIS_URL not set; skipping the Redis-backed test")
	}
	if !strings.Contains(url, "127.0.0.1") && !strings.Contains(url, "localhost") {
		t.Fatalf("HELIOS_TEST_REDIS_URL must be a local Redis, got %q", url)
	}
	client, err := redisx.Connect(t.Context(), url)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	clock := &fakeClock{t: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)}
	h := newHarnessWith(t, func(d *server.Deps) {
		d.Publisher, d.Subscriber = redisx.Publisher{Client: client}, redisx.Subscriber{Client: client}
		d.Clock = clock.now
		slowFallbacks(d)
	})
	x := &tenants{h: h, a: h.newUser("alice"), b: h.newUser("bob")}
	x.wsA, x.wsB = h.me(x.a).Workspaces[0], h.me(x.b).Workspaces[0]
	x.devA, x.devB = envID(t, x.wsA, "dev"), envID(t, x.wsB, "dev")
	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", boolFlag("sched"))
	sA, sB := h.openStream(h.newSDKKey(x.a, x.devA)), h.openStream(h.newSDKKey(x.b, x.devB))
	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags/sched/scheduled-changes", map[string]any{
		"idempotencyKey": "r", "kind": "enable", "runAt": clock.now().Add(5 * time.Minute).Format(time.RFC3339)})
	clock.advance(5 * time.Minute)
	runner := scheduling.NewRunner(h.pool, redisx.Publisher{Client: client}, clock.now, scheduling.Config{})
	if st, err := runner.Run(t.Context()); err != nil || st.Succeeded != 1 {
		t.Fatalf("run: %+v %v", st, err)
	}
	if msg := sA.next(3 * time.Second); !strings.Contains(msg, `"flagKey":"sched"`) {
		t.Errorf("A's stream through real Redis: %q", msg)
	}
	if other := sB.next(400 * time.Millisecond); other != "" {
		t.Errorf("B's stream heard A's scheduled change: %q", other)
	}
}

// Every state change of a scheduled change leaves an audit row, with the actor recorded as who did it: the
// person for create and cancel, the creator "via schedule" for what the scheduler did.
func TestEveryStateChangeIsAudited(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	editor := w.joinAs("eddie", "editor")
	canceller := w.joinAs("carl", "admin")
	ok := w.scheduleOK(t, editor, w.staging, "sched", "ok", "enable", 5*time.Minute, nil)
	toCancel := w.scheduleOK(t, editor, w.staging, "sched", "cancel", "disable", time.Hour, nil)
	late := w.scheduleOK(t, editor, w.staging, "sched", "late", "enable", 2*time.Minute, nil)
	h.expect(200, canceller, "POST", "/environments/"+w.staging+"/scheduled-changes/"+toCancel.ID+"/cancel", nil) // an admin cancels the editor's
	h.expect(200, w.a, "PATCH", "/workspaces/"+w.wsA.ID+"/members/"+editor.id, map[string]any{"role": "viewer"})

	row := func(action, resource string) (actor, severity string, found bool) {
		err := h.pool.QueryRow(t.Context(), `SELECT actor_email, severity FROM audit_logs WHERE workspace_id = $1::uuid AND action = $2 AND resource_id = $3`, w.wsA.ID, action, resource).Scan(&actor, &severity)
		return actor, severity, err == nil
	}
	for _, c := range []struct{ action, id, actor string }{
		{"scheduled_change.create", ok.ID, editor.email},
		{"scheduled_change.create", toCancel.ID, editor.email},
		{"scheduled_change.cancel", toCancel.ID, canceller.email}, // the person who cancelled, not the creator
	} {
		if actor, _, found := row(c.action, c.id); !found || actor != c.actor {
			t.Errorf("%s %s: found=%v actor=%q, want %q", c.action, c.id, found, actor, c.actor)
		}
	}
	w.clock.advance(2*time.Minute + 20*time.Minute) // `late` is 20 minutes overdue, `ok` too: both pass the grace
	w.run(t)
	if actor, sev, found := row("scheduled_change.missed", late.ID); !found || actor != editor.email+" via schedule" || sev != "critical" {
		t.Errorf("missed: found=%v actor=%q severity=%q", found, actor, sev)
	}
	// a fresh change for the failure path (the first is missed too, being overdue): schedule, demote already done, run on time
	fail := w.scheduleOK(t, w.a, w.staging, "sched", "fail-owner", "enable", 5*time.Minute, nil)
	h.pool.Exec(t.Context(), `UPDATE scheduled_changes SET created_by = $2::uuid, created_by_email = $3 WHERE id = $1::uuid`, fail.ID, editor.id, editor.email)
	succeed := w.scheduleOK(t, w.a, w.staging, "sched", "succeeds", "disable", 5*time.Minute, nil)
	w.clock.advance(5 * time.Minute)
	w.run(t)
	if actor, sev, found := row("scheduled_change.failed", fail.ID); !found || actor != editor.email+" via schedule" || sev != "critical" {
		t.Errorf("failed: found=%v actor=%q severity=%q", found, actor, sev)
	}
	if actor, sev, found := row("scheduled_change.succeeded", succeed.ID); !found || actor != w.a.email+" via schedule" || sev != "info" {
		t.Errorf("succeeded: found=%v actor=%q severity=%q", found, actor, sev)
	}
	// no state change is silent: the number of terminal audit rows equals the number of finished changes
	finished := h.count(`SELECT count(*) FROM scheduled_changes WHERE workspace_id = $1::uuid AND status IN ('succeeded', 'failed', 'missed', 'cancelled')`, w.wsA.ID)
	audited := h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action IN ('scheduled_change.succeeded', 'scheduled_change.failed', 'scheduled_change.missed', 'scheduled_change.cancel')`, w.wsA.ID)
	if finished != audited {
		t.Errorf("%d finished changes but %d terminal audit rows", finished, audited)
	}
}
