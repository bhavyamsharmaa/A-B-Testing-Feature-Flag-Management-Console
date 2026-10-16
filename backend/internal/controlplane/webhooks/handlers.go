// Package webhooks lets a workspace tell other systems about changes: endpoint
// management for admins, event emission into the shared outbox, signing, and the
// delivery itself. See docs/WEBHOOKS.md.
//
// WHO MAY DO WHAT. Creating, changing, rotating, testing and deleting endpoints,
// and reading their delivery log, needs a workspace admin or owner and a browser
// session: the routes are classified token-denied, so an API token cannot read a
// signing secret's hint, register an exfiltration endpoint or learn what the
// workspace is subscribed to.
package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/httpx"
	"helios/backend/internal/platform/netguard"
	"helios/backend/internal/platform/secretbox"
)

// Handlers serves the management routes.
type Handlers struct {
	pool   *pgxpool.Pool
	box    *secretbox.Box
	guard  *netguard.Guard
	worker *outbox.Worker
	now    func() time.Time
}

// NewHandlers. worker is used to send test pings and resends at once; it must
// have this package's Sender for KindWebhook.
func NewHandlers(pool *pgxpool.Pool, box *secretbox.Box, guard *netguard.Guard, worker *outbox.Worker, now func() time.Time) *Handlers {
	if now == nil {
		now = time.Now
	}
	return &Handlers{pool: pool, box: box, guard: guard, worker: worker, now: now}
}

type refusal struct {
	status        int
	code, message string
}

func (e refusal) Error() string { return e.message }

func bad(msg string) error { return refusal{http.StatusBadRequest, "INVALID_REQUEST", msg} }

func fail(w http.ResponseWriter, r *http.Request, err error) {
	var ref refusal
	switch {
	case errors.As(err, &ref):
		httpx.WriteError(w, ref.status, ref.code, ref.message)
	case quota.WriteError(w, err):
	default:
		httpx.WriteInternal(w, r, err)
	}
}

func (h *Handlers) requireKey(w http.ResponseWriter) bool {
	if h.box.Available() {
		return true
	}
	httpx.WriteError(w, http.StatusServiceUnavailable, "WEBHOOKS_UNAVAILABLE", "webhooks are not available: no encryption key is configured on the server (WEBHOOK_SECRET_KEY)")
	return false
}

// ---- views ------------------------------------------------------------------------

type envRef struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

type healthView struct {
	ConsecutiveFailures int        `json:"consecutiveFailures"`
	FailingSince        *time.Time `json:"failingSince"`
	LastSuccessAt       *time.Time `json:"lastSuccessAt"`
	LastFailureAt       *time.Time `json:"lastFailureAt"`
	DisabledReason      *string    `json:"disabledReason"`
	DisabledAt          *time.Time `json:"disabledAt"`
}

// endpointView is an endpoint as the API returns it: never the secret, only its last characters.
type endpointView struct {
	ID                      string     `json:"id"`
	URL                     string     `json:"url"`
	Description             string     `json:"description"`
	EventTypes              []string   `json:"eventTypes"`
	Environment             *envRef    `json:"environment"` // nil: every environment
	Enabled                 bool       `json:"enabled"`
	SecretHint              string     `json:"secretHint"`
	PreviousSecretExpiresAt *time.Time `json:"previousSecretExpiresAt"`
	Health                  healthView `json:"health"`
	CreatedAt               time.Time  `json:"createdAt"`
	Status                  string     `json:"status"` // healthy | failing | disabled | idle
}

const selectEndpoints = `
	SELECT w.id::text, w.url, w.description, w.event_types, w.environment_id::text, e.key, w.enabled, w.secret_hint,
	       CASE WHEN w.previous_secret_expires_at > now() THEN w.previous_secret_expires_at END,
	       w.consecutive_failures, w.failing_since, w.last_success_at, w.last_failure_at, w.disabled_reason, w.disabled_at, w.created_at
	FROM webhook_endpoints w
	LEFT JOIN environments e ON e.id = w.environment_id AND e.workspace_id = w.workspace_id`

