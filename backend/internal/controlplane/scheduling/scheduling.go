// Package scheduling implements scheduled changes: enable, disable, kill or
// patch one flag in one environment at a future time.
//
// There are three parts, and none depends on a long-lived process:
//
//   - the API (this file): create, list, get, cancel;
//   - the Runner (run.go): a function that claims due rows, executes each in
//     its own transaction and records the outcome, safe to call from any number
//     of instances at once;
//   - the internal endpoint (internal.go) and cmd/scheduler, two ways to call
//     the Runner from outside (a Cloud Scheduler job, a cron, a person).
//
// A scheduled change is never a second way to edit a flag. Executing one calls
// flags.ApplyConfigChange or flags.KillFlag, the functions the edit and kill
// routes call, so the validation, the version bump, the audit entry and the
// Redis event are the ordinary ones, with the actor shown as the creator "via
// schedule". What IS new is authority: the creator's role is checked when the
// change is scheduled and again when it runs, using their membership as it is
// then. See docs/SCHEDULING.md.
package scheduling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/flags"
	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/httpx"
)

const (
	// MinLead is how far ahead a change must be scheduled: far enough that a
	// typo can be noticed and cancelled, and never "now" through a side door.
	MinLead = time.Minute
	// MaxHorizon is how far ahead one may schedule.
	MaxHorizon = 366 * 24 * time.Hour
)

const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusMissed    = "missed"
	StatusCancelled = "cancelled"
)

const (
	KindEnable  = "enable"
	KindDisable = "disable"
	KindKill    = "kill"
	KindPatch   = "patch"
)

// Result codes recorded on a row.
const (
	CodeApplied       = "APPLIED"
	CodeCancelled     = "CANCELLED"
	CodeMissed        = "MISSED"
	CodeRetrying      = "RETRYING"
	CodeExhausted     = "RETRIES_EXHAUSTED"
	CodeCreatorGone   = "CREATOR_REMOVED"
	CodeNotAuthorized = "CREATOR_NOT_AUTHORIZED"
	CodeConflict      = "CONFLICT"
	CodeInvalid       = "INVALID_CHANGE"
	CodeFlagGone      = "FLAG_NOT_FOUND"
	CodeTokenInvalid  = "TOKEN_NO_LONGER_VALID"
)

// Clock is the time source. Production uses time.Now; tests inject their own
// so no test sleeps.
type Clock func() time.Time

func (c Clock) now() time.Time {
	if c == nil {
		return time.Now().UTC()
	}
	return c().UTC()
}

// RequirementFor is the role a kind of change needs in an environment: the
// same as making that change now. A kill is editor-or-above everywhere; every
// other change is rbac.FlagWrite, an approver for production. This one function
// is used when scheduling and again when executing.
func RequirementFor(kind string) rbac.Requirement {
	if kind == KindKill {
		return rbac.Min(rbac.Editor)
	}
	return rbac.FlagWrite
}

type Handlers struct {
	pool  *pgxpool.Pool
	clock Clock
}

func NewHandlers(pool *pgxpool.Pool, clock Clock) *Handlers {
	return &Handlers{pool: pool, clock: clock}
}

