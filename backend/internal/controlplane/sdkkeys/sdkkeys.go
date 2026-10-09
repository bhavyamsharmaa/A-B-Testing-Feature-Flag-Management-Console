// Package sdkkeys manages an environment's SDK keys over HTTP: list, create
// (the plaintext is shown once) and revoke. Keys are tenant-scoped: every row
// carries the workspace of its environment.
package sdkkeys

import (
	"encoding/json"
	"errors"
	"log"
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
	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/httpx"
)

// Invalidator drops cached verifications of a key (apikey.Verifier does).
type Invalidator interface {
	Invalidate(prefix string)
}

// GenerateKey mints a key (plaintext, display prefix, hash). A variable only so
// a test can force a prefix collision.
var GenerateKey = apikey.Generate

type Handlers struct {
	pool *pgxpool.Pool
	keys Invalidator
	pub  events.Publisher
}

// NewHandlers wires the SDK key routes. pub announces revocations to the other
// API instances (events.RevocationChannel); a failed announcement is logged and
// does not fail the request, since the cache TTL and the streams' re-check still
// bound the delay.
func NewHandlers(pool *pgxpool.Pool, keys Invalidator, pub events.Publisher) *Handlers {
	return &Handlers{pool: pool, keys: keys, pub: pub}
}

type keyView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	Prefix    string     `json:"prefix"`
	CreatedAt time.Time  `json:"createdAt"`
	RevokedAt *time.Time `json:"revokedAt"`
}

// List handles GET /environments/{envId}/sdk-keys (admin+): the environment's
// keys, newest first. Only the display prefix is ever returned.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	access := rbac.AccessFrom(r.Context())
	rows, err := h.pool.Query(r.Context(), `
		SELECT id::text, name, kind::text, key_prefix, created_at, revoked_at
		FROM api_keys
		WHERE environment_id = $1::uuid AND workspace_id = $2::uuid
		ORDER BY created_at DESC, id`, access.Env.ID, access.Env.WorkspaceID)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (keyView, error) {
		var k keyView
		err := row.Scan(&k.ID, &k.Name, &k.Kind, &k.Prefix, &k.CreatedAt, &k.RevokedAt)
		return k, err
	})
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	if keys == nil {
		keys = []keyView{}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

type createRequest struct {
	Name string `json:"name"`
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func cleanName(s string) (string, error) {
	name := strings.TrimSpace(s)
	if len([]rune(name)) > 60 {
		return "", errors.New("name must be at most 60 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("name must not contain control characters")
		}
	}
	return name, nil
}

// Create handles POST /environments/{envId}/sdk-keys (admin+). The response
// carries the plaintext key exactly once; only its Argon2id hash is stored.
// A workspace may have at most quota.MaxActiveSDKKeys unrevoked SDK keys.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	var req createRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	name, err := cleanName(req.Name)
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	// The display prefix is 8 hex characters (32 bits) and globally unique, so
	// with enough keys two will eventually collide: draw a fresh key instead of
	// failing the request. Each attempt is its own transaction.
	var view keyView
	var plaintext, prefix string
	for attempt := 0; attempt < 5; attempt++ {
		var hash string
		plaintext, prefix, hash, err = GenerateKey(apikey.KindSDK)
		if err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
		err = pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
			if err := quota.CheckSDKKey(ctx, tx, access.Env.WorkspaceID); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `
				INSERT INTO api_keys (workspace_id, environment_id, kind, key_prefix, key_hash, name, created_by)
				VALUES ($1::uuid, $2::uuid, 'sdk', $3, $4, $5, $6::uuid)
				RETURNING id::text, name, kind::text, key_prefix, created_at, revoked_at`,
				access.Env.WorkspaceID, access.Env.ID, prefix, hash, name, access.User.ID,
			).Scan(&view.ID, &view.Name, &view.Kind, &view.Prefix, &view.CreatedAt, &view.RevokedAt); err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{
				WorkspaceID:   access.Env.WorkspaceID,
				ActorID:       access.User.ID,
				ActorEmail:    access.User.Email,
				EnvironmentID: access.Env.ID,
				Action:        "api_key.create",
				ResourceType:  "api_key",
				ResourceID:    view.ID,
				After:         map[string]string{"kind": "sdk", "prefix": prefix, "name": name, "key": "[REDACTED]"},
			})
		})
		if !isUniqueViolation(err, "api_keys_key_prefix_key") {
			break
		}
	}
	if quota.WriteError(w, err) {
		return
	}
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"key": view, "plaintext": plaintext})
}

// Revoke handles DELETE /environments/{envId}/sdk-keys/{keyId} (admin+). The
// verification cache of this instance is cleared right after the commit, so
// the key fails immediately here; other instances honour it within their
// cache TTL, and open /sdk/stream connections within their re-check interval.
// Revoking an already revoked key is a no-op.
func (h *Handlers) Revoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := rbac.AccessFrom(ctx)
	keyID := strings.ToLower(r.PathValue("keyId"))
	if !rbac.IsUUID(keyID) {
		httpx.WriteError(w, http.StatusNotFound, "KEY_NOT_FOUND", "no such key in this environment")
		return
	}
	var prefix string
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var wasRevoked bool
		err := tx.QueryRow(ctx, `
			SELECT key_prefix, revoked_at IS NOT NULL FROM api_keys
			WHERE id = $1::uuid AND environment_id = $2::uuid AND workspace_id = $3::uuid FOR UPDATE`,
			keyID, access.Env.ID, access.Env.WorkspaceID).Scan(&prefix, &wasRevoked)
		if err != nil {
			return err
		}
		if wasRevoked {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE api_keys SET revoked_at = now() WHERE id = $1::uuid`, keyID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID:   access.Env.WorkspaceID,
			ActorID:       access.User.ID,
			ActorEmail:    access.User.Email,
			EnvironmentID: access.Env.ID,
			Action:        "api_key.revoke",
			ResourceType:  "api_key",
			ResourceID:    keyID,
			Severity:      audit.SeverityCritical,
			Before:        map[string]string{"prefix": prefix},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "KEY_NOT_FOUND", "no such key in this environment")
		return
	}
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	h.keys.Invalidate(prefix) // this instance, at once
	if h.pub != nil {         // every other instance
		payload, _ := json.Marshal(map[string]string{"prefix": prefix})
		if err := h.pub.Publish(ctx, events.RevocationChannel, payload); err != nil {
			log.Printf("sdkkeys: could not announce a revocation (other instances honour it within their cache TTL): %v", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
