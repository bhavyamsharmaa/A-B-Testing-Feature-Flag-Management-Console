// Package config loads Helios backend configuration from the environment, so the
// same binary runs unchanged across local, CI, and production.
package config

import (
	"os"
	"strings"
)

// Config holds process configuration.
type Config struct {
	Port               string   // HTTP listen port
	DatabaseURL        string   // PostgreSQL connection string
	RedisAddr          string   // Redis host:port
	SupabaseURL        string   // https://<project-ref>.supabase.co; its JWKS verifies access tokens
	CORSAllowedOrigins []string // browser origins allowed to call the API, e.g. the Vercel URL
}

// Load reads configuration from the environment, applying defaults that match
// the local docker-compose services.
func Load() Config {
	return Config{
		Port:               getenv("PORT", "8080"),
		DatabaseURL:        getenv("DATABASE_URL", "postgres://helios:helios@localhost:5432/helios?sslmode=disable"),
		RedisAddr:          getenv("REDIS_ADDR", "localhost:6379"),
		SupabaseURL:        os.Getenv("SUPABASE_URL"),
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
