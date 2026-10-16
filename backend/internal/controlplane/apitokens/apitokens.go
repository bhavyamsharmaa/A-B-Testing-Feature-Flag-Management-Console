// Package apitokens manages API tokens over HTTP: create (the plaintext is shown
// once), list and revoke, plus the one read a pipeline needs first, the
// environments it can see. See docs/API_TOKENS.md.
//
// WHO MAY DO WHAT. Creating, listing and revoking tokens needs an admin or an
// owner of the workspace, and a token itself can never be used on these routes
// (they are classified token-denied in the router registry, so a token cannot
// mint, list or revoke tokens, its own included). An admin or owner may create a
// token with a role up to their own, capped at admin (a token is never an owner),
// and any admin or owner may list and revoke any of the workspace's tokens.
// Members below admin cannot create tokens, so they have none to see: the rule is
// "admins and owners manage all of a workspace's tokens, nobody else manages any".
//
// A token's role is fixed at creation: raising it means creating a new token.
package apitokens

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/apitoken"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/httpx"
)

// Default and maximum lifetime of a token, in days. There is no "never".
const (
	DefaultExpiryDays = 90
	MaxExpiryDays     = 366
)

// Invalidator drops cached verifications (apitoken.Verifier does).
type Invalidator interface{ Invalidate(tokenID string) }

type Handlers struct {
	pool *pgxpool.Pool
	inv  Invalidator
	pub  events.Publisher
}

func NewHandlers(pool *pgxpool.Pool, inv Invalidator, pub events.Publisher) *Handlers {
	return &Handlers{pool: pool, inv: inv, pub: pub}
}

type envRef struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// tokenView is a token as the API returns it: never the secret, never the hash.
type tokenView struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Prefix       string     `json:"prefix"`
	Role         string     `json:"role"`
	Environments []envRef   `json:"environments"` // empty: every environment of the workspace
	ExpiresAt    time.Time  `json:"expiresAt"`
	LastUsedAt   *time.Time `json:"lastUsedAt"`
	CreatedAt    time.Time  `json:"createdAt"`
	CreatedBy    string     `json:"createdBy"`
	RevokedAt    *time.Time `json:"revokedAt"`
	// Status is active, expired or revoked.
	Status string `json:"status"`
}

const selectTokens = `
	SELECT t.id::text, t.name, t.token_prefix, t.role, t.expires_at, t.last_used_at, t.created_at, t.created_by_email, t.revoked_at,
	       CASE WHEN t.revoked_at IS NOT NULL THEN 'revoked' WHEN t.expires_at <= now() THEN 'expired' ELSE 'active' END,
	       COALESCE((SELECT json_agg(json_build_object('id', e.id::text, 'key', e.key) ORDER BY e.key)::text
	                 FROM api_token_environments r JOIN environments e ON e.id = r.environment_id AND e.workspace_id = r.workspace_id
	                 WHERE r.workspace_id = t.workspace_id AND r.token_id = t.id), '[]')
	FROM api_tokens t`

func scanToken(row pgx.Row) (tokenView, error) {
	var v tokenView
	var envs string
	err := row.Scan(&v.ID, &v.Name, &v.Prefix, &v.Role, &v.ExpiresAt, &v.LastUsedAt, &v.CreatedAt, &v.CreatedBy, &v.RevokedAt, &v.Status, &envs)
	if err != nil {
		return v, err
	}
	v.Environments = []envRef{}
	return v, jsonUnmarshal(envs, &v.Environments)
}

type refusal struct {
	status        int
	code, message string
}

func (e refusal) Error() string { return e.message }

func bad(format string, args ...any) error {
	return refusal{http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf(format, args...)}
}

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

func cleanName(s string) (string, error) {
	name := strings.TrimSpace(s)
	if n := len([]rune(name)); n == 0 || n > 60 {
		return "", bad("name must be 1-60 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", bad("name must not contain control characters")
		}
	}
	return name, nil
}

var tokenRoles = map[string]rbac.WorkspaceRole{"viewer": rbac.WorkspaceViewer, "editor": rbac.WorkspaceEditor, "admin": rbac.WorkspaceAdmin}

