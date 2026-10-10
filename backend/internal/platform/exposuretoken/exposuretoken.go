// Package exposuretoken signs and verifies exposure tokens.
//
// Problem: an $exposure event says "this subject was shown this variation of
// this experiment", and the client reports it, so a buggy or hostile client
// could report a variation it was never served. /evaluate therefore returns a
// token proving the server served it, and /track checks the token.
//
// The token is stateless: nothing is stored when it is issued. It is
//
//	v1.<key id>.<issued-at, unix seconds>.<base64url HMAC-SHA256>
//
// where the MAC covers the workspace, environment, experiment, variation, the
// SHA-256 of the subject key (never the subject key itself) and the issued-at
// time. A token is therefore only good for the exact subject, variation and
// experiment it was issued for, in the tenant that issued it.
//
// The secret is only ever held in memory. It is never logged, returned or
// written; errors say what kind of failure it was and never quote the input.
package exposuretoken

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Mode says what /track does with an $exposure event's token.
type Mode string

const (
	// ModeOff ignores tokens: today's behaviour, so old clients keep working.
	ModeOff Mode = "off"
	// ModeReport accepts every exposure but records whether its token verified
	// (events.exposure_verified), so the results can warn about unverified ones.
	ModeReport Mode = "report"
	// ModeRequired rejects an exposure whose token is missing or does not verify.
	ModeRequired Mode = "required"
)

// ParseMode reads EXPOSURE_VERIFICATION. Empty means off.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case "", ModeOff:
		return ModeOff, nil
	case ModeReport, ModeRequired:
		return m, nil
	default:
		return ModeOff, fmt.Errorf("EXPOSURE_VERIFICATION must be off, report or required")
	}
}

// DefaultMaxAge is how long a token stays valid after it was issued.
const DefaultMaxAge = 24 * time.Hour

// maxFuture tolerates clock differences between instances.
const maxFuture = 5 * time.Minute

// MinSecretBytes is the shortest accepted signing secret.
const MinSecretBytes = 32

// Verification failures. They never carry the token or any field of it.
var (
	ErrMalformed = errors.New("exposure token is malformed")
	ErrInvalid   = errors.New("exposure token is not valid for this exposure")
	ErrExpired   = errors.New("exposure token has expired")
)

// Binding is what a token vouches for.
type Binding struct {
	Workspace, Environment, Experiment, Variation, Subject string
}

type key struct {
	id     string
	secret []byte
}

var kidRE = regexp.MustCompile(`^[A-Za-z0-9]{1,16}$`)

// Keyring signs with the current key and verifies with the current key or the
// previous one (selected by the key id inside the token), so a key can be
// rotated without invalidating tokens that are still in flight.
type Keyring struct {
	current  key
	previous *key
	maxAge   time.Duration
	// Dev is true for a key generated at startup.
	Dev bool
}

func parseKey(spec string) (key, error) {
	id, secret, ok := strings.Cut(strings.TrimSpace(spec), ":")
	if !ok || !kidRE.MatchString(id) {
		return key{}, errors.New("a signing key must look like <id>:<secret>, with an id of 1-16 letters or digits")
	}
	if len(secret) < MinSecretBytes {
		return key{}, fmt.Errorf("the signing secret must be at least %d bytes", MinSecretBytes)
	}
	return key{id: id, secret: []byte(secret)}, nil
}

// NewKeyring builds a keyring from "<id>:<secret>" specs (previous may be
// empty). Errors never quote the secret.
func NewKeyring(current, previous string, maxAge time.Duration) (*Keyring, error) {
	cur, err := parseKey(current)
	if err != nil {
		return nil, fmt.Errorf("EXPOSURE_TOKEN_KEY: %w", err)
	}
	k := &Keyring{current: cur, maxAge: maxAge}
	if strings.TrimSpace(previous) != "" {
		prev, err := parseKey(previous)
		if err != nil {
			return nil, fmt.Errorf("EXPOSURE_TOKEN_KEY_PREVIOUS: %w", err)
		}
		if prev.id == cur.id {
			return nil, errors.New("EXPOSURE_TOKEN_KEY_PREVIOUS must have a different id from EXPOSURE_TOKEN_KEY")
		}
		k.previous = &prev
	}
	if k.maxAge <= 0 {
		k.maxAge = DefaultMaxAge
	}
	return k, nil
}

// DevKeyring generates a random key that exists only in this process. Tokens
// signed with it are not valid after a restart or on another instance.
func DevKeyring(maxAge time.Duration) (*Keyring, error) {
	secret := make([]byte, MinSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	log.Printf("exposure tokens: EXPOSURE_TOKEN_KEY is not set, so a random DEVELOPMENT key (id %q) was generated for this process only; tokens will not survive a restart or work across instances", "dev")
	return &Keyring{current: key{id: "dev", secret: secret}, maxAge: maxAge, Dev: true}, nil
}

// MaxAge is how long a token is valid.
func (k *Keyring) MaxAge() time.Duration { return k.maxAge }

func (k *Keyring) find(id string) *key {
	switch {
	case id == k.current.id:
		return &k.current
	case k.previous != nil && id == k.previous.id:
		return k.previous
	}
	return nil
}

// mac computes the HMAC over the binding. Every field is length-prefixed so no
// two different bindings can produce the same input.
func mac(secret []byte, b Binding, issuedAt int64) []byte {
	h := hmac.New(sha256.New, secret)
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	field("helios-exposure-v1")
	field(b.Workspace)
	field(b.Environment)
	field(b.Experiment)
	field(b.Variation)
	subject := sha256.Sum256([]byte(b.Subject)) // the subject key itself never enters the MAC
	field(hex.EncodeToString(subject[:]))
	field(strconv.FormatInt(issuedAt, 10))
	return h.Sum(nil)
}

// Issue returns a token for the binding, signed with the current key.
func (k *Keyring) Issue(b Binding, now time.Time) string {
	iat := now.Unix()
	return "v1." + k.current.id + "." + strconv.FormatInt(iat, 10) + "." + base64.RawURLEncoding.EncodeToString(mac(k.current.secret, b, iat))
}

// Verify checks a token against the binding of the exposure being reported.
// The signature is checked first (in constant time), so the age of a token is
// only revealed to someone holding a genuine one.
func (k *Keyring) Verify(token string, b Binding, now time.Time) error {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return ErrMalformed
	}
	iat, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || iat <= 0 {
		return ErrMalformed
	}
	got, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(got) != sha256.Size {
		return ErrMalformed
	}
	use := k.find(parts[1])
	if use == nil {
		return ErrInvalid // an unknown key id is indistinguishable from a bad signature
	}
	if !hmac.Equal(got, mac(use.secret, b, iat)) { // constant time
		return ErrInvalid
	}
	issued := time.Unix(iat, 0)
	if issued.After(now.Add(maxFuture)) {
		return ErrInvalid
	}
	if now.Sub(issued) > k.maxAge {
		return ErrExpired
	}
	return nil
}
