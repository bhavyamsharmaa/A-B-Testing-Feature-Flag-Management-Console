package apitoken

import (
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

func TestTokenFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok, prefix, hash, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if !Pattern.MatchString(tok) || !WellFormed(tok) || len(tok) != 56 {
			t.Fatalf("token %q does not match its own pattern", tok)
		}
		if !strings.HasPrefix(tok, "hlt_") || strings.HasPrefix(tok, "eyJ") || !HasPrefix(tok) {
			t.Errorf("recognisable prefix: %q", tok)
		}
		if prefix != tok[:12] || Display(tok) != prefix || len(prefix) != 12 {
			t.Errorf("display prefix %q of %q", prefix, tok)
		}
		if seen[tok] || seen[prefix+"/hash"+hex.EncodeToString(hash)] {
			t.Fatal("a token repeated")
		}
		seen[tok] = true
		if len(hash) != 32 || !Equal(hash, Hash(tok)) || Equal(hash, Hash(tok+"x")) {
			t.Errorf("hash of %q", tok)
		}
		if strings.Contains(hex.EncodeToString(hash), tok[13:]) {
			t.Error("the hash contains the secret")
		}
	}
	// 256 bits of randomness: the secret part is 43 base64url characters = 32 bytes
	tok, _, _, _ := Generate()
	if len(tok[13:]) != 43 {
		t.Errorf("secret part is %d characters", len(tok[13:]))
	}
	// the documented scanner pattern finds a token inside text and does not match near misses
	scan := regexp.MustCompile(`\bhlt_[0-9a-f]{8}_[A-Za-z0-9_-]{43}\b`)
	if scan.FindString("export HELIOS_TOKEN="+tok+" # ci") != tok {
		t.Error("the scanner pattern does not find a token in a line of text")
	}
	for _, bad := range []string{tok[:55], tok + "A", "hlt_" + tok[4:12], "hlx" + tok[3:], strings.ToUpper(tok), tok[:12] + tok[13:]} {
		if WellFormed(bad) {
			t.Errorf("%q is accepted", bad)
		}
	}
}

func TestEqualIsLengthSafeAndStrict(t *testing.T) {
	a := Hash("x")
	if !Equal(a, Hash("x")) || Equal(a, Hash("y")) || Equal(a, a[:31]) || Equal(a, nil) || Equal(nil, nil) == false {
		t.Error("Equal")
	}
}
