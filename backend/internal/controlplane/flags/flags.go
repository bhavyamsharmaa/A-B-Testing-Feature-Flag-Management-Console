// Package flags implements control-plane CRUD for feature flags and their
// per-environment configuration (enabled state, targeting rules, rollout).
// Every mutation writes its audit row in the same transaction.
package flags

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/dataplane/evaluation"
	"helios/backend/internal/platform/httpx"
)

type Handlers struct {
	pool *pgxpool.Pool
}

func NewHandlers(pool *pgxpool.Pool) *Handlers {
	return &Handlers{pool: pool}
}

type configView struct {
	Enabled                bool            `json:"enabled"`
	TargetingRules         json.RawMessage `json:"targetingRules"`
	Rollout                json.RawMessage `json:"rollout"`
	FallthroughVariationID string          `json:"fallthroughVariationId"`
	Version                int64           `json:"version"`
	UpdatedAt              time.Time       `json:"updatedAt"`
}

// flagView is a flag's definition plus its config in the requested
// environment. The salt is deliberately left out.
type flagView struct {
	Key           string          `json:"key"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	VariationType string          `json:"variationType"`
	Variations    json.RawMessage `json:"variations"`
	Environment   string          `json:"environment"`
	Config        configView      `json:"config"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// JSONB columns are read as ::text so scanning behaves the same under the
// simple and extended query protocols.
const selectFlagView = `
	SELECT f.key, f.name, COALESCE(f.description, ''), f.variation_type::text, f.variations::text,
	       f.created_at, f.updated_at,
	       fc.enabled, fc.targeting_rules::text, COALESCE(fc.rollout::text, 'null'),
	       fc.fallthrough_variation_id, fc.version, fc.updated_at
	FROM flags f
	JOIN flag_configs fc ON fc.flag_id = f.id AND fc.environment_id = $1::uuid`

type scanner interface {
	Scan(dest ...any) error
}

func scanFlagView(row scanner, envKey string) (flagView, error) {
	var v flagView
	var variations, rules, rollout string
	err := row.Scan(&v.Key, &v.Name, &v.Description, &v.VariationType, &variations,
		&v.CreatedAt, &v.UpdatedAt,
		&v.Config.Enabled, &rules, &rollout,
		&v.Config.FallthroughVariationID, &v.Config.Version, &v.Config.UpdatedAt)
	v.Variations = json.RawMessage(variations)
	v.Config.TargetingRules = json.RawMessage(rules)
	v.Config.Rollout = json.RawMessage(rollout)
	v.Environment = envKey
	return v, err
}

// Create handles POST /environments/{env}/flags. The flag is defined once
// and gets a config in every environment, disabled everywhere, so creating a
// flag never changes user-visible behaviour (US-01 AC-1). Fallthrough
// defaults to the first variation; change it with PATCH.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	var req createRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if err := req.validate(); err != nil {
		var mismatch errTypeMismatch
		if errors.As(err, &mismatch) {
			httpx.WriteError(w, http.StatusBadRequest, "TYPE_MISMATCH", err.Error())
			return
		}
		httpx.BadRequest(w, err.Error())
		return
	}
	variations, err := json.Marshal(req.Variations)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}

	var view flagView
	err = pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var flagID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO flags (key, name, description, variation_type, variations, created_by)
			VALUES ($1, $2, NULLIF($3, ''), $4::variation_type, $5::jsonb, $6::uuid)
			RETURNING id::text`,
			req.Key, req.Name, req.Description, req.VariationType, string(variations), access.User.ID,
		).Scan(&flagID); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `SELECT id::text FROM environments`)
		if err != nil {
			return err
		}
		envIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, envID := range envIDs {
			salt, err := newSalt()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO flag_configs (flag_id, environment_id, salt, fallthrough_variation_id)
				VALUES ($1::uuid, $2::uuid, $3, $4)`,
				flagID, envID, salt, req.Variations[0].ID,
			); err != nil {
				return err
			}
		}

		if err := audit.Write(ctx, tx, audit.Entry{
			ActorID:      access.User.ID,
			ActorEmail:   access.User.Email,
			Action:       "flag.create",
			ResourceType: "flag",
			ResourceID:   req.Key,
			After:        req,
		}); err != nil {
			return err
		}

		view, err = scanFlagView(tx.QueryRow(ctx, selectFlagView+` WHERE f.id = $2::uuid`, access.Env.ID, flagID), access.Env.Key)
		return err
	})
	if isUniqueViolation(err, "flags_key_key") {
		httpx.WriteError(w, http.StatusConflict, "FLAG_KEY_EXISTS", "a flag with key "+req.Key+" already exists")
		return
	}
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, view)
}

// List handles GET /environments/{env}/flags.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	access := rbac.AccessFrom(r.Context())
	rows, err := h.pool.Query(r.Context(), selectFlagView+` ORDER BY f.key`, access.Env.ID)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	defer rows.Close()
	flags := []flagView{}
	for rows.Next() {
		v, err := scanFlagView(rows, access.Env.Key)
		if err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
		flags = append(flags, v)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"flags": flags})
}