// changeView is a scheduled change as the API returns it.
type changeView struct {
	ID             string          `json:"id"`
	FlagKey        string          `json:"flagKey"`
	Environment    string          `json:"environment"`
	Kind           string          `json:"kind"`
	Change         json.RawMessage `json:"change"`
	RunAt          time.Time       `json:"runAt"`
	Status         string          `json:"status"`
	IdempotencyKey string          `json:"idempotencyKey"`
	BaseVersion    int64           `json:"baseVersion"`
	Attempts       int             `json:"attempts"`
	ResultCode     *string         `json:"resultCode"`
	ResultMessage  *string         `json:"resultMessage"`
	CreatedBy      string          `json:"createdBy"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	FinishedAt     *time.Time      `json:"finishedAt"`
}

const selectChange = `
	SELECT sc.id::text, f.key, e.key, sc.kind, sc.change::text, sc.run_at, sc.status, sc.idempotency_key,
	       sc.base_version, sc.attempts, sc.result_code, sc.result_message, sc.created_by_email,
	       sc.created_at, sc.updated_at, sc.finished_at
	FROM scheduled_changes sc
	JOIN flags f ON f.id = sc.flag_id AND f.workspace_id = sc.workspace_id
	JOIN environments e ON e.id = sc.environment_id AND e.workspace_id = sc.workspace_id`

type scanner interface{ Scan(dest ...any) error }

func scanChange(row scanner) (changeView, error) {
	var v changeView
	var change string
	err := row.Scan(&v.ID, &v.FlagKey, &v.Environment, &v.Kind, &change, &v.RunAt, &v.Status, &v.IdempotencyKey,
		&v.BaseVersion, &v.Attempts, &v.ResultCode, &v.ResultMessage, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt, &v.FinishedAt)
	v.Change = json.RawMessage(change)
	return v, err
}

// patchBody is the "change" of a patch. It cannot carry "enabled": turning a
// flag on or off is the enable and disable kinds, which a reader of the list
// can tell apart at a glance.
type patchBody struct {
	TargetingRules         json.RawMessage `json:"targetingRules,omitempty"`
	Rollout                json.RawMessage `json:"rollout,omitempty"`
	FallthroughVariationID *string         `json:"fallthroughVariationId,omitempty"`
}

func (p patchBody) change() flags.Change {
	return flags.Change{TargetingRules: p.TargetingRules, Rollout: p.Rollout, FallthroughVariationID: p.FallthroughVariationID}
}

type createRequest struct {
	IdempotencyKey string          `json:"idempotencyKey"`
	Kind           string          `json:"kind"`
	RunAt          string          `json:"runAt"`
	Change         json.RawMessage `json:"change"`
}

type refusal struct {
	status  int
	code    string
	message string
}

func (e refusal) Error() string { return e.message }

func bad(format string, args ...any) error {
	return refusal{http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf(format, args...)}
}

func writeRefusal(w http.ResponseWriter, r *http.Request, err error) {
	var ref refusal
	var conflict flags.ErrVersionConflict
	switch {
	case errors.As(err, &ref):
		httpx.WriteError(w, ref.status, ref.code, ref.message)
	case flags.IsValidation(err):
		httpx.BadRequest(w, err.Error())
	case quota.WriteError(w, err):
	case errors.Is(err, pgx.ErrNoRows):
		httpx.WriteError(w, http.StatusNotFound, "FLAG_NOT_FOUND", "no flag with key "+r.PathValue("key"))
	case errors.As(err, &conflict):
		httpx.WriteError(w, http.StatusConflict, "VERSION_CONFLICT", conflict.Error())
	default:
		httpx.WriteInternal(w, r, err)
	}
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

// forbid is the 403 for a role that cannot make the change now either.
func forbid(env rbac.Environment, kind string, role rbac.Role) error {
	need := RequirementFor(kind)(env)
	return refusal{http.StatusForbidden, "FORBIDDEN",
		fmt.Sprintf("scheduling this change in %s needs the %s role, the same as making it now; you are %s", env.Key, need, role)}
}

// Create handles POST /environments/{envId}/flags/{key}/scheduled-changes.
//
// The route lets editors through; the handler then applies the kind's own
// requirement (an approver for a production change that is not a kill), so an
// approval-gated change cannot be scheduled by a role that could not make it.
//
// Lock order: the workspace row (the pending caps), then the new row.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	key := r.PathValue("key")
	var req createRequest
	if err := httpx.DecodeJSONLimit(w, r, &req, 256<<10); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	now := h.clock.now()
	runAt, change, err := h.validate(req, now)
	if err != nil {
		writeRefusal(w, r, err)
		return
	}
	if !access.Role.AtLeast(RequirementFor(req.Kind)(access.Env)) {
		writeRefusal(w, r, forbid(access.Env, req.Kind, access.Role))
		return
	}

	var view changeView
	replay := false
	err = pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		in, err := flags.Inspect(ctx, tx, access.Env, key, change.change(), req.Kind == KindPatch)
		if err != nil {
			return err
		}
		stored, _ := json.Marshal(change)
		base, _ := json.Marshal(in.Touched)
		if req.Kind != KindPatch {
			base = []byte("{}")
		}

		// A retried request finds its own earlier row (idempotency), before any cap is consulted.
		if existing, found, err := h.existing(ctx, tx, access.Env.WorkspaceID, req.IdempotencyKey); err != nil {
			return err
		} else if found {
			same, err := sameRequest(ctx, tx, existing, in.FlagID, access.Env.ID, req.Kind, stored, runAt)
			if err != nil {
				return err
			}
			if !same {
				return refusal{http.StatusConflict, "IDEMPOTENCY_CONFLICT", "this idempotency key was already used for a different change"}
			}
			view, replay = existing, true
			return nil
		}

		if err := quota.CheckScheduledChange(ctx, tx, access.Env.WorkspaceID, in.FlagID); err != nil {
			return err
		}
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO scheduled_changes
				(workspace_id, flag_id, environment_id, kind, change, base_version, base_config, run_at, due_at,
				 idempotency_key, created_by, created_by_email, created_at, updated_at, created_by_token_id)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::jsonb, $6, $7::jsonb, $8, $8, $9, NULLIF($10, '')::uuid, $11, $12, $12, NULLIF($13, '')::uuid)
			RETURNING id::text`,
			access.Env.WorkspaceID, in.FlagID, access.Env.ID, req.Kind, string(stored), in.Version, string(base), runAt,
			req.IdempotencyKey, access.User.ID, access.User.Email, now, tokenID(ctx)).Scan(&id)
		if err != nil {
			return err
		}
		view, err = scanChange(tx.QueryRow(ctx, selectChange+` WHERE sc.id = $1::uuid AND sc.workspace_id = $2::uuid`, id, access.Env.WorkspaceID))
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: access.Env.WorkspaceID, ActorID: access.User.ID, ActorEmail: access.User.Email,
			EnvironmentID: access.Env.ID, Action: "scheduled_change.create", ResourceType: "scheduled_change", ResourceID: id,
			After: map[string]any{"flagKey": key, "environment": access.Env.Key, "kind": req.Kind, "runAt": runAt.Format(time.RFC3339), "change": json.RawMessage(stored)},
		})
	})
	if isUniqueViolation(err, "scheduled_changes_idempotency_unique") {
		// two requests with the same key at once: the other won
		writeRefusal(w, r, refusal{http.StatusConflict, "IDEMPOTENCY_CONFLICT", "a request with this idempotency key is already being processed; retry it"})
		return
	}
	if err != nil {
		writeRefusal(w, r, err)
		return
	}
	if replay {
		w.Header().Set("Idempotent-Replay", "true")
		httpx.WriteJSON(w, http.StatusOK, view)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, view)
}

