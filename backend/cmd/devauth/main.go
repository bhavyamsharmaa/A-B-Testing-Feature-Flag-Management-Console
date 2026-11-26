// Command devauth is a LOCAL-ONLY stand-in for Supabase Auth, so the console
// and the backend can be run end to end on a laptop without a Supabase
// project. It speaks just enough of the GoTrue API for supabase-js
// (signUp, signInWithPassword, token refresh, getUser, signOut) and publishes
// a JWKS the backend's verifier accepts. Users are written to the local
// database's auth.users table so workspaces can reference them.
//
//	DATABASE_URL=postgres://helios@127.0.0.1:55432/helios go run ./cmd/devauth
//	SUPABASE_URL=http://127.0.0.1:54321 go run ./cmd/api
//
// Accounts are confirmed immediately, except an address containing
// "+unconfirmed" (alice+unconfirmed@test.dev): it signs up unconfirmed but gets
// a session anyway, like a Supabase project that allows unverified sign-ins.
// Its tokens say user_metadata.email_verified=false until
// GET /dev/confirm?email=... is called and the session is refreshed. This lets
// tests cover both kinds of user. -require-confirmation instead withholds the
// session until confirmed, like Supabase with "Confirm email" on.
//
// It refuses to start unless DATABASE_URL points at this machine, signs with
// a throwaway key generated at startup, and keeps passwords in memory only.
// It is not an authentication system: never deploy it.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"helios/backend/internal/platform/db"
)

const kid = "devauth-1"

type account struct {
	ID        string
	Email     string
	Hash      []byte
	Confirmed bool
	// SessionBeforeConfirm: the account may sign in while unconfirmed.
	SessionBeforeConfirm bool
	Created              time.Time
}

type server struct {
	pool          *pgxpool.Pool
	key           *ecdsa.PrivateKey
	publicURL     string
	needConfirm   bool
	mu            sync.Mutex
	byEmail       map[string]*account
	byID          map[string]*account
	refreshTokens map[string]string // refresh token -> account id
}

func main() {
	addr := flag.String("addr", "127.0.0.1:54321", "listen address (must be loopback)")
	publicURL := flag.String("public-url", "http://127.0.0.1:54321", "the URL clients and the backend use for this server (becomes the token issuer: <url>/auth/v1)")
	requireConfirmation := flag.Bool("require-confirmation", false, "sign-up needs confirming via GET /dev/confirm?email=... first, like Supabase with email confirmation on")
	flag.Parse()

	if !isLoopback(*addr) {
		log.Fatalf("devauth only listens on loopback addresses, got %q", *addr)
	}
	dbURL := os.Getenv("DATABASE_URL")
	if err := checkLocalDatabase(dbURL); err != nil {
		log.Fatalf("devauth: %v", err)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dbURL)
	if err != nil {
		log.Fatalf("devauth: %v", err)
	}
	defer pool.Close()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{
		pool: pool, key: key, publicURL: strings.TrimRight(*publicURL, "/"), needConfirm: *requireConfirmation,
		byEmail: map[string]*account{}, byID: map[string]*account{}, refreshTokens: map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/v1/.well-known/jwks.json", s.jwks)
	mux.HandleFunc("POST /auth/v1/signup", s.signup)
	mux.HandleFunc("POST /auth/v1/token", s.token)
	mux.HandleFunc("POST /auth/v1/resend", s.resend)
	mux.HandleFunc("POST /auth/v1/logout", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /auth/v1/user", s.user)
	mux.HandleFunc("GET /dev/confirm", s.confirm)
	mux.HandleFunc("GET /dev/health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })

	log.Printf("devauth (LOCAL ONLY) listening on http://%s, issuer %s/auth/v1, confirmation required: %v", *addr, s.publicURL, s.needConfirm)
	log.Fatal((&http.Server{Addr: *addr, Handler: cors(mux), ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}

func isLoopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	return host == "127.0.0.1" || host == "localhost" || host == "[::1]"
}

// checkLocalDatabase refuses anything that is not this machine, so a stray
// DATABASE_URL in the shell can never make devauth write to a shared database.
func checkLocalDatabase(raw string) error {
	if raw == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("DATABASE_URL is not a URL")
	}
	if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" && h != "::1" {
		return fmt.Errorf("DATABASE_URL must point at this machine (127.0.0.1 or localhost), got host %q", h)
	}
	return nil
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "authorization, x-client-info, apikey, content-type, x-supabase-api-version")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		h.Set("Access-Control-Expose-Headers", "x-supabase-api-version")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// authError mimics GoTrue's error body, which supabase-js maps to AuthApiError.
func authError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"code": status, "error_code": code, "msg": msg})
}

