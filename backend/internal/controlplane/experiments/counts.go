package experiments

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/httpx"
)

type eventNameCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type exposureCount struct {
	VariationID string `json:"variationId"`
	Count       int64  `json:"count"`
}

// EventCounts handles GET /environments/{envId}/experiments/{key}/event-counts:
// how much data has arrived for an experiment, so a user can see it flowing.
// These are raw counts, not results: no rates, no significance.
func (h *Handlers) EventCounts(w http.ResponseWriter, r *http.Request) {
	access := rbac.AccessFrom(r.Context())
	key := r.PathValue("key")

	var expID, variations string
	err := h.pool.QueryRow(r.Context(), `
		SELECT x.id::text, f.variations::text
		FROM experiments x JOIN flags f ON f.id = x.flag_id
		WHERE x.workspace_id = $1::uuid AND x.environment_id = $2::uuid AND x.key = $3`,
		access.Env.WorkspaceID, access.Env.ID, key).Scan(&expID, &variations)
	if errors.Is(err, pgx.ErrNoRows) {
		writeLifecycleError(w, r, key, errNotFound{})
		return
	}
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	var vs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(variations), &vs); err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}

	byVariation := map[string]int64{}
	rows, err := h.pool.Query(r.Context(), `
		SELECT variation_id, count(*) FROM events
		WHERE workspace_id = $1::uuid AND experiment_id = $2::uuid AND event_name = $3
		GROUP BY variation_id`, access.Env.WorkspaceID, expID, ExposureEvent)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
		byVariation[id] = n
	}
	if err := rows.Err(); err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	rows.Close()

	// Every variation of the flag is listed, zero included, in the flag's order.
	exposures := make([]exposureCount, 0, len(vs))
	var exposureTotal int64
	for _, v := range vs {
		exposures = append(exposures, exposureCount{VariationID: v.ID, Count: byVariation[v.ID]})
		exposureTotal += byVariation[v.ID]
	}

	var events int64
	var last *time.Time
	if err := h.pool.QueryRow(r.Context(), `
		SELECT count(*), max(created_at) FROM events
		WHERE workspace_id = $1::uuid AND experiment_id = $2::uuid AND event_name <> $3`,
		access.Env.WorkspaceID, expID, ExposureEvent).Scan(&events, &last); err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	// The event names that exist for this experiment (what a results metric can
	// be chosen from), most frequent first, capped. Uses idx_events_results.
	names := []eventNameCount{}
	nameRows, err := h.pool.Query(r.Context(), `
		SELECT event_name, count(*) FROM events
		WHERE workspace_id = $1::uuid AND experiment_id = $2::uuid AND event_name <> $3
		GROUP BY event_name ORDER BY count(*) DESC, event_name LIMIT 50`,
		access.Env.WorkspaceID, expID, ExposureEvent)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	defer nameRows.Close()
	for nameRows.Next() {
		var n eventNameCount
		if err := nameRows.Scan(&n.Name, &n.Count); err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
		names = append(names, n)
	}
	if err := nameRows.Err(); err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"eventNames":    names,
		"exposures":     exposures,
		"exposureTotal": exposureTotal,
		"events":        events,
		"lastEventAt":   last,
	})
}
