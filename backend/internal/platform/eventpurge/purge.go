// Package eventpurge deletes events older than the retention window, in small
// batches, without ever reading or logging an event's content.
//
// What is kept regardless of age: every event of an experiment that is running,
// or that stopped inside the retention window. Results need the exposures (the
// denominators) AND the events they are compared with; keeping only one side
// would silently bias a running experiment. Events of experiments stopped
// before the window, and events that belong to no experiment, are deleted once
// they are older than the window.
//
// Safe to repeat and to run concurrently: each batch locks the rows it deletes
// with FOR UPDATE SKIP LOCKED, so two runs never wait on, double-delete or
// deadlock over the same rows.
package eventpurge

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// DefaultRetention matches the ingestion window (track.Retention).
	DefaultRetention = 90 * 24 * time.Hour
	// MinRetention is a guard against a typo: /track accepts events up to 90
	// days old, so retaining less than a week would purge fresh data.
	MinRetention     = 7 * 24 * time.Hour
	DefaultBatchSize = 5000
	MaxBatchSize     = 50000
)

// Options controls one run.
type Options struct {
	Retention time.Duration // zero means DefaultRetention
	BatchSize int           // zero means DefaultBatchSize
	// Now is the clock; zero means time.Now().
	Now time.Time
	// MaxBatches stops the run after this many batches in total (0: no limit),
	// so a scheduled run is bounded.
	MaxBatches int
	// Pause between batches, to leave room for other writers.
	Pause time.Duration
	// DryRun only counts what would be deleted.
	DryRun bool
}

// Stats are counts only: no subject key, event name or id ever leaves here.
type Stats struct {
	Cutoff       time.Time
	Environments int
	Batches      int
	Deleted      int64 // rows deleted (or, for a dry run, rows that would be)
	Truncated    bool  // stopped at MaxBatches with work left
}

func (o Options) normalised() (Options, error) {
	if o.Retention == 0 {
		o.Retention = DefaultRetention
	}
	if o.Retention < MinRetention {
		return o, fmt.Errorf("retention %v is below the minimum %v", o.Retention, MinRetention)
	}
	if o.BatchSize == 0 {
		o.BatchSize = DefaultBatchSize
	}
	if o.BatchSize < 1 || o.BatchSize > MaxBatchSize {
		return o, fmt.Errorf("batch size must be 1-%d", MaxBatchSize)
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	return o, nil
}

// eligible is the predicate shared by the dry run and the delete: events older
// than the cutoff whose experiment (if any) is neither running nor stopped
// inside the window.
const eligible = `
	e.workspace_id = $1::uuid AND e.environment_id = $2::uuid AND e.occurred_at < $3
	AND NOT EXISTS (
		SELECT 1 FROM experiments x
		WHERE x.id = e.experiment_id AND x.workspace_id = e.workspace_id
		  AND (x.status = 'running' OR x.stopped_at >= $3))`

// Purge runs one pass over every environment. It returns the counts so far
// together with any error.
func Purge(ctx context.Context, pool *pgxpool.Pool, opts Options) (Stats, error) {
	opts, err := opts.normalised()
	if err != nil {
		return Stats{}, err
	}
	st := Stats{Cutoff: opts.Now.Add(-opts.Retention)}

	// Environments are walked one at a time so every batch is a range scan of
	// idx_events_time (workspace_id, environment_id, occurred_at).
	rows, err := pool.Query(ctx, `SELECT workspace_id::text, id::text FROM environments ORDER BY workspace_id, id`)
	if err != nil {
		return st, err
	}
	type env struct{ workspace, id string }
	var envs []env
	for rows.Next() {
		var e env
		if err := rows.Scan(&e.workspace, &e.id); err != nil {
			rows.Close()
			return st, err
		}
		envs = append(envs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	st.Environments = len(envs)

	for _, e := range envs {
		if opts.DryRun {
			var n int64
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM events e WHERE`+eligible, e.workspace, e.id, st.Cutoff).Scan(&n); err != nil {
				return st, err
			}
			st.Deleted += n
			continue
		}
		for {
			if err := ctx.Err(); err != nil {
				return st, err
			}
			if opts.MaxBatches > 0 && st.Batches >= opts.MaxBatches {
				st.Truncated = true
				return st, nil
			}
			tag, err := pool.Exec(ctx, `
				DELETE FROM events WHERE id IN (
					SELECT e.id FROM events e WHERE`+eligible+`
					ORDER BY e.occurred_at
					LIMIT $4
					FOR UPDATE OF e SKIP LOCKED)`,
				e.workspace, e.id, st.Cutoff, opts.BatchSize)
			if err != nil {
				return st, err
			}
			n := tag.RowsAffected()
			st.Deleted += n
			if n > 0 {
				st.Batches++
			}
			if n < int64(opts.BatchSize) {
				break // nothing (more) eligible here, or the rest is locked by another run
			}
			if opts.Pause > 0 {
				select {
				case <-ctx.Done():
					return st, ctx.Err()
				case <-time.After(opts.Pause):
				}
			}
		}
	}
	return st, nil
}