// Get handles GET /environments/{env}/flags/{key}.
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	access := rbac.AccessFrom(r.Context())
	key := r.PathValue("key")
	view, err := scanFlagView(h.pool.QueryRow(r.Context(), selectFlagView+` WHERE f.key = $2`, access.Env.ID, key), access.Env.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		writeFlagNotFound(w, key)
		return
	}
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// configSnapshot is what gets recorded in the audit log for config changes.
type configSnapshot struct {
	Enabled                bool            `json:"enabled"`
	TargetingRules         json.RawMessage `json:"targetingRules"`
	Rollout                json.RawMessage `json:"rollout"`
	FallthroughVariationID string          `json:"fallthroughVariationId"`
}

type validationError struct{ error }

// Update handles PATCH /environments/{env}/flags/{key}. Absent fields are
// left alone; targetingRules or rollout set to null are cleared.
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	key := r.PathValue("key")
	var req updateRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if req.empty() {
		httpx.BadRequest(w, "nothing to update: send enabled, targetingRules, rollout, or fallthroughVariationId")
		return
	}

	var view flagView
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var configID, variationsText, rulesText, rolloutText string
		var before configSnapshot
		err := tx.QueryRow(ctx, `
			SELECT fc.id::text, f.variations::text, fc.enabled, fc.targeting_rules::text,
			       COALESCE(fc.rollout::text, 'null'), fc.fallthrough_variation_id
			FROM flag_configs fc
			JOIN flags f ON f.id = fc.flag_id
			WHERE f.key = $1 AND fc.environment_id = $2::uuid
			FOR UPDATE OF fc`,
			key, access.Env.ID,
		).Scan(&configID, &variationsText, &before.Enabled, &rulesText, &rolloutText, &before.FallthroughVariationID)
		if err != nil {
			return err
		}
		before.TargetingRules = json.RawMessage(rulesText)
		before.Rollout = json.RawMessage(rolloutText)

		var variations []evaluation.Variation
		if err := json.Unmarshal([]byte(variationsText), &variations); err != nil {
			return err
		}
		ids := map[string]bool{}
		for _, v := range variations {
			ids[v.ID] = true
		}

		after := before
		if req.Enabled != nil {
			after.Enabled = *req.Enabled
		}
		if req.TargetingRules != nil {
			rules, err := parseRules(req.TargetingRules, ids)
			if err != nil {
				return validationError{err}
			}
			if after.TargetingRules, err = json.Marshal(rules); err != nil {
				return err
			}
		}
		if req.Rollout != nil {
			rollout, err := parseRollout(req.Rollout, ids)
			if err != nil {
				return validationError{err}
			}
			if after.Rollout, err = json.Marshal(rollout); err != nil { // nil map marshals to null
				return err
			}
		}
		if req.FallthroughVariationID != nil {
			if !ids[*req.FallthroughVariationID] {
				return validationError{errors.New("fallthroughVariationId " + *req.FallthroughVariationID + " is not a variation of this flag")}
			}
			after.FallthroughVariationID = *req.FallthroughVariationID
		}

		if _, err := tx.Exec(ctx, `
			UPDATE flag_configs
			SET enabled = $1,
			    targeting_rules = $2::jsonb,
			    rollout = NULLIF($3, 'null')::jsonb,
			    fallthrough_variation_id = $4,
			    version = version + 1,
			    updated_at = now()
			WHERE id = $5::uuid`,
			after.Enabled, string(after.TargetingRules), string(after.Rollout), after.FallthroughVariationID, configID,
		); err != nil {
			return err
		}

		if err := audit.Write(ctx, tx, audit.Entry{
			ActorID:       access.User.ID,
			ActorEmail:    access.User.Email,
			EnvironmentID: access.Env.ID,
			Action:        "flag.update",
			ResourceType:  "flag",
			ResourceID:    key,
			Before:        before,
			After:         after,
		}); err != nil {
			return err
		}

		view, err = scanFlagView(tx.QueryRow(ctx, selectFlagView+` WHERE f.key = $2`, access.Env.ID, key), access.Env.Key)
		return err
	})
	var invalid validationError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeFlagNotFound(w, key)
	case errors.As(err, &invalid):
		httpx.BadRequest(w, invalid.Error())
	case err != nil:
		httpx.WriteInternal(w, r, err)
	default:
		httpx.WriteJSON(w, http.StatusOK, view)
	}
}

type errInUse struct{ environments []string }

func (e errInUse) Error() string { return "in use" }

type errNotAdminEverywhere struct{ environments []string }

func (e errNotAdminEverywhere) Error() string { return "not admin everywhere" }

