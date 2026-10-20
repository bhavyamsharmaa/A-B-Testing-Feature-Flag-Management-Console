package results

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// queryTimeout bounds every results read. A dashboard calls this every few
// seconds, so a runaway plan must fail fast instead of piling up.
const queryTimeout = "5s"

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// experimentRow is what the results need to know about an experiment.
type experimentRow struct {
	ID          string
	Key         string
	Status      string
	StartedAt   *time.Time
	StoppedAt   *time.Time
	StartConfig *startConfig
	FlagID      string
	FlagKey     string
	Variations  []string // flag variation ids, in the flag's order
	Metrics     []MetricSpec
}

// startConfig is experiments.start_config: the assignment-affecting config of
// the flag when the experiment started.
type startConfig struct {
	TargetingRules         json.RawMessage   `json:"targetingRules"`
	Rollout                map[string]uint32 `json:"rollout"`
	FallthroughVariationID string            `json:"fallthroughVariationId"`
	Version                int64             `json:"version"`
}

func (c *startConfig) hasTargeting() bool {
	var rules []json.RawMessage
	return c != nil && json.Unmarshal(c.TargetingRules, &rules) == nil && len(rules) > 0
}

func loadExperiment(ctx context.Context, q querier, workspaceID, envID, key string) (experimentRow, error) {
	var x experimentRow
	var start, variations *string
	err := q.QueryRow(ctx, `
		SELECT x.id::text, x.key, x.status::text, x.started_at, x.stopped_at, x.start_config::text,
		       f.id::text, f.key, f.variations::text
		FROM experiments x JOIN flags f ON f.id = x.flag_id AND f.workspace_id = x.workspace_id
		WHERE x.workspace_id = $1::uuid AND x.environment_id = $2::uuid AND x.key = $3`,
		workspaceID, envID, key).Scan(&x.ID, &x.Key, &x.Status, &x.StartedAt, &x.StoppedAt, &start, &x.FlagID, &x.FlagKey, &variations)
	if err != nil {
		return x, err
	}
	if start != nil {
		var c startConfig
		if err := json.Unmarshal([]byte(*start), &c); err != nil {
			return x, fmt.Errorf("start_config: %w", err)
		}
		x.StartConfig = &c
	}
	var vs []struct {
		ID string `json:"id"`
	}
	if variations != nil {
		if err := json.Unmarshal([]byte(*variations), &vs); err != nil {
			return x, fmt.Errorf("variations: %w", err)
		}
	}
	for _, v := range vs {
		x.Variations = append(x.Variations, v.ID)
	}
	// The designated control (experiment_variations, migration 0014) goes first: results compare every other
	// variation with Variations[0]. The others keep the flag's order. With no designation (an experiment that
	// predates the table and was somehow missed by its backfill) the flag's first variation is the control, as before.
	var control string
	err = q.QueryRow(ctx, `SELECT variation_id FROM experiment_variations WHERE experiment_id = $1::uuid AND is_control`, x.ID).Scan(&control)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return x, err
	}
	x.Variations = controlFirst(x.Variations, control)
	rows, err := q.Query(ctx, `
		SELECT name, event_name, type::text, is_primary FROM experiment_metrics
		WHERE experiment_id = $1::uuid ORDER BY is_primary DESC, name`, x.ID)
	if err != nil {
		return x, err
	}
	defer rows.Close()
	for rows.Next() {
		var m MetricSpec
		if err := rows.Scan(&m.Name, &m.EventName, &m.Kind, &m.IsPrimary); err != nil {
			return x, err
		}
		x.Metrics = append(x.Metrics, m)
	}
	return x, rows.Err()
}

// exposureCounts returns the number of exposed subjects per variation id.
func exposureCounts(ctx context.Context, q querier, workspaceID, expID string) (map[string]int64, error) {
	rows, err := q.Query(ctx, `
		SELECT variation_id, count(*) FROM events
		WHERE workspace_id = $1::uuid AND experiment_id = $2::uuid AND event_name = $3
		GROUP BY variation_id`, workspaceID, expID, ExposureEvent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var v string
		var n int64
		if err := rows.Scan(&v, &n); err != nil {
			return nil, err
		}
		out[v] = n
	}
	return out, rows.Err()
}

