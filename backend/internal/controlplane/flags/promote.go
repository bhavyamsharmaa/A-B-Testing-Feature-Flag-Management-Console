package flags

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/httpx"
)

// Promotion copies a flag's configuration from one environment to another of
// the same workspace, in two stages: PREVIEW computes the difference on the
// server, APPLY makes it. Apply is not a second way to edit: it builds a
// Change and hands it to ApplyConfigChange, so the production approval gate
// (the route needs rbac.FlagWrite in the TARGET), validation, version bump,
// audit entry (naming the source) and Redis event are exactly an edit's.
//
// Which parts are copied is the caller's choice. By default the enabled state
// is NOT copied: turning a flag on in production is a decision of its own.

// Parts of a configuration that can be promoted.
const (
	PartRules       = "rules"
	PartRollout     = "rollout"
	PartFallthrough = "fallthrough"
	PartEnabled     = "enabled"
)

var allParts = []string{PartRules, PartRollout, PartFallthrough, PartEnabled}
var defaultParts = []string{PartRules, PartRollout, PartFallthrough}

type promoteRequest struct {
	SourceEnvironment string   `json:"sourceEnvironment"`
	Parts             []string `json:"parts"`
	// ExpectedVersion is the TARGET configuration's version from the preview,
	// SourceVersion the source's. Apply requires both: it never overwrites an
	// edit made since the caller looked, on either side.
	ExpectedVersion *int64 `json:"expectedVersion"`
	SourceVersion   *int64 `json:"sourceVersion"`
}

type promoConfig struct {
	configSnapshot
	Version int64
}

type promoSide struct {
	Environment string `json:"environment"`
	Version     int64  `json:"version"`
	Enabled     bool   `json:"enabled"`
}

type promoPart struct {
	Part     string          `json:"part"`
	Selected bool            `json:"selected"`
	Changed  bool            `json:"changed"`
	Source   json.RawMessage `json:"source"`
	Target   json.RawMessage `json:"target"`
}

type promotionPreview struct {
	FlagKey      string      `json:"flagKey"`
	Source       promoSide   `json:"source"`
	Target       promoSide   `json:"target"`
	Parts        []promoPart `json:"parts"`
	DefaultParts []string    `json:"defaultParts"`
	// Problems says why the selected parts cannot be applied (empty when they can).
	Problems []string `json:"problems"`
	// NothingToApply: the selected parts are already identical.
	NothingToApply bool `json:"nothingToApply"`
	CanApply       bool `json:"canApply"`
	// ExpectedVersion and SourceVersion go back in the apply request.
	ExpectedVersion int64 `json:"expectedVersion"`
	SourceVersion   int64 `json:"sourceVersion"`
}

// promotionError is a refusal with its code and status.
type promotionError struct {
	Status  int
	Code    string
	Message string
}

func (e promotionError) Error() string { return e.Message }

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func readConfig(ctx context.Context, q queryRower, workspaceID, envID, flagKey string) (promoConfig, string, error) {
	var c promoConfig
	var rules, rollout, variations string
	err := q.QueryRow(ctx, `
		SELECT f.variations::text, fc.enabled, fc.targeting_rules::text, COALESCE(fc.rollout::text, 'null'),
		       fc.fallthrough_variation_id, fc.version
		FROM flag_configs fc JOIN flags f ON f.id = fc.flag_id
		WHERE f.key = $1 AND fc.environment_id = $2::uuid AND f.workspace_id = $3::uuid`,
		flagKey, envID, workspaceID).Scan(&variations, &c.Enabled, &rules, &rollout, &c.FallthroughVariationID, &c.Version)
	c.TargetingRules, c.Rollout = json.RawMessage(rules), json.RawMessage(rollout)
	return c, variations, err
}

