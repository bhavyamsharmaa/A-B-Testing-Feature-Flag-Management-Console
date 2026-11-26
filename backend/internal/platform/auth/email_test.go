package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// The status comes from a token whose signature was verified: Verify reads it.
func TestVerifyReadsTheEmailConfirmationSignals(t *testing.T) {
	f := newFakeSupabase(t)
	v := f.verifier(t)
	with := func(mod func(jwt.MapClaims)) string {
		c := f.claims()
		mod(c)
		return sign(t, jwt.SigningMethodES256, "ec-1", f.ec, c)
	}
	for name, tc := range map[string]struct {
		token string
		want  EmailStatus
	}{
		"no signal at all (documented Supabase tokens)": {with(func(c jwt.MapClaims) {}), EmailUnknown},
		"user_metadata.email_verified true":             {with(func(c jwt.MapClaims) { c["user_metadata"] = map[string]any{"email_verified": true} }), EmailConfirmed},
		"user_metadata.email_verified false":            {with(func(c jwt.MapClaims) { c["user_metadata"] = map[string]any{"email_verified": false} }), EmailUnconfirmed},
		"top-level email_verified true":                 {with(func(c jwt.MapClaims) { c["email_verified"] = true }), EmailConfirmed},
		"top-level email_verified false":                {with(func(c jwt.MapClaims) { c["email_verified"] = false }), EmailUnconfirmed},
		"confirmation timestamp":                        {with(func(c jwt.MapClaims) { c["email_confirmed_at"] = "2026-01-01T00:00:00Z" }), EmailConfirmed},
		"a contradiction counts as unconfirmed": {with(func(c jwt.MapClaims) {
			c["email_confirmed_at"] = "2026-01-01T00:00:00Z"
			c["user_metadata"] = map[string]any{"email_verified": false}
		}), EmailUnconfirmed},
		"wrong type is ignored": {with(func(c jwt.MapClaims) { c["user_metadata"] = map[string]any{"email_verified": "true"} }), EmailUnknown},
	} {
		u, err := v.Verify(tc.token)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if u.EmailStatus != tc.want {
			t.Errorf("%s: status %q, want %q", name, u.EmailStatus, tc.want)
		}
	}

	// A claim in a token that fails verification is never read: forging
	// "email_verified": true needs the signing key.
	forged := with(func(c jwt.MapClaims) { c["user_metadata"] = map[string]any{"email_verified": true} })
	parts := strings.Split(forged, ".")
	if _, err := v.Verify(parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2]))); err == nil {
		t.Error("a token with a bad signature was accepted")
	}
}

