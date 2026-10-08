package experiments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/httpx"
)

type Handlers struct {
	pool *pgxpool.Pool
}

func NewHandlers(pool *pgxpool.Pool) *Handlers {
	return &Handlers{pool: pool}
}

type metricView struct {
	Name      string `json:"name"`
	EventName string `json:"eventName"`
	Type      string `json:"type"`
	IsPrimary bool   `json:"isPrimary"`
}

type experimentView struct {
	ID          string       `json:"id"`
	Key         string       `json:"key"`
	Name        string       `json:"name"`
	Hypothesis  string       `json:"hypothesis"`
	FlagKey     string       `json:"flagKey"`
	Environment string       `json:"environment"`
	Status      Status       `json:"status"`
	StartedAt   *time.Time   `json:"startedAt"`
	StoppedAt   *time.Time   `json:"stoppedAt"`
	Metrics     []metricView `json:"metrics"`
	// ConfigChangedSinceStart is true when the flag's targeting, rollout or
	// fallthrough in this environment differs from when the experiment
	// started (or, once stopped, from when it stopped). Informational only.
	ConfigChangedSinceStart bool      `json:"configChangedSinceStart"`
	CreatedAt               time.Time `json:"createdAt"`
}

// The live flag config is joined in so drift can be computed on read. JSONB
// columns are read as ::text, as in the flags package.
const selectExperiment = `
	SELECT x.id::text, x.key, x.name, COALESCE(x.hypothesis, ''), f.key, x.status::text,
	       x.started_at, x.stopped_at, x.created_at, x.start_config::text, x.stop_config::text,
	       fc.targeting_rules::text, COALESCE(fc.rollout::text, 'null'), fc.fallthrough_variation_id, fc.version
	FROM experiments x
	JOIN flags f ON f.id = x.flag_id
	JOIN flag_configs fc ON fc.flag_id = x.flag_id AND fc.environment_id = x.environment_id
	WHERE x.environment_id = $1::uuid`

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scanExperiment(row pgx.Row, envKey string) (experimentView, error) {
	var v experimentView
	var startText, stopText *string
	var live configSnapshot
	var rules, rollout string
	if err := row.Scan(&v.ID, &v.Key, &v.Name, &v.Hypothesis, &v.FlagKey, &v.Status,
		&v.StartedAt, &v.StoppedAt, &v.CreatedAt, &startText, &stopText,
		&rules, &rollout, &live.FallthroughVariationID, &live.Version); err != nil {
		return v, err
	}
	live.TargetingRules, live.Rollout = json.RawMessage(rules), json.RawMessage(rollout)
	start, err := parseSnapshot(startText)
	if err != nil {
		return v, err
	}
	stop, err := parseSnapshot(stopText)
	if err != nil {
		return v, err
	}
	v.Environment = envKey
	v.Metrics = []metricView{}
	v.ConfigChangedSinceStart = configChangedSinceStart(v.Status, start, stop, live)
	return v, nil
}