func scanEndpoint(row pgx.Row) (endpointView, error) {
	var v endpointView
	var envID, envKey *string
	err := row.Scan(&v.ID, &v.URL, &v.Description, &v.EventTypes, &envID, &envKey, &v.Enabled, &v.SecretHint, &v.PreviousSecretExpiresAt,
		&v.Health.ConsecutiveFailures, &v.Health.FailingSince, &v.Health.LastSuccessAt, &v.Health.LastFailureAt, &v.Health.DisabledReason, &v.Health.DisabledAt, &v.CreatedAt)
	if err != nil {
		return v, err
	}
	if envID != nil && envKey != nil {
		v.Environment = &envRef{ID: *envID, Key: *envKey}
	}
	sort.Strings(v.EventTypes)
	switch {
	case !v.Enabled:
		v.Status = "disabled"
	case v.Health.ConsecutiveFailures > 0:
		v.Status = "failing"
	case v.Health.LastSuccessAt != nil:
		v.Status = "healthy"
	default:
		v.Status = "idle"
	}
	return v, nil
}

// ---- requests ---------------------------------------------------------------------

type endpointRequest struct {
	URL           *string   `json:"url"`
	Description   *string   `json:"description"`
	EventTypes    *[]string `json:"eventTypes"`
	EnvironmentID *string   `json:"environmentId"` // "" or null with the key present: every environment
	Enabled       *bool     `json:"enabled"`
}

var ctrl = regexp.MustCompile(`[\x00-\x1f\x7f]`)

func (h *Handlers) normalizeTypes(types []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, t := range types {
		t = strings.TrimSpace(t)
		if !ValidType(t) {
			return nil, bad("unknown event type " + strconv.Quote(clip(t, 60)))
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, bad("subscribe to at least one event type")
	}
	return out, nil
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (h *Handlers) validURL(ctx context.Context, raw string) (string, error) {
	u, err := h.guard.Check(ctx, strings.TrimSpace(raw))
	if err != nil {
		if errors.Is(err, netguard.ErrBlocked) {
			return "", bad(strings.TrimPrefix(err.Error(), netguard.ErrBlocked.Error()+": "))
		}
		return "", bad("the host could not be resolved")
	}
	return u.String(), nil
}

func (h *Handlers) environmentIn(ctx context.Context, tx pgx.Tx, workspaceID, envID string) error {
	if !rbac.IsUUID(envID) {
		return bad("environmentId is not an environment of this workspace")
	}
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM environments WHERE id = $1::uuid AND workspace_id = $2::uuid`, envID, workspaceID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return bad("environmentId is not an environment of this workspace")
	}
	return err
}

// ---- routes -----------------------------------------------------------------------

// List handles GET /workspaces/{wsId}/webhooks (admin or owner).
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	ws := rbac.WorkspaceAccessFrom(r.Context())
	rows, err := h.pool.Query(r.Context(), selectEndpoints+` WHERE w.workspace_id = $1::uuid ORDER BY w.created_at, w.id`, ws.WorkspaceID)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (endpointView, error) { return scanEndpoint(row) })
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	if list == nil {
		list = []endpointView{}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"endpoints": list, "available": h.box.Available(), "eventTypes": Types, "limit": quota.MaxWebhookEndpoints})
}

// Create handles POST /workspaces/{wsId}/webhooks. The signing secret is returned
// ONCE, in this response; only its last characters can be read again.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := rbac.WorkspaceAccessFrom(ctx)
	if !h.requireKey(w) {
		return
	}
	var req endpointRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if req.URL == nil || req.EventTypes == nil {
		fail(w, r, bad("url and eventTypes are required"))
		return
	}
	target, err := h.validURL(ctx, *req.URL)
	if err != nil {
		fail(w, r, err)
		return
	}
	types, err := h.normalizeTypes(*req.EventTypes)
	if err != nil {
		fail(w, r, err)
		return
	}
	desc := ""
	if req.Description != nil {
		desc = strings.TrimSpace(*req.Description)
	}
	if len(desc) > 200 || ctrl.MatchString(desc) {
		fail(w, r, bad("description must be at most 200 characters, without control characters"))
		return
	}
	secret, err := NewSecret()
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	var view endpointView
	err = pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		if err := quota.CheckWebhookEndpoint(ctx, tx, ws.WorkspaceID); err != nil {
			return err
		}
		envID := ""
		if req.EnvironmentID != nil && *req.EnvironmentID != "" {
			envID = strings.ToLower(*req.EnvironmentID)
			if err := h.environmentIn(ctx, tx, ws.WorkspaceID, envID); err != nil {
				return err
			}
		}
		var id string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			return err
		}
		sealed, err := h.box.Seal([]byte(secret), []byte(id))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO webhook_endpoints (id, workspace_id, url, description, event_types, environment_id, secret_enc, secret_hint, created_by)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, NULLIF($6, '')::uuid, $7, $8, $9::uuid)`,
			id, ws.WorkspaceID, target, desc, types, envID, sealed, SecretHint(secret), ws.User.ID); err != nil {
			return err
		}
		view, err = scanEndpoint(tx.QueryRow(ctx, selectEndpoints+` WHERE w.id = $1::uuid AND w.workspace_id = $2::uuid`, id, ws.WorkspaceID))
		if err != nil {
			return err
		}
		return h.audit(ctx, tx, ws, "webhook_endpoint.create", id, nil, map[string]any{"url": view.URL, "eventTypes": types, "environmentId": envID}, audit.SeverityInfo)
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"endpoint": view, "secret": secret})
}