// validate checks the request's shape and timing.
func (h *Handlers) validate(req createRequest, now time.Time) (time.Time, patchBody, error) {
	var patch patchBody
	if k := req.IdempotencyKey; k == "" || len(k) > 128 || strings.ContainsFunc(k, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return time.Time{}, patch, bad("idempotencyKey is required: 1-128 characters, chosen by you, the same on a retry of the same request")
	}
	switch req.Kind {
	case KindEnable, KindDisable, KindKill, KindPatch:
	default:
		return time.Time{}, patch, bad("kind must be one of enable, disable, kill, patch")
	}
	runAt, err := time.Parse(time.RFC3339, req.RunAt)
	if err != nil {
		return time.Time{}, patch, bad("runAt must be an RFC 3339 timestamp such as 2026-12-01T09:00:00Z")
	}
	runAt = runAt.UTC()
	if runAt.Before(now.Add(MinLead)) {
		return time.Time{}, patch, bad("runAt must be at least %d minute from now (it is %s UTC now)", int(MinLead.Minutes()), now.Format("2006-01-02 15:04"))
	}
	if runAt.After(now.Add(MaxHorizon)) {
		return time.Time{}, patch, bad("runAt must be within a year from now")
	}
	hasChange := len(req.Change) > 0 && string(req.Change) != "null"
	if req.Kind != KindPatch {
		if hasChange && string(req.Change) != "{}" {
			return time.Time{}, patch, bad("a %s change takes no \"change\"", req.Kind)
		}
		return runAt, patch, nil
	}
	if !hasChange {
		return time.Time{}, patch, bad("a patch needs a \"change\" with rollout, fallthroughVariationId or targetingRules")
	}
	dec := json.NewDecoder(strings.NewReader(string(req.Change)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&patch); err != nil {
		return time.Time{}, patch, bad("change: %v (a patch may set rollout, fallthroughVariationId and targetingRules; use the enable and disable kinds for the enabled state)", err)
	}
	if patch.change().Empty() {
		return time.Time{}, patch, bad("a patch needs a \"change\" with rollout, fallthroughVariationId or targetingRules")
	}
	return runAt, patch, nil
}

func (h *Handlers) existing(ctx context.Context, tx pgx.Tx, workspaceID, key string) (changeView, bool, error) {
	v, err := scanChange(tx.QueryRow(ctx, selectChange+` WHERE sc.workspace_id = $1::uuid AND sc.idempotency_key = $2`, workspaceID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, err
}

// sameRequest reports whether an existing row is the change being asked for
// again (same flag, environment, kind, content and time).
func sameRequest(ctx context.Context, tx pgx.Tx, existing changeView, flagID, envID, kind string, change []byte, runAt time.Time) (bool, error) {
	var same bool
	err := tx.QueryRow(ctx, `
		SELECT flag_id = $2::uuid AND environment_id = $3::uuid AND kind = $4 AND change = $5::jsonb AND run_at = $6
		FROM scheduled_changes WHERE id = $1::uuid`, existing.ID, flagID, envID, kind, string(change), runAt).Scan(&same)
	return same, err
}

// List handles GET /environments/{envId}/scheduled-changes[?flag=key][&status=pending]:
// what is waiting first (soonest first), then what has happened (newest first).
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	access := rbac.AccessFrom(r.Context())
	q := r.URL.Query()
	where := []string{"sc.workspace_id = $1::uuid", "sc.environment_id = $2::uuid"}
	args := []any{access.Env.WorkspaceID, access.Env.ID}
	if f := q.Get("flag"); f != "" {
		args = append(args, f)
		where = append(where, fmt.Sprintf("f.key = $%d", len(args)))
	}
	if s := q.Get("status"); s != "" {
		switch s {
		case StatusPending, StatusRunning, StatusSucceeded, StatusFailed, StatusMissed, StatusCancelled:
		default:
			httpx.BadRequest(w, "status must be pending, running, succeeded, failed, missed or cancelled")
			return
		}
		args = append(args, s)
		where = append(where, fmt.Sprintf("sc.status = $%d", len(args)))
	}
	rows, err := h.pool.Query(r.Context(), selectChange+` WHERE `+strings.Join(where, " AND ")+`
		ORDER BY (sc.status IN ('pending', 'running')) DESC,
		         CASE WHEN sc.status IN ('pending', 'running') THEN sc.run_at END ASC,
		         sc.run_at DESC
		LIMIT 200`, args...)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (changeView, error) { return scanChange(row) })
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	if out == nil {
		out = []changeView{}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"scheduledChanges": out})
}

