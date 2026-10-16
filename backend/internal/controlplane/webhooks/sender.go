package webhooks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/platform/netguard"
	"helios/backend/internal/platform/secretbox"
)

// Health thresholds: an endpoint that has failed this many deliveries in a row, or
// has been failing continuously for this long, is disabled and the workspace told.
const (
	AutoDisableAfterFailures = 50
	AutoDisableAfter         = 3 * 24 * time.Hour
	// RotationGrace is how long the previous secret keeps signing (both signatures are sent).
	RotationGrace = 24 * time.Hour
)

// Sender delivers webhook outbox rows. It implements outbox.Sender and
// outbox.Finisher.
type Sender struct {
	Pool  *pgxpool.Pool
	Box   *secretbox.Box
	Guard *netguard.Guard
	Now   func() time.Time
}

func (s *Sender) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Send does one HTTP attempt. It reads the endpoint fresh (a URL or secret may have
// changed since the event was queued), re-validates the destination, signs, posts
// with the guarded client, and reports a Result. The error text it produces is a
// fixed vocabulary: Go's own errors embed the full URL, which may hold a token in
// its path, so they are never stored.
func (s *Sender) Send(ctx context.Context, d outbox.Delivery) outbox.Result {
	var url string
	var enabled bool
	var secretEnc, prevEnc []byte
	var prevExp *time.Time
	err := s.Pool.QueryRow(ctx, `
		SELECT url, enabled, secret_enc, previous_secret_enc, previous_secret_expires_at
		FROM webhook_endpoints WHERE id = $1::uuid AND workspace_id = $2::uuid`, d.EndpointID, d.WorkspaceID).
		Scan(&url, &enabled, &secretEnc, &prevEnc, &prevExp)
	if errors.Is(err, pgx.ErrNoRows) {
		return outbox.Result{Err: "the endpoint no longer exists", Permanent: true}
	}
	if err != nil {
		return outbox.Result{Err: "could not read the endpoint", Internal: true}
	}
	if !enabled && d.EventType != EventPing {
		return outbox.Result{Err: "the endpoint is disabled", Permanent: true}
	}
	secrets, err := s.secrets(d.EndpointID, secretEnc, prevEnc, prevExp)
	if err != nil {
		return outbox.Result{Err: "the signing key is not available", Internal: true}
	}
	if _, err := s.Guard.Check(ctx, url); err != nil {
		return outbox.Result{Err: blockedText(err)}
	}

	ts := s.now().Unix()
	body := []byte(d.Payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return outbox.Result{Err: "the URL is not valid", Permanent: true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Helios-Webhooks/1")
	req.Header.Set(HeaderTimestamp, itoa(ts))
	req.Header.Set(HeaderDeliveryID, d.DeliveryID)
	req.Header.Set(HeaderEventID, d.EventID)
	req.Header.Set(HeaderEventType, d.EventType)
	req.Header.Set(HeaderSignature, SignatureHeader(secrets, ts, d.DeliveryID, body))

	start := time.Now()
	res, err := s.Guard.Client().Do(req)
	took := time.Since(start)
	if err != nil {
		return outbox.Result{Err: transportText(err), Duration: took}
	}
	defer res.Body.Close()
	hint, _ := io.ReadAll(io.LimitReader(res.Body, netguard.MaxResponseCap))
	out := outbox.Result{HTTPStatus: res.StatusCode, Duration: took, Hint: string(hint[:min(len(hint), 400)])}
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		out.Delivered = true
		out.Hint = "" // a successful receiver's body is of no interest
	case res.StatusCode >= 300 && res.StatusCode < 400:
		out.Err = "the receiver answered with a redirect, which is not followed"
	default:
		out.Err = "the receiver answered " + itoa(int64(res.StatusCode))
	}
	return out
}

func (s *Sender) secrets(endpointID string, cur, prev []byte, prevExp *time.Time) ([]string, error) {
	aad := []byte(endpointID)
	c, err := s.Box.Open(cur, aad)
	if err != nil {
		return nil, err
	}
	out := []string{string(c)}
	if len(prev) > 0 && prevExp != nil && s.now().Before(*prevExp) {
		if p, err := s.Box.Open(prev, aad); err == nil {
			out = append(out, string(p))
		}
	}
	return out, nil
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = digits[n%10]
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func blockedText(err error) string {
	if errors.Is(err, netguard.ErrBlocked) {
		// the message after "destination not allowed: " names the rule, never the URL
		return "destination blocked: " + strings.TrimPrefix(err.Error(), netguard.ErrBlocked.Error()+": ")
	}
	return "the host could not be resolved"
}

// transportText maps a transport error to a fixed phrase.
func transportText(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, netguard.ErrBlocked):
		return blockedText(err)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timed out"
	case strings.Contains(err.Error(), "x509") || strings.Contains(err.Error(), "tls:"):
		return "the TLS handshake failed or the certificate is not valid"
	case strings.Contains(err.Error(), "refused"):
		return "connection refused"
	case strings.Contains(err.Error(), "no such host"), strings.Contains(err.Error(), "could not be resolved"):
		return "the host could not be resolved"
	}
	return "could not connect"
}

// Finish runs in the transaction that records the attempt: it keeps the
// endpoint's health and, when it has been failing too long, disables it.
func (s *Sender) Finish(ctx context.Context, tx pgx.Tx, d outbox.Delivery, o outbox.Outcome) error {
	if d.EventType == EventPing || o.Internal {
		return nil // a test must not make a healthy endpoint look sick, nor our own fault count against it
	}
	if o.Status == outbox.StatusDelivered {
		_, err := tx.Exec(ctx, `
			UPDATE webhook_endpoints SET consecutive_failures = 0, failing_since = NULL, last_success_at = $3, updated_at = $3
			WHERE id = $1::uuid AND workspace_id = $2::uuid`, d.EndpointID, d.WorkspaceID, o.Now)
		return err
	}
	if o.Permanent {
		return nil // disabled or deleted: no new information about the receiver
	}
	var enabled bool
	var failures int
	var since *time.Time
	err := tx.QueryRow(ctx, `
		UPDATE webhook_endpoints
		SET consecutive_failures = consecutive_failures + 1, failing_since = COALESCE(failing_since, $3), last_failure_at = $3, updated_at = $3
		WHERE id = $1::uuid AND workspace_id = $2::uuid
		RETURNING enabled, consecutive_failures, failing_since`, d.EndpointID, d.WorkspaceID, o.Now).Scan(&enabled, &failures, &since)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	long := since != nil && o.Now.Sub(*since) >= AutoDisableAfter
	if failures < AutoDisableAfterFailures && !long {
		return nil
	}
	reason := "disabled automatically after " + itoa(int64(failures)) + " failed deliveries in a row"
	if long && failures < AutoDisableAfterFailures {
		reason = "disabled automatically: every delivery has failed for more than 3 days"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE webhook_endpoints SET enabled = false, disabled_reason = $3, disabled_at = $4, updated_at = $4
		WHERE id = $1::uuid AND workspace_id = $2::uuid`, d.EndpointID, d.WorkspaceID, reason, o.Now); err != nil {
		return err
	}
	return audit.Write(ctx, tx, audit.Entry{
		WorkspaceID: d.WorkspaceID, ActorEmail: "system", Action: "webhook_endpoint.auto_disable", ResourceType: "webhook_endpoint",
		ResourceID: d.EndpointID, Severity: audit.SeverityCritical, After: map[string]any{"reason": reason, "consecutiveFailures": failures},
	})
}
