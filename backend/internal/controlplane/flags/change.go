package flags

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/dataplane/evaluation"
)

// Change is a partial change to one flag's configuration in one environment:
// the body of PATCH /environments/{envId}/flags/{key}. Absent fields are left
// alone; targetingRules or rollout set to null are cleared. RawMessage tells
// "absent" from an explicit null.
type Change struct {
	Enabled                *bool           `json:"enabled"`
	TargetingRules         json.RawMessage `json:"targetingRules"`
	Rollout                json.RawMessage `json:"rollout"`
	FallthroughVariationID *string         `json:"fallthroughVariationId"`
}

type updateRequest = Change

func (c Change) empty() bool {
	return c.Enabled == nil && c.TargetingRules == nil && c.Rollout == nil && c.FallthroughVariationID == nil
}

// Empty reports whether the change touches nothing.
func (c Change) Empty() bool { return c.empty() }

// View is a flag with its configuration in one environment, as the API returns it.
type View = flagView

// Actor is who an edit is recorded as. Via names the mechanism when it is not
// the person at a keyboard ("schedule"); the audit entry then reads
// "alice@example.com via schedule" and carries the details.
type Actor struct {
	ID    string
	Email string
	Via   string
}

func (a Actor) auditEmail() string {
	if a.Via == "" {
		return a.Email
	}
	return a.Email + " via " + a.Via
}

// ApplyOptions are the optional, audited extras of an edit.
type ApplyOptions struct {
	// ExpectedVersion, when set, refuses the edit with ErrVersionConflict if
	// the configuration is no longer at that version: nothing is overwritten.
	ExpectedVersion *int64
	// PromotedFrom is the key of the environment the change was copied from.
	PromotedFrom string
	// ScheduledChangeID is the scheduled change this edit executes.
	ScheduledChangeID string
}

// ErrVersionConflict says the configuration moved since the caller looked.
type ErrVersionConflict struct{ Current int64 }

func (e ErrVersionConflict) Error() string {
	return "the flag's configuration changed since it was read"
}

// IsValidation reports whether err is a refusal of the change's content (a
// bad rule, an unknown segment, a variation that does not exist).
func IsValidation(err error) bool {
	var v validationError
	return errors.As(err, &v)
}

// Invalid wraps a message as a validation refusal.
func Invalid(msg string) error { return validationError{errors.New(msg)} }

// afterSnapshot is the audit "after" of an edit: the configuration, plus
// where the edit came from when it is not an ordinary one. The extra fields
// are omitted when empty, so an ordinary edit's entry is exactly what it was.
type afterSnapshot struct {
	configSnapshot
	PromotedFrom      string `json:"promotedFrom,omitempty"`
	Via               string `json:"via,omitempty"`
	ScheduledChangeID string `json:"scheduledChangeId,omitempty"`
}