func notFound(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusNotFound, "SCHEDULED_CHANGE_NOT_FOUND", "no such scheduled change")
}

// Get handles GET /environments/{envId}/scheduled-changes/{changeId}.
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	access := rbac.AccessFrom(r.Context())
	id := r.PathValue("changeId")
	if !rbac.IsUUID(id) {
		notFound(w)
		return
	}
	v, err := scanChange(h.pool.QueryRow(r.Context(), selectChange+` WHERE sc.id = $1::uuid AND sc.workspace_id = $2::uuid AND sc.environment_id = $3::uuid`,
		id, access.Env.WorkspaceID, access.Env.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

// Cancel handles POST /environments/{envId}/scheduled-changes/{changeId}/cancel.
//
// The change's creator may always cancel it; anyone else needs the role the
// change itself needs (the creator's kind-specific requirement), so an editor
// cannot cancel an approver's production change but a person can always take
// back their own.
//
// A pending or running change is cancelled by one UPDATE guarded by its status.
// For a running change that UPDATE waits for the row lock the executor holds
// while it applies the change: if the executor commits first the change is
// "succeeded" and the cancel is refused (409); if the cancel gets there first
// the executor, which re-reads the row under its lock, sees "cancelled" and
// does nothing. Either way a change is applied completely or not at all.
func (h *Handlers) Cancel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	id := r.PathValue("changeId")
	if !rbac.IsUUID(id) {
		notFound(w)
		return
	}
	var view changeView
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var kind string
		var creator *string
		err := tx.QueryRow(ctx, `SELECT kind, created_by::text FROM scheduled_changes WHERE id = $1::uuid AND workspace_id = $2::uuid AND environment_id = $3::uuid`,
			id, access.Env.WorkspaceID, access.Env.ID).Scan(&kind, &creator)
		if err != nil {
			return err
		}
		mine := creator != nil && *creator == access.User.ID
		if !mine && !access.Role.AtLeast(RequirementFor(kind)(access.Env)) {
			return refusal{http.StatusForbidden, "FORBIDDEN", "only the person who scheduled this change, or someone who could make it now, can cancel it"}
		}
		now := h.clock.now()
		tag, err := tx.Exec(ctx, `
			UPDATE scheduled_changes
			SET status = 'cancelled', result_code = $4, result_message = 'cancelled by ' || $5,
			    finished_at = $6, updated_at = $6, lease_owner = NULL, lease_expires_at = NULL
			WHERE id = $1::uuid AND workspace_id = $2::uuid AND environment_id = $3::uuid AND status IN ('pending', 'running')`,
			id, access.Env.WorkspaceID, access.Env.ID, CodeCancelled, access.User.Email, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var status string
			if err := tx.QueryRow(ctx, `SELECT status FROM scheduled_changes WHERE id = $1::uuid`, id).Scan(&status); err != nil {
				return err
			}
			return refusal{http.StatusConflict, "NOT_CANCELLABLE", "this change is already " + status + " and can no longer be cancelled"}
		}
		view, err = scanChange(tx.QueryRow(ctx, selectChange+` WHERE sc.id = $1::uuid AND sc.workspace_id = $2::uuid`, id, access.Env.WorkspaceID))
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: access.Env.WorkspaceID, ActorID: access.User.ID, ActorEmail: access.User.Email,
			EnvironmentID: access.Env.ID, Action: "scheduled_change.cancel", ResourceType: "scheduled_change", ResourceID: id,
			After: map[string]any{"flagKey": view.FlagKey, "environment": view.Environment, "kind": view.Kind, "createdBy": view.CreatedBy},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		writeRefusal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// tokenID is the API token making this request, or "" for a session.
func tokenID(ctx context.Context) string {
	if t, ok := auth.APITokenFrom(ctx); ok {
		return t.ID
	}
	return ""
}

// CancelForToken cancels the pending changes an API token scheduled, in the
// caller's transaction (the revocation). The reason is recorded on each row and
// each cancellation is audited. Running rows are left to the executor, which
// refuses them for the same reason.
func CancelForToken(ctx context.Context, tx pgx.Tx, workspaceID, tokenID, reason string, now time.Time) (int, error) {
	rows, err := tx.Query(ctx, `
		UPDATE scheduled_changes
		SET status = 'cancelled', result_code = $3, result_message = $4, finished_at = $5, updated_at = $5,
		    lease_owner = NULL, lease_expires_at = NULL
		WHERE workspace_id = $1::uuid AND created_by_token_id = $2::uuid AND status = 'pending'
		RETURNING id::text, environment_id::text`, workspaceID, tokenID, CodeTokenInvalid, reason, now)
	if err != nil {
		return 0, err
	}
	type pair struct{ id, env string }
	cancelled, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pair, error) {
		var p pair
		return p, row.Scan(&p.id, &p.env)
	})
	if err != nil {
		return 0, err
	}
	for _, c := range cancelled {
		if err := audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: workspaceID, EnvironmentID: c.env, Action: "scheduled_change.cancel", ResourceType: "scheduled_change", ResourceID: c.id,
			Severity: audit.SeverityCritical, After: map[string]any{"resultCode": CodeTokenInvalid, "reason": reason, "tokenId": tokenID},
		}); err != nil {
			return 0, err
		}
	}
	return len(cancelled), nil
}
