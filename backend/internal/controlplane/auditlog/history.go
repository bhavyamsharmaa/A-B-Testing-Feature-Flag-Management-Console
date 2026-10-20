package auditlog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/httpx"
)

// PER-FLAG HISTORY. GET /environments/{envId}/flags/{key}/history is the flag drawer's History tab: the changes to
// ONE flag in this environment (plus the workspace-wide events of that flag, its creation and deletion), newest
// first, paged by id, with who did it (a person, an API token, or a schedule acting for a person), when, and a
// readable before/after of exactly the fields that changed. It is read only and open to any role that can see the
// flag, as the audit log is. It reads the audit log, so it can show nothing the audit log does not hold, and it
// scopes by workspace and environment the same way.

type historyChange struct {
	Field  string   `json:"field"`
	Label  string   `json:"label"`
	Before []string `json:"before"`
	After  []string `json:"after"`
}

type historyActor struct {
	// Type is user, api_token or schedule (a person's scheduled change, run later).
	Type  string `json:"type"`
	Label string `json:"label"`
	// For a token, the person who made the token; for a schedule, the person who scheduled it.
	OnBehalfOf string `json:"onBehalfOf,omitempty"`
}

type historyEntry struct {
	ID        int64           `json:"id"`
	CreatedAt time.Time       `json:"createdAt"`
	Action    string          `json:"action"`
	Summary   string          `json:"summary"`
	Severity  string          `json:"severity"`
	Actor     historyActor    `json:"actor"`
	Changes   []historyChange `json:"changes"`
	// Source: "promotion from <env>" or "scheduled change", when the edit came that way.
	Source string `json:"source,omitempty"`
}

var historyActions = []string{"flag.create", "flag.update", "flag.kill", "flag.delete", "flag.force_delete"}

