package apitoken

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/httpx"
	"helios/backend/internal/platform/ratelimit"
)

// DefaultCacheTTL bounds how long an instance keeps trusting a verified token
// it was not told was revoked. Revocation is announced to every instance
// (events.RevocationChannel) and dropped at once on the instance that handled it,
// so this is only the worst case when the announcement is lost (broker down).
// What the token may DO is never cached: the creator's role is read on every request.
const DefaultCacheTTL = 15 * time.Second

// lastUsedEvery is how often a token's last_used_at is written at most.
const lastUsedEvery = time.Minute

// Why a token was refused. Only the first is "unknown"; the others are told to
// the audit log and never to the caller.
type Reason string

const (
	ReasonUnknown   Reason = "unknown"
	ReasonExpired   Reason = "expired"
	ReasonRevoked   Reason = "revoked"
	ReasonNoCreator Reason = "creator_gone"
)

// Rejection describes a refused token whose row exists, for the audit log.
type Rejection struct {
	TokenID, WorkspaceID, Name, Prefix, CreatorID, CreatorEmail string
	Reason                                                      Reason
}

type entry struct {
	tok     auth.APIToken
	expires time.Time // the token's own expiry
	loaded  time.Time
}

// Verifier authenticates API tokens against the api_tokens table.
type Verifier struct {
	pool *pgxpool.Pool
	lim  *ratelimit.Limiter
	ttl  time.Duration
	now  func() time.Time
	// OnRejected is told about an expired or revoked token being presented (at
	// most once an hour per token and reason on this instance), to audit it. It
	// must not block for long; its error handling is its own.
	OnRejected func(ctx context.Context, rej Rejection)

	mu       sync.Mutex
	cache    map[[32]byte]entry
	lastUsed map[string]time.Time // token id -> last time this instance wrote last_used_at
	reported map[string]time.Time // token id + reason -> last audit
}

// NewVerifier. ttl <= 0 means DefaultCacheTTL; now nil means the real clock.
func NewVerifier(pool *pgxpool.Pool, lim *ratelimit.Limiter, ttl time.Duration, now func() time.Time) *Verifier {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Verifier{pool: pool, lim: lim, ttl: ttl, now: now,
		cache: map[[32]byte]entry{}, lastUsed: map[string]time.Time{}, reported: map[string]time.Time{}}
}

// Invalidate forgets every cached verification of the token.
func (v *Verifier) Invalidate(tokenID string) {
	v.mu.Lock()
	for d, e := range v.cache {
		if e.tok.ID == tokenID {
			delete(v.cache, d)
		}
	}
	v.mu.Unlock()
}

// InvalidateWorkspace forgets every cached token of the workspace (it was scheduled for deletion).
func (v *Verifier) InvalidateWorkspace(workspaceID string) {
	v.mu.Lock()
	for d, e := range v.cache {
		if e.tok.WorkspaceID == workspaceID {
			delete(v.cache, d)
		}
	}
	v.mu.Unlock()
}

// InvalidateAll forgets everything cached. Used after the revocation
// subscription was down: announcements may have been missed.
func (v *Verifier) InvalidateAll() {
	v.mu.Lock()
	v.cache = map[[32]byte]entry{}
	v.mu.Unlock()
}

// Announce tells every other instance that a token was revoked.
func Announce(ctx context.Context, pub events.Publisher, tokenID string) {
	if pub == nil {
		return
	}
	payload, _ := json.Marshal(map[string]string{"tokenId": tokenID})
	if err := pub.Publish(ctx, events.RevocationChannel, payload); err != nil {
		log.Printf("apitoken: could not announce a revocation (other instances honour it within %s): %v", DefaultCacheTTL, err)
	}
}

