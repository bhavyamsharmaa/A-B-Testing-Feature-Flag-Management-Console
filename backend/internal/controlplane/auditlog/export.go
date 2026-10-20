package auditlog

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/httpx"
)

// Export limits.
const (
	// MaxExportRange is the longest date range one export covers.
	MaxExportRange = 90 * 24 * time.Hour
	// DefaultExportRange is the range when only the end (or nothing) is given.
	DefaultExportRange = 30 * 24 * time.Hour
)

// MaxExportRows is the most rows one export may contain; more is refused up front (never silently cut). ExportBatch
// is how many rows are read at a time, which is what bounds memory. They are variables only so tests can shrink them.
var (
	MaxExportRows = 100_000
	ExportBatch   = 1000
)

// EXPORT. GET /environments/{envId}/audit-logs/export?format=csv|json&from=&to= streams the audit log of one
// environment (plus the workspace-wide entries every environment's log includes) for a date range. It is for admins
// and owners, session only, and it is rate limited. It uses the same scoping as the audit read route (the guard
// resolves the environment through the caller's membership, so another tenant's or a restricted caller's environment
// is a 404) and the same filters (workspace, then environment or workspace-wide), and it reads in keyset batches of
// 1,000 rows, so memory stays bounded however large the log. The export itself is audited, before any byte is sent.
//
// Everything written is untrusted text: a flag's name is chosen by a user and ends up in a spreadsheet. CSV cells that
// begin with = + - @ tab or carriage return are neutralised (CSV injection), quotes and newlines are escaped by
// encoding/csv, and secrets-shaped values are removed from the diffs (see scrubJSON) so a token, a hash, a webhook
// secret or an invite token that ever reached an audit entry cannot leave through an export.

type exportRow struct {
	ID           int64           `json:"id"`
	CreatedAt    string          `json:"createdAt"`
	ActorType    string          `json:"actorType"`
	Actor        string          `json:"actor"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resourceType"`
	ResourceID   string          `json:"resourceId"`
	Severity     string          `json:"severity"`
	Scope        string          `json:"scope"`
	DiffBefore   json.RawMessage `json:"diffBefore"`
	DiffAfter    json.RawMessage `json:"diffAfter"`
	ActorDetail  json.RawMessage `json:"actorDetail"`
}

var exportColumns = []string{"id", "createdAt", "actorType", "actor", "action", "resourceType", "resourceId", "severity", "scope", "diffBefore", "diffAfter", "actorDetail"}

// csvSafe neutralises a cell that a spreadsheet would run as a formula: a leading = + - @ tab or carriage return
// (or newline) gets a single quote in front, which the spreadsheet shows as text.
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r', '\n':
		return "'" + s
	}
	return s
}

// ---- secrets never leave ------------------------------------------------------------------------------------

var secretKeys = map[string]bool{
	"token": true, "tokens": true, "tokenhash": true, "token_hash": true, "plaintext": true, "secret": true, "secrets": true, "secretenc": true, "secret_enc": true,
	"previoussecret": true, "previous_secret_enc": true, "signingsecret": true, "password": true, "passwd": true, "authorization": true, "bearer": true,
	"hash": true, "keyhash": true, "key_hash": true, "apikey": true, "api_key": true, "sdkkey": true, "signature": true, "cookie": true, "jwt": true,
	"accesstoken": true, "refreshtoken": true, "invitetoken": true, "salt": true, "privatekey": true,
}

var secretValues = []*regexp.Regexp{
	regexp.MustCompile(`hlt_[0-9a-f]{8}_[A-Za-z0-9_-]{20,}`),                         // API tokens
	regexp.MustCompile(`whsec_[A-Za-z0-9_-]{16,}`),                                   // webhook signing secrets
	regexp.MustCompile(`hinv_[A-Za-z0-9_-]{16,}`),                                    // invite tokens
	regexp.MustCompile(`hsdk_[0-9a-f]{8}_[A-Za-z0-9_-]{16,}`),                        // SDK keys
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{12,}`),                       // an Authorization header's value
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{4,}`), // a JWT
	regexp.MustCompile(`\$argon2id\$[^\s"]+`),                                        // a password-style hash
}

