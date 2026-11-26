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

// EnvironmentID returns the environment the request's SDK key belongs to.
func EnvironmentID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(ctxKey{}).(string)
	return id, ok
}

type cacheEntry struct {
	environmentID string
	expires       time.Time
}

// Verifier authenticates SDK keys against the api_keys table.
//
// Argon2id is slow on purpose, which is at odds with the /evaluate latency
// budget, so successful verifications are cached (keyed by a SHA-256 of the
// key, never the key itself) for ttl. The cost: a revoked key keeps working
// for up to ttl on each instance.
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

func (v *Verifier) environmentFor(ctx context.Context, plaintext string) (string, error) {
	digest := sha256.Sum256([]byte(plaintext))
	now := time.Now()

	v.mu.RLock()
	entry, hit := v.cache[digest]
	v.mu.RUnlock()
	if hit && now.Before(entry.expires) {
		return entry.environmentID, nil
	}

	prefix, ok := Prefix(plaintext)
	if !ok {
		return "", errInvalidKey
	}
	var envID, hash string
	err := v.pool.QueryRow(ctx, `
		SELECT environment_id::text, key_hash
		FROM api_keys
		WHERE key_prefix = $1 AND kind = 'sdk' AND revoked_at IS NULL`,
		prefix,
	).Scan(&envID, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errInvalidKey
	}
	if err != nil {
		return "", err
	}
	match, err := Verify(plaintext, hash)
	if err != nil {
		return "", err
	}
	if !match {
		return "", errInvalidKey
	}

	v.mu.Lock()
	v.cache[digest] = cacheEntry{environmentID: envID, expires: now.Add(v.ttl)}
	v.mu.Unlock()
	return envID, nil
}

// Middleware rejects requests without a valid, unrevoked SDK key with 401
// and stores the key's environment in the request context.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(HeaderSDKKey)
		if key == "" {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing "+HeaderSDKKey+" header")
			return
		}
		envID, err := v.environmentFor(r.Context(), key)
		if errors.Is(err, errInvalidKey) {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or revoked SDK key")
			return
		}
		if err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, envID)))
	})
}