func TestEmailPolicyModes(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		mode   EmailMode
		status EmailStatus
		want   error
	}{
		"off, unconfirmed":      {EmailOff, EmailUnconfirmed, nil},
		"enforce, confirmed":    {EmailEnforce, EmailConfirmed, nil},
		"enforce, unconfirmed":  {EmailEnforce, EmailUnconfirmed, ErrEmailNotConfirmed},
		"enforce, unknown":      {EmailEnforce, EmailUnknown, nil},
		"enforce, empty status": {EmailEnforce, "", nil},
		"strict, confirmed":     {EmailStrict, EmailConfirmed, nil},
		"strict, unconfirmed":   {EmailStrict, EmailUnconfirmed, ErrEmailNotConfirmed},
		"strict, unknown":       {EmailStrict, EmailUnknown, ErrEmailNotConfirmed},
		"strict, empty status":  {EmailStrict, "", ErrEmailNotConfirmed},
	} {
		got := NewEmailPolicy(tc.mode, "", "", nil).Require(ctx, User{ID: "u", EmailStatus: tc.status})
		if !errors.Is(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
	if err := (*EmailPolicy)(nil).Require(ctx, User{EmailStatus: EmailUnconfirmed}); err != nil {
		t.Errorf("a nil policy must not block: %v", err)
	}
	for in, want := range map[string]EmailMode{"": EmailEnforce, "ENFORCE": EmailEnforce, " strict ": EmailStrict, "off": EmailOff} {
		if m, ok := ParseEmailMode(in); !ok || m != want {
			t.Errorf("ParseEmailMode(%q) = %q, %v", in, m, ok)
		}
	}
	if _, ok := ParseEmailMode("sometimes"); ok {
		t.Error("an unknown mode was accepted")
	}
}

// Supabase's own answer (GET /auth/v1/user with the user's token and the public
// anon key) overrides the token's claims and is cached for confirmed users.
func TestEmailPolicyAsksSupabase(t *testing.T) {
	var calls atomic.Int32
	var answer atomic.Value // string body
	answer.Store(`{"email_confirmed_at": "2026-01-01T00:00:00Z"}`)
	var status atomic.Int32
	status.Store(200)
	var sawKey, sawBearer atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		sawKey.Store(r.Header.Get("apikey"))
		sawBearer.Store(r.Header.Get("Authorization"))
		if r.URL.Path != "/auth/v1/user" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(answer.Load().(string)))
	}))
	defer srv.Close()

	user := User{ID: "u1", EmailStatus: EmailUnknown}
	ctx := WithToken(context.Background(), "the-users-token")
	p := NewEmailPolicy(EmailStrict, srv.URL, "public-anon-key", nil)
	if !p.HasLookup() {
		t.Fatal("no lookup configured")
	}

	// Confirmed according to Supabase, although the token says nothing; cached.
	if err := p.Require(ctx, user); err != nil {
		t.Fatalf("confirmed user refused: %v", err)
	}
	if err := p.Require(ctx, user); err != nil || calls.Load() != 1 {
		t.Errorf("second check: err %v, %d lookups, want 1 (confirmed is cached)", err, calls.Load())
	}
	if sawKey.Load() != "public-anon-key" || sawBearer.Load() != "Bearer the-users-token" {
		t.Errorf("lookup sent apikey %v and Authorization %v", sawKey.Load(), sawBearer.Load())
	}

	// Supabase says unconfirmed: that beats a token claiming "confirmed".
	answer.Store(`{"email_confirmed_at": null, "confirmed_at": null}`)
	p = NewEmailPolicy(EmailEnforce, srv.URL, "public-anon-key", nil)
	if err := p.Require(ctx, User{ID: "u2", EmailStatus: EmailConfirmed}); !errors.Is(err, ErrEmailNotConfirmed) {
		t.Errorf("claims said confirmed, Supabase said not: %v", err)
	}

	// Supabase unreachable or refusing: a confirmed claim is trusted, anything else is "try again", never "allowed".
	status.Store(500)
	p = NewEmailPolicy(EmailStrict, srv.URL, "public-anon-key", nil)
	if err := p.Require(ctx, User{ID: "u3", EmailStatus: EmailConfirmed}); err != nil {
		t.Errorf("a confirmed claim should survive a lookup failure: %v", err)
	}
	for _, st := range []EmailStatus{EmailUnknown, EmailUnconfirmed} {
		if err := p.Require(ctx, User{ID: "u4", EmailStatus: st}); !errors.Is(err, ErrEmailStatusUnavailable) {
			t.Errorf("status %q during an outage: %v, want ErrEmailStatusUnavailable", st, err)
		}
	}
	srv.Close()
	if err := p.Require(ctx, User{ID: "u5", EmailStatus: EmailUnknown}); !errors.Is(err, ErrEmailStatusUnavailable) {
		t.Errorf("Supabase down: %v", err)
	}

	// Errors never carry the token, the key or the URL.
	for _, e := range []error{ErrEmailNotConfirmed, ErrEmailStatusUnavailable} {
		for _, secret := range []string{"the-users-token", "public-anon-key", "127.0.0.1"} {
			if strings.Contains(e.Error(), secret) {
				t.Errorf("%v contains %q", e, secret)
			}
		}
	}
}

// Unconfirmed accounts are refused again right after the lookup says so, and
// let through as soon as they confirm (the short negative cache expires).
func TestEmailPolicyNoticesConfirmationQuickly(t *testing.T) {
	var confirmed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if confirmed.Load() {
			_, _ = w.Write([]byte(`{"email_confirmed_at": "2026-01-01T00:00:00Z"}`))
			return
		}
		_, _ = w.Write([]byte(`{"email_confirmed_at": null}`))
	}))
	defer srv.Close()
	p := NewEmailPolicy(EmailEnforce, srv.URL, "k", nil)
	ctx := WithToken(context.Background(), "tok")
	if err := p.Require(ctx, User{ID: "u"}); !errors.Is(err, ErrEmailNotConfirmed) {
		t.Fatalf("unconfirmed: %v", err)
	}
	confirmed.Store(true)
	// A fresh token (what refreshing the session after confirming gives) is looked up again at once.
	if err := p.Require(WithToken(context.Background(), "tok-after-refresh"), User{ID: "u"}); err != nil {
		t.Errorf("after confirming: %v", err)
	}
}
