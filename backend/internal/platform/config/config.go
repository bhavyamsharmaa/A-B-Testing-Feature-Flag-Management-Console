// Package config loads Helios backend configuration from the environment, so the
// same binary runs unchanged across local, CI, and production.
package config

import (
	"os"
	"strings"
)

// Config holds process configuration.
type Config struct {
	Port              string // HTTP listen port
	DatabaseURL       string // PostgreSQL connection string
	RedisURL          string // redis:// or rediss:// connection string; empty disables propagation (falls back to Postgres-only, never blocks startup)
	SupabaseURL       string // https://<project-ref>.supabase.co; its JWKS verifies access tokens
	SupabaseAnonKey   string // the project's PUBLIC anon/publishable key (not a secret); lets the backend ask Supabase whether an email is confirmed
	EmailConfirmation string // off | enforce (default) | strict; see auth.EmailPolicy
	// Rate limiting (all optional; see internal/platform/ratelimit).
	RateLimitDisabled  string   // "true" switches every limit off
	RateLimits         string   // overrides, e.g. "me=5/60,accept=2/50" (per second / burst)
	TrustedProxyHops   string   // reverse proxies in front of the API (Render: 1)
	StreamsPerKey      string   // max concurrent /sdk/stream connections per SDK key
	CORSAllowedOrigins []string // browser origins allowed to call the API, e.g. the Vercel URL
}

// Load reads configuration from the environment, applying defaults that match
// the local docker-compose services.
func Load() Config {
	return Config{
		Port:               getenv("PORT", "8080"),
		DatabaseURL:        getenv("DATABASE_URL", "postgres://helios:helios@localhost:5432/helios?sslmode=disable"),
		RedisURL:           os.Getenv("REDIS_URL"),
		SupabaseURL:        os.Getenv("SUPABASE_URL"),
		SupabaseAnonKey:    os.Getenv("SUPABASE_ANON_KEY"),
		EmailConfirmation:  os.Getenv("EMAIL_CONFIRMATION"),
		RateLimitDisabled:  os.Getenv("RATE_LIMIT_DISABLED"),
		RateLimits:         os.Getenv("RATE_LIMITS"),
		TrustedProxyHops:   os.Getenv("TRUSTED_PROXY_HOPS"),
		StreamsPerKey:      os.Getenv("STREAMS_PER_KEY"),
		CORSAllowedOrigins: splitList(os.Getenv("CORS_ALLOWED_ORIGINS")),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimRight(strings.TrimSpace(part), "/"); p != "" {
			out = append(out, p)
		}
	}
	return out
}
