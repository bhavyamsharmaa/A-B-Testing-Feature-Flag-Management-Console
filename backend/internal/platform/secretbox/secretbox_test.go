package secretbox

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func k32(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func TestSealOpenRoundTripAndFreshNonces(t *testing.T) {
	b, err := New(k32(1))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := b.Seal([]byte("whsec_secret"), []byte("row-1"))
	c, _ := b.Seal([]byte("whsec_secret"), []byte("row-1"))
	if bytes.Equal(a, c) {
		t.Error("two seals of the same value are identical: the nonce is reused")
	}
	if bytes.Contains(a, []byte("whsec_secret")) {
		t.Error("plaintext visible in the sealed value")
	}
	got, err := b.Open(a, []byte("row-1"))
	if err != nil || string(got) != "whsec_secret" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestFailsClosedWithoutAKey(t *testing.T) {
	b, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	if b.Available() {
		t.Error("available with no key")
	}
	if _, err := b.Seal([]byte("x"), nil); !errors.Is(err, ErrNoKey) {
		t.Errorf("seal: %v", err)
	}
	if _, err := b.Open(make([]byte, 64), nil); !errors.Is(err, ErrNoKey) {
		t.Errorf("open: %v", err)
	}
	var nilBox *Box
	if nilBox.Available() {
		t.Error("nil box available")
	}
}

func TestACiphertextIsBoundToItsRowAndRejectsTampering(t *testing.T) {
	b, _ := New(k32(2))
	s, _ := b.Seal([]byte("secret"), []byte("endpoint-A"))
	if _, err := b.Open(s, []byte("endpoint-B")); !errors.Is(err, ErrCorrupt) {
		t.Errorf("opened under another row's identity: %v", err)
	}
	s[len(s)-1] ^= 1
	if _, err := b.Open(s, []byte("endpoint-A")); !errors.Is(err, ErrCorrupt) {
		t.Errorf("opened a tampered value: %v", err)
	}
	if _, err := b.Open([]byte("short"), nil); !errors.Is(err, ErrCorrupt) {
		t.Errorf("short: %v", err)
	}
}

func TestRotationOldKeysStillOpenNewSealsUseTheCurrent(t *testing.T) {
	old, _ := New(k32(3))
	sealedOld, _ := old.Seal([]byte("v"), []byte("r"))
	rotated, err := New(k32(4), k32(3))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rotated.Open(sealedOld, []byte("r")); err != nil || string(got) != "v" {
		t.Fatalf("old value after rotation: %q %v", got, err)
	}
	if rotated.SealedUnderCurrent(sealedOld) {
		t.Error("an old value reads as current")
	}
	fresh, _ := rotated.Seal([]byte("v"), []byte("r"))
	if !rotated.SealedUnderCurrent(fresh) {
		t.Error("a new value is not under the current key")
	}
	// after the old key is dropped, old values are unreadable, loudly
	dropped, _ := New(k32(4))
	if _, err := dropped.Open(sealedOld, []byte("r")); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("%v", err)
	}
}

func TestBadKeysAreRejectedWithAHelpfulMessage(t *testing.T) {
	for _, k := range []string{"short", base64.StdEncoding.EncodeToString([]byte("only 16 bytes!!!")), "!!!notbase64!!!"} {
		if _, err := New(k); err == nil || !strings.Contains(err.Error(), "32 random bytes") {
			t.Errorf("%q: %v", k, err)
		}
	}
	if _, err := New("", k32(1)); err == nil {
		t.Error("previous without current accepted")
	}
}