type createRequest struct {
	Name string `json:"name"`
	// Role is viewer, editor or admin, and at most the caller's own.
	Role string `json:"role"`
	// Environments restricts the token to these environments (keys such as
	// "dev", or ids). Empty or absent: every environment of the workspace.
	Environments []string `json:"environments"`
	// ExpiresInDays defaults to 90 and is at most 366. There is no way to ask for "never".
	ExpiresInDays *int `json:"expiresInDays"`
}

// Create handles POST /workspaces/{wsId}/api-tokens (admin or owner, session only).
// The response carries the plaintext token exactly once.
//
// Lock order: the workspace row (the caps), then the new rows.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := rbac.WorkspaceAccessFrom(ctx)
	var req createRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	name, err := cleanName(req.Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, ok := tokenRoles[req.Role]
	if !ok {
		fail(w, r, bad("role must be viewer, editor or admin"))
		return
	}
	// A token can never exceed the person who creates it: its role is at most theirs.
	if !ws.Role.AtLeast(want) {
		fail(w, r, refusal{http.StatusForbidden, "FORBIDDEN", fmt.Sprintf("you are %s and cannot create a token with the %s role", ws.Role, want)})
		return
	}
	days := DefaultExpiryDays
	if req.ExpiresInDays != nil {
		days = *req.ExpiresInDays
	}
	if days < 1 || days > MaxExpiryDays {
		fail(w, r, bad("expiresInDays must be between 1 and %d: a token cannot be created without an expiry", MaxExpiryDays))
		return
	}
	if len(req.Environments) > 20 {
		fail(w, r, bad("at most 20 environments"))
		return
	}

	var view tokenView
	var plaintext string
	err = pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		// Environments are resolved inside THIS workspace only: another workspace's is "not found".
		envIDs := make([]string, 0, len(req.Environments))
		seen := map[string]bool{}
		for _, ref := range req.Environments {
			var id string
			err := tx.QueryRow(ctx, `SELECT id::text FROM environments WHERE workspace_id = $1::uuid AND (key = $2 OR id::text = lower($2))`, ws.WorkspaceID, ref).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				return bad("environment %q does not exist in this workspace", ref)
			}
			if err != nil {
				return err
			}
			if !seen[id] {
				seen[id] = true
				envIDs = append(envIDs, id)
			}
		}
		if err := quota.CheckAPIToken(ctx, tx, ws.WorkspaceID, ws.User.ID); err != nil {
			return err
		}
		var hash []byte
		var prefix string
		var err error
		plaintext, prefix, hash, err = apitoken.Generate()
		if err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO api_tokens (workspace_id, name, token_prefix, token_hash, role, created_by, created_by_email, expires_at)
			VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7, now() + make_interval(days => $8))
			RETURNING id::text`,
			ws.WorkspaceID, name, prefix, hash, req.Role, ws.User.ID, ws.User.Email, days).Scan(&id); err != nil {
			return err
		}
		for _, envID := range envIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO api_token_environments (workspace_id, token_id, environment_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`,
				ws.WorkspaceID, id, envID); err != nil {
				return err
			}
		}
		view, err = scanToken(tx.QueryRow(ctx, selectTokens+` WHERE t.id = $1::uuid AND t.workspace_id = $2::uuid`, id, ws.WorkspaceID))
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: ws.WorkspaceID, ActorID: ws.User.ID, ActorEmail: ws.User.Email,
			Action: "api_token.create", ResourceType: "api_token", ResourceID: id,
			After: map[string]any{"name": name, "prefix": prefix, "role": req.Role, "environments": view.Environments, "expiresAt": view.ExpiresAt, "token": "[REDACTED]"},
		})
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"token": view, "plaintext": plaintext})
}

