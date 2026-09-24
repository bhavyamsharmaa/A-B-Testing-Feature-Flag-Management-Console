package apikey

import (
	"strings"
	"testing"
)

func TestGenerateVerifyRoundTrip(t *testing.T) {
	for range 50 { // enough draws that some secrets contain '_'
		plaintext, prefix, hash, err := Generate(KindSDK)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(plaintext, prefix+"_") {
			t.Fatalf("key %q doesn't start with prefix %q", plaintext, prefix)
		}
		if got, ok := Prefix(plaintext); !ok || got != prefix {
			t.Fatalf("Prefix(%q) = %q, %v; want %q", plaintext, got, ok, prefix)
		}
		if strings.Contains(hash, plaintext) {
			t.Fatal("hash contains the plaintext key")
		}
		if ok, err := Verify(plaintext, hash); err != nil || !ok {
			t.Fatalf("Verify(own key) = %v, %v", ok, err)
		}
		if ok, _ := Verify(plaintext+"x", hash); ok {
			t.Fatal("Verify accepted a different key")
		}
	}
}

func TestPrefixRejectsForeignShapes(t *testing.T) {
	for _, k := range []string{"", "hsdk", "hsdk_1234abcd", "xxxx_1234abcd_" + strings.Repeat("a", 43), "hsdk_123_" + strings.Repeat("a", 43)} {
		if _, ok := Prefix(k); ok {
			t.Errorf("Prefix(%q) accepted", k)
		}
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	if _, err := Verify("k", "$bcrypt$whatever"); err == nil {
		t.Fatal("malformed hash accepted")
	}
}
