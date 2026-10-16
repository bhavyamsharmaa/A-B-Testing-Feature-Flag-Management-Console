package experiments

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/controlplane/webhooks"
)

// emitLifecycle queues experiment.started / experiment.stopped in the caller's transaction:
// the experiment's key and name, its flag's key, and the status change. Never metrics or results.
func emitLifecycle(ctx context.Context, tx pgx.Tx, access rbac.Access, typ, key, from, to string) error {
	var name, flagKey string
	if err := tx.QueryRow(ctx, `
		SELECT x.name, f.key FROM experiments x JOIN flags f ON f.id = x.flag_id AND f.workspace_id = x.workspace_id
		WHERE x.environment_id = $1::uuid AND x.key = $2`, access.Env.ID, key).Scan(&name, &flagKey); err != nil {
		return err
	}
	return webhooks.Emit(ctx, tx, webhooks.Event{
		Type: typ, WorkspaceID: access.Env.WorkspaceID, EnvironmentID: access.Env.ID, EnvironmentKey: access.Env.Key,
		Actor: webhooks.ActorFrom(ctx, access.User.ID, ""),
		Data: map[string]any{
			"experiment": map[string]string{"key": key, "name": name},
			"flag":       map[string]string{"key": flagKey},
			"status":     map[string]string{"before": from, "after": to},
		},
	}, time.Now())
}
