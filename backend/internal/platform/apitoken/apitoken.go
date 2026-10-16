// Package apitoken authenticates API tokens: scoped bearer tokens for CI pipelines
// and scripts. It is a second authentication path next to the user JWT, chosen
// by the token's shape alone: a user JWT starts with "eyJ", a token with "hlt_".
//
// A token looks like
//
//	hlt_1a2b3c4d_<43 base64url characters>
//
// "hlt_" and 8 hex characters are the display prefix (shown in lists and audit
// entries, not secret). The 43 characters are 32 random bytes: 256 bits.
// The regular expression for secret scanners is in Pattern.
//
// HASHING. Only SHA-256 of the whole token is stored, found by a unique index.
// A password hash such as Argon2id exists to make guessing a LOW-entropy secret
// slow; this secret has 256 bits, so guessing is hopeless however fast the hash
// is, and a slow hash would only add cost. It would add a real risk too: on
// the unauthenticated path every garbage token would force a memory-hard hash,
// a cheap way to burn the server's CPU and memory. (The SDK keys use Argon2id and
// hide its cost behind a cache; they predate this package and their hot path,
// /evaluate, is already cached. For tokens the cheap hash makes an exact-match
// indexed lookup possible, with no prefix to scan.) The stored hash is still
// compared in constant time after the lookup.
//
// THE CALLER, NOT THE TOKEN, IS THE AUTHORITY. A token carries only a ceiling.
// What it may do at a given moment is the lower of that ceiling and its
// creator's CURRENT role, read from the membership table on every request by the
// workspace guard; see package rbac.
package apitoken

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
)

// Prefix starts every token. A user JWT never does.
const Prefix = "hlt_"

// Pattern matches a whole token. For secret scanners (GitHub custom patterns,
// gitleaks, trufflehog), the unanchored form is `\bhlt_[0-9a-f]{8}_[A-Za-z0-9_-]{43}\b`.
var Pattern = regexp.MustCompile(`^hlt_[0-9a-f]{8}_[A-Za-z0-9_-]{43}$`)

// Generate returns a new token (show it once), its display prefix, and the
// SHA-256 hash to store.
func Generate() (plaintext, displayPrefix string, hash []byte, err error) {
	id := make([]byte, 4)
	secret := make([]byte, 32)
	if _, err = rand.Read(id); err != nil {
		return "", "", nil, err
	}
	if _, err = rand.Read(secret); err != nil {
		return "", "", nil, err
	}
	displayPrefix = Prefix + hex.EncodeToString(id)
	plaintext = displayPrefix + "_" + base64.RawURLEncoding.EncodeToString(secret)
	return plaintext, displayPrefix, Hash(plaintext), nil
}

// Hash is the value stored and looked up.
func Hash(plaintext string) []byte {
	sum := sha256.Sum256([]byte(plaintext))
	return sum[:]
}

// WellFormed reports whether s has the shape of a token. It does no database
// work and no hashing, so it is the first thing a garbage token meets.
func WellFormed(s string) bool { return len(s) == 56 && Pattern.MatchString(s) }

// HasPrefix reports whether a bearer credential is meant as an API token.
func HasPrefix(bearer string) bool { return strings.HasPrefix(bearer, Prefix) }

// Equal compares two hashes in constant time.
func Equal(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }

// Display makes a token safe to mention: the display prefix only.
func Display(plaintext string) string {
	if len(plaintext) >= 12 {
		return plaintext[:12]
	}
	return Prefix + "…"
}