const redacted = "[REDACTED]"

// scrubString removes secret-shaped substrings.
func scrubString(s string) string {
	for _, re := range secretValues {
		s = re.ReplaceAllString(s, redacted)
	}
	return s
}

func scrubValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if secretKeys[strings.ToLower(strings.ReplaceAll(k, "-", ""))] {
				x[k] = redacted
				continue
			}
			x[k] = scrubValue(val)
		}
		return x
	case []any:
		for i := range x {
			x[i] = scrubValue(x[i])
		}
		return x
	case string:
		return scrubString(x)
	}
	return v
}

// scrubJSON parses a JSON document, redacts values under secret-looking keys and secret-shaped strings anywhere,
// and re-serialises it. Text that is not JSON is scrubbed as a string.
func scrubJSON(raw string) json.RawMessage {
	if raw == "" || raw == "null" {
		return json.RawMessage("null")
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		b, _ := json.Marshal(scrubString(raw))
		return b
	}
	b, err := json.Marshal(scrubValue(v))
	if err != nil {
		return json.RawMessage(`"` + redacted + `"`)
	}
	return b
}

// ---- parameters ---------------------------------------------------------------------------------------------

type exportParams struct {
	Format   string
	From, To time.Time
}

func parseTime(s string, endOfDay bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("dates are RFC 3339 (2026-11-03T10:00:00Z) or YYYY-MM-DD")
	}
	if endOfDay {
		t = t.Add(24 * time.Hour) // a bare date as the end means the whole of that day
	}
	return t.UTC(), nil
}

func parseExportParams(q map[string][]string, now time.Time) (exportParams, error) {
	p := exportParams{Format: "csv", To: now}
	get := func(name string) (string, bool, error) {
		v, ok := q[name]
		if !ok {
			return "", false, nil
		}
		if len(v) > 1 {
			return "", false, fmt.Errorf("%s was given more than once", name)
		}
		return v[0], true, nil
	}
	if v, ok, err := get("format"); err != nil {
		return p, err
	} else if ok {
		if v != "csv" && v != "json" {
			return p, fmt.Errorf("format must be csv or json")
		}
		p.Format = v
	}
	if v, ok, err := get("to"); err != nil {
		return p, err
	} else if ok {
		t, err := parseTime(v, true)
		if err != nil {
			return p, err
		}
		p.To = t
	}
	p.From = p.To.Add(-DefaultExportRange)
	if v, ok, err := get("from"); err != nil {
		return p, err
	} else if ok {
		t, err := parseTime(v, false)
		if err != nil {
			return p, err
		}
		p.From = t
	}
	if !p.From.Before(p.To) {
		return p, fmt.Errorf("from must be before to")
	}
	if p.To.Sub(p.From) > MaxExportRange {
		return p, fmt.Errorf("the range is at most 90 days; ask for a shorter one and export again for the rest")
	}
	return p, nil
}

// ---- the handler --------------------------------------------------------------------------------------------

const exportWhere = `
	workspace_id = $1::uuid AND (environment_id = $2::uuid OR environment_id IS NULL) AND created_at >= $3 AND created_at < $4`