// Delete handles DELETE /environments/{env}/flags/{key}.
//
// A flag's definition is shared by every environment, so deleting it removes
// it everywhere. The route requires admin in {env}; this handler additionally
// requires admin in every other environment, so a dev-only admin can't
// delete a flag that production depends on.
//
// It's refused with 409 IN_USE while the flag is enabled with targeting
// rules in any environment, unless ?force=true.
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	key := r.PathValue("key")
	force := r.URL.Query().Get("force") == "true"

	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var flagID string
		var snapshot struct {
			Name          string          `json:"name"`
			VariationType string          `json:"variationType"`
			Variations    json.RawMessage `json:"variations"`
		}
		var variations string
		if err := tx.QueryRow(ctx, `
			SELECT id::text, name, variation_type::text, variations::text
			FROM flags WHERE key = $1 FOR UPDATE`, key,
		).Scan(&flagID, &snapshot.Name, &snapshot.VariationType, &variations); err != nil {
			return err
		}
		snapshot.Variations = json.RawMessage(variations)

		rows, err := tx.Query(ctx, `
			SELECT e.key FROM environments e
			WHERE NOT EXISTS (
				SELECT 1 FROM user_environment_roles r
				WHERE r.environment_id = e.id AND r.user_id = $1::uuid AND r.role = 'admin'
			)
			ORDER BY e.key`, access.User.ID)
		if err != nil {
			return err
		}
		notAdmin, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if len(notAdmin) > 0 {
			return errNotAdminEverywhere{notAdmin}
		}

		if !force {
			rows, err := tx.Query(ctx, `
				SELECT e.key FROM flag_configs fc
				JOIN environments e ON e.id = fc.environment_id
				WHERE fc.flag_id = $1::uuid AND fc.enabled AND jsonb_array_length(fc.targeting_rules) > 0
				ORDER BY e.key`, flagID)
			if err != nil {
				return err
			}
			inUse, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			if len(inUse) > 0 {
				return errInUse{inUse}
			}
		}

		if _, err := tx.Exec(ctx, `DELETE FROM flags WHERE id = $1::uuid`, flagID); err != nil {
			return err
		}

		action, severity := "flag.delete", audit.SeverityInfo
		if force {
			action, severity = "flag.force_delete", audit.SeverityCritical
		}
		return audit.Write(ctx, tx, audit.Entry{
			ActorID:       access.User.ID,
			ActorEmail:    access.User.Email,
			EnvironmentID: access.Env.ID,
			Action:        action,
			ResourceType:  "flag",
			ResourceID:    key,
			Severity:      severity,
			Before:        snapshot,
		})
	})

	var inUse errInUse
	var notAdmin errNotAdminEverywhere
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeFlagNotFound(w, key)
	case errors.As(err, &notAdmin):
		httpx.WriteError(w, http.StatusForbidden, "FORBIDDEN",
			"deleting a flag removes it from every environment; you are not an admin in: "+strings.Join(notAdmin.environments, ", "))
	case errors.As(err, &inUse):
		httpx.WriteError(w, http.StatusConflict, "IN_USE",
			"flag is enabled with active targeting rules in: "+strings.Join(inUse.environments, ", ")+"; disable it first or pass ?force=true")
	case err != nil:
		httpx.WriteInternal(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// Kill handles POST /environments/{env}/flags/{key}/kill: disable the flag
// in this environment immediately. Idempotent, and audited at
// severity=critical every time, even when the flag was already off (US-08).
func (h *Handlers) Kill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	key := r.PathValue("key")

	var view flagView
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var configID string
		var wasEnabled bool
		if err := tx.QueryRow(ctx, `
			SELECT fc.id::text, fc.enabled
			FROM flag_configs fc
			JOIN flags f ON f.id = fc.flag_id
			WHERE f.key = $1 AND fc.environment_id = $2::uuid
			FOR UPDATE OF fc`,
			key, access.Env.ID,
		).Scan(&configID, &wasEnabled); err != nil {
			return err
		}
		if wasEnabled {
			if _, err := tx.Exec(ctx, `
				UPDATE flag_configs
				SET enabled = false, version = version + 1, updated_at = now()
				WHERE id = $1::uuid`, configID,
			); err != nil {
				return err
			}
		}
		if err := audit.Write(ctx, tx, audit.Entry{
			ActorID:       access.User.ID,
			ActorEmail:    access.User.Email,
			EnvironmentID: access.Env.ID,
			Action:        "flag.kill",
			ResourceType:  "flag",
			ResourceID:    key,
			Severity:      audit.SeverityCritical,
			Before:        map[string]bool{"enabled": wasEnabled},
			After:         map[string]bool{"enabled": false},
		}); err != nil {
			return err
		}
		var err error
		view, err = scanFlagView(tx.QueryRow(ctx, selectFlagView+` WHERE f.key = $2`, access.Env.ID, key), access.Env.Key)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeFlagNotFound(w, key)
		return
	}
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

func writeFlagNotFound(w http.ResponseWriter, key string) {
	httpx.WriteError(w, http.StatusNotFound, "FLAG_NOT_FOUND", "no flag with key "+key)
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func newSalt() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