// ApplyConfigChange is THE way a flag's configuration in an environment is
// edited. The PATCH handler, promotion between environments and scheduled
// changes all call it, so each gets the same validation (rules, rollout,
// fallthrough, segments that must exist), the same version bump and the same
// audit entry. The caller has already checked the role, in its own way: the
// route's guard for a request, or a re-check of the creator's current role
// for a scheduled change (see package scheduling). The caller publishes the
// Redis event after its transaction commits.
//
// Lock order: the segments the new rules name FOR SHARE, then the config row
// FOR UPDATE (see package segments). It returns pgx.ErrNoRows for an unknown flag.
func ApplyConfigChange(ctx context.Context, tx pgx.Tx, env rbac.Environment, flagKey string, actor Actor, req Change, opts ApplyOptions) (View, error) {
	knownSegments, err := lockSegments(ctx, tx, env.WorkspaceID, ruleSegmentRefs(req.TargetingRules))
	if err != nil {
		return View{}, err
	}
	var configID, variationsText, rulesText, rolloutText string
	var before configSnapshot
	var version int64
	err = tx.QueryRow(ctx, `
		SELECT fc.id::text, f.variations::text, fc.enabled, fc.targeting_rules::text,
		       COALESCE(fc.rollout::text, 'null'), fc.fallthrough_variation_id, fc.version
		FROM flag_configs fc
		JOIN flags f ON f.id = fc.flag_id
		WHERE f.key = $1 AND fc.environment_id = $2::uuid AND f.workspace_id = $3::uuid
		FOR UPDATE OF fc`,
		flagKey, env.ID, env.WorkspaceID,
	).Scan(&configID, &variationsText, &before.Enabled, &rulesText, &rolloutText, &before.FallthroughVariationID, &version)
	if err != nil {
		return View{}, err
	}
	if opts.ExpectedVersion != nil && *opts.ExpectedVersion != version {
		return View{}, ErrVersionConflict{Current: version}
	}
	before.TargetingRules = json.RawMessage(rulesText)
	before.Rollout = json.RawMessage(rolloutText)

	after, err := applyToSnapshot(before, variationsText, req, knownSegments)
	if err != nil {
		return View{}, err
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
		return View{}, err
	}

	if err := audit.Write(ctx, tx, audit.Entry{
		WorkspaceID:   env.WorkspaceID,
		ActorID:       actor.ID,
		ActorEmail:    actor.auditEmail(),
		EnvironmentID: env.ID,
		Action:        "flag.update",
		ResourceType:  "flag",
		ResourceID:    flagKey,
		Before:        before,
		After:         afterSnapshot{configSnapshot: after, PromotedFrom: opts.PromotedFrom, Via: actor.Via, ScheduledChangeID: opts.ScheduledChangeID},
	}); err != nil {
		return View{}, err
	}

	return scanFlagView(tx.QueryRow(ctx, selectFlagView+` WHERE f.key = $2 AND f.workspace_id = $3::uuid`,
		env.ID, flagKey, env.WorkspaceID), env.Key)
}

// applyToSnapshot validates req against the flag and returns the configuration
// it would produce. It writes nothing, so scheduling and promotion preview can
// use it to refuse early with the very same errors an edit would give.
func applyToSnapshot(before configSnapshot, variationsText string, req Change, knownSegments map[string]bool) (configSnapshot, error) {
	var variations []evaluation.Variation
	if err := json.Unmarshal([]byte(variationsText), &variations); err != nil {
		return configSnapshot{}, err
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
			return configSnapshot{}, validationError{err}
		}
		if err := checkSegmentsExist(rules, knownSegments); err != nil {
			return configSnapshot{}, validationError{err}
		}
		if after.TargetingRules, err = json.Marshal(rules); err != nil {
			return configSnapshot{}, err
		}
	}
	if req.Rollout != nil {
		rollout, err := parseRollout(req.Rollout, ids)
		if err != nil {
			return configSnapshot{}, validationError{err}
		}
		if after.Rollout, err = json.Marshal(rollout); err != nil { // a nil map marshals to null
			return configSnapshot{}, err
		}
	}
	if req.FallthroughVariationID != nil {
		if !ids[*req.FallthroughVariationID] {
			return configSnapshot{}, validationError{fmt.Errorf("fallthroughVariationId %s is not a variation of this flag", *req.FallthroughVariationID)}
		}
		after.FallthroughVariationID = *req.FallthroughVariationID
	}
	return after, nil
}

