// Package config loads Helios backend configuration from the environment, so the
// same binary runs unchanged across local, CI, and production.
package config

import "os"

// Config holds process configuration.
type Config struct {
	Port        string // HTTP listen port
	DatabaseURL string // PostgreSQL connection string
	RedisAddr   string // Redis host:port
}

// Load reads configuration from the environment, applying defaults that match
// the local docker-compose services.
func Load() Config {
	return Config{
		Port:        getenv("PORT", "8080"),
		DatabaseURL: getenv("DATABASE_URL", "postgres://helios:helios@localhost:5432/helios?sslmode=disable"),
		RedisAddr:   getenv("REDIS_ADDR", "localhost:6379"),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
