package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"
)

// SMTPConfig configures the SMTP sender.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string // a SECRET: never logged
	From     string
	// TLS: "starttls" (default; the server must offer it, there is no downgrade),
	// "implicit" (TLS from the first byte, usually port 465), or "none".
	TLS string
	// AllowInsecure must be true for TLS "none": a development switch for a local
	// catcher such as MailHog. Without it, New refuses.
	AllowInsecure bool
	// TLSConfig is for tests (a private CA); nil uses the system roots.
	TLSConfig *tls.Config
	// Timeout bounds one send (dial, handshake and conversation); default 30s.
	Timeout time.Duration
}

// SMTP sends through an SMTP server over TLS.
type SMTP struct{ cfg SMTPConfig }

// NewSMTP validates the configuration. Plain-text SMTP is refused unless AllowInsecure is set.
func NewSMTP(c SMTPConfig) (*SMTP, error) {
	if c.TLS == "" {
		c.TLS = "starttls"
	}
	switch c.TLS {
	case "starttls", "implicit":
	case "none":
		if !c.AllowInsecure {
			return nil, errors.New("SMTP_TLS=none sends mail (and the password) unencrypted; set MAIL_ALLOW_INSECURE_SMTP=true only for a local development catcher")
		}
	default:
		return nil, fmt.Errorf("SMTP_TLS must be starttls, implicit or none, got %q", c.TLS)
	}
	if c.Host == "" || c.Port < 1 || c.Port > 65535 {
		return nil, errors.New("SMTP_HOST and a valid SMTP_PORT are required")
	}
	if _, err := ParseAddress(c.From); err != nil {
		return nil, errors.New("SMTP_FROM must be a bare email address")
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	return &SMTP{cfg: c}, nil
}

func (s *SMTP) Mode() string { return "smtp" }

// Send delivers one message. The error text never includes the message or the password.
func (s *SMTP) Send(ctx context.Context, msg Message) error {
	raw, err := Build(s.cfg.From, msg, time.Now(), s.cfg.Host)
	if err != nil {
		return err
	}
	to := msg.To
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	deadline := time.Now().Add(s.cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	tlsCfg := s.cfg.TLSConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{}
	}
	tlsCfg = tlsCfg.Clone()
	if tlsCfg.ServerName == "" {
		tlsCfg.ServerName = s.cfg.Host
	}
	tlsCfg.MinVersion = tls.VersionTLS12

	var conn net.Conn
	if s.cfg.TLS == "implicit" {
		conn, err = (&tls.Dialer{NetDialer: &dialer, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return errors.New("mail: could not connect to the SMTP server")
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return errors.New("mail: the SMTP server did not greet us")
	}
	defer c.Close()
	if err := c.Hello("helios"); err != nil {
		return errors.New("mail: the SMTP server refused the greeting")
	}
	if s.cfg.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			// No downgrade: a server that does not offer TLS gets nothing, not even a MAIL command.
			return errors.New("mail: the SMTP server does not offer STARTTLS, so nothing was sent")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return errors.New("mail: the TLS handshake with the SMTP server failed")
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return errors.New("mail: the SMTP server rejected the credentials")
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return errors.New("mail: the SMTP server rejected the sender")
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("%w: the SMTP server rejected the recipient", ErrInvalid)
	}
	w, err := c.Data()
	if err != nil {
		return errors.New("mail: the SMTP server refused the message")
	}
	if _, err := w.Write(raw); err != nil {
		return errors.New("mail: could not send the message")
	}
	if err := w.Close(); err != nil {
		return errors.New("mail: the SMTP server did not accept the message")
	}
	_ = c.Quit()
	return nil
}
