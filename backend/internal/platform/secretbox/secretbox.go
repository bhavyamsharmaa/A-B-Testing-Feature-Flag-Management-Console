// Package secretbox encrypts small secrets (webhook signing secrets) at rest.
//
// WHY ENCRYPTION AND NOT A HASH. A webhook secret must be recoverable: the
// worker signs every delivery with it, so unlike an API token or an invite
// token it cannot be stored as a hash. It is therefore stored encrypted with
// AES-256-GCM under a key that lives only in the environment (WEBHOOK_SECRET_KEY),
// never in the database: someone who can read the database (a backup, a SQL
// injection, a curious operator with read access) gets ciphertext, and the key is
// a separate thing to steal.
//
//   - Each value gets a fresh random 96-bit nonce. The stored form is
//     keyID(4 bytes) || nonce(12) || ciphertext||tag.
//   - The row's identity (additional authenticated data) is bound in, so
//     ciphertext copied from one endpoint's row to another's fails to open.
//   - FAIL CLOSED: with no key configured, Seal and Open return ErrNoKey; callers
//     refuse to create endpoints or to deliver. There is no plaintext fallback.
//   - Rotation: the box holds one current key (used to seal) and any number of
//     previous keys (used only to open). Rekey re-seals under the current key.
//     The key id is the first 4 bytes of SHA-256(key), which reveals nothing useful.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNoKey: no encryption key is configured, so secrets can be neither stored nor read.
	ErrNoKey = errors.New("secretbox: no encryption key configured")
	// ErrUnknownKey: the value was sealed under a key this box no longer has.
	ErrUnknownKey = errors.New("secretbox: sealed under an unknown key")
	// ErrCorrupt: the value is malformed, tampered with, or bound to a different row.
	ErrCorrupt = errors.New("secretbox: cannot open")
)

type key struct {
	id   [4]byte
	aead cipher.AEAD
}

// Box seals with its current key and opens with any it holds.
type Box struct {
	current  *key
	previous []*key
}

// New parses base64 keys (32 bytes each). current may be empty (a box that
// refuses everything); previous keys are only used to open.
func New(current string, previous ...string) (*Box, error) {
	b := &Box{}
	if strings.TrimSpace(current) != "" {
		k, err := parseKey(current)
		if err != nil {
			return nil, fmt.Errorf("current key: %w", err)
		}
		b.current = k
	}
	for i, p := range previous {
		if strings.TrimSpace(p) == "" {
			continue
		}
		k, err := parseKey(p)
		if err != nil {
			return nil, fmt.Errorf("previous key %d: %w", i+1, err)
		}
		b.previous = append(b.previous, k)
	}
	if b.current == nil && len(b.previous) > 0 {
		return nil, errors.New("previous keys are set but no current key")
	}
	return b, nil
}

func parseKey(s string) (*key, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(s))
	}
	if err != nil || len(raw) != 32 {
		return nil, errors.New("must be 32 random bytes, base64 encoded (generate with: openssl rand -base64 32)")
	}
	blk, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	k := &key{aead: g}
	copy(k.id[:], sum[:4])
	return k, nil
}

// Available reports whether secrets can be sealed.
func (b *Box) Available() bool { return b != nil && b.current != nil }

// Seal encrypts plaintext, binding it to aad (the owning row's id).
func (b *Box) Seal(plaintext, aad []byte) ([]byte, error) {
	if !b.Available() {
		return nil, ErrNoKey
	}
	nonce := make([]byte, b.current.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{}, b.current.id[:]...)
	out = append(out, nonce...)
	return b.current.aead.Seal(out, nonce, plaintext, aad), nil
}

// Open decrypts a sealed value for the row aad names.
func (b *Box) Open(sealed, aad []byte) ([]byte, error) {
	if !b.Available() {
		return nil, ErrNoKey
	}
	if len(sealed) < 4+12+16 {
		return nil, ErrCorrupt
	}
	var id [4]byte
	copy(id[:], sealed[:4])
	var k *key
	for _, c := range append([]*key{b.current}, b.previous...) {
		if c.id == id {
			k = c
			break
		}
	}
	if k == nil {
		return nil, ErrUnknownKey
	}
	n := k.aead.NonceSize()
	pt, err := k.aead.Open(nil, sealed[4:4+n], sealed[4+n:], aad)
	if err != nil {
		return nil, ErrCorrupt
	}
	return pt, nil
}

// CurrentID is the id of the sealing key, for "is this row on the current key?".
func (b *Box) CurrentID() []byte {
	if !b.Available() {
		return nil
	}
	return append([]byte{}, b.current.id[:]...)
}

// SealedUnderCurrent reports whether sealed was produced by the current key.
func (b *Box) SealedUnderCurrent(sealed []byte) bool {
	return b.Available() && len(sealed) >= 4 && string(sealed[:4]) == string(b.current.id[:])
}
