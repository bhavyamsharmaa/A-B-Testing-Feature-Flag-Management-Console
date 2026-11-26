package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// EmailStatus is what is known about whether the user's email address was
// confirmed.
type EmailStatus string

const (
	EmailConfirmed   EmailStatus = "confirmed"
	EmailUnconfirmed EmailStatus = "unconfirmed"
	EmailUnknown     EmailStatus = "unknown"
)

// ErrEmailNotConfirmed: the user's email is not confirmed (or, in strict mode,
// not known to be).
var ErrEmailNotConfirmed = errors.New("email not confirmed")

// ErrEmailStatusUnavailable: the status could not be established right now
// (the identity provider did not answer). Callers answer 503, not 403.
var ErrEmailStatusUnavailable = errors.New("email confirmation status unavailable")

type tokenKey struct{}

// withToken keeps the raw bearer token (for the confirmation lookup only;
// it is never logged or returned).
func withToken(ctx context.Context, raw string) context.Context {
	return context.WithValue(ctx, tokenKey{}, raw)
}

// WithToken is withToken for tests and internal tooling.
func WithToken(ctx context.Context, raw string) context.Context { return withToken(ctx, raw) }

func tokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(tokenKey{}).(string)
	return t
}

// EmailMode says how strictly confirmation is required.
type EmailMode string

const (
	// EmailOff: no check.
	EmailOff EmailMode = "off"
	// EmailEnforce: refuse when the best available signal says the address is
	// NOT confirmed; allow when nothing is known.
	EmailEnforce EmailMode = "enforce"
	// EmailStrict: refuse unless the address is positively known to be confirmed.
	EmailStrict EmailMode = "strict"
)

// ParseEmailMode reads EMAIL_CONFIRMATION; empty means "enforce".
func ParseEmailMode(s string) (EmailMode, bool) {
	switch EmailMode(strings.ToLower(strings.TrimSpace(s))) {
	case "":
		return EmailEnforce, true
	case EmailOff:
		return EmailOff, true
	case EmailEnforce:
		return EmailEnforce, true
	case EmailStrict:
		return EmailStrict, true
	}
	return "", false
}

// EmailPolicy decides whether a user may bootstrap a workspace or accept an
// invite. Two signals, both read from Supabase, never from the client:
//
//  1. the verified token's own claims (User.EmailStatus), when the project puts
//     any there;
//  2. optionally, Supabase's own answer: GET <SUPABASE_URL>/auth/v1/user made
//     with the user's token (and the PUBLIC anon key) returns email_confirmed_at.
//     It is authoritative and overrides the claims.
//
// What neither can show: with "Confirm email" switched OFF, Supabase marks every
// new address confirmed in its database, so such an address looks confirmed.
// This check cannot detect that setting; see docs/SECURITY_REVIEW.md.
type EmailPolicy struct {
	mode   EmailMode
	lookup func(ctx context.Context, token string) (EmailStatus, error)

	mu    sync.Mutex
	cache map[string]cachedStatus
}

type cachedStatus struct {
	status  EmailStatus
	expires time.Time
}

// NewEmailPolicy builds the policy. With anonKey empty only token claims are
// used.
func NewEmailPolicy(mode EmailMode, supabaseURL, anonKey string, client *http.Client) *EmailPolicy {
	p := &EmailPolicy{mode: mode, cache: map[string]cachedStatus{}}
	if anonKey != "" && supabaseURL != "" {
		if client == nil {
			client = &http.Client{Timeout: 5 * time.Second}
		}
		p.lookup = userEndpointLookup(strings.TrimRight(supabaseURL, "/"), anonKey, client)
	}
	return p
}

// Mode reports the configured mode.
func (p *EmailPolicy) Mode() EmailMode { return p.mode }

// HasLookup reports whether Supabase's user endpoint is consulted.
func (p *EmailPolicy) HasLookup() bool { return p.lookup != nil }

func userEndpointLookup(base, anonKey string, client *http.Client) func(context.Context, string) (EmailStatus, error) {
	return func(ctx context.Context, token string) (EmailStatus, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/auth/v1/user", nil)
		if err != nil {
			return EmailUnknown, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("apikey", anonKey)
		res, err := client.Do(req)
		if err != nil {
			return EmailUnknown, errors.New("user lookup failed") // never wrap: the URL and headers stay out of errors
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return EmailUnknown, errors.New("user lookup refused")
		}
		var body struct {
			EmailConfirmedAt *string `json:"email_confirmed_at"`
			ConfirmedAt      *string `json:"confirmed_at"`
		}
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			return EmailUnknown, errors.New("user lookup unreadable")
		}
		if (body.EmailConfirmedAt != nil && *body.EmailConfirmedAt != "") || (body.ConfirmedAt != nil && *body.ConfirmedAt != "") {
			return EmailConfirmed, nil
		}
		return EmailUnconfirmed, nil
	}
}

// Require returns nil when the user may proceed. It uses the token stored by
// Middleware for the Supabase lookup, if one is configured.
func (p *EmailPolicy) Require(ctx context.Context, user User) error {
	if p == nil || p.mode == EmailOff {
		return nil
	}
	status := user.EmailStatus
	if status == "" {
		status = EmailUnknown
	}
	if p.lookup != nil {
		if token := tokenFrom(ctx); token != "" {
			s, err := p.lookupCached(ctx, user.ID, token)
			switch {
			case err == nil:
				status = s // Supabase's own answer wins over the token's claims
			case status != EmailConfirmed:
				return ErrEmailStatusUnavailable // can't tell, and the claims don't vouch: don't guess
			}
		}
	}
	switch status {
	case EmailConfirmed:
		return nil
	case EmailUnconfirmed:
		return ErrEmailNotConfirmed
	default:
		if p.mode == EmailStrict {
			return ErrEmailNotConfirmed
		}
		return nil
	}
}

// Confirmed addresses stay confirmed, so they are remembered for a while; an
// unconfirmed answer is only kept for a few seconds so that confirming (and
// pressing "continue") takes effect at once.
func (p *EmailPolicy) lookupCached(ctx context.Context, userID, token string) (EmailStatus, error) {
	key := userID + ":" + string(sha256Sum(token))
	now := time.Now()
	p.mu.Lock()
	if c, ok := p.cache[key]; ok && now.Before(c.expires) {
		p.mu.Unlock()
		return c.status, nil
	}
	p.mu.Unlock()

	s, err := p.lookup(ctx, token)
	if err != nil {
		return EmailUnknown, err
	}
	ttl := 5 * time.Minute
	if s != EmailConfirmed {
		ttl = 3 * time.Second
	}
	p.mu.Lock()
	if len(p.cache) > 10_000 { // bounded: forget everything rather than grow forever
		p.cache = map[string]cachedStatus{}
	}
	p.cache[key] = cachedStatus{status: s, expires: now.Add(ttl)}
	p.mu.Unlock()
	return s, nil
}

func sha256Sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