// The clock rule (docs/RESULTS.md, "Clock skew"). A client's timestamp
// (occurred_at) can be wrong; the server's receipt time (created_at, the time
// the batch was stored) cannot, and an event cannot have happened after it was
// received nor, for this experiment, before the experiment started. So:
//
//   - the run window is checked against the RECEIPT time only;
//   - "after the exposure" compares the two client times after pulling each
//     into [started_at, received_at], the only range in which it can be true;
//     when that leaves them equal, the raw client times decide (a client whose
//     clock is far off still orders its own events correctly);
//   - events stored by the same request as the exposure get one second of grace,
//     since a batch is one moment and client stamps within it race.
//
// $4::timestamptz is the window start in every fragment below.
const (
	effTimeExposure = `LEAST(GREATEST(x.occurred_at, $4::timestamptz), x.created_at)`
	effTimeEvent    = `LEAST(GREATEST(e.occurred_at, $4::timestamptz), e.created_at)`
	afterExposure   = `(` + effTimeEvent + ` > ` + effTimeExposure + `
		          OR (` + effTimeEvent + ` = ` + effTimeExposure + ` AND e.occurred_at >= x.occurred_at)
		          OR (e.created_at = x.created_at AND e.occurred_at + interval '1 second' >= x.occurred_at))`
)

// MetricSumsSQL is the per-variation aggregation of one event name; exported
// so its plan can be inspected (EXPLAIN) by tests and tooling. Arguments:
// workspace, experiment, event name, window start, window end, exposure event.
const MetricSumsSQL = `
		WITH exposed AS (
			SELECT x.subject_key, x.variation_id, x.occurred_at, x.created_at
			FROM events x
			WHERE x.workspace_id = $1::uuid AND x.experiment_id = $2::uuid AND x.event_name = $6
		), per_subject AS (
			SELECT x.variation_id, count(e.id) AS n, COALESCE(sum(e.value), 0) AS s
			FROM exposed x
			LEFT JOIN events e
			       ON e.workspace_id = $1::uuid AND e.experiment_id = $2::uuid
			      AND e.subject_key = x.subject_key AND e.event_name = $3
			      AND e.created_at >= $4::timestamptz AND e.created_at <= $5::timestamptz
			      AND ` + afterExposure + `
			GROUP BY x.variation_id, x.subject_key
		)
		SELECT variation_id,
		       count(*),
		       count(*) FILTER (WHERE n > 0),
		       COALESCE(sum(n), 0)::bigint,
		       COALESCE(sum(n::float8 * n), 0),
		       COALESCE(sum(s), 0)::float8,
		       COALESCE(sum(s * s), 0)::float8
		FROM per_subject GROUP BY variation_id`

// armSums are the per-variation sufficient statistics of one event name.
type armSums struct {
	Subjects   int64   // exposed subjects
	Converters int64   // subjects with >= 1 matching event
	Events     int64   // sum of n
	SumN2      float64 // sum of n^2   (count metric: second moment)
	SumV       float64 // sum of s     (value metric: per-subject sum of values)
	SumV2      float64 // sum of s^2
}

// metricSums aggregates one event name over the exposed subjects, per variation.
// Each subject contributes n (their matching events) and s (the sum of those
// events' values), counting only events at or after the subject's own exposure
// and inside [start, end]. The aggregation is a join of the experiment's
// exposures to its events on (workspace, experiment, subject), which is the
// shape idx_events_subject serves.
func metricSums(ctx context.Context, q querier, workspaceID, expID, eventName string, start, end time.Time) (map[string]armSums, error) {
	rows, err := q.Query(ctx, MetricSumsSQL,
		workspaceID, expID, eventName, start, end, ExposureEvent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]armSums{}
	for rows.Next() {
		var v string
		var a armSums
		if err := rows.Scan(&v, &a.Subjects, &a.Converters, &a.Events, &a.SumN2, &a.SumV, &a.SumV2); err != nil {
			return nil, err
		}
		out[v] = a
	}
	return out, rows.Err()
}

// attribution counts the events of one name that the metric did NOT use (and
// how many of the used ones had a client clock the rule had to correct).
type attribution struct {
	NoExposure     int64 // from subjects with no exposure at all
	BeforeExposure int64 // before the subject's own exposure, by the clock rule
	OutsideWindow  int64 // received before the start or after the stop
	ClockAdjusted  int64 // counted, but the client's timestamp was more than a minute outside [started_at, received_at]
}

