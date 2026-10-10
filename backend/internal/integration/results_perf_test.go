package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"helios/backend/internal/controlplane/results"
)

// Performance sanity, not a benchmark. Opt in with HELIOS_RESULTS_PERF=1:
//
//	HELIOS_RESULTS_PERF=1 HELIOS_TEST_DATABASE_URL=... go test -v -run ResultsPerformance ./internal/integration
//
// It seeds one million events for one experiment (250,000 exposed subjects
// split 50/50, a 10% / 12% conversion, plus other events), prints the plan of
// the aggregation and the endpoint latency, and fails if the plan scans the
// whole table instead of using the events indexes.
func TestResultsPerformance(t *testing.T) {
	if os.Getenv("HELIOS_RESULTS_PERF") == "" {
		t.Skip("set HELIOS_RESULTS_PERF=1 to seed a million events")
	}
	x := newTenants(t)
	h := x.h
	exp := x.backdated()
	ctx := context.Background()

	// An unrelated tenant's events share the table, so the plan must filter by workspace.
	t0 := time.Now()
	if err := h.rawExec(`
		INSERT INTO events (workspace_id, environment_id, experiment_id, event_id, event_name, subject_key, variation_id, occurred_at)
		SELECT x.workspace_id, x.environment_id, x.id, 'x-' || g, '$exposure', 's' || g,
		       CASE WHEN g % 2 = 0 THEN 'on' ELSE 'off' END, x.started_at + interval '1 minute' + (g % 100000) * interval '1 second'
		FROM experiments x, generate_series(1, 250000) g WHERE x.id = $1::uuid`, exp); err != nil {
		t.Fatal(err)
	}
	// conversions: 10% of "on" (even g), 12% of "off"; the rest is noise events of other names
	if err := h.rawExec(`
		INSERT INTO events (workspace_id, environment_id, experiment_id, event_id, event_name, subject_key, value, occurred_at)
		SELECT x.workspace_id, x.environment_id, x.id, 'c-' || g, 'e', 's' || g, (g % 50)::float8, x.started_at + interval '2 minutes' + (g % 100000) * interval '1 second'
		FROM experiments x, generate_series(1, 250000) g
		WHERE x.id = $1::uuid AND ((g % 2 = 0 AND g % 10 = 0) OR (g % 2 = 1 AND g % 25 < 3))`, exp); err != nil {
		t.Fatal(err)
	}
	if err := h.rawExec(`
		INSERT INTO events (workspace_id, environment_id, experiment_id, event_id, event_name, subject_key, occurred_at)
		SELECT x.workspace_id, x.environment_id, x.id, 'n-' || g, 'page_view', 's' || (g % 250000 + 1), x.started_at + interval '3 minutes' + (g % 100000) * interval '1 second'
		FROM experiments x, generate_series(1, 650000) g WHERE x.id = $1::uuid`, exp); err != nil {
		t.Fatal(err)
	}
	if err := h.rawExec(`ANALYZE events`); err != nil {
		t.Fatal(err)
	}
	total := h.eventRows(`true`)
	t.Logf("seeded %d events in %v", total, time.Since(t0).Round(time.Millisecond))

	start, end := time.Now().Add(-4*24*time.Hour), time.Now()
	rows, err := h.pool.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF) `+results.MetricSumsSQL,
		x.wsA.ID, exp, "e", start, end, results.ExposureEvent)
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var line string
		_ = rows.Scan(&line)
		plan = append(plan, line)
	}
	rows.Close()
	p := strings.Join(plan, "\n")
	t.Logf("plan for the aggregation over %d events:\n%s", total, p)
	if strings.Contains(p, "Seq Scan on events") {
		t.Error("the aggregation scans the whole events table")
	}

	// Endpoint latency. Different metric strings avoid the 5 s cache.
	path := "/environments/" + x.devA + "/experiments/" + expKey + "/results"
	for _, q := range []string{"?metrics=e:conversion", "?metrics=e:value", "?metrics=page_view:count", "?metrics=e:conversion,e:count,page_view:conversion"} {
		t1 := time.Now()
		h.expect(200, x.a, "GET", path+q, nil)
		t.Logf("GET results%-52s %v (uncached)", q, time.Since(t1).Round(time.Millisecond))
	}
	t1 := time.Now()
	h.expect(200, x.a, "GET", path+"?metrics=e:conversion", nil)
	t.Logf("GET results?metrics=e:conversion again, within 5 s: %v (cache)", time.Since(t1).Round(time.Millisecond))

	r := x.results(x.a, x.devA, expKey, "?metrics=e:conversion"[1:])
	on, off := variation(r, 0, "on"), variation(r, 0, "off")
	t.Logf("exposures on=%d off=%d, conversions on=%d off=%d, comparison: %s", on.Exposures, off.Exposures, on.ConvertingSubjects, off.ConvertingSubjects, r.Metrics[0].Comparisons[0].Status)
}
