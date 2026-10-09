package apikey

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/platform/httpx"
)

// HeaderSDKKey is the header SDKs send their key in.
const HeaderSDKKey = "X-Helios-SDK-Key"

type ctxKey struct{}

// Scope is what an SDK key is allowed to see: one environment of one
// workspace. It is derived from the key's own database row, never from
// anything the request says.
type Scope struct {
	WorkspaceID   string
	EnvironmentID string
	// Prefix identifies the key (the part before the secret) so a long-lived
	// stream can re-check that it has not been revoked.
	Prefix string
}

// ScopeFrom returns the scope of the request's SDK key.
func ScopeFrom(ctx context.Context) (Scope, bool) {
	sc, ok := ctx.Value(ctxKey{}).(Scope)
	return sc, ok
}

// EnvironmentID returns the environment the request's SDK key belongs to.
func EnvironmentID(ctx context.Context) (string, bool) {
	sc, ok := ScopeFrom(ctx)
	return sc.EnvironmentID, ok
}

type cacheEntry struct {
	scope   Scope
	expires time.Time
}

// Verifier authenticates SDK keys against the api_keys table.
//
// Argon2id is slow on purpose, which is at odds with the /evaluate latency
// budget, so successful verifications are cached (keyed by a SHA-256 of the
// key, never the key itself) for ttl. The cost: a revoked key keeps working
// for up to ttl on an instance that was not told about the revocation. The
// instance that handles the revocation drops the entry at once (Invalidate),
// so with a single instance the delay is zero; with several, it is at most ttl.
type Verifier struct {
	pool  *pgxpool.Pool
	ttl   time.Duration
	mu    sync.RWMutex
	cache map[[sha256.Size]byte]cacheEntry
}

func NewVerifier(pool *pgxpool.Pool, ttl time.Duration) *Verifier {
	return &Verifier{pool: pool, ttl: ttl, cache: map[[sha256.Size]byte]cacheEntry{}}
}

var errInvalidKey = errors.New("invalid key")

func (v *Verifier) scopeFor(ctx context.Context, plaintext string) (Scope, error) {
	digest := sha256.Sum256([]byte(plaintext))
	now := time.Now()

	v.mu.RLock()
	entry, hit := v.cache[digest]
	v.mu.RUnlock()
	if hit && now.Before(entry.expires) {
		return entry.scope, nil
	}

	prefix, ok := Prefix(plaintext)
	if !ok {
		return Scope{}, errInvalidKey
	}
	sc := Scope{Prefix: prefix}
	var hash string
	err := v.pool.QueryRow(ctx, `
		SELECT workspace_id::text, environment_id::text, key_hash
		FROM api_keys
		WHERE key_prefix = $1 AND kind = 'sdk' AND revoked_at IS NULL`,
		prefix,
	).Scan(&sc.WorkspaceID, &sc.EnvironmentID, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Scope{}, errInvalidKey
	}
	if err != nil {
		return Scope{}, err
	}
	match, err := Verify(plaintext, hash)
	if err != nil {
		return Scope{}, err
	}
	if !match {
		return Scope{}, errInvalidKey
	}

	v.mu.Lock()
	v.cache[digest] = cacheEntry{scope: sc, expires: now.Add(v.ttl)}
	v.mu.Unlock()
	return sc, nil
}

// Invalidate forgets every cached verification of the key with this prefix.
// Call it right after revoking the key.
func (v *Verifier) Invalidate(prefix string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for digest, e := range v.cache {
		if e.scope.Prefix == prefix {
			delete(v.cache, digest)
		}
	}
}

// Active reports whether the key with this prefix exists and is unrevoked. It
// reads the database, bypassing the cache: long-lived /sdk/stream connections
// use it to notice a revocation made on another instance.
func (v *Verifier) Active(ctx context.Context, prefix string) (bool, error) {
	var active bool
	err := v.pool.QueryRow(ctx, `SELECT revoked_at IS NULL FROM api_keys WHERE key_prefix = $1 AND kind = 'sdk'`, prefix).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return active, err
}

// Middleware rejects requests without a valid, unrevoked SDK key with 401
// and stores the key's scope in the request context.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(HeaderSDKKey)
		if key == "" {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing "+HeaderSDKKey+" header")
			return
		}
		sc, err := v.scopeFor(r.Context(), key)
		if errors.Is(err, errInvalidKey) {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or revoked SDK key")
			return
		}
		if err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sc)))
	})
}
