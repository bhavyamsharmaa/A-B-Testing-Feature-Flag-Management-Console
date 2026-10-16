package webhooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Headers sent with every delivery.
const (
	HeaderSignature  = "Helios-Signature"   // "v1=<hex>" (two values, comma separated, during a secret rotation)
	HeaderTimestamp  = "Helios-Timestamp"   // unix seconds when this attempt was signed
	HeaderDeliveryID = "Helios-Delivery-Id" // stable across every attempt of one delivery: dedupe on it
	HeaderEventID    = "Helios-Event-Id"
	HeaderEventType  = "Helios-Event-Type"
)

// SecretPrefix starts every signing secret.
const SecretPrefix = "whsec_"

// Tolerance is how far a timestamp may be from the receiver's clock. Documented
// as five minutes; receivers choose their own.
const Tolerance = 5 * time.Minute

// NewSecret returns a fresh signing secret: whsec_ plus 256 random bits.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return SecretPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// SecretHint is the last four characters, enough to tell secrets apart.
func SecretHint(secret string) string {
	if len(secret) < 4 {
		return ""
	}
	return "…" + secret[len(secret)-4:]
}

// Sign returns the hex HMAC-SHA256 of "timestamp.deliveryId.body". Binding the
// delivery id and the timestamp into the signed string means neither can be
// replaced on a captured request without invalidating the signature.
func Sign(secret string, timestamp int64, deliveryID string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write([]byte(deliveryID))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// SignatureHeader builds the header value for one or two secrets (the current
// one, and the previous one during a rotation's grace period).
func SignatureHeader(secrets []string, timestamp int64, deliveryID string, body []byte) string {
	parts := make([]string, 0, len(secrets))
	for _, s := range secrets {
		parts = append(parts, "v1="+Sign(s, timestamp, deliveryID, body))
	}
	return strings.Join(parts, ",")
}

// Errors from Verify.
var (
	ErrBadTimestamp = errors.New("webhooks: timestamp missing or outside the tolerance")
	ErrBadSignature = errors.New("webhooks: no signature matches")
)

// Verify is what a receiver does, and the reference for docs/WEBHOOKS.md: reject
// a timestamp outside the tolerance, recompute the HMAC over
// "timestamp.deliveryId.body" with its secret, and accept if ANY v1 value in the
// header matches (compared in constant time).
func Verify(secret, header, timestamp, deliveryID string, body []byte, now time.Time, tolerance time.Duration) error {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrBadTimestamp
	}
	age := now.Sub(time.Unix(ts, 0))
	if age > tolerance || age < -tolerance {
		return ErrBadTimestamp
	}
	want := Sign(secret, ts, deliveryID, body)
	ok := false
	for _, part := range strings.Split(header, ",") {
		got, found := strings.CutPrefix(strings.TrimSpace(part), "v1=")
		if found && hmac.Equal([]byte(got), []byte(want)) {
			ok = true // keep going: no early exit on the first match
		}
	}
	if !ok {
		return ErrBadSignature
	}
	return nil
}
