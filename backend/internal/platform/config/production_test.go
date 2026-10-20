package config

import (
	"strings"
	"testing"
)

// goodProduction is a configuration that passes every check.
func goodProduction() Config {
	return Config{
		Environment:      "production",
		DatabaseURL:      "postgres://u:p@aws-0.pooler.supabase.com:6543/postgres?sslmode=require",
		SupabaseURL:      "https://abc.supabase.co",
		RedisURL:         "rediss://default:x@host:6379",
		ExposureTokenKey: "k1:0123456789abcdef0123456789abcdef",
		WebhookSecretKey: "x", CORSAllowedOrigins: []string{"https://console.example.com"},
		PublicConsoleURL: "https://console.example.com", MailMode: "smtp", TrustedProxyHops: "1",
		EmailConfirmation: "",
	}
}

func problemsOf(c Config, role Role) string {
	p, _ := c.ProductionProblems(role)
	return strings.Join(p, " | ")
}

func TestAGoodProductionConfigurationHasNoProblems(t *testing.T) {
	if p, w := goodProduction().ProductionProblems(RoleAPI); len(p) != 0 || len(w) != 0 {
		t.Errorf("problems %v warnings %v", p, w)
	}
}

func TestEveryDevelopmentOnlySettingIsRefusedInProduction(t *testing.T) {
	for name, c := range map[string]struct {
		mod  func(*Config)
		want string
		role Role
	}{
		"insecure local webhooks":  {func(c *Config) { c.WebhookAllowInsecureLocal = "true" }, "WEBHOOK_ALLOW_INSECURE_LOCAL", RoleOutbox},
		"insecure smtp":            {func(c *Config) { c.MailAllowInsecure = "true" }, "without encryption", RoleOutbox},
		"smtp tls none":            {func(c *Config) { c.SMTPTLS = "none" }, "without encryption", RoleOutbox},
		"a mail sink directory":    {func(c *Config) { c.MailSinkDir = "/tmp/mail" }, "MAIL_SINK_DIR", RoleOutbox},
		"rate limits off":          {func(c *Config) { c.RateLimitDisabled = "true" }, "RATE_LIMIT_DISABLED", RoleAPI},
		"email confirmation off":   {func(c *Config) { c.EmailConfirmation = "off" }, "EMAIL_CONFIRMATION=off", RoleAPI},
		"no exposure token key":    {func(c *Config) { c.ExposureTokenKey = "" }, "EXPOSURE_TOKEN_KEY", RoleAPI},
		"no database url":          {func(c *Config) { c.DatabaseURL = defaultDatabaseURL }, "DATABASE_URL is not set", RoleJob},
		"database without tls":     {func(c *Config) { c.DatabaseURL = "postgres://u:p@h/db?sslmode=disable" }, "sslmode=disable", RoleJob},
		"http supabase":            {func(c *Config) { c.SupabaseURL = "http://127.0.0.1:54321" }, "SUPABASE_URL must be https", RoleAPI},
		"no supabase":              {func(c *Config) { c.SupabaseURL = "" }, "SUPABASE_URL is not set", RoleAPI},
		"cors wildcard":            {func(c *Config) { c.CORSAllowedOrigins = []string{"*"} }, "contains *", RoleAPI},
		"cors localhost":           {func(c *Config) { c.CORSAllowedOrigins = []string{"https://localhost:5173"} }, "local origin", RoleAPI},
		"cors http":                {func(c *Config) { c.CORSAllowedOrigins = []string{"http://console.example.com"} }, "non-https", RoleAPI},
		"console url on localhost": {func(c *Config) { c.PublicConsoleURL = "http://127.0.0.1:5173" }, "PUBLIC_CONSOLE_URL must be", RoleAPI},
		"listening on loopback":    {func(c *Config) { c.listenAddr = "127.0.0.1:8080" }, "loopback", RoleAPI},
	} {
		cfg := goodProduction()
		c.mod(&cfg)
		if got := problemsOf(cfg, c.role); !strings.Contains(got, c.want) {
			t.Errorf("%s: problems %q do not mention %q", name, got, c.want)
		}
	}
}

func TestRolesOnlyCheckWhatTheyUse(t *testing.T) {
	cfg := goodProduction()
	cfg.RateLimitDisabled, cfg.ExposureTokenKey, cfg.WebhookAllowInsecureLocal = "true", "", "true"
	if got := problemsOf(cfg, RoleJob); got != "" {
		t.Errorf("a job only needs the database: %q", got)
	}
	if got := problemsOf(cfg, RoleOutbox); !strings.Contains(got, "WEBHOOK_ALLOW_INSECURE_LOCAL") || strings.Contains(got, "RATE_LIMIT") {
		t.Errorf("the outbox job checks delivery settings only: %q", got)
	}
	if got := problemsOf(cfg, RoleAPI); !strings.Contains(got, "RATE_LIMIT") || !strings.Contains(got, "EXPOSURE_TOKEN_KEY") {
		t.Errorf("the API checks everything: %q", got)
	}
}

func TestProblemsNameSettingsAndNeverQuoteValues(t *testing.T) {
	cfg := goodProduction()
	cfg.DatabaseURL = "postgres://admin:hunter2-very-secret@db.internal/prod?sslmode=disable"
	cfg.ExposureTokenKey = ""
	cfg.CORSAllowedOrigins = []string{"http://secret-host.example.com"}
	p, w := cfg.ProductionProblems(RoleAPI)
	all := strings.Join(append(p, w...), " ")
	for _, leak := range []string{"hunter2", "admin", "db.internal", "secret-host"} {
		if strings.Contains(all, leak) {
			t.Errorf("a problem quotes %q: %s", leak, all)
		}
	}
}

func TestHeliosEnvIsStrictAndDevelopmentIsTheDefault(t *testing.T) {
	for in, want := range map[string]Environment{"": Development, "development": Development, "DEV": Development, "local": Development, "production": Production, " Prod ": Production} {
		if got, err := ParseEnvironment(in); err != nil || got != want {
			t.Errorf("%q -> %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"prodution", "staging", "true", "1"} {
		if _, err := ParseEnvironment(bad); err == nil {
			t.Errorf("%q accepted: a typo must not select development mode", bad)
		}
	}
	// development mode never reports problems, whatever is on
	dev := goodProduction()
	dev.Environment, dev.RateLimitDisabled, dev.WebhookAllowInsecureLocal = "development", "true", "true"
	if _, p, _, err := dev.CheckEnvironment(RoleAPI); err != nil || len(p) != 0 {
		t.Errorf("development: %v %v", p, err)
	}
	prod := dev
	prod.Environment = "production"
	if env, p, _, err := prod.CheckEnvironment(RoleAPI); err != nil || env != Production || len(p) < 2 {
		t.Errorf("production: %v %v %v", env, p, err)
	}
}

func TestEveryDevFlagIsListed(t *testing.T) {
	// the documented list and the checks must not drift apart: each name appears in ProductionProblems' source of truth
	names := map[string]bool{}
	for _, f := range DevFlags {
		names[f.Name] = true
	}
	for _, want := range []string{"WEBHOOK_ALLOW_INSECURE_LOCAL", "RATE_LIMIT_DISABLED=true", "EMAIL_CONFIRMATION=off", "MAIL_SINK_DIR", "MAIL_MODE=sink"} {
		if !names[want] {
			t.Errorf("DevFlags lacks %q", want)
		}
	}
}