// normalParts validates the requested parts (default: everything but enabled).
func normalParts(parts []string) ([]string, error) {
	if len(parts) == 0 {
		return slices.Clone(defaultParts), nil
	}
	seen := map[string]bool{}
	for _, p := range parts {
		if !slices.Contains(allParts, p) {
			return nil, promotionError{http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("unknown part %q: choose from rules, rollout, fallthrough, enabled", p)}
		}
		seen[p] = true
	}
	var out []string
	for _, p := range allParts { // a stable order
		if seen[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

// canonicalJSON re-encodes JSON so key order and spacing do not make two equal values differ.
func canonicalJSON(raw json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, _ := json.Marshal(v)
	return b
}

// plan reads both sides and works out the preview and the Change to apply.
func plan(ctx context.Context, q queryRower, target rbac.Environment, flagKey string, req promoteRequest) (promotionPreview, Change, error) {
	var prev promotionPreview
	if req.SourceEnvironment == "" {
		return prev, Change{}, promotionError{http.StatusBadRequest, "INVALID_REQUEST", "sourceEnvironment is required (an environment key such as staging)"}
	}
	parts, err := normalParts(req.Parts)
	if err != nil {
		return prev, Change{}, err
	}
	// The source is looked up inside the target's workspace only, by key or id:
	// another workspace's environment is "not found", never "forbidden".
	var srcID, srcKey string
	err = q.QueryRow(ctx, `
		SELECT id::text, key FROM environments
		WHERE workspace_id = $1::uuid AND (key = $2 OR id::text = lower($2))`, target.WorkspaceID, req.SourceEnvironment).Scan(&srcID, &srcKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return prev, Change{}, promotionError{http.StatusNotFound, "ENVIRONMENT_NOT_FOUND", "source environment not found in this workspace"}
	}
	if err != nil {
		return prev, Change{}, err
	}
	if srcID == target.ID {
		return prev, Change{}, promotionError{http.StatusBadRequest, "INVALID_REQUEST", "the source and the target are the same environment"}
	}
	src, _, err := readConfig(ctx, q, target.WorkspaceID, srcID, flagKey)
	if err != nil {
		return prev, Change{}, err // pgx.ErrNoRows: no such flag
	}
	tgt, variations, err := readConfig(ctx, q, target.WorkspaceID, target.ID, flagKey)
	if err != nil {
		return prev, Change{}, err
	}

	prev = promotionPreview{
		FlagKey:         flagKey,
		Source:          promoSide{srcKey, src.Version, src.Enabled},
		Target:          promoSide{target.Key, tgt.Version, tgt.Enabled},
		DefaultParts:    defaultParts,
		Problems:        []string{},
		ExpectedVersion: tgt.Version,
		SourceVersion:   src.Version,
	}
	var ch Change
	anyChange := false
	for _, p := range allParts {
		pp := promoPart{Part: p, Selected: slices.Contains(parts, p)}
		switch p {
		case PartRules:
			pp.Source, pp.Target = src.TargetingRules, tgt.TargetingRules
			pp.Changed = string(canonicalJSON(src.TargetingRules)) != string(canonicalJSON(tgt.TargetingRules))
			if pp.Selected && pp.Changed {
				ch.TargetingRules = src.TargetingRules
			}
		case PartRollout:
			pp.Source, pp.Target = src.Rollout, tgt.Rollout
			pp.Changed = string(canonicalJSON(src.Rollout)) != string(canonicalJSON(tgt.Rollout))
			if pp.Selected && pp.Changed {
				ch.Rollout = src.Rollout
			}
		case PartFallthrough:
			pp.Source, pp.Target = jsonString(src.FallthroughVariationID), jsonString(tgt.FallthroughVariationID)
			pp.Changed = src.FallthroughVariationID != tgt.FallthroughVariationID
			if pp.Selected && pp.Changed {
				v := src.FallthroughVariationID
				ch.FallthroughVariationID = &v
			}
		case PartEnabled:
			pp.Source, pp.Target = jsonBool(src.Enabled), jsonBool(tgt.Enabled)
			pp.Changed = src.Enabled != tgt.Enabled
			if pp.Selected && pp.Changed {
				v := src.Enabled
				ch.Enabled = &v
			}
		}
		anyChange = anyChange || (pp.Selected && pp.Changed)
		prev.Parts = append(prev.Parts, pp)
	}
	prev.NothingToApply = !anyChange

	// Would the target accept it? The same validation an edit runs: the rules
	// must be valid for this flag's variations and every segment must exist.
	if anyChange {
		known, err := existingSegments(ctx, q, target.WorkspaceID, ruleSegmentRefs(ch.TargetingRules))
		if err != nil {
			return prev, Change{}, err
		}
		if _, err := applyToSnapshot(tgt.configSnapshot, variations, ch, known); err != nil {
			if !IsValidation(err) {
				return prev, Change{}, err
			}
			prev.Problems = append(prev.Problems, err.Error())
		}
	}
	prev.CanApply = anyChange && len(prev.Problems) == 0
	return prev, ch, nil
}

func jsonString(s string) json.RawMessage { b, _ := json.Marshal(s); return b }
func jsonBool(v bool) json.RawMessage     { b, _ := json.Marshal(v); return b }

// existingSegments reports which of the keys are segments of the workspace (no locks: a preview).
func existingSegments(ctx context.Context, q queryRower, workspaceID string, keys []string) (map[string]bool, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT key FROM segments WHERE workspace_id = $1::uuid AND key = ANY($2::text[])`, workspaceID, keys)
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, k := range found {
		known[k] = true
	}
	return known, nil
}

func writePromotionError(w http.ResponseWriter, r *http.Request, flagKey string, err error) {
	var pe promotionError
	var conflict ErrVersionConflict
	var invalid validationError
	switch {
	case errors.As(err, &pe):
		httpx.WriteError(w, pe.Status, pe.Code, pe.Message)
	case errors.Is(err, pgx.ErrNoRows):
		writeFlagNotFound(w, flagKey)
	case errors.As(err, &conflict):
		httpx.WriteError(w, http.StatusConflict, "VERSION_CONFLICT",
			fmt.Sprintf("the target's configuration changed since the preview (now version %d); preview again", conflict.Current))
	case errors.As(err, &invalid):
		httpx.BadRequest(w, invalid.Error())
	default:
		httpx.WriteInternal(w, r, err)
	}
}

// PromotePreview handles POST /environments/{envId}/flags/{key}/promotions/preview.
// {envId} is the TARGET. Read-only, so any role may preview.
func (h *Handlers) PromotePreview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	key := r.PathValue("key")
	var req promoteRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	prev, _, err := plan(ctx, h.pool, access.Env, key, req)
	if err != nil {
		writePromotionError(w, r, key, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, prev)
}

// PromoteApply handles POST /environments/{envId}/flags/{key}/promotions. The
// route requires rbac.FlagWrite in the target: an approver for production.
func (h *Handlers) PromoteApply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	key := r.PathValue("key")
	var req promoteRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if req.ExpectedVersion == nil || req.SourceVersion == nil {
		httpx.BadRequest(w, "expectedVersion and sourceVersion are required: take them from the preview")
		return
	}

	var view View
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		prev, change, err := plan(ctx, tx, access.Env, key, req)
		if err != nil {
			return err
		}
		if prev.SourceVersion != *req.SourceVersion {
			return promotionError{http.StatusConflict, "SOURCE_CHANGED",
				fmt.Sprintf("the source configuration changed since the preview (now version %d); preview again", prev.SourceVersion)}
		}
		if prev.NothingToApply {
			return promotionError{http.StatusConflict, "NOTHING_TO_PROMOTE", "the selected parts are already identical in both environments"}
		}
		if len(prev.Problems) > 0 {
			return validationError{errors.New(prev.Problems[0])}
		}
		view, err = ApplyConfigChange(ctx, tx, access.Env, key,
			Actor{ID: access.User.ID, Email: access.User.Email}, change,
			ApplyOptions{ExpectedVersion: req.ExpectedVersion, PromotedFrom: prev.Source.Environment})
		return err
	})
	if err != nil {
		writePromotionError(w, r, key, err)
		return
	}
	h.publish(ctx, access.Env.WorkspaceID, access.Env.ID, "updated", view.Key, view.Config.Enabled, view.Config.Version)
	httpx.WriteJSON(w, http.StatusOK, view)
}