func droppedEvents(ctx context.Context, q querier, workspaceID, expID, eventName string, start, end time.Time) (attribution, error) {
	var a attribution
	err := q.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE x.subject_key IS NULL),
		       count(*) FILTER (WHERE x.subject_key IS NOT NULL AND e.created_at >= $4::timestamptz AND e.created_at <= $5::timestamptz
		                          AND NOT `+afterExposure+`),
		       count(*) FILTER (WHERE x.subject_key IS NOT NULL AND (e.created_at < $4::timestamptz OR e.created_at > $5::timestamptz)),
		       count(*) FILTER (WHERE x.subject_key IS NOT NULL AND e.created_at >= $4::timestamptz AND e.created_at <= $5::timestamptz
		                          AND `+afterExposure+`
		                          AND (e.occurred_at < $4::timestamptz - interval '1 minute' OR e.occurred_at > e.created_at + interval '1 minute'))
		FROM events e
		LEFT JOIN events x
		       ON x.workspace_id = e.workspace_id AND x.experiment_id = e.experiment_id
		      AND x.subject_key = e.subject_key AND x.event_name = $6
		WHERE e.workspace_id = $1::uuid AND e.experiment_id = $2::uuid AND e.event_name = $3`,
		workspaceID, expID, eventName, start, end, ExposureEvent).Scan(&a.NoExposure, &a.BeforeExposure, &a.OutsideWindow, &a.ClockAdjusted)
	return a, err
}

// eventExists reports whether the experiment has at least one event with this name.
func eventExists(ctx context.Context, q querier, workspaceID, expID, eventName string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM events WHERE workspace_id = $1::uuid AND experiment_id = $2::uuid AND event_name = $3)`,
		workspaceID, expID, eventName).Scan(&ok)
	return ok, err
}

// flagChange is one audit entry that changed the flag during the run.
type flagChange struct {
	At      time.Time
	Actor   string
	Action  string
	Changed []string
}

// flagChanges lists the flag's config changes inside [start, end], read from
// the audit log (flag.update carries the whole before/after config).
func flagChanges(ctx context.Context, q querier, workspaceID, envID, flagKey string, start, end time.Time) ([]flagChange, error) {
	rows, err := q.Query(ctx, `
		SELECT created_at, actor_email, action, diff_before::text, diff_after::text
		FROM audit_logs
		WHERE workspace_id = $1::uuid AND environment_id = $2::uuid
		  AND resource_type = 'flag' AND resource_id = $3
		  AND action IN ('flag.update', 'flag.kill')
		  AND created_at >= $4::timestamptz AND created_at <= $5::timestamptz
		ORDER BY created_at
		LIMIT 50`, workspaceID, envID, flagKey, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []flagChange
	for rows.Next() {
		var c flagChange
		var before, after *string
		if err := rows.Scan(&c.At, &c.Actor, &c.Action, &before, &after); err != nil {
			return nil, err
		}
		c.Changed = changedFields(before, after)
		out = append(out, c)
	}
	return out, rows.Err()
}

type otherRun struct {
	Key       string
	StartedAt time.Time
	StoppedAt *time.Time
}

// otherRuns are other experiments on the same flag and environment that ran
// before or overlapping this one (stopped is terminal, so a "restart" is a new
// experiment on the same flag).
func otherRuns(ctx context.Context, q querier, workspaceID, envID, flagID, expID string) ([]otherRun, error) {
	rows, err := q.Query(ctx, `
		SELECT key, started_at, stopped_at FROM experiments
		WHERE workspace_id = $1::uuid AND environment_id = $2::uuid AND flag_id = $3::uuid
		  AND id <> $4::uuid AND started_at IS NOT NULL
		ORDER BY started_at LIMIT 20`, workspaceID, envID, flagID, expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []otherRun
	for rows.Next() {
		var r otherRun
		if err := rows.Scan(&r.Key, &r.StartedAt, &r.StoppedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// changedFields names which of the assignment-relevant fields differ between two audit snapshots.
func changedFields(before, after *string) []string {
	if before == nil || after == nil {
		return nil
	}
	var b, a map[string]json.RawMessage
	if json.Unmarshal([]byte(*before), &b) != nil || json.Unmarshal([]byte(*after), &a) != nil {
		return nil
	}
	var out []string
	for _, f := range []string{"targetingRules", "rollout", "fallthroughVariationId", "enabled", "variations"} {
		if !jsonSame(b[f], a[f]) {
			out = append(out, f)
		}
	}
	return out
}

func jsonSame(x, y json.RawMessage) bool {
	var a, b any
	_ = json.Unmarshal(x, &a)
	_ = json.Unmarshal(y, &b)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// unverifiedExposures counts the exposures that were accepted although their
// exposure token did not verify (EXPOSURE_VERIFICATION=report). Uses the
// partial index idx_events_unverified_exposures.
func unverifiedExposures(ctx context.Context, q querier, workspaceID, expID string) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM events
		WHERE workspace_id = $1::uuid AND experiment_id = $2::uuid AND exposure_verified = false`,
		workspaceID, expID).Scan(&n)
	return n, err
}

// controlFirst returns ids with the control moved to the front, the rest in their order. An unknown or empty
// control leaves ids as they are.
func controlFirst(ids []string, control string) []string {
	if control == "" {
		return ids
	}
	out := make([]string, 0, len(ids))
	found := false
	for _, id := range ids {
		if id == control {
			found = true
		}
	}
	if !found {
		return ids
	}
	out = append(out, control)
	for _, id := range ids {
		if id != control {
			out = append(out, id)
		}
	}
	return out
}
