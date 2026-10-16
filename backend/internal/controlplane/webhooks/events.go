package webhooks

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/platform/auth"
)

// Event types a webhook can subscribe to. The payload of each is documented in
// docs/WEBHOOKS.md. Changing the SHAPE of an event's data bumps SchemaVersion.
const (
	EventFlagCreated             = "flag.created"
	EventFlagUpdated             = "flag.updated"
	EventFlagKilled              = "flag.killed"
	EventFlagDeleted             = "flag.deleted"
	EventExperimentStarted       = "experiment.started"
	EventExperimentStopped       = "experiment.stopped"
	EventPromotionApplied        = "promotion.applied"
	EventScheduledChangeExecuted = "scheduled_change.executed"
	EventScheduledChangeFailed   = "scheduled_change.failed"
	// EventPing is the test event; it cannot be subscribed to and is sent only on request.
	EventPing = "webhook.ping"
)

// SchemaVersion is the version of the envelope and of every event's data.
const SchemaVersion = 1

// MaxEventBytes caps one event body. Over it, the variable part of the data is
// dropped and "truncated": true is set.
const MaxEventBytes = 16 << 10

// Types lists the subscribable events in documentation order.
var Types = []string{
	EventFlagCreated, EventFlagUpdated, EventFlagKilled, EventFlagDeleted,
	EventExperimentStarted, EventExperimentStopped, EventPromotionApplied,
	EventScheduledChangeExecuted, EventScheduledChangeFailed,
}

// ValidType reports whether t is subscribable.
func ValidType(t string) bool {
	for _, x := range Types {
		if x == t {
			return true
		}
	}
	return false
}

// Actor is who caused an event: an id, never an email address. For a person it
// is their user id; for an API token the token's id and name (the name is the
// admin's own label); "via" names a mechanism such as "schedule".
type Actor struct {
	Type              string `json:"type"` // user | api_token
	ID                string `json:"id,omitempty"`
	Name              string `json:"name,omitempty"`
	Via               string `json:"via,omitempty"`
	ScheduledChangeID string `json:"scheduledChangeId,omitempty"`
}

// ActorFrom is the actor of the request in ctx: the API token if one made it,
// otherwise the user. A token is never recorded as the person.
func ActorFrom(ctx context.Context, userID, via string) Actor {
	if tok, ok := auth.APITokenFrom(ctx); ok {
		return Actor{Type: "api_token", ID: tok.ID, Name: tok.Name}
	}
	return Actor{Type: "user", ID: userID, Via: via}
}

// Event is one thing that happened, before it is fanned out to endpoints.
type Event struct {
	Type           string
	WorkspaceID    string
	EnvironmentID  string
	EnvironmentKey string
	Actor          Actor
	// Data: ids, names and compact before/after of changed fields. NEVER SDK keys, tokens,
	// member emails or targeting-rule contents. See the builders below.
	Data map[string]any
}

// Emit queues the event for every enabled endpoint of the workspace that
// subscribes to its type and (if filtered) its environment, in the caller's
// transaction. With no endpoints it costs one indexed query. It never fails
// the caller's change for a reason that is not a database error: an oversized
// event is shrunk, a full queue drops the oldest (see outbox).
func Emit(ctx context.Context, tx pgx.Tx, ev Event, now time.Time) error {
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM webhook_endpoints
		WHERE workspace_id = $1::uuid AND enabled AND $2 = ANY(event_types)
		  AND (environment_id IS NULL OR environment_id = NULLIF($3, '')::uuid)
		ORDER BY created_at, id`, ev.WorkspaceID, ev.Type, ev.EnvironmentID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) == 0 {
		return err
	}
	var eventID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&eventID); err != nil {
		return err
	}
	env := Envelope(eventID, ev, now)
	for _, id := range ids {
		if _, err := outbox.Enqueue(ctx, tx, outbox.Item{
			WorkspaceID: ev.WorkspaceID, Kind: outbox.KindWebhook, EventType: ev.Type, EventID: eventID,
			EndpointID: id, TargetKey: id, Payload: env,
		}, now); err != nil {
			return err
		}
	}
	return nil
}

// Envelope is the JSON body of a delivery.
func Envelope(eventID string, ev Event, now time.Time) map[string]any {
	env := map[string]any{
		"id":          eventID,
		"type":        ev.Type,
		"version":     SchemaVersion,
		"createdAt":   now.UTC().Format(time.RFC3339),
		"workspaceId": ev.WorkspaceID,
		"actor":       ev.Actor,
		"data":        ev.Data,
	}
	if ev.EnvironmentID != "" {
		env["environment"] = map[string]string{"id": ev.EnvironmentID, "key": ev.EnvironmentKey}
	}
	if b, _ := json.Marshal(env); len(b) > MaxEventBytes {
		small := map[string]any{"truncated": true}
		for _, k := range []string{"flag", "experiment", "scheduledChange", "promotion"} {
			if v, ok := ev.Data[k]; ok {
				small[k] = v
			}
		}
		env["data"] = small
	}
	return env
}

// ---- builders: the only places event data is assembled -------------------------------------

// FlagRef identifies a flag. Name is the flag's display name (not a person's).
type FlagRef struct {
	Key  string `json:"key"`
	Name string `json:"name,omitempty"`
}

// Change is one changed field. Values are small scalars or identifier maps.
type Change struct {
	Before any `json:"before"`
	After  any `json:"after"`
}

// FlagData is the data of flag.created / flag.deleted.
func FlagData(f FlagRef) map[string]any { return map[string]any{"flag": f} }

// FlagUpdatedData: the flag, the configuration version before and after, and the
// compact changes. Targeting rules appear only as a changed marker and a rule
// count: never their contents (attributes, values, segment lists).
func FlagUpdatedData(f FlagRef, versionBefore, versionAfter int64, changes map[string]Change, extra map[string]any) map[string]any {
	d := map[string]any{"flag": f, "version": map[string]int64{"before": versionBefore, "after": versionAfter}, "changes": changes}
	for k, v := range extra {
		d[k] = v
	}
	return d
}
