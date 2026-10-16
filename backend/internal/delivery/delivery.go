// Package delivery assembles the outbox worker from configuration, so the API
// process (test pings, resends) and cmd/outbox build it identically.
package delivery

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/invitemail"
	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/controlplane/webhooks"
	"helios/backend/internal/platform/config"
	"helios/backend/internal/platform/mail"
	"helios/backend/internal/platform/netguard"
	"helios/backend/internal/platform/secretbox"
)

// Options are the parts a worker is built from.
type Options struct {
	Box        *secretbox.Box
	Guard      *netguard.Guard
	Mail       mail.Sender
	ConsoleURL string
	Outbox     outbox.Config
}

// FromConfig reads the environment-driven options. It fails on a setting that is
// present and wrong; it never fails on one that is merely absent (webhooks then
// report themselves unavailable, and mail goes to the sink).
func FromConfig(cfg config.Config) (Options, error) {
	box, err := secretbox.New(cfg.WebhookSecretKey, cfg.WebhookSecretKeyPrevious)
	if err != nil {
		return Options{}, fmt.Errorf("WEBHOOK_SECRET_KEY: %w", err) // the message never quotes the key
	}
	o := Options{
		Box:        box,
		Guard:      netguard.New(netguard.Policy{AllowLocalHTTP: cfg.WebhookAllowInsecureLocal == "true"}),
		ConsoleURL: cfg.PublicConsoleURL,
	}
	switch strings.ToLower(cfg.MailMode) {
	case "", "sink":
		o.Mail = &mail.Sink{Dir: cfg.MailSinkDir}
		if o.ConsoleURL == "" {
			o.ConsoleURL = "http://localhost:5173" // the sink is for development; the link only has to look right
		}
	case "smtp":
		port, err := strconv.Atoi(cfg.SMTPPort)
		if err != nil {
			return Options{}, errors.New("SMTP_PORT must be a number")
		}
		s, err := mail.NewSMTP(mail.SMTPConfig{
			Host: cfg.SMTPHost, Port: port, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
			From: cfg.SMTPFrom, TLS: strings.ToLower(cfg.SMTPTLS), AllowInsecure: cfg.MailAllowInsecure == "true",
		})
		if err != nil {
			return Options{}, err
		}
		o.Mail = s
		if o.ConsoleURL == "" {
			return Options{}, errors.New("PUBLIC_CONSOLE_URL (the console's address, for the link in invite emails) is required with MAIL_MODE=smtp")
		}
	default:
		return Options{}, fmt.Errorf("MAIL_MODE must be sink or smtp, got %q", cfg.MailMode)
	}
	return o, nil
}

// NewWorker builds the worker with the webhook and invite-email senders.
func NewWorker(pool *pgxpool.Pool, o Options) *outbox.Worker {
	senders := map[outbox.Kind]outbox.Sender{
		outbox.KindWebhook: &webhooks.Sender{Pool: pool, Box: o.Box, Guard: o.Guard, Now: o.Outbox.Now},
		outbox.KindEmail:   &invitemail.Sender{Pool: pool, Mail: o.Mail, ConsoleURL: o.ConsoleURL},
	}
	return outbox.NewWorker(pool, senders, o.Outbox)
}