func (h *Handlers) audit(ctx context.Context, tx pgx.Tx, ws rbac.WorkspaceAccess, action, id string, before, after any, sev string) error {
	return audit.Write(ctx, tx, audit.Entry{
		WorkspaceID: ws.WorkspaceID, ActorID: ws.User.ID, ActorEmail: ws.User.Email,
		Action: action, ResourceType: "webhook_endpoint", ResourceID: id, Severity: sev, Before: before, After: after,
	})
}

func endpointID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.ToLower(r.PathValue("endpointId"))
	if !rbac.IsUUID(id) {
		httpx.WriteError(w, http.StatusNotFound, "WEBHOOK_NOT_FOUND", "no such webhook endpoint in this workspace")
		return "", false
	}
	return id, true
}

func notFound(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusNotFound, "WEBHOOK_NOT_FOUND", "no such webhook endpoint in this workspace")
}

// Update handles PATCH /workspaces/{wsId}/webhooks/{endpointId}. Re-enabling an
// endpoint clears its failure counters, so it gets a fresh chance.
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := rbac.WorkspaceAccessFrom(ctx)
	id, ok := endpointID(w, r)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if err := httpx.DecodeJSON(w, r, &raw); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	var req endpointRequest
	for k, v := range raw {
		var err error
		switch k {
		case "url":
			err = json.Unmarshal(v, &req.URL)
		case "description":
			err = json.Unmarshal(v, &req.Description)
		case "eventTypes":
			err = json.Unmarshal(v, &req.EventTypes)
		case "environmentId":
			err = json.Unmarshal(v, &req.EnvironmentID)
		case "enabled":
			err = json.Unmarshal(v, &req.Enabled)
		default:
			err = errors.New("unknown field " + strconv.Quote(clip(k, 40)))
		}
		if err != nil {
			httpx.BadRequest(w, "invalid "+clip(k, 40))
			return
		}
	}
	var view endpointView
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		before, err := scanEndpoint(tx.QueryRow(ctx, selectEndpoints+` WHERE w.id = $1::uuid AND w.workspace_id = $2::uuid FOR UPDATE OF w`, id, ws.WorkspaceID))
		if err != nil {
			return err
		}
		next := before
		if req.URL != nil {
			if next.URL, err = h.validURL(ctx, *req.URL); err != nil {
				return err
			}
		}
		if req.Description != nil {
			d := strings.TrimSpace(*req.Description)
			if len(d) > 200 || ctrl.MatchString(d) {
				return bad("description must be at most 200 characters, without control characters")
			}
			next.Description = d
		}
		if req.EventTypes != nil {
			if next.EventTypes, err = h.normalizeTypes(*req.EventTypes); err != nil {
				return err
			}
		}
		envID := ""
		if before.Environment != nil {
			envID = before.Environment.ID
		}
		if _, present := raw["environmentId"]; present {
			envID = ""
			if req.EnvironmentID != nil && *req.EnvironmentID != "" {
				envID = strings.ToLower(*req.EnvironmentID)
				if err := h.environmentIn(ctx, tx, ws.WorkspaceID, envID); err != nil {
					return err
				}
			}
		}
		enabled := before.Enabled
		reset := false
		if req.Enabled != nil {
			enabled = *req.Enabled
			reset = enabled && !before.Enabled
		}
		if _, err := tx.Exec(ctx, `
			UPDATE webhook_endpoints
			SET url = $3, description = $4, event_types = $5, environment_id = NULLIF($6, '')::uuid, enabled = $7, updated_at = $8,
			    consecutive_failures = CASE WHEN $9 THEN 0 ELSE consecutive_failures END,
			    failing_since = CASE WHEN $9 THEN NULL ELSE failing_since END,
			    disabled_reason = CASE WHEN $7 THEN NULL ELSE COALESCE(disabled_reason, 'disabled by an admin') END,
			    disabled_at = CASE WHEN $7 THEN NULL ELSE COALESCE(disabled_at, $8) END
			WHERE id = $1::uuid AND workspace_id = $2::uuid`,
			id, ws.WorkspaceID, next.URL, next.Description, next.EventTypes, envID, enabled, h.now(), reset); err != nil {
			return err
		}
		view, err = scanEndpoint(tx.QueryRow(ctx, selectEndpoints+` WHERE w.id = $1::uuid AND w.workspace_id = $2::uuid`, id, ws.WorkspaceID))
		if err != nil {
			return err
		}
		return h.audit(ctx, tx, ws, "webhook_endpoint.update", id,
			map[string]any{"url": before.URL, "eventTypes": before.EventTypes, "enabled": before.Enabled},
			map[string]any{"url": view.URL, "eventTypes": view.EventTypes, "enabled": view.Enabled}, audit.SeverityInfo)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// Delete handles DELETE /workspaces/{wsId}/webhooks/{endpointId}. Its queued deliveries go with it.
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := rbac.WorkspaceAccessFrom(ctx)
	id, ok := endpointID(w, r)
	if !ok {
		return
	}
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var u string
		if err := tx.QueryRow(ctx, `DELETE FROM webhook_endpoints WHERE id = $1::uuid AND workspace_id = $2::uuid RETURNING url`, id, ws.WorkspaceID).Scan(&u); err != nil {
			return err
		}
		return h.audit(ctx, tx, ws, "webhook_endpoint.delete", id, map[string]any{"url": u}, nil, audit.SeverityCritical)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RotateSecret handles POST /workspaces/{wsId}/webhooks/{endpointId}/rotate-secret.
