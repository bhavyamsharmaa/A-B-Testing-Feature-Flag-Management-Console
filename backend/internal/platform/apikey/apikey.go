// Package apikey mints, hashes, and verifies Helios API keys.
//
// A key looks like "hsdk_1a2b3c4d_<43 base64url chars>". The
// "hsdk_1a2b3c4d" part is stored in plaintext as key_prefix so a presented
// key can be found with an indexed lookup; the full key is stored only as an
// Argon2id hash (PRD §5).
package apikey

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

type Kind string

const (
	KindSDK    Kind = "sdk"
	KindServer Kind = "server"
)

var kindTag = map[Kind]string{KindSDK: "hsdk", KindServer: "hsrv"}

// OWASP's minimum Argon2id profile. Deliberately modest: verification is
// cached by the middleware, so this cost is paid once per key per TTL rather
// than on every /evaluate call.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024 // KiB
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// Generate returns a new plaintext key (show it to the user exactly once),
// its display prefix, and the Argon2id hash to store.
func Generate(kind Kind) (plaintext, prefix, hash string, err error) {
	tag, ok := kindTag[kind]
	if !ok {
		return "", "", "", fmt.Errorf("apikey: unknown kind %q", kind)
	}
	id := make([]byte, 4)
	secret := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return "", "", "", err
	}
	if _, err := rand.Read(secret); err != nil {
		return "", "", "", err
	}
	prefix = tag + "_" + hex.EncodeToString(id)
	plaintext = prefix + "_" + base64.RawURLEncoding.EncodeToString(secret)
	hash, err = Hash(plaintext)
	return plaintext, prefix, hash, err
}

// Prefix extracts the display prefix from a presented key, reporting false
// if the key isn't shaped like one of ours.
func Prefix(plaintext string) (string, bool) {
	// SplitN, not Split: the base64url secret may itself contain '_'.
	parts := strings.SplitN(plaintext, "_", 3)
	if len(parts) != 3 || len(parts[1]) != 8 || len(parts[2]) != 43 {
		return "", false
	}
	if parts[0] != kindTag[KindSDK] && parts[0] != kindTag[KindServer] {
		return "", false
	}
	return parts[0] + "_" + parts[1], true
}

// Hash returns an encoded Argon2id hash in the standard PHC string format.
func Hash(plaintext string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(plaintext), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum)), nil
}

var errMalformedHash = errors.New("apikey: malformed hash")

// Verify reports whether plaintext matches an encoded hash produced by Hash.
// Parameters are read from the hash itself, so they can be raised later
// without invalidating existing keys.
func Verify(plaintext, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errMalformedHash
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, errMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errMalformedHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errMalformedHash
	}
	got := argon2.IDKey([]byte(plaintext), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
