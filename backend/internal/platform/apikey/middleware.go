package apikey

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/httpx"
	"helios/backend/internal/platform/ratelimit"
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
	pool     *pgxpool.Pool
	ttl      time.Duration
	mu       sync.RWMutex
	cache    map[[sha256.Size]byte]cacheEntry
	throttle Throttle

	watchMu  sync.Mutex
	watchers map[string]map[chan struct{}]struct{} // key prefix -> open streams to wake
}

// Throttle bounds how fast one client can make the verifier do expensive work
// (a database read and an Argon2id hash). It only gates cache MISSES: a valid
// key that is already cached is never delayed by it. A client that has used up
// its allowance for failed attempts is refused before any hashing happens.
type Throttle interface {
	// Allowed reports whether this client may try another uncached key.
	Allowed(r *http.Request) (ok bool, retryAfter time.Duration)
	// Failed records a failed verification by this client.
	Failed(r *http.Request)
}

// SetThrottle installs the throttle (nil removes it).
func (v *Verifier) SetThrottle(t Throttle) { v.throttle = t }

func (v *Verifier) cached(plaintext string) (Scope, bool) {
	digest := sha256.Sum256([]byte(plaintext))
	v.mu.RLock()
	entry, hit := v.cache[digest]
	v.mu.RUnlock()
	if hit && time.Now().Before(entry.expires) {
		return entry.scope, true
	}
	return Scope{}, false
}

func NewVerifier(pool *pgxpool.Pool, ttl time.Duration) *Verifier {
	return &Verifier{pool: pool, ttl: ttl, cache: map[[sha256.Size]byte]cacheEntry{}, watchers: map[string]map[chan struct{}]struct{}{}}
}

var errInvalidKey = errors.New("invalid key")

func (v *Verifier) scopeFor(ctx context.Context, plaintext string) (Scope, error) {
	digest := sha256.Sum256([]byte(plaintext))
	now := time.Now()

	if sc, ok := v.cached(plaintext); ok {
		return sc, nil
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

// Invalidate forgets every cached verification of the key with this prefix and
// wakes the open streams using it (they close). Call it right after revoking
// the key, and when another instance announces a revocation.
func (v *Verifier) Invalidate(prefix string) {
	v.mu.Lock()
	for digest, e := range v.cache {
		if e.scope.Prefix == prefix {
			delete(v.cache, digest)
		}
	}
	v.mu.Unlock()

	v.watchMu.Lock()
	for ch := range v.watchers[prefix] {
		select {
		case ch <- struct{}{}:
		default: // already notified
		}
	}
	v.watchMu.Unlock()
}

// InvalidateAll forgets every cached verification. Used after the revocation
// subscription was down: announcements may have been missed.
func (v *Verifier) InvalidateAll() {
	v.mu.Lock()
	v.cache = map[[sha256.Size]byte]cacheEntry{}
	v.mu.Unlock()
}

// Revoked returns a channel that receives when the key with this prefix is
// revoked (announced to this instance), and a function that stops listening.
// /sdk/stream selects on it so a revoked key's streams close at once instead
// of at the next database re-check.
func (v *Verifier) Revoked(prefix string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	v.watchMu.Lock()
	if v.watchers[prefix] == nil {
		v.watchers[prefix] = map[chan struct{}]struct{}{}
	}
	v.watchers[prefix][ch] = struct{}{}
	v.watchMu.Unlock()
	return ch, func() {
		v.watchMu.Lock()
		delete(v.watchers[prefix], ch)
		if len(v.watchers[prefix]) == 0 {
			delete(v.watchers, prefix)
		}
		v.watchMu.Unlock()
	}
}

// Watch applies revocations announced by other instances until ctx ends:
// each message names a key prefix, which is dropped from the cache and wakes
// its streams. It resubscribes after a failure (with backoff) and clears the
// cache whenever a subscription (re)starts, since announcements may have been
// missed in between. With no broker configured it returns at once; the cache
// TTL and the streams' periodic re-check are then the only bound.
func (v *Verifier) Watch(ctx context.Context, sub events.Subscriber) {
	backoff := time.Second
	for ctx.Err() == nil {
		msgs, closeFn, err := sub.Subscribe(ctx, events.RevocationChannel)
		if errors.Is(err, events.ErrUnavailable) {
			return
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		v.InvalidateAll()
		for payload := range msgs {
			var m struct {
				Prefix string `json:"prefix"`
			}
			if json.Unmarshal([]byte(payload), &m) == nil && m.Prefix != "" {
				v.Invalidate(m.Prefix)
			}
		}
		_ = closeFn()
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
		sc, hit := v.cached(key)
		var err error
		if !hit {
			if v.throttle != nil {
				if ok, wait := v.throttle.Allowed(r); !ok {
					ratelimit.Reject(w, wait)
					return
				}
			}
			sc, err = v.scopeFor(r.Context(), key)
		}
		if errors.Is(err, errInvalidKey) {
			if v.throttle != nil {
				v.throttle.Failed(r)
			}
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
