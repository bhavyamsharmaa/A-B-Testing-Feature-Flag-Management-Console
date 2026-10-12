package segments

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/dataplane/evaluation"
	"helios/backend/internal/platform/httpx"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// referencing returns the JSON containment patterns that match a flag
// configuration whose targeting rules reference the segment with either
// operator. jsonb @> on arrays means "some rule has some clause containing
// this", so it needs no scan of the rules in Go.
func referencing(segKey string) (inSegment, notInSegment string) {
	mk := func(op string) string {
		b, _ := json.Marshal([]any{map[string]any{"clauses": []any{map[string]any{"operator": op, "value": segKey}}}})
		return string(b)
	}
	return mk(evaluation.OpInSegment), mk(evaluation.OpNotInSegment)
}

// bumpFlagConfigs raises the version of every flag configuration (flag in
// environment) whose rules reference the segment, because who they serve has
// changed. The rows are locked in id order, so two segment changes touching
// the same flags cannot deadlock. The caller holds the segment's row lock.
func bumpFlagConfigs(ctx context.Context, tx pgx.Tx, workspaceID, segKey string) ([]affected, error) {
	in, notIn := referencing(segKey)
	rows, err := tx.Query(ctx, `
		SELECT fc.id::text FROM flag_configs fc
		WHERE fc.workspace_id = $1::uuid AND (fc.targeting_rules @> $2::jsonb OR fc.targeting_rules @> $3::jsonb)
		ORDER BY fc.id FOR UPDATE OF fc`, workspaceID, in, notIn)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	rows, err = tx.Query(ctx, `
		WITH bumped AS (
			UPDATE flag_configs SET version = version + 1, updated_at = now()
			WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[])
			RETURNING flag_id, environment_id
		)
		SELECT b.environment_id::text, f.key FROM bumped b
		JOIN flags f ON f.id = b.flag_id AND f.workspace_id = $1::uuid
		ORDER BY f.key, b.environment_id`, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (affected, error) {
		var a affected
		return a, r.Scan(&a.EnvironmentID, &a.FlagKey)
	})
}

type flagUse struct {
	FlagKey      string           `json:"flagKey"`
	FlagName     string           `json:"flagName"`
	Environments []environmentUse `json:"environments"`
}

type environmentUse struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

type usageView struct {
	SegmentKey string    `json:"segmentKey"`
	Flags      []flagUse `json:"flags"`
}

// usageOf lists the flags whose rules in some environment reference the segment.
func usageOf(ctx context.Context, q queryer, workspaceID, segKey string) ([]flagUse, error) {
	in, notIn := referencing(segKey)
	rows, err := q.Query(ctx, `
		SELECT f.key, f.name, e.key, fc.enabled
		FROM flag_configs fc
		JOIN flags f ON f.id = fc.flag_id AND f.workspace_id = fc.workspace_id
		JOIN environments e ON e.id = fc.environment_id AND e.workspace_id = fc.workspace_id
		WHERE fc.workspace_id = $1::uuid AND (fc.targeting_rules @> $2::jsonb OR fc.targeting_rules @> $3::jsonb)
		ORDER BY f.key, e.key`, workspaceID, in, notIn)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []flagUse{}
	for rows.Next() {
		var flagKey, flagName string
		var env environmentUse
		if err := rows.Scan(&flagKey, &flagName, &env.Key, &env.Enabled); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].FlagKey != flagKey {
			out = append(out, flagUse{FlagKey: flagKey, FlagName: flagName})
		}
		out[len(out)-1].Environments = append(out[len(out)-1].Environments, env)
	}
	return out, rows.Err()
}

// Usage handles GET /workspaces/{wsId}/segments/{segKey}/usage.
func (h *Handlers) Usage(w http.ResponseWriter, r *http.Request) {
	ws := rbac.WorkspaceAccessFrom(r.Context())
	key := r.PathValue("segKey")
	var one int
	if err := h.pool.QueryRow(r.Context(), `SELECT 1 FROM segments WHERE workspace_id = $1::uuid AND key = $2`, ws.WorkspaceID, key).Scan(&one); err != nil {
		writeFailure(w, r, err)
		return
	}
	uses, err := usageOf(r.Context(), h.pool, ws.WorkspaceID, key)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, usageView{SegmentKey: key, Flags: uses})
}

type errInUse struct{ uses []flagUse }

func (e errInUse) Error() string { return "in use" }

func describeUses(uses []flagUse) string {
	parts := make([]string, 0, len(uses))
	for _, u := range uses {
		envs := make([]string, 0, len(u.Environments))
		for _, e := range u.Environments {
			envs = append(envs, e.Key)
		}
		parts = append(parts, u.FlagKey+" ("+strings.Join(envs, ", ")+")")
	}
	return strings.Join(parts, "; ")
}

// Delete handles DELETE /workspaces/{wsId}/segments/{segKey}. A segment that
// flag rules still reference is refused with 409 SEGMENT_IN_USE, naming the
// flags, unless ?confirm=true. Confirmed, it is deleted and audited as
// critical; the rules that name it then match nobody (inSegment and
// notInSegment alike), and their flags' versions are bumped.
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := rbac.WorkspaceAccessFrom(ctx)
	key := r.PathValue("segKey")
	confirm := r.URL.Query().Get("confirm") == "true"

	var hit []affected
	var gone int64
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		row, err := scanSegment(tx.QueryRow(ctx, selectSegment+` WHERE s.workspace_id = $1::uuid AND s.key = $2 FOR UPDATE OF s`, ws.WorkspaceID, key))
		if err != nil {
			return err
		}
		uses, err := usageOf(ctx, tx, ws.WorkspaceID, key)
		if err != nil {
			return err
		}
		if len(uses) > 0 && !confirm {
			return errInUse{uses}
		}
		if hit, err = bumpFlagConfigs(ctx, tx, ws.WorkspaceID, key); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM segments WHERE id = $1::uuid AND workspace_id = $2::uuid`, row.ID, ws.WorkspaceID); err != nil {
			return err
		}
		gone = row.View.Version + 1
		action, severity := "segment.delete", audit.SeverityInfo
		if len(uses) > 0 {
			action, severity = "segment.force_delete", audit.SeverityCritical
		}
		before := summaryOf(row.View)
		n := len(hit)
		before.FlagsUsing = &n
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: ws.WorkspaceID, ActorID: ws.User.ID, ActorEmail: ws.User.Email,
			Action: action, ResourceType: "segment", ResourceID: key, Severity: severity, Before: before,
		})
	})
	var inUse errInUse
	switch {
	case errors.As(err, &inUse):
		httpx.WriteError(w, http.StatusConflict, "SEGMENT_IN_USE",
			"segment is used by flag rules in: "+describeUses(inUse.uses)+"; remove it from those rules first, or pass ?confirm=true to delete it anyway (those rules will then match nobody)")
	case err != nil:
		writeFailure(w, r, err)
	default:
		h.publish(ctx, ws.WorkspaceID, "segment_deleted", key, gone, hit)
		w.WriteHeader(http.StatusNoContent)
	}
}