// History handles GET /environments/{envId}/flags/{key}/history (any role).
func (h *Handlers) History(w http.ResponseWriter, r *http.Request) {
	access := rbac.AccessFrom(r.Context())
	key := r.PathValue("key")
	if key == "" || len(key) > 128 {
		httpx.BadRequest(w, "flag key is required")
		return
	}
	q := r.URL.Query()
	limit, before := 20, int64(0)
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			httpx.BadRequest(w, "limit must be 1 to 100")
			return
		}
		limit = n
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			httpx.BadRequest(w, "before must be a positive integer (an entry id)")
			return
		}
		before = n
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, created_at, actor_type, actor_email, actor_detail::text, action, severity,
		       COALESCE(diff_before::text, 'null'), COALESCE(diff_after::text, 'null')
		FROM audit_logs
		WHERE workspace_id = $1::uuid AND (environment_id = $2::uuid OR environment_id IS NULL)
		  AND resource_type = 'flag' AND resource_id = $3 AND action = ANY($4) AND ($5 = 0 OR id < $5)
		ORDER BY id DESC LIMIT $6`, access.Env.WorkspaceID, access.Env.ID, key, historyActions, before, limit+1)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	type rec struct {
		id                       int64
		at                       time.Time
		actorType, actor, detail string
		action, severity         string
		before, after            string
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (rec, error) {
		var x rec
		var detail *string
		err := row.Scan(&x.id, &x.at, &x.actorType, &x.actor, &detail, &x.action, &x.severity, &x.before, &x.after)
		if detail != nil {
			x.detail = *detail
		}
		return x, err
	})
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	var next *int64
	if len(list) > limit {
		list = list[:limit]
		n := list[limit-1].id
		next = &n
	}
	out := make([]historyEntry, 0, len(list))
	for _, x := range list {
		out = append(out, describeEntry(x.id, x.at, x.actorType, x.actor, x.detail, x.action, x.severity, x.before, x.after))
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"entries": out, "nextCursor": next})
}

// describeEntry turns one audit row into a readable history entry. It never trusts the shape of the stored JSON.
func describeEntry(id int64, at time.Time, actorType, actor, detail, action, severity, beforeRaw, afterRaw string) historyEntry {
	e := historyEntry{ID: id, CreatedAt: at.UTC(), Action: action, Severity: severity, Changes: []historyChange{}}
	var before, after map[string]any
	_ = json.Unmarshal([]byte(beforeRaw), &before)
	_ = json.Unmarshal([]byte(afterRaw), &after)

	// who
	switch {
	case actorType == "api_token":
		e.Actor = historyActor{Type: "api_token", Label: scrubString(actor)}
		var d struct {
			CreatedBy string `json:"createdBy"`
		}
		_ = json.Unmarshal([]byte(detail), &d)
		e.Actor.OnBehalfOf = scrubString(d.CreatedBy)
	case after != nil && after["via"] == "schedule":
		e.Actor = historyActor{Type: "schedule", Label: "Scheduled change", OnBehalfOf: scrubString(strings.TrimSuffix(actor, " via schedule"))}
	default:
		e.Actor = historyActor{Type: "user", Label: scrubString(actor)}
	}
	if after != nil {
		if from, ok := after["promotedFrom"].(string); ok && from != "" {
			e.Source = "promotion from " + scrubString(from)
		} else if _, ok := after["scheduledChangeId"].(string); ok {
			e.Source = "scheduled change"
		}
	}

	switch action {
	case "flag.create":
		e.Summary = "Flag created"
		if name, ok := after["name"].(string); ok {
			e.Changes = append(e.Changes, historyChange{Field: "name", Label: "Name", Before: []string{}, After: []string{scrubString(name)}})
		}
	case "flag.delete", "flag.force_delete":
		e.Summary = "Flag deleted"
		if action == "flag.force_delete" {
			e.Summary = "Flag deleted (forced)"
		}
	case "flag.kill":
		e.Summary = "Kill switch used"
		e.Changes = append(e.Changes, historyChange{Field: "enabled", Label: "Enabled", Before: []string{onOff(before["enabled"])}, After: []string{"off"}})
	default: // flag.update
		e.Changes = diffConfig(before, after)
		switch {
		case e.Source != "" && strings.HasPrefix(e.Source, "promotion"):
			e.Summary = "Configuration promoted"
		case len(e.Changes) == 0:
			e.Summary = "Saved with no change"
		default:
			e.Summary = "Configuration changed"
		}
	}
	return e
}

func onOff(v any) string {
	if b, ok := v.(bool); ok && b {
		return "on"
	}
	return "off"
}

// diffConfig lists the fields that differ between two configuration snapshots, each as readable lines.
func diffConfig(before, after map[string]any) []historyChange {
	type field struct{ key, label string }
	out := []historyChange{} // never null in the JSON
	for _, f := range []field{{"enabled", "Enabled"}, {"fallthroughVariationId", "Default variation"}, {"rollout", "Rollout"}, {"targetingRules", "Targeting rules"}} {
		b, a := render(f.key, before[f.key]), render(f.key, after[f.key])
		if strings.Join(b, "\n") == strings.Join(a, "\n") {
			continue
		}
		out = append(out, historyChange{Field: f.key, Label: f.label, Before: b, After: a})
	}
	return out
}

// render shows a config field as short lines of text. Values are scrubbed like the export's.
func render(key string, v any) []string {
	switch key {
	case "enabled":
		return []string{onOff(v)}
	case "fallthroughVariationId":
		s, _ := v.(string)
		return []string{scrubString(s)}
	case "rollout":
		m, ok := v.(map[string]any)
		if !ok || len(m) == 0 {
			return []string{"none"}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines := make([]string, 0, len(keys))
		for _, k := range keys {
			n, _ := m[k].(float64)
			lines = append(lines, fmt.Sprintf("%s: %.1f%%", scrubString(k), n/1000))
		}
		return lines
	case "targetingRules":
		rules, _ := v.([]any)
		if len(rules) == 0 {
			return []string{"no rules"}
		}
		lines := make([]string, 0, len(rules))
		for i, r := range rules {
			lines = append(lines, fmt.Sprintf("%d. %s", i+1, scrubString(describeRule(r))))
		}
		return lines
	}
	return nil
}

func describeRule(r any) string {
	m, _ := r.(map[string]any)
	clauses, _ := m["clauses"].([]any)
	parts := make([]string, 0, len(clauses))
	for _, c := range clauses {
		cm, _ := c.(map[string]any)
		attr, _ := cm["attribute"].(string)
		op, _ := cm["operator"].(string)
		val, _ := json.Marshal(cm["value"])
		s := strings.TrimSpace(attr + " " + op + " " + string(val))
		if neg, _ := cm["negate"].(bool); neg {
			s = "not (" + s + ")"
		}
		parts = append(parts, s)
	}
	to, _ := m["variationId"].(string)
	if len(parts) == 0 {
		return "always → " + to
	}
	return "if " + strings.Join(parts, " and ") + " → " + to
}