func (s *server) jwks(w http.ResponseWriter, r *http.Request) {
	pub := s.key.PublicKey
	b64 := base64.RawURLEncoding
	pad := func(b []byte) []byte { // fixed 32-byte coordinates
		out := make([]byte, 32)
		copy(out[32-len(b):], b)
		return out
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": kid,
		"x": b64.EncodeToString(pad(pub.X.Bytes())), "y": b64.EncodeToString(pad(pub.Y.Bytes())),
	}}})
}

type credentials struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	RefreshToken string `json:"refresh_token"`
}

func (s *server) userJSON(a *account) map[string]any {
	confirmed := any(nil)
	if a.Confirmed {
		confirmed = a.Created.Format(time.RFC3339)
	}
	return map[string]any{
		"id": a.ID, "aud": "authenticated", "role": "authenticated", "email": a.Email,
		"email_confirmed_at": confirmed, "phone": "", "confirmed_at": confirmed,
		"last_sign_in_at": time.Now().Format(time.RFC3339),
		"app_metadata":    map[string]any{"provider": "email", "providers": []string{"email"}},
		"user_metadata":   map[string]any{"email": a.Email, "email_verified": a.Confirmed, "sub": a.ID}, "identities": []any{},
		"created_at": a.Created.Format(time.RFC3339), "updated_at": a.Created.Format(time.RFC3339),
		"is_anonymous": false,
	}
}

