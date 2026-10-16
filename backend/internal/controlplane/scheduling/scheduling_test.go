package scheduling

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"helios/backend/internal/controlplane/rbac"
)

func TestRequirementFor(t *testing.T) {
	prod, dev := rbac.Environment{IsProduction: true}, rbac.Environment{}
	for _, c := range []struct {
		kind string
		env  rbac.Environment
		want rbac.Role
	}{
		{KindKill, prod, rbac.Editor}, {KindKill, dev, rbac.Editor},
		{KindEnable, prod, rbac.Approver}, {KindDisable, prod, rbac.Approver}, {KindPatch, prod, rbac.Approver},
		{KindEnable, dev, rbac.Editor}, {KindPatch, dev, rbac.Editor},
	} {
		if got := RequirementFor(c.kind)(c.env); got != c.want {
			t.Errorf("%s in production=%v needs %s, got %s", c.kind, c.env.IsProduction, c.want, got)
		}
	}
}

func TestConfigDefaults(t *testing.T) {
	c := Config{}.withDefaults()
	if c.Grace != 15*time.Minute || c.Lease != 2*time.Minute || c.MaxAttempts != 5 || c.BatchSize != 20 || c.MaxRows != 200 || c.WorkerID == "" {
		t.Errorf("defaults: %+v", c)
	}
	var total time.Duration
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	for i, w := range want {
		if got := c.Backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, w)
		}
		if i < c.MaxAttempts-1 {
			total += c.Backoff(i + 1)
		}
	}
	if total >= c.Grace {
		t.Errorf("the retries (%v) outlast the grace window (%v): a retry would be a stale change", total, c.Grace)
	}
	if a, b := (Config{}).withDefaults().WorkerID, (Config{}).withDefaults().WorkerID; a == b {
		t.Error("two workers got the same id")
	}
}

func TestClockDefaultsToTheRealClockInUTC(t *testing.T) {
	var c Clock
	if n := c.now(); n.Location() != time.UTC || time.Since(n) > time.Minute {
		t.Errorf("default clock: %v", n)
	}
	fixed := Clock(func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("x", 3600)) })
	if got := fixed.now(); got.Location() != time.UTC || got.Hour() != 11 {
		t.Errorf("an injected clock is normalised to UTC: %v", got)
	}
}

// The secret check is a code-level property: it must compare with crypto/subtle and never with ==, and
// it must not log. A refactor that changes either fails here, before a timing leak ships.
func TestInternalHandlerComparesInConstantTime(t *testing.T) {
	src, err := os.ReadFile("internal.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	// strip comments so prose cannot satisfy or trip the checks
	code = regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(code, "")
	if !strings.Contains(code, `"crypto/subtle"`) || !strings.Contains(code, "subtle.ConstantTimeCompare(") {
		t.Error("the secret is not compared with crypto/subtle.ConstantTimeCompare")
	}
	for _, bad := range []string{`strings.Equal`, `bytes.Equal`, `strings.EqualFold`, `reflect.DeepEqual`} {
		if strings.Contains(code, bad) {
			t.Errorf("internal.go uses %s, which is not constant time", bad)
		}
	}
	// no ==/!= on the header value or on the configured secret
	for _, pat := range []string{`Header\.Get\(SecretHeader\)\s*[!=]=`, `[!=]=\s*r\.Header\.Get\(SecretHeader\)`, `secret\s*[!=]=\s*[a-z]`, `[a-z]\s*[!=]=\s*secret\b`} {
		if regexp.MustCompile(pat).MatchString(code) {
			t.Errorf("internal.go compares the secret with ==/!= (%s)", pat)
		}
	}
	// the digests compared have equal length, so the length of the secret does not leak either
	if !strings.Contains(code, "sha256.Sum256") {
		t.Error("the secret and the candidate are not reduced to equal-length digests before comparing")
	}
	// it never logs, and never writes the header into an answer
	for _, bad := range []string{"log.", "fmt.Print", "Printf(", "Println("} {
		if strings.Contains(code, bad) {
			t.Errorf("internal.go contains %q: the handler must not log", bad)
		}
	}
}
