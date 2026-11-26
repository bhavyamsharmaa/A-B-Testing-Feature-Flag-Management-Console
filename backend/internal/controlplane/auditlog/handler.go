// Package auditlog is the read side of the audit log: GET
// /environments/{env}/audit-logs. Writing lives in package audit, which rbac
// imports, so this lives apart to avoid an import cycle. Nothing here writes.
package auditlog

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/httpx"
)

type Handlers struct {
	pool *pgxpool.Pool
}

func NewHandlers(pool *pgxpool.Pool) *Handlers {
	return &Handlers{pool: pool}
}

type entry struct {
	ID           int64           `json:"id"`
	CreatedAt    time.Time       `json:"createdAt"`
	ActorEmail   string          `json:"actorEmail"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resourceType"`
	ResourceID   string          `json:"resourceId"`
	Severity     string          `json:"severity"`
	DiffBefore   json.RawMessage `json:"diffBefore"`
	DiffAfter    json.RawMessage `json:"diffAfter"`
	// Scope is "global" for entries that belong to no single environment
	// (flag.create) and "environment" for everything else.
	Scope string `json:"scope"`
}

type listResponse struct {
	Entries    []entry `json:"entries"`
	NextCursor *int64  `json:"nextCursor"`
}

// List handles GET /environments/{envId}/audit-logs. It must run behind
// rbac.Guard.Require (viewer or above), which resolves the environment and
// answers 404 for unknown environments, other tenants' environments and
// environments the caller holds no role in.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	access := rbac.AccessFrom(r.Context())
	params, err := parseListParams(r.URL.Query())
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}

	query, args := buildListQuery(access.Env.WorkspaceID, access.Env.ID, params)
	rows, err := h.pool.Query(r.Context(), query, args...)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (entry, error) {
		var e entry
		var before, after string
		err := row.Scan(&e.ID, &e.CreatedAt, &e.ActorEmail, &e.Action, &e.ResourceType, &e.ResourceID, &e.Severity, &before, &after, &e.Scope)
		e.DiffBefore, e.DiffAfter = json.RawMessage(before), json.RawMessage(after)
		return e, err
	})
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}

	resp := listResponse{Entries: entries}
	if len(entries) > params.Limit { // the extra row only proves another page exists
		resp.Entries = entries[:params.Limit]
		next := resp.Entries[params.Limit-1].ID
		resp.NextCursor = &next
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, resp)
}
