package delivery

import (
	"encoding/base64"
	"strings"
	"testing"

	"helios/backend/internal/platform/config"
)

var key32 = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("K", 32)))

// With nothing configured nothing can be sent: the mail sender is the sink, and webhooks are unavailable.
func TestNothingConfiguredMeansTheSinkAndNoWebhooks(t *testing.T) {
	o, err := FromConfig(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if o.Mail.Mode() != "sink" {
		t.Errorf("default mail mode %q: mail must not be sent by accident", o.Mail.Mode())
	}
	if o.Box.Available() {
		t.Error("webhook secrets can be sealed with no key")
	}
	if o.ConsoleURL == "" {
		t.Error("the sink's links have no console address")
	}
	if o2, err := FromConfig(config.Config{MailMode: "sink"}); err != nil || o2.Mail.Mode() != "sink" {
		t.Errorf("explicit sink: %v", err)
	}
}

func TestRealMailNeedsEverythingItNeedsAndTLS(t *testing.T) {
	smtp := config.Config{MailMode: "smtp", SMTPHost: "smtp.example.com", SMTPPort: "587", SMTPFrom: "noreply@example.com", PublicConsoleURL: "https://console.example.com"}
	o, err := FromConfig(smtp)
	if err != nil || o.Mail.Mode() != "smtp" {
		t.Fatalf("a complete smtp configuration: %v", err)
	}
	for name, mod := range map[string]func(*config.Config){
		"no console address": func(c *config.Config) { c.PublicConsoleURL = "" },
		"no host":            func(c *config.Config) { c.SMTPHost = "" },
		"port not a number":  func(c *config.Config) { c.SMTPPort = "five" },
		"plaintext":          func(c *config.Config) { c.SMTPTLS = "none" },
		"unknown TLS":        func(c *config.Config) { c.SMTPTLS = "ssl" },
		"bad from":           func(c *config.Config) { c.SMTPFrom = "not an address" },
	} {
		c := smtp
		mod(&c)
		if _, err := FromConfig(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c := smtp
	c.SMTPTLS, c.MailAllowInsecure = "none", "true"
	if _, err := FromConfig(c); err != nil {
		t.Errorf("plaintext with the development flag: %v", err)
	}
	if _, err := FromConfig(config.Config{MailMode: "sendgrid"}); err == nil {
		t.Error("an unknown mail mode was accepted (it would silently send nothing, or something)")
	}
}

func TestKeysAreValidatedAndNeverQuoted(t *testing.T) {
	o, err := FromConfig(config.Config{WebhookSecretKey: key32})
	if err != nil || !o.Box.Available() {
		t.Fatalf("a good key: %v", err)
	}
	_, err = FromConfig(config.Config{WebhookSecretKey: "SUPERSECRETBUTTOOSHORT"})
	if err == nil || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("a bad key: %v (the error must not quote the key)", err)
	}
	if o, err := FromConfig(config.Config{WebhookSecretKey: key32, WebhookAllowInsecureLocal: "true"}); err != nil {
		t.Fatal(err)
	} else if _, err := o.Guard.ValidateURL("http://127.0.0.1:9/h"); err != nil {
		t.Errorf("the development switch did not reach the guard: %v", err)
	}
	if o, _ := FromConfig(config.Config{WebhookSecretKey: key32}); o.Guard != nil {
		if _, err := o.Guard.ValidateURL("http://127.0.0.1:9/h"); err == nil {
			t.Error("loopback http accepted without the development switch")
		}
	}
}