// Watch applies revocations announced by other instances until ctx ends. It
// shares the channel the SDK keys use; each side ignores the other's messages.
// It resubscribes after a failure and clears the cache whenever a subscription
// (re)starts, since announcements may have been missed in between.
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
				TokenID     string `json:"tokenId"`
				WorkspaceID string `json:"workspaceId"`
			}
			if json.Unmarshal([]byte(payload), &m) == nil {
				switch {
				case m.WorkspaceID != "":
					v.InvalidateWorkspace(m.WorkspaceID)
				case m.TokenID != "":
					v.Invalidate(m.TokenID)
				}
			}
		}
		_ = closeFn()
	}
}

const selectToken = `
	SELECT t.id::text, t.workspace_id::text, t.name, t.token_prefix, t.role, COALESCE(t.created_by::text, ''), t.created_by_email,
	       t.expires_at, t.revoked_at IS NOT NULL, t.token_hash, w.deleted_at IS NOT NULL,
	       COALESCE((SELECT array_agg(e.environment_id::text ORDER BY e.environment_id) FROM api_token_environments e
	                 WHERE e.workspace_id = t.workspace_id AND e.token_id = t.id), '{}')
	FROM api_tokens t JOIN workspaces w ON w.id = t.workspace_id WHERE t.token_hash = $1`

// lookup finds a well-formed token: from the cache, else the database.
func (v *Verifier) lookup(ctx context.Context, raw string) (auth.APIToken, time.Time, *Rejection, error) {
	digest := [32]byte(Hash(raw))
	now := v.now()
	v.mu.Lock()
	e, hit := v.cache[digest]
	v.mu.Unlock()
	if hit && now.Sub(e.loaded) < v.ttl {
		if !now.Before(e.expires) { // expiry is exact even for a cached token
			return auth.APIToken{}, time.Time{}, &Rejection{TokenID: e.tok.ID, WorkspaceID: e.tok.WorkspaceID, Name: e.tok.Name, Prefix: e.tok.Prefix,
				CreatorID: e.tok.CreatorID, CreatorEmail: e.tok.CreatorEmail, Reason: ReasonExpired}, nil
		}
		return e.tok, e.expires, nil, nil
	}

	var t auth.APIToken
	var expires time.Time
	var revoked, workspaceGone bool
	var stored []byte
	err := v.pool.QueryRow(ctx, selectToken, digest[:]).Scan(&t.ID, &t.WorkspaceID, &t.Name, &t.Prefix, &t.Role, &t.CreatorID, &t.CreatorEmail,
		&expires, &revoked, &stored, &workspaceGone, &t.EnvironmentIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.APIToken{}, time.Time{}, &Rejection{Reason: ReasonUnknown}, nil
	}
	if err != nil {
		return auth.APIToken{}, time.Time{}, nil, err
	}
	if !Equal(stored, digest[:]) { // the index matched, so this cannot differ; compared anyway, in constant time
		return auth.APIToken{}, time.Time{}, &Rejection{Reason: ReasonUnknown}, nil
	}
	rej := func(r Reason) *Rejection {
		return &Rejection{TokenID: t.ID, WorkspaceID: t.WorkspaceID, Name: t.Name, Prefix: t.Prefix, CreatorID: t.CreatorID, CreatorEmail: t.CreatorEmail, Reason: r}
	}
	t.WorkspaceGone = workspaceGone
	switch {
	case workspaceGone && (revoked || !now.Before(expires) || t.CreatorID == ""):
		// Nothing about a workspace scheduled for deletion is audited (it may be restored exactly as it was).
		return auth.APIToken{}, time.Time{}, &Rejection{Reason: ReasonUnknown}, nil
	case revoked:
		return auth.APIToken{}, time.Time{}, rej(ReasonRevoked), nil
	case !now.Before(expires):
		return auth.APIToken{}, time.Time{}, rej(ReasonExpired), nil
	case t.CreatorID == "":
		return auth.APIToken{}, time.Time{}, rej(ReasonNoCreator), nil
	}
	v.mu.Lock()
	if len(v.cache) > 10_000 {
		v.cache = map[[32]byte]entry{}
	}
	v.cache[digest] = entry{tok: t, expires: expires, loaded: now}
	v.mu.Unlock()
	return t, expires, nil, nil
}