// The new secret is returned once. The old one keeps signing for RotationGrace
// (both signatures are sent), so receivers can switch without dropping events.
func (h *Handlers) RotateSecret(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := rbac.WorkspaceAccessFrom(ctx)
	id, ok := endpointID(w, r)
	if !ok || !h.requireKey(w) {
		return
	}
	secret, err := NewSecret()
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	var view endpointView
	err = pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var cur []byte
		if err := tx.QueryRow(ctx, `SELECT secret_enc FROM webhook_endpoints WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE`, id, ws.WorkspaceID).Scan(&cur); err != nil {
			return err
		}
		sealed, err := h.box.Seal([]byte(secret), []byte(id))
		if err != nil {
			return err
		}
		now := h.now()
		if _, err := tx.Exec(ctx, `
			UPDATE webhook_endpoints
			SET previous_secret_enc = secret_enc, previous_secret_expires_at = $3, secret_enc = $4, secret_hint = $5, updated_at = $6
			WHERE id = $1::uuid AND workspace_id = $2::uuid`, id, ws.WorkspaceID, now.Add(RotationGrace), sealed, SecretHint(secret), now); err != nil {
			return err
		}
		view, err = scanEndpoint(tx.QueryRow(ctx, selectEndpoints+` WHERE w.id = $1::uuid AND w.workspace_id = $2::uuid`, id, ws.WorkspaceID))
		if err != nil {
			return err
		}
		return h.audit(ctx, tx, ws, "webhook_endpoint.rotate_secret", id, nil, map[string]any{"graceUntil": now.Add(RotationGrace).UTC().Format(time.RFC3339)}, audit.SeverityCritical)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"endpoint": view, "secret": secret})
}

// ---- deliveries -------------------------------------------------------------------

