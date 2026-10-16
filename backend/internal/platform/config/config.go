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
	RateLimitDisabled string // "true" switches every limit off
	RateLimits        string // overrides, e.g. "me=5/60,accept=2/50" (per second / burst)
	TrustedProxyHops  string // reverse proxies in front of the API (Render: 1)
	StreamsPerKey     string // max concurrent /sdk/stream connections per SDK key
	// InvitesByID: "false" disables accepting an invite by its id (and hides
	// open invites in /me), leaving only the one-time link. See docs/SECURITY_REVIEW.md.
	InvitesByID string
	// Signed exposure tokens (see internal/platform/exposuretoken). The key is a
	// SECRET: "<id>:<secret of at least 32 bytes>". The previous key is only for rotation.
	ExposureTokenKey         string
	ExposureTokenKeyPrevious string
	ExposureVerification     string // off (default) | report | required
	ExposureTokenMaxAge      string // e.g. "24h"
	// SchedulerSecret (a SECRET, at least 32 characters) enables POST
	// /internal/scheduler/run; unset leaves it answering 404. SchedulerGrace is how
	// late a scheduled change may still run (default 15m); an unparseable value is
	// an error at startup, not a silent default.
	SchedulerSecret    string
	SchedulerGrace     string
	CORSAllowedOrigins []string // browser origins allowed to call the API, e.g. the Vercel URL

	// Webhooks and the outbox (see docs/WEBHOOKS.md, docs/OUTBOX.md). WebhookSecretKey is a SECRET:
	// 32 random bytes, base64 (openssl rand -base64 32); without it webhooks are unavailable
	// (fail closed). The previous key is only for rotation. WebhookAllowInsecureLocal ("true")
	// lets endpoints use http and loopback addresses: a development switch, never for production.
	WebhookSecretKey          string
	WebhookSecretKeyPrevious  string
	WebhookAllowInsecureLocal string
	// OutboxSecret (a SECRET, at least 32 characters) enables POST /internal/outbox/run; unset leaves it answering 404.
	OutboxSecret string

	// Email (invites). MailMode is "sink" (the default: nothing is sent) or "smtp".
	MailMode          string
	MailSinkDir       string // sink: also write each message to a file here (development only; the files hold invite links)
	MailAllowInsecure string // "true" allows SMTP_TLS=none (a local catcher)
	SMTPHost          string
	SMTPPort          string
	SMTPUsername      string
	SMTPPassword      string // a SECRET
	SMTPFrom          string
	SMTPTLS           string // starttls (default) | implicit | none
	// PublicConsoleURL is the console's origin, used for the link in invite emails.
	PublicConsoleURL string
}

// Load reads configuration from the environment, applying defaults that match
// the local docker-compose services.
func Load() Config {
	return Config{
		Port:                     getenv("PORT", "8080"),
		DatabaseURL:              getenv("DATABASE_URL", "postgres://helios:helios@localhost:5432/helios?sslmode=disable"),
		RedisURL:                 os.Getenv("REDIS_URL"),
		SupabaseURL:              os.Getenv("SUPABASE_URL"),
		SupabaseAnonKey:          os.Getenv("SUPABASE_ANON_KEY"),
		EmailConfirmation:        os.Getenv("EMAIL_CONFIRMATION"),
		RateLimitDisabled:        os.Getenv("RATE_LIMIT_DISABLED"),
		RateLimits:               os.Getenv("RATE_LIMITS"),
		TrustedProxyHops:         trustedProxyHops(),
		InvitesByID:              os.Getenv("INVITES_BY_ID"),
		StreamsPerKey:            os.Getenv("STREAMS_PER_KEY"),
		ExposureTokenKey:         os.Getenv("EXPOSURE_TOKEN_KEY"),
		ExposureTokenKeyPrevious: os.Getenv("EXPOSURE_TOKEN_KEY_PREVIOUS"),
		ExposureVerification:     os.Getenv("EXPOSURE_VERIFICATION"),
		ExposureTokenMaxAge:      os.Getenv("EXPOSURE_TOKEN_MAX_AGE"),
		SchedulerSecret:          os.Getenv("SCHEDULER_SECRET"),
		SchedulerGrace:           os.Getenv("SCHEDULER_GRACE"),
		CORSAllowedOrigins:       splitList(os.Getenv("CORS_ALLOWED_ORIGINS")),

		WebhookSecretKey:          os.Getenv("WEBHOOK_SECRET_KEY"),
		WebhookSecretKeyPrevious:  os.Getenv("WEBHOOK_SECRET_KEY_PREVIOUS"),
		WebhookAllowInsecureLocal: os.Getenv("WEBHOOK_ALLOW_INSECURE_LOCAL"),
		OutboxSecret:              os.Getenv("OUTBOX_SECRET"),
		MailMode:                  os.Getenv("MAIL_MODE"),
		MailSinkDir:               os.Getenv("MAIL_SINK_DIR"),
		MailAllowInsecure:         os.Getenv("MAIL_ALLOW_INSECURE_SMTP"),
		SMTPHost:                  os.Getenv("SMTP_HOST"),
		SMTPPort:                  os.Getenv("SMTP_PORT"),
		SMTPUsername:              os.Getenv("SMTP_USERNAME"),
		SMTPPassword:              os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:                  os.Getenv("SMTP_FROM"),
		SMTPTLS:                   os.Getenv("SMTP_TLS"),
		PublicConsoleURL:          strings.TrimRight(os.Getenv("PUBLIC_CONSOLE_URL"), "/"),
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

// trustedProxyHops is TRUSTED_PROXY_HOPS, or 1 on Render when unset: Render
// puts one reverse proxy in front of every service and sets RENDER=true. Left at
// 0 there, every client would share the proxy's address and therefore one
// rate-limit bucket.
func trustedProxyHops() string {
	if v := os.Getenv("TRUSTED_PROXY_HOPS"); v != "" {
		return v
	}
	if os.Getenv("RENDER") == "true" {
		return "1"
	}
	return ""
}
