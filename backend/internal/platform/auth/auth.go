// Package auth verifies Supabase-issued access tokens and carries the
// authenticated user through the request context. Signup, login, and
// password handling all stay in Supabase Auth; this package only checks
// the resulting JWT.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"

	"helios/backend/internal/platform/httpx"
)

// User is the caller identified by a verified access token.
type User struct {
	ID    string // Supabase auth.users.id
	Email string
}

type ctxKey struct{}

// FromContext returns the user stored by Middleware.
func FromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(ctxKey{}).(User)
	return u, ok
}

// WithUser stores u in ctx. Exported for tests and internal tooling.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

type claims struct {
	Email string `json:"email"`
	Role  string `json:"role"`
	jwt.RegisteredClaims
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Verifier checks access tokens against the public keys the Supabase
// project publishes at its JWKS endpoint. Only asymmetric algorithms are
// accepted: allowing HS256 would let anyone who has fetched the public key
// sign tokens with it as an HMAC secret.
type Verifier struct {
	keys   jwt.Keyfunc
	parser *jwt.Parser
}

// NewVerifier fetches the JWKS for the project at supabaseURL (e.g.
// https://<ref>.supabase.co) and keeps it fresh in the background until ctx
// ends. It fails if the first fetch fails or returns no keys, so a wrong
// SUPABASE_URL stops the deploy instead of starting a server that answers
// every request with 401.
func NewVerifier(ctx context.Context, supabaseURL string) (*Verifier, error) {
	base, err := url.Parse(strings.TrimRight(supabaseURL, "/"))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, fmt.Errorf("auth: SUPABASE_URL must look like https://<project-ref>.supabase.co, got %q", supabaseURL)
	}
	issuer := base.String() + "/auth/v1"
	jwksURL := issuer + "/.well-known/jwks.json"

	failFirstFetch := false
	kf, err := keyfunc.NewDefaultOverrideCtx(ctx, []string{jwksURL}, keyfunc.Override{
		NoErrorReturnFirstHTTPReq: &failFirstFetch,
		HTTPTimeout:               10 * time.Second,
		// Pick up rotated or revoked signing keys within 10 minutes.
		RefreshInterval: 10 * time.Minute,
		// A token with an unknown kid triggers an immediate refetch (at most
		// once per 5 minutes). Past that budget, fail at once instead of
		// holding the request open, so tokens with made-up kids can't stall
		// the server.
		RateLimitWaitMax: time.Millisecond,
	})
	if err != nil {
		return nil, fmt.Errorf("auth: fetch signing keys from %s: %w", jwksURL, err)
	}
	all, err := kf.Storage().KeyReadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: read signing keys: %w", err)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("auth: %s returned no signing keys", jwksURL)
	}

	return &Verifier{
		keys: kf.Keyfunc,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{"ES256", "RS256"}),
			jwt.WithIssuer(issuer),
			jwt.WithAudience("authenticated"),
			jwt.WithExpirationRequired(),
			jwt.WithLeeway(30*time.Second),
		),
	}, nil
}

var errNotUserSession = errors.New("token is not a user session")

// Verify checks a token's signature, expiry, issuer, and audience, then that
// it belongs to a signed-in user. The role and subject checks keep out
// Supabase's non-user tokens (anon, service_role), which may be signed by
// the same keys.
func (v *Verifier) Verify(raw string) (User, error) {
	var c claims
	if _, err := v.parser.ParseWithClaims(raw, &c, v.keys); err != nil {
		return User{}, err
	}
	if c.Role != "authenticated" || !uuidRE.MatchString(c.Subject) {
		return User{}, errNotUserSession
	}
	return User{ID: strings.ToLower(c.Subject), Email: c.Email}, nil
}

// Middleware rejects requests without a valid "Authorization: Bearer <jwt>"
// header with 401, and otherwise stores the caller in the request context.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer token")
			return
		}
		user, err := v.Verify(raw)
		if err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
	})
}
