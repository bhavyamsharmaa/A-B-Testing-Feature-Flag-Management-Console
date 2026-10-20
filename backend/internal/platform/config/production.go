package config

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
)

// Environment is HELIOS_ENV: the switch between a developer's machine and a deployment.
//
// In production mode the process REFUSES TO START if any development-only setting is on, or if something production
// needs is missing (ProductionProblems). Development is the default when HELIOS_ENV is unset, so a laptop needs
// nothing; the container image sets HELIOS_ENV=production, so a deployment gets the checks without anyone remembering.
type Environment string

const (
	Development Environment = "development"
	Production  Environment = "production"
)

// ParseEnvironment reads HELIOS_ENV. Anything but the known words is an error: a typo ("prodution") must not
// quietly select development mode.
func ParseEnvironment(v string) (Environment, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "development", "dev", "local":
		return Development, nil
	case "production", "prod":
		return Production, nil
	}
	return Development, fmt.Errorf("HELIOS_ENV must be development or production (got %q)", v)
}

// Role is which process is asking: the checks that apply differ.
type Role string

const (
	RoleAPI    Role = "api"    // everything
	RoleOutbox Role = "outbox" // the job that sends webhooks and invite email
	RoleJob    Role = "job"    // the other jobs: they only need the database
)

// DevFlags names every setting that is for development only, with why. docs/DEPLOYMENT.md lists the same set (a test
// keeps them equal).
var DevFlags = []struct{ Name, Why string }{
	{"WEBHOOK_ALLOW_INSECURE_LOCAL", "lets webhooks use http and loopback addresses, which defeats the guard against server-side request forgery"},
	{"MAIL_MODE=sink", "invite email goes nowhere (a development sink); production sends through MAIL_MODE=smtp"},
	{"MAIL_SINK_DIR", "writes every message, invite links included, to files"},
	{"MAIL_ALLOW_INSECURE_SMTP / SMTP_TLS=none", "sends email without encryption"},
	{"RATE_LIMIT_DISABLED=true", "turns every rate limit off"},
	{"EMAIL_CONFIRMATION=off", "lets people with unconfirmed email addresses create workspaces and accept invites"},
	{"EXPOSURE_TOKEN_KEY unset", "a random key is generated per process, so exposure tokens fail on any other instance and after any restart"},
	{"DATABASE_URL unset or sslmode=disable", "the default points at a local database without TLS"},
	{"SUPABASE_URL over http", "tokens would be verified against an unencrypted (development) auth server"},
	{"CORS_ALLOWED_ORIGINS with localhost, http or *", "lets local or unencrypted pages call the API from a browser"},
	{"LISTEN_ADDR on loopback", "nothing outside the container could reach the API"},
}