// Export handles GET /environments/{envId}/audit-logs/export (admin, session only).
func (h *Handlers) Export(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	now := time.Now()
	p, err := parseExportParams(r.URL.Query(), now)
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	var rows int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE`+exportWhere, access.Env.WorkspaceID, access.Env.ID, p.From, p.To).Scan(&rows); err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	if rows > MaxExportRows {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "EXPORT_TOO_LARGE",
			fmt.Sprintf("that range holds %d entries and one export is at most %d; choose a shorter range", rows, MaxExportRows))
		return
	}
	// The export is itself an audited event, written before the first byte leaves.
	err = pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: access.Env.WorkspaceID, ActorID: access.User.ID, ActorEmail: access.User.Email, EnvironmentID: access.Env.ID,
			Action: "audit.export", ResourceType: "audit_log", ResourceID: access.Env.Key, Severity: audit.SeverityCritical,
			After: map[string]any{"format": p.Format, "from": p.From.Format(time.RFC3339), "to": p.To.Format(time.RFC3339), "rows": rows},
		})
	})
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}

	name := fmt.Sprintf("audit-%s-%s-to-%s.%s", access.Env.Key, p.From.Format("20060102"), p.To.Format("20060102"), p.Format)
	h2 := w.Header()
	h2.Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(name)+`"`)
	h2.Set("Cache-Control", "no-store")
	h2.Set("X-Content-Type-Options", "nosniff")
	h2.Set("X-Export-Rows", fmt.Sprint(rows))
	if p.Format == "json" {
		h2.Set("Content-Type", "application/json; charset=utf-8")
	} else {
		h2.Set("Content-Type", "text/csv; charset=utf-8")
	}
	w.WriteHeader(http.StatusOK)
	// From here the status is sent: a failure ends the stream (the client sees a truncated file and no trailer).
	if err := h.stream(ctx, w, access, p); err != nil {
		panic(http.ErrAbortHandler) // abort the connection so a truncated export cannot be mistaken for a complete one
	}
}

func sanitizeFilename(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, s)
}

func (h *Handlers) stream(ctx context.Context, w http.ResponseWriter, access rbac.Access, p exportParams) error {
	flusher, _ := w.(http.Flusher)
	var cw *csv.Writer
	var first = true
	if p.Format == "csv" {
		cw = csv.NewWriter(w)
		if err := cw.Write(exportColumns); err != nil {
			return err
		}
	} else if _, err := w.Write([]byte("[")); err != nil {
		return err
	}
	var last int64
	total := 0
	for total < MaxExportRows {
		rows, err := h.pool.Query(ctx, `
			SELECT id, created_at, actor_type, actor_email, action, resource_type, resource_id, severity,
			       CASE WHEN environment_id IS NULL THEN 'global' ELSE 'environment' END,
			       COALESCE(diff_before::text, 'null'), COALESCE(diff_after::text, 'null'), COALESCE(actor_detail::text, 'null')
			FROM audit_logs WHERE`+exportWhere+` AND id > $5
			ORDER BY id LIMIT $6`, access.Env.WorkspaceID, access.Env.ID, p.From, p.To, last, ExportBatch)
		if err != nil {
			return err
		}
		batch := 0
		for rows.Next() {
			var e exportRow
			var at time.Time
			var before, after, detail string
			if err := rows.Scan(&e.ID, &at, &e.ActorType, &e.Actor, &e.Action, &e.ResourceType, &e.ResourceID, &e.Severity, &e.Scope, &before, &after, &detail); err != nil {
				rows.Close()
				return err
			}
			e.CreatedAt = at.UTC().Format(time.RFC3339)
			e.Actor, e.Action, e.ResourceType, e.ResourceID = scrubString(e.Actor), scrubString(e.Action), scrubString(e.ResourceType), scrubString(e.ResourceID)
			e.DiffBefore, e.DiffAfter, e.ActorDetail = scrubJSON(before), scrubJSON(after), scrubJSON(detail)
			last = e.ID
			batch++
			total++
			if cw != nil {
				rec := []string{fmt.Sprint(e.ID), e.CreatedAt, csvSafe(e.ActorType), csvSafe(e.Actor), csvSafe(e.Action), csvSafe(e.ResourceType), csvSafe(e.ResourceID),
					csvSafe(e.Severity), csvSafe(e.Scope), csvSafe(string(e.DiffBefore)), csvSafe(string(e.DiffAfter)), csvSafe(string(e.ActorDetail))}
				if err := cw.Write(rec); err != nil {
					rows.Close()
					return err
				}
			} else {
				b, _ := json.Marshal(e)
				if !first {
					b = append([]byte(","), b...)
				}
				first = false
				if _, err := w.Write(b); err != nil {
					rows.Close()
					return err
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if cw != nil {
			cw.Flush()
			if err := cw.Error(); err != nil {
				return err
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		if batch < ExportBatch {
			break
		}
	}
	if cw == nil {
		_, err := w.Write([]byte("]"))
		return err
	}
	return nil
}
