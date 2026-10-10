package exposuretoken

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	secretA = "A-secret-value-that-is-at-least-32-bytes-long!!"
	secretB = "B-secret-value-that-is-at-least-32-bytes-long!!"
)

var (
	now  = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	base = Binding{Workspace: "w1", Environment: "e1", Experiment: "x1", Variation: "on", Subject: "user-42"}
)

func ring(t *testing.T, cur, prev string) *Keyring {
	t.Helper()
	k, err := NewKeyring(cur, prev, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestIssuedTokenVerifiesOnlyForTheExactBinding(t *testing.T) {
	k := ring(t, "k1:"+secretA, "")
	tok := k.Issue(base, now)
	if err := k.Verify(tok, base, now.Add(time.Hour)); err != nil {
		t.Fatalf("the token it issued does not verify: %v", err)
	}
	// Each field of the binding is covered by the signature.
	for name, change := range map[string]func(b Binding) Binding{
		"workspace":   func(b Binding) Binding { b.Workspace = "w2"; return b },
		"environment": func(b Binding) Binding { b.Environment = "e2"; return b },
		"experiment":  func(b Binding) Binding { b.Experiment = "x2"; return b },
		"variation":   func(b Binding) Binding { b.Variation = "off"; return b },
		"subject":     func(b Binding) Binding { b.Subject = "user-43"; return b },
	} {
		if err := k.Verify(tok, change(base), now); !errors.Is(err, ErrInvalid) {
			t.Errorf("a token for another %s: %v, want ErrInvalid", name, err)
		}
	}
	// Fields cannot be shifted into each other.
	if err := k.Verify(k.Issue(Binding{Workspace: "w1e", Environment: "1", Experiment: "x1", Variation: "on", Subject: "user-42"}, now), base, now); err == nil {
		t.Error("a token for ('w1e','1') verified for ('w1','e1')")
	}
}

func TestForgedMalformedAndTamperedTokens(t *testing.T) {
	k := ring(t, "k1:"+secretA, "")
	good := k.Issue(base, now)
	parts := strings.Split(good, ".")
	flip := parts[3][:len(parts[3])-1] + map[bool]string{true: "A", false: "B"}[strings.HasSuffix(parts[3], "B")]
	for name, tok := range map[string]string{
		"signed with another secret": ring(t, "k1:"+secretB, "").Issue(base, now),
		"last character changed":     strings.Join([]string{parts[0], parts[1], parts[2], flip}, "."),
		"a later issue time":         strings.Join([]string{parts[0], parts[1], "9999999999", parts[3]}, "."),
		"an earlier issue time":      strings.Join([]string{parts[0], parts[1], "1", parts[3]}, "."),
		"an unknown key id":          ring(t, "zz:"+secretA, "").Issue(base, now), // same secret, other id
	} {
		if err := k.Verify(tok, base, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	for _, tok := range []string{"", "x", "v1.k1.1", "v2.k1.1.AAAA", "v1.k1.notanumber." + parts[3], "v1.k1." + parts[2] + ".!!!", "v1.k1." + parts[2] + ".AAAA"} {
		if err := k.Verify(tok, base, now); !errors.Is(err, ErrMalformed) {
			t.Errorf("%q: %v, want ErrMalformed", tok, err)
		}
	}
}

func TestExpiryAndFuture(t *testing.T) {
	k := ring(t, "k1:"+secretA, "")
	tok := k.Issue(base, now)
	if err := k.Verify(tok, base, now.Add(24*time.Hour)); err != nil {
		t.Errorf("exactly at the limit: %v", err)
	}
	if err := k.Verify(tok, base, now.Add(24*time.Hour+time.Second)); !errors.Is(err, ErrExpired) {
		t.Errorf("a second past the window: %v", err)
	}
	if err := k.Verify(k.Issue(base, now.Add(time.Hour)), base, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("issued an hour in the future: %v", err)
	}
	if err := k.Verify(k.Issue(base, now.Add(time.Minute)), base, now); err != nil {
		t.Errorf("one minute of clock difference between instances is tolerated: %v", err)
	}
	// A forged token is never told it is "expired": the age is only revealed after the signature checks out.
	old := ring(t, "k1:"+secretB, "").Issue(base, now.Add(-72*time.Hour))
	if err := k.Verify(old, base, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("an old forged token: %v", err)
	}
}

func TestRotationOverlap(t *testing.T) {
	oldRing := ring(t, "k1:"+secretA, "")
	tok := oldRing.Issue(base, now)

	rotated := ring(t, "k2:"+secretB, "k1:"+secretA) // current k2, previous k1
	if err := rotated.Verify(tok, base, now); err != nil {
		t.Errorf("a token signed with the previous key is refused during the overlap: %v", err)
	}
	if got := rotated.Issue(base, now); !strings.HasPrefix(got, "v1.k2.") {
		t.Errorf("new tokens must be signed with the current key: %s", got[:8])
	}
	if err := rotated.Verify(rotated.Issue(base, now), base, now); err != nil {
		t.Error(err)
	}
	finished := ring(t, "k2:"+secretB, "") // the previous key removed
	if err := finished.Verify(tok, base, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("after the overlap the old key must stop working: %v", err)
	}
	// the previous key cannot be the same id as the current one
	if _, err := NewKeyring("k1:"+secretA, "k1:"+secretB, 0); err == nil {
		t.Error("two keys with one id accepted")
	}
}

func TestKeySpecsAreValidatedWithoutQuotingTheSecret(t *testing.T) {
	for _, spec := range []string{"", "nocolon", ":short", "k1:too-short-secret", "bad id:" + secretA, "waytoolongidentifier123:" + secretA} {
		_, err := NewKeyring(spec, "", 0)
		if err == nil {
			t.Errorf("%q accepted", spec)
			continue
		}
		for _, leak := range []string{spec, "too-short-secret", secretA} {
			if leak != "" && strings.Contains(err.Error(), leak) {
				t.Errorf("the error quotes the key: %v", err)
			}
		}
	}
	if _, err := NewKeyring("k1:"+secretA, "bad", 0); err == nil || strings.Contains(err.Error(), secretA) {
		t.Errorf("previous key: %v", err)
	}
}

func TestSecretsNeverReachLogsOrErrors(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	dev, err := DevKeyring(0)
	if err != nil {
		t.Fatal(err)
	}
	k := ring(t, "k1:"+secretA, "k0:"+secretB)
	tok := k.Issue(base, now)
	_ = k.Verify(tok, Binding{}, now)
	_ = k.Verify("v1.k1.1.AAAA", base, now)
	out := buf.String()
	if !strings.Contains(out, "DEVELOPMENT key") {
		t.Fatalf("the development key was not announced:\n%s", out)
	}
	for _, secret := range []string{secretA, secretB, string(dev.current.secret)} {
		if strings.Contains(out, secret) || strings.Contains(tok, secret) {
			t.Errorf("a signing secret reached the logs or a token:\n%s", out)
		}
	}
	if !dev.Dev || dev.current.id != "dev" {
		t.Error("the development keyring is not marked")
	}
}

func TestModes(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeOff, "off": ModeOff, " Report ": ModeReport, "REQUIRED": ModeRequired} {
		if got, err := ParseMode(in); err != nil || got != want {
			t.Errorf("%q -> %q %v", in, got, err)
		}
	}
	if _, err := ParseMode("strict"); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

// The signature is compared in constant time. A code-level check: the package
// must use hmac.Equal and must never compare a MAC with bytes.Equal, ==,
// strings.EqualFold, or bytes.Compare.
func TestSignatureIsComparedInConstantTime(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "exposuretoken.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	usesHMACEqual := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, _ := sel.X.(*ast.Ident)
		if pkg == nil {
			return true
		}
		switch pkg.Name + "." + sel.Sel.Name {
		case "hmac.Equal":
			usesHMACEqual = true
		case "bytes.Equal", "bytes.Compare", "strings.EqualFold", "strings.Compare", "subtle.ConstantTimeEq":
			t.Errorf("%s: %s.%s must not be used to compare signatures", fset.Position(call.Pos()), pkg.Name, sel.Sel.Name)
		}
		return true
	})
	if !usesHMACEqual {
		t.Error("the signature is not compared with hmac.Equal")
	}
}
