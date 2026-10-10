package track

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/platform/apikey"
)

// Writer stores validated events. It is the seam where a queue-backed writer
// (publish the batch, insert from a worker) would replace the direct insert:
// the handler only needs to know which event ids were new.
type Writer interface {
	// Insert stores the batch in the key's own workspace and environment and
	// returns the ids that were newly stored. An id missing from the result was
	// already there (a retry), or was an exposure the subject already had.
	Insert(ctx context.Context, sc apikey.Scope, batch []Event) (inserted map[string]bool, err error)
}

// Experiment is what ingestion needs to know about an experiment.
type Experiment struct {
	Running    bool
	Variations map[string]bool
}

// ExperimentLookup finds experiments by id inside one environment.
type ExperimentLookup interface {
	// Lookup returns only experiments of the key's own workspace AND
	// environment; any other id is simply absent.
	Lookup(ctx context.Context, sc apikey.Scope, ids []string) (map[string]Experiment, error)
}

// PgStore implements both against Postgres.
type PgStore struct{ pool *pgxpool.Pool }

func NewPgStore(pool *pgxpool.Pool) *PgStore { return &PgStore{pool: pool} }

func (s *PgStore) Lookup(ctx context.Context, sc apikey.Scope, ids []string) (map[string]Experiment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT x.id::text, x.status = 'running', f.variations::text
		FROM experiments x
		JOIN flags f ON f.id = x.flag_id AND f.workspace_id = x.workspace_id
		WHERE x.workspace_id = $1::uuid AND x.environment_id = $2::uuid AND x.id = ANY($3::uuid[])`,
		sc.WorkspaceID, sc.EnvironmentID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Experiment{}
	for rows.Next() {
		var id, variations string
		var running bool
		if err := rows.Scan(&id, &running, &variations); err != nil {
			return nil, err
		}
		var vs []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(variations), &vs); err != nil {
			return nil, err
		}
		e := Experiment{Running: running, Variations: map[string]bool{}}
		for _, v := range vs {
			e.Variations[v.ID] = true
		}
		out[id] = e
	}
	return out, rows.Err()
}

// Insert writes the whole batch with ONE statement. ON CONFLICT DO NOTHING
// (no target) covers both unique indexes: the client event id, and the single
// exposure per (workspace, environment, experiment, subject). The first
// writer wins; RETURNING says which rows were really new.
func (s *PgStore) Insert(ctx context.Context, sc apikey.Scope, batch []Event) (map[string]bool, error) {
	n := len(batch)
	ids, names, subjects := make([]string, n), make([]string, n), make([]string, n)
	exps, vars, props := make([]*string, n), make([]*string, n), make([]*string, n)
	vals := make([]*float64, n)
	times := make([]time.Time, n)
	for i, e := range batch {
		ids[i], names[i], subjects[i], times[i] = e.ID, e.Name, e.SubjectKey, e.OccurredAt
		if e.ExperimentID != "" {
			exps[i] = &batch[i].ExperimentID
		}
		if e.VariationID != "" {
			vars[i] = &batch[i].VariationID
		}
		if e.Properties != "" {
			props[i] = &batch[i].Properties
		}
		vals[i] = e.Value
	}
	rows, err := s.pool.Query(ctx, `
		INSERT INTO events (workspace_id, environment_id, event_id, event_name, subject_key,
		                    experiment_id, variation_id, value, properties, occurred_at)
		SELECT $1::uuid, $2::uuid, t.event_id, t.name, t.subject, t.exp::uuid, t.var, t.val, t.props::jsonb, t.ts
		FROM unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::float8[], $9::text[], $10::timestamptz[])
		     WITH ORDINALITY AS t(event_id, name, subject, exp, var, val, props, ts, ord)
		ORDER BY t.ord
		ON CONFLICT DO NOTHING
		RETURNING event_id`,
		sc.WorkspaceID, sc.EnvironmentID, ids, names, subjects, exps, vars, vals, props, times)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inserted := make(map[string]bool, n)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		inserted[id] = true
	}
	return inserted, rows.Err()
}
