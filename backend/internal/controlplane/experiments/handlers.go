package experiments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/controlplane/webhooks"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/httpx"
)

type Handlers struct {
	pool   *pgxpool.Pool
	events events.Publisher
}

// NewHandlers: publisher (nil is fine) hears about an experiment starting or stopping, because that changes what
// /evaluate returns for the flag (the experiment id, the exposure token) and the configuration cache must drop it.
func NewHandlers(pool *pgxpool.Pool, publisher events.Publisher) *Handlers {
	return &Handlers{pool: pool, events: publisher}
}

// publishRunState is best-effort and runs after the transaction committed, like the flag events.
func (h *Handlers) publishRunState(ctx context.Context, env rbac.Environment, action, experimentKey string) {
	if h.events == nil {
		return
	}
	payload, err := json.Marshal(map[string]string{"action": action, "experimentKey": experimentKey})
	if err != nil {
		return
	}
	if err := h.events.Publish(ctx, events.FlagsChannel(env.WorkspaceID, env.ID), payload); err != nil {
		log.Printf("experiments: publish %s for %s: %v", action, experimentKey, err)
	}
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
	// Variations are the flag's variations in the flag's order, with the one that is the control marked. The
	// control is what results compare every other variation with (see migration 0014).
	Variations         []variationView `json:"variations"`
	ControlVariationID string          `json:"controlVariationId"`
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

type variationView struct {
	ID        string `json:"id"`
	IsControl bool   `json:"isControl"`
}

// attachVariations fills Variations and ControlVariationID on views.
func attachVariations(ctx context.Context, q querier, views []experimentView) error {
	ids := make([]string, len(views))
	for i, v := range views {
		ids[i] = v.ID
	}
	rows, err := q.Query(ctx, `
		SELECT experiment_id::text, variation_id, is_control FROM experiment_variations
		WHERE experiment_id = ANY($1::uuid[]) ORDER BY experiment_id, position`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	by := map[string][]variationView{}
	for rows.Next() {
		var id string
		var v variationView
		if err := rows.Scan(&id, &v.ID, &v.IsControl); err != nil {
			return err
		}
		by[id] = append(by[id], v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range views {
		views[i].Variations = by[views[i].ID]
		if views[i].Variations == nil {
			views[i].Variations = []variationView{}
		}
		for _, v := range views[i].Variations {
			if v.IsControl {
				views[i].ControlVariationID = v.ID
			}
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
	if err := attachVariations(ctx, q, views); err != nil {
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

// Create handles POST /environments/{envId}/experiments. The experiment starts
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
		var flagID, variationsText string
		var variationCount int
		if err := tx.QueryRow(ctx,
			`SELECT id::text, jsonb_array_length(variations), variations::text FROM flags WHERE key = $1 AND workspace_id = $2::uuid`,
			req.FlagKey, access.Env.WorkspaceID,
		).Scan(&flagID, &variationCount, &variationsText); errors.Is(err, pgx.ErrNoRows) {
			return errFlagNotFound{}
		} else if err != nil {
			return err
		}
		if err := checkVariationCount(req.FlagKey, variationCount); err != nil {
			return validationError{err}
		}
		var flagVariations []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(variationsText), &flagVariations); err != nil || len(flagVariations) == 0 {
			return fmt.Errorf("flag variations: %v", err)
		}
		if req.ControlVariationID == "" {
			req.ControlVariationID = flagVariations[0].ID
		}
		known := false
		for _, v := range flagVariations {
			known = known || v.ID == req.ControlVariationID
		}
		if !known {
			return validationError{fmt.Errorf("controlVariationId %q is not a variation of flag %s", req.ControlVariationID, req.FlagKey)}
		}

		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO experiments (workspace_id, environment_id, flag_id, key, name, hypothesis, created_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, NULLIF($6, ''), $7::uuid)
			RETURNING id::text`,
			access.Env.WorkspaceID, access.Env.ID, flagID, req.Key, req.Name, req.Hypothesis, access.User.ID,
		).Scan(&id); err != nil {
			return err
		}
		// Every variation of the flag, the chosen one as the control: exactly one (the database refuses two).
		for i, v := range flagVariations {
			if _, err := tx.Exec(ctx, `
				INSERT INTO experiment_variations (experiment_id, workspace_id, variation_id, position, is_control)
				VALUES ($1::uuid, $2::uuid, $3, $4, $5)`, id, access.Env.WorkspaceID, v.ID, i, v.ID == req.ControlVariationID); err != nil {
				return err
			}
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
			WorkspaceID:   access.Env.WorkspaceID,
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

// List handles GET /environments/{envId}/experiments, newest first, capped at
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
		if err := attachVariations(ctx, h.pool, views); err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"experiments": views})
}

// Get handles GET /environments/{envId}/experiments/{key}.
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

// Start handles POST /environments/{envId}/experiments/{key}/start.
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
			WorkspaceID:   access.Env.WorkspaceID,
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
		if err := emitLifecycle(ctx, tx, access, webhooks.EventExperimentStarted, key, string(StatusDraft), string(StatusRunning)); err != nil {
			return err
		}
		view, err = loadOne(ctx, tx, access.Env, key)
		return err
	})
	if err != nil {
		writeLifecycleError(w, r, key, err)
		return
	}
	h.publishRunState(ctx, access.Env, "experiment_started", key)
	httpx.WriteJSON(w, http.StatusOK, view)
}

// Stop handles POST /environments/{envId}/experiments/{key}/stop. Stopped is
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
			WorkspaceID:   access.Env.WorkspaceID,
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
		if err := emitLifecycle(ctx, tx, access, webhooks.EventExperimentStopped, key, string(StatusRunning), string(StatusStopped)); err != nil {
			return err
		}
		view, err = loadOne(ctx, tx, access.Env, key)
		return err
	})
	if err != nil {
		writeLifecycleError(w, r, key, err)
		return
	}
	h.publishRunState(ctx, access.Env, "experiment_stopped", key)
	httpx.WriteJSON(w, http.StatusOK, view)
}

// SetControl handles PATCH /environments/{envId}/experiments/{key} with {"controlVariationId"}: choose which variation is
// the control. Allowed until the experiment has any exposure; after that the comparison would change under the data
// already collected, so it is refused (409 CONTROL_FROZEN). Editors and above, like creating an experiment.
func (h *Handlers) SetControl(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	key := r.PathValue("key")
	var req struct {
		ControlVariationID string `json:"controlVariationId"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil || req.ControlVariationID == "" {
		httpx.BadRequest(w, "controlVariationId is required")
		return
	}
	var view experimentView
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var expID, current string
		if err := tx.QueryRow(ctx, `
			SELECT x.id::text, COALESCE((SELECT v.variation_id FROM experiment_variations v WHERE v.experiment_id = x.id AND v.is_control), '')
			FROM experiments x WHERE x.environment_id = $1::uuid AND x.key = $2 FOR UPDATE OF x`, access.Env.ID, key).Scan(&expID, &current); err != nil {
			return err
		}
		var known bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM experiment_variations WHERE experiment_id = $1::uuid AND variation_id = $2)`, expID, req.ControlVariationID).Scan(&known); err != nil {
			return err
		}
		if !known {
			return validationError{fmt.Errorf("controlVariationId %q is not a variation of this experiment's flag", req.ControlVariationID)}
		}
		if current == req.ControlVariationID {
			var err error
			view, err = loadOne(ctx, tx, access.Env, key)
			return err
		}
		frozen := func() (bool, error) {
			var has bool
			err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE workspace_id = $1::uuid AND experiment_id = $2::uuid AND event_name = '$exposure')`,
				access.Env.WorkspaceID, expID).Scan(&has)
			return has, err
		}
		if has, err := frozen(); err != nil || has {
			if err != nil {
				return err
			}
			return errControlFrozen{}
		}
		// Two statements in this transaction: the old control off, then the new one on. The partial unique index
		// is checked row by row, so a single UPDATE could briefly show two controls; nothing outside the
		// transaction ever sees zero or two.
		if _, err := tx.Exec(ctx, `UPDATE experiment_variations SET is_control = false WHERE experiment_id = $1::uuid AND is_control`, expID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE experiment_variations SET is_control = true WHERE experiment_id = $1::uuid AND variation_id = $2`, expID, req.ControlVariationID); err != nil {
			return err
		}
		// An exposure that committed while we were deciding still wins: look once more before committing.
		if has, err := frozen(); err != nil || has {
			if err != nil {
				return err
			}
			return errControlFrozen{}
		}
		if err := audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: access.Env.WorkspaceID, ActorID: access.User.ID, ActorEmail: access.User.Email, EnvironmentID: access.Env.ID,
			Action: "experiment.control_change", ResourceType: "experiment", ResourceID: key,
			Before: map[string]string{"controlVariationId": current}, After: map[string]string{"controlVariationId": req.ControlVariationID},
		}); err != nil {
			return err
		}
		var err error
		view, err = loadOne(ctx, tx, access.Env, key)
		return err
	})
	var invalid validationError
	switch {
	case errors.As(err, new(errControlFrozen)):
		httpx.WriteError(w, http.StatusConflict, "CONTROL_FROZEN", "this experiment already has exposures, so its control can no longer be changed; create a new experiment to compare against a different control")
	case errors.As(err, &invalid):
		httpx.BadRequest(w, invalid.Error())
	case errors.Is(err, pgx.ErrNoRows):
		writeLifecycleError(w, r, key, errNotFound{})
	case err != nil:
		httpx.WriteInternal(w, r, err)
	default:
		httpx.WriteJSON(w, http.StatusOK, view)
	}
}

type errControlFrozen struct{}

func (errControlFrozen) Error() string { return "control frozen" }
