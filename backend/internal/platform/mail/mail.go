// Package mail sends email through one of two implementations, chosen by
// configuration: SMTP (TLS required) and a development sink that never sends
// anything. The default is the sink, so no environment sends mail by accident.
//
// Everything that reaches a header is validated or encoded here, once: callers
// pass plain strings (a workspace name, a person's name, in the subject) and
// cannot inject a header, because control characters are removed before
// encoding and non-ASCII text is RFC 2047 encoded. Bodies are quoted-printable,
// so a body can neither contain a header nor a MIME boundary line by accident.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
	"unicode"
)

// Message is one email to one recipient.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers a message. Implementations must not log the message body.
type Sender interface {
	Send(ctx context.Context, msg Message) error
	// Mode names the implementation: "sink" or "smtp".
	Mode() string
}

// ErrInvalid: the message cannot be sent as given (a bad address, say). Retrying will not help.
var ErrInvalid = errors.New("mail: invalid message")

// CleanHeader makes a string safe for a header value: control characters
// (including CR and LF) become spaces, runs of whitespace collapse, the result
// is trimmed and capped at max runes.
func CleanHeader(s string, max int) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == ' ' || r == ' ' || r == '\u0085' {
			space = true
			continue
		}
		if !unicode.IsPrint(r) {
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
			n++
		}
		space = false
		b.WriteRune(r)
		n++
		if n >= max {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// ParseAddress validates a bare address (no display name, no comments) and
// rejects anything with a control character.
func ParseAddress(s string) (string, error) {
	if strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || r == '<' || r == '>' || r == ',' || r == ';' || r == '"' || r == ' '
	}) {
		return "", fmt.Errorf("%w: bad address", ErrInvalid)
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" || len(s) > 254 || !strings.Contains(s[strings.LastIndex(s, "@")+1:], ".") {
		return "", fmt.Errorf("%w: bad address", ErrInvalid)
	}
	return s, nil
}

// Build renders the message as RFC 5322 bytes (CRLF line endings).
func Build(from string, msg Message, now time.Time, host string) ([]byte, error) {
	fromAddr, err := ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("%w: bad From address", ErrInvalid)
	}
	to, err := ParseAddress(msg.To)
	if err != nil {
		return nil, err
	}
	subject := CleanHeader(msg.Subject, 200)
	if subject == "" {
		return nil, fmt.Errorf("%w: empty subject", ErrInvalid)
	}
	boundary, id := token(), token()
	var b bytes.Buffer
	w := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	w("From", fromAddr)
	w("To", to)
	w("Subject", mime.QEncoding.Encode("utf-8", subject))
	w("Date", now.UTC().Format(time.RFC1123Z))
	w("Message-ID", "<"+id+"@"+CleanHeader(host, 100)+">")
	w("MIME-Version", "1.0")
	w("Auto-Submitted", "auto-generated")
	w("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	part := func(ctype, body string) {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + ctype + "; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		qp := quotedprintable.NewWriter(&b)
		_, _ = qp.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")))
		_ = qp.Close()
		b.WriteString("\r\n")
	}
	part("text/plain", msg.Text)
	if msg.HTML != "" {
		part("text/html", msg.HTML)
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

func token() string {
	r := make([]byte, 12)
	_, _ = rand.Read(r)
	return hex.EncodeToString(r)
}

// Redact shows enough of an address to recognise it in a log and no more.
func Redact(addr string) string {
	local, domain, ok := strings.Cut(addr, "@")
	if !ok || local == "" {
		return "***"
	}
	return string([]rune(local)[0]) + "***@" + domain
}