// touch records the use of a token: last_used_at, at most once a minute per
// token on this instance, and in SQL at most once a minute across instances.
func (v *Verifier) touch(ctx context.Context, id string) {
	now := v.now()
	v.mu.Lock()
	last, seen := v.lastUsed[id]
	due := !seen || now.Sub(last) >= lastUsedEvery
	if due {
		if len(v.lastUsed) > 10_000 {
			v.lastUsed = map[string]time.Time{}
		}
		v.lastUsed[id] = now
	}
	v.mu.Unlock()
	if !due {
		return
	}
	if _, err := v.pool.Exec(ctx, `
		UPDATE api_tokens SET last_used_at = $2::timestamptz
		WHERE id = $1::uuid AND (last_used_at IS NULL OR last_used_at <= $2::timestamptz - interval '1 minute')`, id, now); err != nil {
		log.Printf("apitoken: could not record a token's last use: %v", err) // never the token
	}
}

func (v *Verifier) report(ctx context.Context, rej Rejection) {
	if v.OnRejected == nil || rej.TokenID == "" {
		return
	}
	key := rej.TokenID + "/" + string(rej.Reason)
	now := v.now()
	v.mu.Lock()
	last, seen := v.reported[key]
	due := !seen || now.Sub(last) >= time.Hour
	if due {
		if len(v.reported) > 10_000 {
			v.reported = map[string]time.Time{}
		}
		v.reported[key] = now
	}
	v.mu.Unlock()
	if due {
		v.OnRejected(ctx, rej)
	}
}

// Wrap returns the authentication middleware for one route: a bearer that
// starts with "hlt_" is authenticated as an API token, anything else goes to
// the user JWT middleware unchanged. tokenAllowed is the route's classification
// in the router registry: a valid token on a route that is not marked is refused
// with 403 whatever its role.
//
// Order of work for a token, cheapest first, so garbage costs almost nothing:
//  1. the per-IP bucket of FAILED token attempts (no hashing, no database);
//  2. the shape check (no hashing, no database);
//  3. the lookup by SHA-256 (cache, then one indexed read);
//  4. the route's classification, then the per-token rate limit.
//
// Every refusal of the credential itself is the same 401 with the same body, so
// a caller cannot tell an unknown token from an expired or a revoked one.
func (v *Verifier) Wrap(jwt func(http.Handler) http.Handler, tokenAllowed bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		viaJWT := jwt(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || !HasPrefix(raw) {
				viaJWT.ServeHTTP(w, r)
				return
			}
			ip := v.lim.ClientIPOf(r)
			if ok, wait := v.lim.Peek(ratelimit.ClassTokenBad, ip); !ok {
				ratelimit.Reject(w, wait)
				return
			}
			fail := func() {
				v.lim.Take(ratelimit.ClassTokenBad, ip)
				httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token")
			}
			if !WellFormed(raw) {
				fail()
				return
			}
			tok, _, rej, err := v.lookup(r.Context(), raw)
			if err != nil {
				httpx.WriteInternal(w, r, err)
				return
			}
			if rej != nil {
				v.report(r.Context(), *rej)
				fail()
				return
			}
			if !tokenAllowed {
				httpx.WriteError(w, http.StatusForbidden, "TOKEN_NOT_ALLOWED", "API tokens cannot be used on this route")
				return
			}
			if ok, wait := v.lim.Take(ratelimit.ClassToken, tok.ID); !ok {
				ratelimit.Reject(w, wait)
				return
			}
			if !tok.WorkspaceGone {
				v.touch(r.Context(), tok.ID)
			}
			ctx := auth.WithUser(r.Context(), auth.User{ID: tok.CreatorID, Email: tok.CreatorEmail, EmailStatus: auth.EmailConfirmed})
			next.ServeHTTP(w, r.WithContext(auth.WithAPIToken(ctx, tok)))
		})
	}
}
