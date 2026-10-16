package flags

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/controlplane/webhooks"
)

// emit queues a webhook event in the caller's transaction (see package webhooks).
func emit(ctx context.Context, tx pgx.Tx, env rbac.Environment, typ string, actor webhooks.Actor, data map[string]any) error {
	return webhooks.Emit(ctx, tx, webhooks.Event{
		Type: typ, WorkspaceID: env.WorkspaceID, EnvironmentID: env.ID, EnvironmentKey: env.Key, Actor: actor, Data: data,
	}, time.Now())
}

// eventActor: the API token if the request came from one, else the person, "via" the
// mechanism when it is not their own hand.
func eventActor(ctx context.Context, a Actor, scheduledChangeID string) webhooks.Actor {
	out := webhooks.ActorFrom(ctx, a.ID, a.Via)
	if out.Type == "user" {
		out.ScheduledChangeID = scheduledChangeID
	}
	return out
}

func countRules(raw json.RawMessage) int {
	var rules []json.RawMessage
	if json.Unmarshal(raw, &rules) != nil {
		return 0
	}
	return len(rules)
}

// smallJSON returns raw as a value if it is short (a rollout is a map of variation ids to
// numbers), otherwise nil with ok=false so only a "changed" marker is sent.
func smallJSON(raw json.RawMessage) (any, bool) {
	if len(raw) > 512 {
		return nil, false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	return v, true
}

// configChanges is the compact before/after of what an edit changed. Targeting rules appear
// only as a changed marker and a rule count, never their contents (attributes, values,
// segments): a webhook is for knowing THAT and roughly WHAT changed, and the audit log and
// console are where the rules are read.
func configChanges(before, after configSnapshot) map[string]webhooks.Change {
	out := map[string]webhooks.Change{}
	if before.Enabled != after.Enabled {
		out["enabled"] = webhooks.Change{Before: before.Enabled, After: after.Enabled}
	}
	if before.FallthroughVariationID != after.FallthroughVariationID {
		out["fallthroughVariationId"] = webhooks.Change{Before: before.FallthroughVariationID, After: after.FallthroughVariationID}
	}
	if string(canonicalJSON(before.Rollout)) != string(canonicalJSON(after.Rollout)) {
		b, bok := smallJSON(before.Rollout)
		a, aok := smallJSON(after.Rollout)
		if bok && aok {
			out["rollout"] = webhooks.Change{Before: b, After: a}
		} else {
			out["rollout"] = webhooks.Change{Before: "changed", After: "changed"}
		}
	}
	if string(canonicalJSON(before.TargetingRules)) != string(canonicalJSON(after.TargetingRules)) {
		out["targetingRules"] = webhooks.Change{Before: map[string]int{"ruleCount": countRules(before.TargetingRules)}, After: map[string]int{"ruleCount": countRules(after.TargetingRules)}}
	}
	return out
}