// ProductionProblems returns what makes this configuration unfit for production, one sentence each. It names settings,
// never values. problems are fatal in production mode; warnings are logged but do not stop the start.
func (c Config) ProductionProblems(role Role) (problems, warnings []string) {
	p := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	w := func(format string, a ...any) { warnings = append(warnings, fmt.Sprintf(format, a...)) }

	// the database, for every process
	if c.DatabaseURL == "" || c.DatabaseURL == defaultDatabaseURL {
		p("DATABASE_URL is not set (the default is a local database without TLS)")
	} else if u, err := url.Parse(c.DatabaseURL); err == nil {
		switch u.Query().Get("sslmode") {
		case "disable", "allow", "prefer":
			p("DATABASE_URL has sslmode=%s: production connections to the database must be encrypted (use sslmode=require or verify-full)", u.Query().Get("sslmode"))
		}
	}
	if role == RoleJob {
		return
	}

	// delivery: webhooks and email, for the API and the outbox job
	if c.WebhookAllowInsecureLocal == "true" {
		p("WEBHOOK_ALLOW_INSECURE_LOCAL=true: webhooks could target http and loopback addresses (development only)")
	}
	if c.MailAllowInsecure == "true" || strings.EqualFold(c.SMTPTLS, "none") {
		p("MAIL_ALLOW_INSECURE_SMTP / SMTP_TLS=none: email would be sent without encryption (development only)")
	}
	if c.MailSinkDir != "" {
		p("MAIL_SINK_DIR is set: every message, invite links included, would be written to files (development only)")
	}
	if !strings.EqualFold(c.MailMode, "smtp") {
		w("MAIL_MODE is not smtp: invite email is not actually sent (the invite link still works if the inviter shares it)")
	}
	if role == RoleOutbox {
		return
	}

	// the API
	if c.RateLimitDisabled == "true" {
		p("RATE_LIMIT_DISABLED=true: every rate limit would be off (development only)")
	}
	if strings.EqualFold(strings.TrimSpace(c.EmailConfirmation), "off") {
		p("EMAIL_CONFIRMATION=off: unconfirmed email addresses could create workspaces and accept invites (use enforce or strict)")
	}
	if strings.TrimSpace(c.ExposureTokenKey) == "" {
		p("EXPOSURE_TOKEN_KEY is not set: a random key per process would make exposure tokens fail on other instances and after restarts")
	}
	if c.SupabaseURL == "" {
		p("SUPABASE_URL is not set")
	} else if u, err := url.Parse(c.SupabaseURL); err != nil || u.Scheme != "https" {
		p("SUPABASE_URL must be https in production")
	}
	if len(c.CORSAllowedOrigins) == 0 {
		w("CORS_ALLOWED_ORIGINS is empty: browsers on other origins (the console) cannot call this API")
	}
	for _, o := range c.CORSAllowedOrigins {
		u, err := url.Parse(o)
		switch {
		case o == "*":
			p("CORS_ALLOWED_ORIGINS contains *")
		case err != nil || u.Scheme != "https":
			p("CORS_ALLOWED_ORIGINS contains a non-https origin")
		case isLocalHost(u.Hostname()):
			p("CORS_ALLOWED_ORIGINS contains a local origin")
		}
	}
	if c.PublicConsoleURL == "" {
		w("PUBLIC_CONSOLE_URL is not set: invite emails cannot link to the console")
	} else if u, err := url.Parse(c.PublicConsoleURL); err != nil || u.Scheme != "https" || isLocalHost(u.Hostname()) {
		p("PUBLIC_CONSOLE_URL must be the console's https address")
	}
	if addr, err := c.ListenAddress(); err == nil {
		if host, _, err := net.SplitHostPort(addr); err == nil && host != "" && (host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
			p("the API would listen on loopback (%s): nothing outside the container could reach it", addr)
		}
	}
	if c.PprofAddr != "" {
		w("PPROF_ADDR is set: profiling is on (loopback only); turn it off unless you are investigating")
	}
	if c.RedisURL == "" {
		w("REDIS_URL is not set: flag changes reach other instances only when their cache entries expire, and /sdk/stream is unavailable")
	}
	if c.WebhookSecretKey == "" {
		w("WEBHOOK_SECRET_KEY is not set: webhooks are unavailable")
	}
	if c.TrustedProxyHops == "" || c.TrustedProxyHops == "0" {
		w("TRUSTED_PROXY_HOPS is 0: behind a load balancer every client shares its address, and so one rate-limit bucket")
	}
	return
}

func isLocalHost(h string) bool {
	h = strings.ToLower(h)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

const defaultDatabaseURL = "postgres://helios:helios@localhost:5432/helios?sslmode=disable"

// CheckEnvironment reads HELIOS_ENV and, in production, the problems and warnings for this role. The caller refuses
// to start when env is Production and problems is not empty.
func (c Config) CheckEnvironment(role Role) (env Environment, problems, warnings []string, err error) {
	env, err = ParseEnvironment(c.Environment)
	if err != nil {
		return env, nil, nil, err
	}
	if env == Production {
		problems, warnings = c.ProductionProblems(role)
	}
	return env, problems, warnings, nil
}

// MustBeSafe is called first thing by every command: in production mode it logs each problem and stops the process
// before it connects to anything; in development it only says which mode it is in. It returns the mode.
func MustBeSafe(c Config, role Role) Environment {
	env, problems, warnings, err := c.CheckEnvironment(role)
	if err != nil {
		log.Fatal(err)
	}
	for _, w := range warnings {
		log.Printf("production warning: %s", w)
	}
	if len(problems) > 0 {
		for _, p := range problems {
			log.Printf("production check FAILED: %s", p)
		}
		log.Fatalf("refusing to start in production mode (HELIOS_ENV=production): %d development-only or missing setting(s) listed above; see docs/DEPLOYMENT.md", len(problems))
	}
	if env == Production {
		log.Printf("production mode: all checks passed")
	} else if role == RoleAPI {
		log.Printf("development mode (HELIOS_ENV is not production): development-only settings are allowed; a deployment must set HELIOS_ENV=production")
	}
	return env
}