// List handles GET /workspaces/{wsId}/api-tokens (admin or owner, session only):
// every token of the workspace, newest first, with its status. Only display data.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	ws := rbac.WorkspaceAccessFrom(r.Context())
	rows, err := h.pool.Query(r.Context(), selectTokens+` WHERE t.workspace_id = $1::uuid ORDER BY t.created_at DESC, t.id`, ws.WorkspaceID)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	tokens, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (tokenView, error) { return scanToken(row) })
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	if tokens == nil {
		tokens = []tokenView{}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

// Revoke handles DELETE /workspaces/{wsId}/api-tokens/{tokenId} (admin or owner,
// session only). The token stops working at once on this instance and, through
// the revocation announcement, on every other; if the announcement is lost, within
// the verifier's cache TTL (apitoken.DefaultCacheTTL). Revoking a revoked token
// is a no-op.
func (h *Handlers) Revoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := rbac.WorkspaceAccessFrom(ctx)
	id := strings.ToLower(r.PathValue("tokenId"))
	if !rbac.IsUUID(id) {
		httpx.WriteError(w, http.StatusNotFound, "TOKEN_NOT_FOUND", "no such token in this workspace")
		return
	}
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var name, prefix string
		var wasRevoked bool
		err := tx.QueryRow(ctx, `SELECT name, token_prefix, revoked_at IS NOT NULL FROM api_tokens WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE`,
			id, ws.WorkspaceID).Scan(&name, &prefix, &wasRevoked)
		if err != nil {
			return err
		}
		if wasRevoked {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: ws.WorkspaceID, ActorID: ws.User.ID, ActorEmail: ws.User.Email,
			Action: "api_token.revoke", ResourceType: "api_token", ResourceID: id, Severity: audit.SeverityCritical,
			Before: map[string]string{"name": name, "prefix": prefix},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "TOKEN_NOT_FOUND", "no such token in this workspace")
		return
	}
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	h.inv.Invalidate(id) // this instance, at once
	apitoken.Announce(ctx, h.pub, id)
	w.WriteHeader(http.StatusNoContent)
}

// Environments handles GET /workspaces/{wsId}/environments (any member, or a
// token): the environments the caller can see. For an API token limited to some
// environments that is only those, so a pipeline cannot even learn the names of
// the others.
func (h *Handlers) Environments(w http.ResponseWriter, r *http.Request) {
	ws := rbac.WorkspaceAccessFrom(r.Context())
	tok, isToken := auth.APITokenFrom(r.Context())
	rows, err := h.pool.Query(r.Context(), `
		SELECT id::text, key, name, is_production FROM environments WHERE workspace_id = $1::uuid ORDER BY key`, ws.WorkspaceID)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	type env struct {
		ID           string `json:"id"`
		Key          string `json:"key"`
		Name         string `json:"name"`
		IsProduction bool   `json:"isProduction"`
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (env, error) {
		var e env
		return e, row.Scan(&e.ID, &e.Key, &e.Name, &e.IsProduction)
	})
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	out := []env{}
	for _, e := range all {
		if !isToken || tok.AllowsEnvironment(e.ID) {
			out = append(out, e)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"environments": out})
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// AuditRejection records that a token which exists was presented and refused
// (expired, revoked, or its creator gone): the apitoken.Verifier calls it, at most
// once an hour per token and reason. The caller is told nothing of this.
func AuditRejection(pool *pgxpool.Pool) func(ctx context.Context, rej apitoken.Rejection) {
	return func(ctx context.Context, rej apitoken.Rejection) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		sev := audit.SeverityInfo
		if rej.Reason != apitoken.ReasonExpired {
			sev = audit.SeverityCritical // a revoked token still being used is worth a look
		}
		_ = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			return audit.Write(ctx, tx, audit.Entry{
				WorkspaceID: rej.WorkspaceID, ActorType: audit.ActorAPIToken, ActorID: rej.TokenID,
				ActorEmail:  rej.Name + " (" + rej.Prefix + ")",
				ActorDetail: audit.TokenActor(rej.TokenID, rej.Name, rej.Prefix, rej.CreatorID, rej.CreatorEmail),
				Action:      "api_token.rejected", ResourceType: "api_token", ResourceID: rej.TokenID, Severity: sev,
				After: map[string]string{"reason": string(rej.Reason)},
			})
		})
	}
}