func parseSnapshot(text *string) (*configSnapshot, error) {
	if text == nil {
		return nil, nil
	}
	var s configSnapshot
	if err := json.Unmarshal([]byte(*text), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// attachMetrics fills Metrics on views. where is a fragment written in this
// file (never user input) selecting experiment_metrics rows.
func attachMetrics(ctx context.Context, q querier, views []experimentView, where string, args ...any) error {
	rows, err := q.Query(ctx, `
		SELECT experiment_id::text, name, event_name, type::text, is_primary
		FROM experiment_metrics WHERE `+where+` ORDER BY is_primary DESC, name`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	byExperiment := map[string][]metricView{}
	for rows.Next() {
		var id string
		var m metricView
		if err := rows.Scan(&id, &m.Name, &m.EventName, &m.Type, &m.IsPrimary); err != nil {
			return err
		}
		byExperiment[id] = append(byExperiment[id], m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range views {
		if ms := byExperiment[views[i].ID]; ms != nil {
			views[i].Metrics = ms
		}
	}
	return nil
}

// loadOne returns the experiment with its metrics.
func loadOne(ctx context.Context, q querier, env rbac.Environment, key string) (experimentView, error) {
	v, err := scanExperiment(q.QueryRow(ctx, selectExperiment+` AND x.key = $2`, env.ID, key), env.Key)
	if err != nil {
		return v, err
	}
	views := []experimentView{v}
	if err := attachMetrics(ctx, q, views, `experiment_id = $1::uuid`, v.ID); err != nil {
		return v, err
	}
	return views[0], nil
}

type (
	errNotFound          struct{}
	errFlagNotFound      struct{}
	errFlagDisabled      struct{}
	errInvalidTransition struct{ from, to Status }
	validationError      struct{ error }
)

func (errNotFound) Error() string     { return "experiment not found" }
func (errFlagNotFound) Error() string { return "flag not found" }
func (errFlagDisabled) Error() string { return "flag disabled" }
func (e errInvalidTransition) Error() string {
	return fmt.Sprintf("cannot go from %s to %s", e.from, e.to)
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

// isFlagDeletedViolation reports whether err is the foreign-key violation on
// experiments.flag_id: the flag was deleted between create's lookup and its
// insert.
func isFlagDeletedViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "experiments_flag_id_fkey"
}

// writeLifecycleError maps the errors shared by get, start and stop.
func writeLifecycleError(w http.ResponseWriter, r *http.Request, key string, err error) {
	var invalid errInvalidTransition
	switch {
	case errors.As(err, new(errNotFound)), errors.Is(err, pgx.ErrNoRows):
		httpx.WriteError(w, http.StatusNotFound, "EXPERIMENT_NOT_FOUND", "no experiment with key "+key)
	case errors.As(err, &invalid):
		msg := fmt.Sprintf("experiment is %s; ", invalid.from)
		if invalid.to == StatusRunning {
			msg += "only draft experiments can be started"
		} else {
			msg += "only running experiments can be stopped"
		}
		httpx.WriteError(w, http.StatusConflict, "INVALID_TRANSITION", msg)
	case errors.As(err, new(errFlagDisabled)):
		httpx.WriteError(w, http.StatusConflict, "FLAG_DISABLED", "enable the flag in this environment before starting its experiment")
	case isUniqueViolation(err, "one_running_per_flag_env"):
		httpx.WriteError(w, http.StatusConflict, "EXPERIMENT_ALREADY_RUNNING", "another experiment is already running on this flag in this environment; stop it first")
	default:
		httpx.WriteInternal(w, r, err)
	}
}

// Create handles POST /environments/{env}/experiments. The experiment starts
// as a draft and has no effect on evaluation.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	var req createRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if err := req.validate(); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}

	var view experimentView
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var flagID string
		var variationCount int
		if err := tx.QueryRow(ctx,
			`SELECT id::text, jsonb_array_length(variations) FROM flags WHERE key = $1`, req.FlagKey,
		).Scan(&flagID, &variationCount); errors.Is(err, pgx.ErrNoRows) {
			return errFlagNotFound{}
		} else if err != nil {
			return err
		}
		if err := checkVariationCount(req.FlagKey, variationCount); err != nil {
			return validationError{err}
		}

		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO experiments (environment_id, flag_id, key, name, hypothesis, created_by)
			VALUES ($1::uuid, $2::uuid, $3, $4, NULLIF($5, ''), $6::uuid)
			RETURNING id::text`,
			access.Env.ID, flagID, req.Key, req.Name, req.Hypothesis, access.User.ID,
		).Scan(&id); err != nil {
			return err
		}
		for _, m := range req.Metrics {
			if _, err := tx.Exec(ctx, `
				INSERT INTO experiment_metrics (experiment_id, name, event_name, type, is_primary)
				VALUES ($1::uuid, $2, $3, $4::metric_type, $5)`,
				id, m.Name, m.EventName, m.Type, m.IsPrimary,
			); err != nil {
				return err
			}
		}

		if err := audit.Write(ctx, tx, audit.Entry{
			ActorID:       access.User.ID,
			ActorEmail:    access.User.Email,
			EnvironmentID: access.Env.ID,
			Action:        "experiment.create",
			ResourceType:  "experiment",
			ResourceID:    req.Key,
			After:         req,
		}); err != nil {
			return err
		}

		var err error
		view, err = loadOne(ctx, tx, access.Env, req.Key)
		return err
	})
	var invalid validationError
	switch {
	case errors.As(err, new(errFlagNotFound)), isFlagDeletedViolation(err):
		httpx.WriteError(w, http.StatusNotFound, "FLAG_NOT_FOUND", "no flag with key "+req.FlagKey)
	case errors.As(err, &invalid):
		httpx.BadRequest(w, invalid.Error())
	case isUniqueViolation(err, "experiments_env_key_unique"):
		httpx.WriteError(w, http.StatusConflict, "EXPERIMENT_KEY_EXISTS", "an experiment with key "+req.Key+" already exists in "+access.Env.Key)
	case err != nil:
		httpx.WriteInternal(w, r, err)
	default:
		httpx.WriteJSON(w, http.StatusCreated, view)
	}
}

// List handles GET /environments/{env}/experiments, newest first, capped at
// 200 rows (no pagination yet). Optional filters: status, flagKey.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	status := r.URL.Query().Get("status")
	if status != "" && status != string(StatusDraft) && status != string(StatusRunning) && status != string(StatusStopped) {
		httpx.BadRequest(w, "status must be draft, running or stopped")
		return
	}
	flagKey := r.URL.Query().Get("flagKey")

	const filter = ` AND ($2 = '' OR x.status::text = $2) AND ($3 = '' OR f.key = $3)`
	rows, err := h.pool.Query(ctx,
		selectExperiment+filter+fmt.Sprintf(` ORDER BY x.created_at DESC, x.id LIMIT %d`, maxListResults),
		access.Env.ID, status, flagKey)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	defer rows.Close()
	views := []experimentView{}
	for rows.Next() {
		v, err := scanExperiment(rows, access.Env.Key)
		if err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
		views = append(views, v)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	rows.Close()

	if len(views) > 0 {
		// Same filter as above, so metrics are fetched for exactly these rows.
		if err := attachMetrics(ctx, h.pool, views, fmt.Sprintf(`experiment_id IN (
			SELECT x.id FROM experiments x JOIN flags f ON f.id = x.flag_id
			WHERE x.environment_id = $1::uuid AND ($2 = '' OR x.status::text = $2) AND ($3 = '' OR f.key = $3)
			ORDER BY x.created_at DESC, x.id LIMIT %d)`, maxListResults),
			access.Env.ID, status, flagKey); err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"experiments": views})
}

// Get handles GET /environments/{env}/experiments/{key}.
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	access := rbac.AccessFrom(r.Context())
	key := r.PathValue("key")
	view, err := loadOne(r.Context(), h.pool, access.Env, key)
	if errors.Is(err, pgx.ErrNoRows) {
		writeLifecycleError(w, r, key, errNotFound{})
		return
	}
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

type statusChange struct {
	Status Status `json:"status"`
}

// Start handles POST /environments/{env}/experiments/{key}/start.
//
// Lock order matters: the flag row is locked FOR SHARE first, which
// conflicts with flags.Delete's FOR UPDATE, so a flag can't be deleted
// between the "no running experiment" check there and this start committing.
// Then the experiment, then the flag's config (which serialises with flag
// PATCH and kill, so the captured snapshot is consistent).
func (h *Handlers) Start(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	key := r.PathValue("key")

	var view experimentView
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var flagID string
		if err := tx.QueryRow(ctx, `
			SELECT flag_id::text FROM experiments WHERE environment_id = $1::uuid AND key = $2`,
			access.Env.ID, key,
		).Scan(&flagID); err != nil {
			return err
		}
		var locked int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM flags WHERE id = $1::uuid FOR SHARE`, flagID).Scan(&locked); err != nil {
			return err
		}
		var status Status
		if err := tx.QueryRow(ctx, `
			SELECT status::text FROM experiments WHERE environment_id = $1::uuid AND key = $2 FOR UPDATE`,
			access.Env.ID, key,
		).Scan(&status); err != nil {
			return err
		}
		if !canTransition(status, StatusRunning) {
			return errInvalidTransition{status, StatusRunning}
		}

		var enabled bool
		snap := configSnapshot{}
		var rules, rollout string
		if err := tx.QueryRow(ctx, `
			SELECT enabled, targeting_rules::text, COALESCE(rollout::text, 'null'), fallthrough_variation_id, version
			FROM flag_configs WHERE flag_id = $1::uuid AND environment_id = $2::uuid FOR UPDATE`,
			flagID, access.Env.ID,
		).Scan(&enabled, &rules, &rollout, &snap.FallthroughVariationID, &snap.Version); err != nil {
			return err
		}
		if !enabled {
			return errFlagDisabled{}
		}
		snap.TargetingRules, snap.Rollout = json.RawMessage(rules), json.RawMessage(rollout)
		snapJSON, err := json.Marshal(snap)
		if err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE experiments SET status = 'running', started_at = now(), start_config = $1::jsonb
			WHERE environment_id = $2::uuid AND key = $3`,
			string(snapJSON), access.Env.ID, key,
		); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{
			ActorID:       access.User.ID,
			ActorEmail:    access.User.Email,
			EnvironmentID: access.Env.ID,
			Action:        "experiment.start",
			ResourceType:  "experiment",
			ResourceID:    key,
			Before:        statusChange{StatusDraft},
			After:         statusChange{StatusRunning},
		}); err != nil {
			return err
		}
		view, err = loadOne(ctx, tx, access.Env, key)
		return err
	})
	if err != nil {
		writeLifecycleError(w, r, key, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// Stop handles POST /environments/{env}/experiments/{key}/stop. Stopped is
// terminal.
func (h *Handlers) Stop(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	key := r.PathValue("key")

	var view experimentView
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var flagID string
		var status Status
		if err := tx.QueryRow(ctx, `
			SELECT flag_id::text, status::text FROM experiments
			WHERE environment_id = $1::uuid AND key = $2 FOR UPDATE`,
			access.Env.ID, key,
		).Scan(&flagID, &status); err != nil {
			return err
		}
		if !canTransition(status, StatusStopped) {
			return errInvalidTransition{status, StatusStopped}
		}

		snap := configSnapshot{}
		var rules, rollout string
		if err := tx.QueryRow(ctx, `
			SELECT targeting_rules::text, COALESCE(rollout::text, 'null'), fallthrough_variation_id, version
			FROM flag_configs WHERE flag_id = $1::uuid AND environment_id = $2::uuid`,
			flagID, access.Env.ID,
		).Scan(&rules, &rollout, &snap.FallthroughVariationID, &snap.Version); err != nil {
			return err
		}
		snap.TargetingRules, snap.Rollout = json.RawMessage(rules), json.RawMessage(rollout)
		snapJSON, err := json.Marshal(snap)
		if err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE experiments SET status = 'stopped', stopped_at = now(), stop_config = $1::jsonb
			WHERE environment_id = $2::uuid AND key = $3`,
			string(snapJSON), access.Env.ID, key,
		); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Entry{
			ActorID:       access.User.ID,
			ActorEmail:    access.User.Email,
			EnvironmentID: access.Env.ID,
			Action:        "experiment.stop",
			ResourceType:  "experiment",
			ResourceID:    key,
			Before:        statusChange{StatusRunning},
			After:         statusChange{StatusStopped},
		}); err != nil {
			return err
		}
		view, err = loadOne(ctx, tx, access.Env, key)
		return err
	})
	if err != nil {
		writeLifecycleError(w, r, key, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}