type deliveryView struct {
	ID            string     `json:"id"` // the delivery id, the same one the receiver sees
	EventID       string     `json:"eventId"`
	EventType     string     `json:"eventType"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	HTTPStatus    *int       `json:"httpStatus"`
	DurationMs    *int       `json:"durationMs"`
	Error         *string    `json:"error"`
	ResponseHint  *string    `json:"responseHint"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastAttemptAt *time.Time `json:"lastAttemptAt"`
	NextAttemptAt *time.Time `json:"nextAttemptAt"`
	DeliveredAt   *time.Time `json:"deliveredAt"`
}

const selectDeliveries = `
	SELECT delivery_id::text, event_id::text, event_type, status, attempts, last_http_status, last_duration_ms, last_error, last_response_hint,
	       created_at, last_attempt_at, CASE WHEN status IN ('pending', 'failed') THEN next_attempt_at END, delivered_at
	FROM outbox`

func scanDelivery(row pgx.Row) (deliveryView, error) {
	var v deliveryView
	err := row.Scan(&v.ID, &v.EventID, &v.EventType, &v.Status, &v.Attempts, &v.HTTPStatus, &v.DurationMs, &v.Error, &v.ResponseHint,
		&v.CreatedAt, &v.LastAttemptAt, &v.NextAttemptAt, &v.DeliveredAt)
	return v, err
}

func (h *Handlers) endpointExists(ctx context.Context, wsID, id string) (bool, error) {
	var one int
	err := h.pool.QueryRow(ctx, `SELECT 1 FROM webhook_endpoints WHERE id = $1::uuid AND workspace_id = $2::uuid`, id, wsID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Deliveries handles GET /workspaces/{wsId}/webhooks/{endpointId}/deliveries: the
// newest first, at most 100 (?limit=), optionally one ?status=.
func (h *Handlers) Deliveries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := rbac.WorkspaceAccessFrom(ctx)
	id, ok := endpointID(w, r)
	if !ok {
		return
	}
	if found, err := h.endpointExists(ctx, ws.WorkspaceID, id); err != nil {
		httpx.WriteInternal(w, r, err)
		return
	} else if !found {
		notFound(w)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			httpx.BadRequest(w, "limit must be 1 to 100")
			return
		}
		limit = n
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "pending", "failed", "dead", "delivered", "delivering":
	default:
		httpx.BadRequest(w, "unknown status")
		return
	}
	rows, err := h.pool.Query(ctx, selectDeliveries+`
		WHERE workspace_id = $1::uuid AND endpoint_id = $2::uuid AND ($3 = '' OR status = $3)
		ORDER BY created_at DESC, id DESC LIMIT $4`, ws.WorkspaceID, id, status, limit)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (deliveryView, error) { return scanDelivery(row) })
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	if list == nil {
		list = []deliveryView{}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deliveries": list})
}

