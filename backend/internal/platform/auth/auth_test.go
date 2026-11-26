package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const userID = "8f14e45f-ceea-467a-9575-4a9f1c3e0b2d"

// fakeSupabase serves a JWKS the way a Supabase project does, at
// <url>/auth/v1/.well-known/jwks.json, with one P-256 and one RSA key.
type fakeSupabase struct {
	*httptest.Server
	ec  *ecdsa.PrivateKey
	rsa *rsa.PrivateKey
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func newFakeSupabase(t *testing.T) *fakeSupabase {
	t.Helper()
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecdhPub, err := ecKey.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	point := ecdhPub.Bytes() // 0x04 || X || Y
	jwks := map[string]any{"keys": []map[string]string{
		{"kty": "EC", "crv": "P-256", "kid": "ec-1", "alg": "ES256", "use": "sig",
			"x": b64(point[1:33]), "y": b64(point[33:65])},
		{"kty": "RSA", "kid": "rsa-1", "alg": "RS256", "use": "sig",
			"n": b64(rsaKey.N.Bytes()), "e": b64(big.NewInt(int64(rsaKey.E)).Bytes())},
	}}
	f := &fakeSupabase{ec: ecKey, rsa: rsaKey}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/v1/.well-known/jwks.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSupabase) verifier(t *testing.T) *Verifier {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	v, err := NewVerifier(ctx, f.URL)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f *fakeSupabase) claims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":   f.URL + "/auth/v1",
		"sub":   userID,
		"email": "dev@helios.dev",
		"role":  "authenticated",
		"aud":   "authenticated",
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
}

func sign(t *testing.T, method jwt.SigningMethod, kid string, key any, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerifyAcceptsES256AndRS256(t *testing.T) {
	f := newFakeSupabase(t)
	v := f.verifier(t)
	for name, tok := range map[string]string{
		"ES256": sign(t, jwt.SigningMethodES256, "ec-1", f.ec, f.claims()),
		"RS256": sign(t, jwt.SigningMethodRS256, "rsa-1", f.rsa, f.claims()),
	} {
		u, err := v.Verify(tok)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if u.ID != userID || u.Email != "dev@helios.dev" {
			t.Fatalf("%s: got %+v", name, u)
		}
	}
}

func TestVerifyRejects(t *testing.T) {
	f := newFakeSupabase(t)
	v := f.verifier(t)
	with := func(mutate func(jwt.MapClaims)) jwt.MapClaims {
		c := f.claims()
		mutate(c)
		return c
	}
	otherEC, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	cases := map[string]string{
		// Classic alg confusion: HS256 "signed" with something public.
		"HS256 with public key as secret": sign(t, jwt.SigningMethodHS256, "ec-1", []byte(f.URL), f.claims()),
		"HS256 without kid":               sign(t, jwt.SigningMethodHS256, "", []byte("legacy-secret"), f.claims()),
		"forged key reusing real kid":     sign(t, jwt.SigningMethodES256, "ec-1", otherEC, f.claims()),
		"unknown kid":                     sign(t, jwt.SigningMethodES256, "made-up", otherEC, f.claims()),
		"RS256 token claiming EC kid":     sign(t, jwt.SigningMethodRS256, "ec-1", f.rsa, f.claims()),
		"alg none":                        sign(t, jwt.SigningMethodNone, "ec-1", jwt.UnsafeAllowNoneSignatureType, f.claims()),
		"expired":                         sign(t, jwt.SigningMethodES256, "ec-1", f.ec, with(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() })),
		"no exp":                          sign(t, jwt.SigningMethodES256, "ec-1", f.ec, with(func(c jwt.MapClaims) { delete(c, "exp") })),
		"other project's issuer":          sign(t, jwt.SigningMethodES256, "ec-1", f.ec, with(func(c jwt.MapClaims) { c["iss"] = "https://other.supabase.co/auth/v1" })),
		"wrong audience":                  sign(t, jwt.SigningMethodES256, "ec-1", f.ec, with(func(c jwt.MapClaims) { c["aud"] = "anon" })),
		"anon role":                       sign(t, jwt.SigningMethodES256, "ec-1", f.ec, with(func(c jwt.MapClaims) { c["role"] = "anon"; delete(c, "sub") })),
		"service_role":                    sign(t, jwt.SigningMethodES256, "ec-1", f.ec, with(func(c jwt.MapClaims) { c["role"] = "service_role" })),
		"non-uuid sub":                    sign(t, jwt.SigningMethodES256, "ec-1", f.ec, with(func(c jwt.MapClaims) { c["sub"] = "admin" })),
		"garbage":                         "not.a.jwt",
	}
	for name, tok := range cases {
		if _, err := v.Verify(tok); err == nil {
			t.Errorf("%s: accepted, want rejected", name)
		}
	}
}

// Tokens with made-up kids trigger a JWKS refetch, which is rate limited.
// Once over budget they must fail immediately, not hold the request open.
func TestUnknownKIDsFailFast(t *testing.T) {
	f := newFakeSupabase(t)
	v := f.verifier(t)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	start := time.Now()
	for i := range 20 {
		tok := sign(t, jwt.SigningMethodES256, "bogus-"+string(rune('a'+i)), other, f.claims())
		if _, err := v.Verify(tok); err == nil {
			t.Fatal("bogus kid accepted")
		}
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("20 unknown-kid tokens took %v; requests are stalling on the refetch rate limit", elapsed)
	}
}

func TestNewVerifierFailsFast(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{"", "ckmzqbomqnhi", "ftp://x.supabase.co"} {
		if _, err := NewVerifier(ctx, bad); err == nil {
			t.Errorf("NewVerifier(%q) succeeded", bad)
		}
	}

	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	if _, err := NewVerifier(ctx, down.URL); err == nil {
		t.Error("unreachable JWKS accepted")
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer empty.Close()
	if _, err := NewVerifier(ctx, empty.URL); err == nil {
		t.Error("empty JWKS accepted")
	}
}

func TestMiddleware(t *testing.T) {
	f := newFakeSupabase(t)
	v := f.verifier(t)
	var seen User
	h := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = FromContext(r.Context())
	}))

	for _, header := range []string{"", "Bearer ", "Basic abc", "Bearer nope"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d, want 401", header, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+sign(t, jwt.SigningMethodES256, "ec-1", f.ec, f.claims()))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || seen.ID != userID {
		t.Fatalf("valid token: status %d, user %+v", rec.Code, seen)
	}
}
