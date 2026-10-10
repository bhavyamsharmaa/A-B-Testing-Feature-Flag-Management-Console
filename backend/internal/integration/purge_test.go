package integration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"helios/backend/internal/platform/eventpurge"
)

// ageEvents moves every event of the given subjects back by d.
func (h *harness) ageEvents(d string, subjectPrefix string) {
	h.t.Helper()
	if err := h.rawExec(`UPDATE events SET occurred_at = occurred_at - $1::interval WHERE subject_key LIKE $2`, d, subjectPrefix+"%"); err != nil {
		h.t.Fatal(err)
	}
}

func TestPurgeKeepsRunningAndRecentExperimentsAndDeletesTheRest(t *testing.T) {
	logs := captureLogs(t)
	x := newTenants(t)
	h := x.h
	exp := x.backdated()

	// Another experiment (on a second flag) that is stopped long ago, and one stopped inside the window.
	mk := func(flag, key string) string {
		h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", boolFlag(flag))
		h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/"+flag, map[string]any{"enabled": true})
		h.expect(201, x.a, "POST", "/environments/"+x.devA+"/experiments", map[string]any{
			"flagKey": flag, "key": key, "name": key,
			"metrics": []map[string]any{{"name": "M", "eventName": "e", "type": "conversion", "isPrimary": true}}})
		h.expect(200, x.a, "POST", "/environments/"+x.devA+"/experiments/"+key+"/start", nil)
		var id string
		if err := h.pool.QueryRow(context.Background(), `SELECT id::text FROM experiments WHERE key = $1`, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldStopped, recentStopped := mk("flag-old", "exp-old"), mk("flag-recent", "exp-recent")
	for _, key := range []string{"exp-old", "exp-recent"} {
		h.expect(200, x.a, "POST", "/environments/"+x.devA+"/experiments/"+key+"/stop", nil)
	}
	if err := h.rawExec(`UPDATE experiments SET started_at = now() - interval '300 days', stopped_at = now() - interval '200 days' WHERE id = $1::uuid`, oldStopped); err != nil {
		t.Fatal(err)
	}
	if err := h.rawExec(`UPDATE experiments SET started_at = now() - interval '120 days', stopped_at = now() - interval '10 days' WHERE id = $1::uuid`, recentStopped); err != nil {
		t.Fatal(err)
	}

	for _, e := range []struct{ id, tag string }{{exp, "run"}, {oldStopped, "old"}, {recentStopped, "rec"}} {
		x.seed(e.id, e.tag+"-1", "$exposure", "on", "1 second", nil)
		x.seed(e.id, e.tag+"-1", "e", "", "2 seconds", nil)
		x.seed(e.id, e.tag+"-fresh", "$exposure", "off", "1 second", nil) // stays fresh
		h.ageEvents("150 days", e.tag+"-1")
	}
	if err := h.rawExec(`UPDATE events SET occurred_at = now() WHERE subject_key LIKE '%-fresh'`); err != nil {
		t.Fatal(err)
	}
	// an event of no experiment, old and fresh
	h.rawExec(`INSERT INTO events (workspace_id, environment_id, event_id, event_name, subject_key, occurred_at)
		VALUES ($1::uuid, $2::uuid, 'orphan-old', 'x', 'orphan-old', now() - interval '200 days'),
		       ($1::uuid, $2::uuid, 'orphan-new', 'x', 'orphan-new', now())`, x.wsA.ID, x.devA)

	before := h.eventRows(`true`)
	dry, err := eventpurge.Purge(t.Context(), h.pool, eventpurge.Options{DryRun: true})
	if err != nil || h.eventRows(`true`) != before {
		t.Fatalf("a dry run must delete nothing: %v", err)
	}
	st, err := eventpurge.Purge(t.Context(), h.pool, eventpurge.Options{BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	// deleted: both old events of the long-stopped experiment, and the old orphan
	if st.Deleted != 3 || dry.Deleted != 3 {
		t.Errorf("deleted %d (dry run said %d), want 3", st.Deleted, dry.Deleted)
	}
	if st.Batches < 2 {
		t.Errorf("batch size 2 with 3 rows should take at least 2 batches, took %d", st.Batches)
	}
	count := func(subject string) int { return h.eventRows(`subject_key = $1`, subject) }
	for subject, want := range map[string]int{
		"run-1":      2, // running experiment: its old exposure and event survive
		"rec-1":      2, // stopped 10 days ago: inside the window
		"old-1":      0, // stopped 200 days ago and older than the window: gone
		"orphan-old": 0, // belongs to no experiment: gone
		"orphan-new": 1, // recent: stays
		"old-fresh":  1, "run-fresh": 1, "rec-fresh": 1,
	} {
		if got := count(subject); got != want {
			t.Errorf("%s: %d rows, want %d", subject, got, want)
		}
	}
	// Running it again deletes nothing more.
	if again, err := eventpurge.Purge(t.Context(), h.pool, eventpurge.Options{}); err != nil || again.Deleted != 0 {
		t.Errorf("second run: %+v %v", again, err)
	}
	if strings.Contains(logs.String(), "run-1") || strings.Contains(logs.String(), "orphan") {
		t.Error("subject keys reached the logs")
	}
	if _, err := eventpurge.Purge(t.Context(), h.pool, eventpurge.Options{Retention: time.Hour}); err == nil {
		t.Error("a retention below the minimum must be refused")
	}
}

func TestConcurrentPurgesDoNotDoubleDeleteOrDeadlock(t *testing.T) {
	x := newTenants(t)
	h := x.h
	exp := x.backdated()
	h.expect(200, x.a, "POST", "/environments/"+x.devA+"/experiments/"+expKey+"/stop", nil)
	if err := h.rawExec(`UPDATE experiments SET started_at = now() - interval '300 days', stopped_at = now() - interval '200 days' WHERE id = $1::uuid`, exp); err != nil {
		t.Fatal(err)
	}
	const n = 6000
	if err := h.rawExec(`
		INSERT INTO events (workspace_id, environment_id, experiment_id, event_id, event_name, subject_key, variation_id, occurred_at)
		SELECT $1::uuid, $2::uuid, $3::uuid, 'bulk-' || g, 'e', 'bulk-' || g, NULL, now() - interval '150 days' - g * interval '1 second'
		FROM generate_series(1, $4::int) g`, x.wsA.ID, x.devA, exp, n); err != nil {
		t.Fatal(err)
	}
	if got := h.eventRows(`subject_key LIKE 'bulk-%'`); got != n {
		t.Fatalf("seeded %d", got)
	}

	const runners = 4
	var wg sync.WaitGroup
	deleted := make([]int64, runners)
	errs := make([]error, runners)
	for i := 0; i < runners; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, err := eventpurge.Purge(context.Background(), h.pool, eventpurge.Options{BatchSize: 250})
			deleted[i], errs[i] = st.Deleted, err
		}(i)
	}
	wg.Wait()
	var total int64
	for i := range deleted {
		if errs[i] != nil {
			t.Errorf("runner %d: %v", i, errs[i])
		}
		total += deleted[i]
	}
	// Re-run until a pass finds nothing: a runner may have skipped rows another held locked.
	for {
		st, err := eventpurge.Purge(context.Background(), h.pool, eventpurge.Options{BatchSize: 250})
		if err != nil {
			t.Fatal(err)
		}
		total += st.Deleted
		if st.Deleted == 0 {
			break
		}
	}
	if total != n {
		t.Errorf("%d rows deleted in total across runners, want exactly %d (%v)", total, n, deleted)
	}
	if left := h.eventRows(`subject_key LIKE 'bulk-%'`); left != 0 {
		t.Errorf("%d rows left", left)
	}
	_ = fmt.Sprint
}

// A row another run (or anything else) holds locked is skipped, not waited
// for: this is what lets two purges overlap without blocking or deadlocking.
func TestPurgeSkipsRowsLockedByAnotherTransaction(t *testing.T) {
	x := newTenants(t)
	h := x.h
	exp := x.backdated()
	h.expect(200, x.a, "POST", "/environments/"+x.devA+"/experiments/"+expKey+"/stop", nil)
	if err := h.rawExec(`UPDATE experiments SET started_at = now() - interval '300 days', stopped_at = now() - interval '200 days' WHERE id = $1::uuid`, exp); err != nil {
		t.Fatal(err)
	}
	if err := h.rawExec(`
		INSERT INTO events (workspace_id, environment_id, experiment_id, event_id, event_name, subject_key, occurred_at)
		SELECT $1::uuid, $2::uuid, $3::uuid, 'lk-' || g, 'e', 'lk-' || g, now() - interval '150 days'
		FROM generate_series(1, 20) g`, x.wsA.ID, x.devA, exp); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id int64
	if err := tx.QueryRow(ctx, `SELECT id FROM events WHERE subject_key = 'lk-1' FOR UPDATE`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	start := time.Now()
	st, err := eventpurge.Purge(runCtx, h.pool, eventpurge.Options{})
	if err != nil {
		t.Fatalf("a purge must not wait for a locked row: %v after %v", err, time.Since(start))
	}
	if st.Deleted != 19 {
		t.Errorf("deleted %d, want 19 (one row is locked)", st.Deleted)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if st, err := eventpurge.Purge(ctx, h.pool, eventpurge.Options{}); err != nil || st.Deleted != 1 {
		t.Errorf("after the lock is released: %+v %v", st, err)
	}
}