// Test handles POST /workspaces/{wsId}/webhooks/{endpointId}/test: queues a
// webhook.ping and sends it now (at most one request to the endpoint). It works on
// a disabled endpoint, so a fix can be checked before re-enabling.
func (h *Handlers) Test(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := rbac.WorkspaceAccessFrom(ctx)
	id, ok := endpointID(w, r)
	if !ok {
		return
	}
	var deliveryID string
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM webhook_endpoints WHERE id = $1::uuid AND workspace_id = $2::uuid FOR SHARE`, id, ws.WorkspaceID).Scan(&one); err != nil {
			return err
		}
		now := h.now()
		var eventID string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&eventID); err != nil {
			return err
		}
		env := Envelope(eventID, Event{Type: EventPing, WorkspaceID: ws.WorkspaceID, Actor: ActorFrom(ctx, ws.User.ID, ""), Data: map[string]any{"message": "This is a test event from Helios."}}, now)
		var err error
		deliveryID, err = outbox.Enqueue(ctx, tx, outbox.Item{
			WorkspaceID: ws.WorkspaceID, Kind: outbox.KindWebhook, EventType: EventPing, EventID: eventID, EndpointID: id, TargetKey: id, Payload: env,
		}, now)
		if err != nil {
			return err
		}
		return h.audit(ctx, tx, ws, "webhook_endpoint.test", id, nil, map[string]any{"deliveryId": deliveryID}, audit.SeverityInfo)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	h.sendNow(w, r, ws.WorkspaceID, deliveryID)
}

// Resend handles POST /workspaces/{wsId}/webhooks/{endpointId}/deliveries/{deliveryId}/resend:
// a failed or dead delivery gets a new attempt with the SAME delivery id, sent now.
func (h *Handlers) Resend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := rbac.WorkspaceAccessFrom(ctx)
	id, ok := endpointID(w, r)
	if !ok {
		return
	}
	did := strings.ToLower(r.PathValue("deliveryId"))
	if !rbac.IsUUID(did) {
		httpx.WriteError(w, http.StatusNotFound, "DELIVERY_NOT_FOUND", "no such delivery for this endpoint")
		return
	}
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		// One attempt is enough to bring the row back; a row that has used every attempt gets exactly one more.
		tag, err := tx.Exec(ctx, `
			UPDATE outbox SET status = 'pending', next_attempt_at = $4, attempts = LEAST(attempts, $5), updated_at = $4,
			       lease_owner = NULL, lease_expires_at = NULL
			WHERE delivery_id = $1::uuid AND workspace_id = $2::uuid AND endpoint_id = $3::uuid AND status IN ('failed', 'dead')`,
			did, ws.WorkspaceID, id, h.now(), outbox.MaxAttempts-1)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var status string
			err := tx.QueryRow(ctx, `SELECT status FROM outbox WHERE delivery_id = $1::uuid AND workspace_id = $2::uuid AND endpoint_id = $3::uuid`, did, ws.WorkspaceID, id).Scan(&status)
			if err != nil {
				return err
			}
			return refusal{http.StatusConflict, "NOT_RESENDABLE", "only a failed delivery can be resent; this one is " + status}
		}
		return h.audit(ctx, tx, ws, "webhook_endpoint.resend", id, nil, map[string]any{"deliveryId": did}, audit.SeverityInfo)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "DELIVERY_NOT_FOUND", "no such delivery for this endpoint")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	h.sendNow(w, r, ws.WorkspaceID, did)
}

// sendNow delivers one queued row in this request and answers with its record.
func (h *Handlers) sendNow(w http.ResponseWriter, r *http.Request, workspaceID, deliveryID string) {
	ctx := r.Context()
	if _, _, err := h.worker.SendNow(ctx, workspaceID, deliveryID); err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	v, err := scanDelivery(h.pool.QueryRow(ctx, selectDeliveries+` WHERE delivery_id = $1::uuid AND workspace_id = $2::uuid`, deliveryID, workspaceID))
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

// Rekey re-encrypts every endpoint's secrets (current and previous) under the box's
// current key. Run it after setting a new WEBHOOK_SECRET_KEY with the old one in
// WEBHOOK_SECRET_KEY_PREVIOUS; afterwards the old key can be dropped. It returns
// the number of endpoints touched, and is safe to repeat.
func Rekey(ctx context.Context, pool *pgxpool.Pool, box *secretbox.Box) (int, error) {
	if !box.Available() {
		return 0, secretbox.ErrNoKey
	}
	n := 0
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, secret_enc, previous_secret_enc FROM webhook_endpoints ORDER BY id FOR UPDATE`)
		if err != nil {
			return err
		}
		type row struct {
			id        string
			cur, prev []byte
		}
		all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var x row
			return x, r.Scan(&x.id, &x.cur, &x.prev)
		})
		if err != nil {
			return err
		}
		for _, x := range all {
			reseal := func(b []byte) ([]byte, bool, error) {
				if len(b) == 0 || box.SealedUnderCurrent(b) {
					return b, false, nil
				}
				pt, err := box.Open(b, []byte(x.id))
				if err != nil {
					return nil, false, err
				}
				out, err := box.Seal(pt, []byte(x.id))
				return out, true, err
			}
			cur, c1, err := reseal(x.cur)
			if err != nil {
				return err
			}
			prev, c2, err := reseal(x.prev)
			if err != nil {
				return err
			}
			if c1 || c2 {
				if _, err := tx.Exec(ctx, `UPDATE webhook_endpoints SET secret_enc = $2, previous_secret_enc = $3 WHERE id = $1::uuid`, x.id, cur, prev); err != nil {
					return err
				}
				n++
			}
		}
		return nil
	})
	return n, err
}