func (s *server) session(a *account) (map[string]any, error) {
	now := time.Now()
	exp := now.Add(time.Hour)
	claims := jwt.MapClaims{
		"iss": s.publicURL + "/auth/v1", "aud": "authenticated", "sub": a.ID, "email": a.Email,
		"role": "authenticated", "iat": now.Unix(), "exp": exp.Unix(), "session_id": newID(),
		"app_metadata": map[string]any{"provider": "email"},
		// GoTrue puts the confirmation state in user_metadata; the backend reads it from here.
		"user_metadata": map[string]any{"email": a.Email, "email_verified": a.Confirmed, "sub": a.ID},
		"is_anonymous":  false,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = kid
	access, err := tok.SignedString(s.key)
	if err != nil {
		return nil, err
	}
	refresh := newID()
	s.mu.Lock()
	s.refreshTokens[refresh] = a.ID
	s.mu.Unlock()
	return map[string]any{
		"access_token": access, "token_type": "bearer", "expires_in": 3600, "expires_at": exp.Unix(),
		"refresh_token": refresh, "user": s.userJSON(a),
	}, nil
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *server) signup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		authError(w, 400, "bad_json", "invalid request body")
		return
	}
	addr, err := mail.ParseAddress(c.Email)
	if err != nil || addr.Address != strings.TrimSpace(c.Email) {
		authError(w, 422, "validation_failed", "Unable to validate email address: invalid format")
		return
	}
	email := strings.ToLower(addr.Address)
	if len(c.Password) < 6 {
		authError(w, 422, "weak_password", "Password should be at least 6 characters.")
		return
	}
	s.mu.Lock()
	_, exists := s.byEmail[email]
	s.mu.Unlock()
	if exists {
		authError(w, 422, "user_already_exists", "User already registered")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.MinCost)
	if err != nil {
		authError(w, 500, "unexpected_failure", "could not hash password")
		return
	}
	// Reuse the id if a previous devauth run already created this user, so
	// their workspaces survive a restart of this server.
	var id string
	if err := s.pool.QueryRow(r.Context(),
		`INSERT INTO auth.users (email) VALUES ($1) ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email RETURNING id::text`,
		email).Scan(&id); err != nil {
		log.Printf("devauth: insert user: %v", err)
		authError(w, 500, "unexpected_failure", "could not create user")
		return
	}
	local, _, _ := strings.Cut(email, "@")
	unverifiedSignIn := strings.Contains(local, "+unconfirmed")
	a := &account{ID: id, Email: email, Hash: hash, Confirmed: !s.needConfirm && !unverifiedSignIn,
		SessionBeforeConfirm: unverifiedSignIn, Created: time.Now()}
	s.mu.Lock()
	s.byEmail[email], s.byID[id] = a, a
	s.mu.Unlock()
	if s.needConfirm && !unverifiedSignIn {
		// Like Supabase with confirmation on: a user, no session.
		u := s.userJSON(a)
		u["confirmation_sent_at"] = time.Now().Format(time.RFC3339)
		writeJSON(w, http.StatusOK, u)
		return
	}
	sess, err := s.session(a)
	if err != nil {
		authError(w, 500, "unexpected_failure", "could not sign token")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *server) token(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		authError(w, 400, "bad_json", "invalid request body")
		return
	}
	switch r.URL.Query().Get("grant_type") {
	case "password":
		s.mu.Lock()
		a := s.byEmail[strings.ToLower(strings.TrimSpace(c.Email))]
		s.mu.Unlock()
		if a == nil || bcrypt.CompareHashAndPassword(a.Hash, []byte(c.Password)) != nil {
			authError(w, 400, "invalid_credentials", "Invalid login credentials")
			return
		}
		if !a.Confirmed && !a.SessionBeforeConfirm {
			authError(w, 400, "email_not_confirmed", "Email not confirmed")
			return
		}
		s.respondSession(w, a)
	case "refresh_token":
		s.mu.Lock()
		id, ok := s.refreshTokens[c.RefreshToken]
		delete(s.refreshTokens, c.RefreshToken)
		a := s.byID[id]
		s.mu.Unlock()
		if !ok || a == nil {
			authError(w, 400, "refresh_token_not_found", "Invalid Refresh Token: Refresh Token Not Found")
			return
		}
		s.respondSession(w, a)
	default:
		authError(w, 400, "validation_failed", "unsupported grant_type")
	}
}

func (s *server) respondSession(w http.ResponseWriter, a *account) {
	sess, err := s.session(a)
	if err != nil {
		authError(w, 500, "unexpected_failure", "could not sign token")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *server) user(w http.ResponseWriter, r *http.Request) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		authError(w, 401, "no_authorization", "This endpoint requires a Bearer token")
		return
	}
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return &s.key.PublicKey, nil },
		jwt.WithValidMethods([]string{"ES256"})); err != nil {
		authError(w, 401, "bad_jwt", "invalid JWT")
		return
	}
	id, _ := claims["sub"].(string)
	s.mu.Lock()
	a := s.byID[id]
	s.mu.Unlock()
	if a == nil {
		authError(w, 401, "user_not_found", "User from sub claim in JWT does not exist")
		return
	}
	writeJSON(w, http.StatusOK, s.userJSON(a))
}

// confirm is the dev stand-in for clicking the link in the confirmation email.
func (s *server) confirm(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(r.URL.Query().Get("email"))
	s.mu.Lock()
	a := s.byEmail[email]
	if a != nil {
		a.Confirmed = true
	}
	s.mu.Unlock()
	if a == nil {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	_, _ = fmt.Fprintf(w, "confirmed %s\n", email)
}

// resend is the stand-in for "send the confirmation email again".
func (s *server) resend(w http.ResponseWriter, r *http.Request) {
	var c struct {
		Type  string `json:"type"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil || c.Type == "" || c.Email == "" {
		authError(w, 400, "validation_failed", "type and email are required")
		return
	}
	log.Printf("devauth: confirmation email requested (no mail is sent); confirm with GET /dev/confirm?email=<address>")
	writeJSON(w, http.StatusOK, map[string]any{})
}