// KillFlag disables the flag in the environment at once: idempotent, and
// audited at severity=critical every time, even when it was already off. Like
// ApplyConfigChange it is shared by the kill route and by scheduled kills; the
// caller has checked the role (editor or above, in every environment).
func KillFlag(ctx context.Context, tx pgx.Tx, env rbac.Environment, flagKey string, actor Actor, scheduledChangeID string) (View, error) {
	var configID string
	var wasEnabled bool
	if err := tx.QueryRow(ctx, `
		SELECT fc.id::text, fc.enabled
		FROM flag_configs fc
		JOIN flags f ON f.id = fc.flag_id
		WHERE f.key = $1 AND fc.environment_id = $2::uuid AND f.workspace_id = $3::uuid
		FOR UPDATE OF fc`,
		flagKey, env.ID, env.WorkspaceID,
	).Scan(&configID, &wasEnabled); err != nil {
		return View{}, err
	}
	if wasEnabled {
		if _, err := tx.Exec(ctx, `
			UPDATE flag_configs
			SET enabled = false, version = version + 1, updated_at = now()
			WHERE id = $1::uuid`, configID,
		); err != nil {
			return View{}, err
		}
	}
	after := map[string]any{"enabled": false}
	if actor.Via != "" {
		after["via"] = actor.Via
	}
	if scheduledChangeID != "" {
		after["scheduledChangeId"] = scheduledChangeID
	}
	if err := audit.Write(ctx, tx, audit.Entry{
		WorkspaceID:   env.WorkspaceID,
		ActorID:       actor.ID,
		ActorEmail:    actor.auditEmail(),
		EnvironmentID: env.ID,
		Action:        "flag.kill",
		ResourceType:  "flag",
		ResourceID:    flagKey,
		Severity:      audit.SeverityCritical,
		Before:        map[string]bool{"enabled": wasEnabled},
		After:         after,
	}); err != nil {
		return View{}, err
	}
	return scanFlagView(tx.QueryRow(ctx, selectFlagView+` WHERE f.key = $2 AND f.workspace_id = $3::uuid`,
		env.ID, flagKey, env.WorkspaceID), env.Key)
}

// PublishChange is the best-effort Redis event after a committed change,
// for callers outside a request (the scheduler). Errors are logged only.
func (h *Handlers) PublishChange(ctx context.Context, env rbac.Environment, action string, v View) {
	h.publish(ctx, env.WorkspaceID, env.ID, action, v.Key, v.Config.Enabled, v.Config.Version)
}

// Inspection is what scheduling needs to know about a flag's configuration in
// an environment: which flag, its version, and the parts a change would touch
// as they are now (canonical JSON, comparable).
type Inspection struct {
	FlagID  string
	Version int64
	Touched map[string]json.RawMessage
}

// Inspect reads the configuration and, with validate set, refuses a change an
// edit would refuse (a bad rule, an unknown segment, a variation that does not
// exist) with the same errors, writing nothing. It returns pgx.ErrNoRows for an
// unknown flag.
func Inspect(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, env rbac.Environment, flagKey string, ch Change, validate bool) (Inspection, error) {
	var in Inspection
	if err := q.QueryRow(ctx, `SELECT f.id::text FROM flags f WHERE f.key = $1 AND f.workspace_id = $2::uuid`, flagKey, env.WorkspaceID).Scan(&in.FlagID); err != nil {
		return in, err
	}
	cur, variations, err := readConfig(ctx, q, env.WorkspaceID, env.ID, flagKey)
	if err != nil {
		return in, err
	}
	in.Version = cur.Version
	in.Touched = map[string]json.RawMessage{}
	if ch.TargetingRules != nil {
		in.Touched["targetingRules"] = canonicalJSON(cur.TargetingRules)
	}
	if ch.Rollout != nil {
		in.Touched["rollout"] = canonicalJSON(cur.Rollout)
	}
	if ch.FallthroughVariationID != nil {
		in.Touched["fallthroughVariationId"] = jsonString(cur.FallthroughVariationID)
	}
	if ch.Enabled != nil {
		in.Touched["enabled"] = jsonBool(cur.Enabled)
	}
	if validate {
		known, err := existingSegments(ctx, q, env.WorkspaceID, ruleSegmentRefs(ch.TargetingRules))
		if err != nil {
			return in, err
		}
		if _, err := applyToSnapshot(cur.configSnapshot, variations, ch, known); err != nil {
			return in, err
		}
	}
	return in, nil
}
