// Package invitemail turns an invite into an email and delivers it from the outbox.
//
// THE TOKEN. The invite's one-time token lives in exactly two places: the link in
// the email, and the outbox row's payload WHILE the email is still to be sent. It
// is removed from the payload as soon as the row is delivered or dead (Finish, in
// the same transaction that records the result), and it is never logged. The
// invites table keeps only the token's hash, as before. A resend issues a NEW token
// (the old link stops working), because the old one cannot be recovered.
//
// HOSTILE TEXT. The workspace name and the inviter's name are user-supplied.
// They reach the HTML body only through html.EscapeString, the text body as text,
// and the subject through mail.CleanHeader and RFC 2047 encoding, so a newline in a
// name cannot add a header and markup in a name cannot become markup.
package invitemail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/platform/mail"
)

// EventType is the outbox event type of an invite email.
const EventType = "invite.email"

// Payload is what the outbox row carries while the email is pending.
type Payload struct {
	To            string `json:"to"`
	Token         string `json:"token,omitempty"` // removed after delivery (or death)
	WorkspaceName string `json:"workspaceName"`
	InviterName   string `json:"inviterName"`
	Role          string `json:"role"`
	ExpiresAt     string `json:"expiresAt"`
}

// Compose builds the message. consoleURL is the console's origin; the link is
// <consoleURL>/invite/<token>.
func Compose(p Payload, consoleURL string) (mail.Message, error) {
	base, err := url.Parse(strings.TrimRight(consoleURL, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return mail.Message{}, errors.New("PUBLIC_CONSOLE_URL is not set to the console's address")
	}
	link := base.String() + "/invite/" + url.PathEscape(p.Token)
	ws := mail.CleanHeader(p.WorkspaceName, 80)
	who := mail.CleanHeader(p.InviterName, 80)
	if ws == "" {
		ws = "a workspace"
	}
	if who == "" {
		who = "A teammate"
	}
	role := mail.CleanHeader(p.Role, 20)
	expires := ""
	if t, err := time.Parse(time.RFC3339, p.ExpiresAt); err == nil {
		expires = t.UTC().Format("2 January 2006")
	}
	subject := "You're invited to join " + ws + " on Helios"

	text := who + " invited you to join the workspace \"" + ws + "\" on Helios as " + article(role) + " " + role + ".\n\n" +
		"Accept the invitation:\n" + link + "\n\n"
	if expires != "" {
		text += "The invitation expires on " + expires + ".\n\n"
	}
	text += "Sign in with this email address (" + p.To + ") to accept. If you were not expecting this, you can ignore this message; nothing happens unless you accept.\n"

	e := html.EscapeString
	htmlBody := "<!doctype html><html><body style=\"font-family:system-ui,sans-serif;line-height:1.5;color:#111\">" +
		"<p><strong>" + e(who) + "</strong> invited you to join the workspace <strong>" + e(ws) + "</strong> on Helios as " + e(article(role)) + " " + e(role) + ".</p>" +
		"<p><a href=\"" + e(link) + "\" style=\"display:inline-block;padding:10px 16px;background:#4f46e5;color:#fff;border-radius:6px;text-decoration:none\">Accept the invitation</a></p>" +
		"<p>Or paste this link into your browser:<br><span style=\"word-break:break-all\">" + e(link) + "</span></p>"
	if expires != "" {
		htmlBody += "<p>The invitation expires on " + e(expires) + ".</p>"
	}
	htmlBody += "<p style=\"color:#555\">Sign in with this email address (" + e(p.To) + ") to accept. If you were not expecting this, you can ignore this message; nothing happens unless you accept.</p></body></html>"
	return mail.Message{To: p.To, Subject: subject, Text: text, HTML: htmlBody}, nil
}

func article(role string) string {
	if role != "" && strings.ContainsRune("aeiou", rune(role[0])) {
		return "an"
	}
	return "a"
}

// HashToken is the invite-token hash the invites table stores (SHA-256, hex).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Sender delivers invite email rows. It implements outbox.Sender and outbox.Finisher.
type Sender struct {
	Pool       *pgxpool.Pool
	Mail       mail.Sender
	ConsoleURL string
}

func (s *Sender) Send(ctx context.Context, d outbox.Delivery) outbox.Result {
	var p Payload
	if err := json.Unmarshal(d.Payload, &p); err != nil || p.Token == "" {
		return outbox.Result{Err: "the email has no token any more", Permanent: true}
	}
	// Only send for an invite that is still open AND whose current token is this one: a revoked,
	// accepted or expired invite is not emailed, and a resend supersedes the older email.
	var open, current bool
	err := s.Pool.QueryRow(ctx, `
		SELECT accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now(), token_hash = $3
		FROM workspace_invites WHERE id = $1::uuid AND workspace_id = $2::uuid`, d.InviteID, d.WorkspaceID, HashToken(p.Token)).Scan(&open, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return outbox.Result{Err: "the invite no longer exists", Permanent: true}
	}
	if err != nil {
		return outbox.Result{Err: "could not read the invite", Internal: true}
	}
	if !open {
		return outbox.Result{Err: "not sent: the invite is no longer open", Permanent: true}
	}
	if !current {
		return outbox.Result{Err: "not sent: a newer email replaced this one", Permanent: true}
	}
	msg, err := Compose(p, s.ConsoleURL)
	if err != nil {
		return outbox.Result{Err: err.Error(), Internal: true}
	}
	start := time.Now()
	if err := s.Mail.Send(ctx, msg); err != nil {
		res := outbox.Result{Err: err.Error(), Duration: time.Since(start), Permanent: errors.Is(err, mail.ErrInvalid)}
		return res
	}
	return outbox.Result{Delivered: true, Duration: time.Since(start)}
}

// Finish removes the token from the payload once the row is delivered or dead. It
// runs in the transaction that records the result, so there is no moment when the
// row is final and still holds the token.
func (s *Sender) Finish(ctx context.Context, tx pgx.Tx, d outbox.Delivery, o outbox.Outcome) error {
	if o.Status != outbox.StatusDelivered && o.Status != outbox.StatusDead {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE outbox SET payload = payload - 'token' WHERE id = $1::uuid AND workspace_id = $2::uuid`, d.ID, d.WorkspaceID)
	return err
}
