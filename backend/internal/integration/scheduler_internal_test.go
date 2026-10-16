package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/server"
)

const goodSecret = "s3cr3t-value-for-tests-0123456789-abcdef" // 40 characters: a throwaway, used only here

func (h *harness) internalRun(secret string, headers map[string]string) (int, http.Header, []byte) {
	h.t.Helper()
	req, _ := http.NewRequest("POST", h.srv.URL+"/internal/scheduler/run", strings.NewReader("{}"))
	if secret != "" {
		req.Header.Set("X-Helios-Scheduler-Secret", secret)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, b
}

func schedulerHarness(t *testing.T, secret string, rules map[string]ratelimit.Rule) (*harness, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)}
	h := newHarnessWith(t, func(d *server.Deps) {
		d.SchedulerSecret = secret
		d.Clock = clock.now
		if rules != nil {
			d.Limiter = ratelimit.New(ratelimit.Config{Rules: rules, StreamsPerKey: 100})
		}
	})
	return h, clock
}

const refused = `{"code":"UNAUTHORIZED","message":"unauthorized"}` + "\n"

func TestInternalEndpointNeedsTheSecret(t *testing.T) {
	h, _ := schedulerHarness(t, goodSecret, nil)
	// no secret, a wrong one, a near miss, the right one in the wrong header, a browser: one fixed answer, no detail
	for name, c := range map[string]struct {
		secret  string
		headers map[string]string
	}{
		"no secret":        {"", nil},
		"wrong":            {"not-the-secret", nil},
		"prefix":           {goodSecret[:20], nil},
		"longer":           {goodSecret + "x", nil},
		"in Authorization": {"", map[string]string{"Authorization": "Bearer " + goodSecret}},
		"from a browser":   {goodSecret, map[string]string{"Origin": "https://console.example.com"}},
		"empty header":     {"", map[string]string{"X-Helios-Scheduler-Secret": ""}},
	} {
		st, hdr, body := h.internalRun(c.secret, c.headers)
		if st != 401 || string(body) != refused {
			t.Errorf("%s: status %d body %q", name, st, body)
		}
		if hdr.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: CORS header on an internal route", name)
		}
	}
	// the right secret: counts, and only counts
	st, hdr, body := h.internalRun(goodSecret, nil)
	if st != 200 || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("right secret: %d %s", st, body)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"missed": true, "claimed": true, "reclaimed": true, "succeeded": true, "failed": true, "retried": true, "skipped": true}
	for k, v := range m {
		if !want[k] {
			t.Errorf("unexpected field %q in the answer", k)
		}
		if _, isNumber := v.(float64); !isNumber {
			t.Errorf("field %q is %T, want a count", k, v)
		}
	}
	if len(m) != len(want) {
		t.Errorf("answer = %s", body)
	}
	// only POST
	req, _ := http.NewRequest("GET", h.srv.URL+"/internal/scheduler/run", nil)
	req.Header.Set("X-Helios-Scheduler-Secret", goodSecret)
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 405 {
		t.Errorf("GET: %d", res.StatusCode)
	}
}

func TestInternalEndpointRunsDueChangesAndReportsCounts(t *testing.T) {
	h, clock := schedulerHarness(t, goodSecret, nil)
	x := &tenants{h: h, a: h.newUser("alice"), b: h.newUser("bob")}
	x.wsA = h.me(x.a).Workspaces[0]
	x.devA = envID(t, x.wsA, "dev")
	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", boolFlag("viahttp"))
	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags/viahttp/scheduled-changes", map[string]any{
		"idempotencyKey": "k", "kind": "enable", "runAt": clock.now().Add(5 * time.Minute).Format(time.RFC3339)})
	if _, _, b := h.internalRun(goodSecret, nil); !bytes.Contains(b, []byte(`"claimed":0`)) {
		t.Errorf("not due yet: %s", b)
	}
	clock.advance(5 * time.Minute)
	_, _, b := h.internalRun(goodSecret, nil)
	if !bytes.Contains(b, []byte(`"claimed":1`)) || !bytes.Contains(b, []byte(`"succeeded":1`)) {
		t.Errorf("due: %s", b)
	}
	if bytes.Contains(b, []byte("viahttp")) || bytes.Contains(b, []byte(x.a.email)) {
		t.Errorf("the answer names something: %s", b)
	}
}

func TestInternalEndpointIsDisabledWhenUnset(t *testing.T) {
	for name, secret := range map[string]string{"unset": "", "too short": "short-secret", "31 characters": strings.Repeat("a", 31)} {
		h, _ := schedulerHarness(t, secret, nil)
		for _, send := range []string{"", secret, "anything"} {
			st, _, body := h.internalRun(send, nil)
			if st != 404 || strings.TrimSpace(string(body)) != "404 page not found" {
				t.Errorf("%s (sent %q): status %d body %q, want exactly an unknown path's 404", name, send, st, body)
			}
		}
	}
	// the smallest accepted secret works
	h, _ := schedulerHarness(t, strings.Repeat("a", 32), nil)
	if st, _, _ := h.internalRun(strings.Repeat("a", 32), nil); st != 200 {
		t.Errorf("32 characters: %d", st)
	}
}

func TestTheSecretNeverReachesTheLogs(t *testing.T) {
	logs := captureLogs(t)
	h, _ := schedulerHarness(t, goodSecret, nil)
	wrong := "wrong-secret-value-should-not-be-logged-0123456789"
	h.internalRun(goodSecret, nil)
	h.internalRun(wrong, nil)
	h.internalRun("", map[string]string{"Authorization": "Bearer " + wrong})
	h.internalRun(goodSecret, map[string]string{"Origin": "https://evil.example"})
	out := logs.String()
	for _, secret := range []string{goodSecret, wrong, goodSecret[:12]} {
		if strings.Contains(out, secret) {
			t.Errorf("the logs contain %q:\n%s", secret, out)
		}
	}
	for _, body := range h.bodies {
		if bytes.Contains(body, []byte(goodSecret)) {
			t.Error("a response echoed the secret")
		}
	}
}

func TestInternalEndpointIsRateLimitedBeforeTheSecretIsChecked(t *testing.T) {
	h, _ := schedulerHarness(t, goodSecret, map[string]ratelimit.Rule{ratelimit.ClassSchedulerRun: tiny(3)})
	// guessing spends the same budget as using it
	for i := 0; i < 3; i++ {
		if st, _, _ := h.internalRun("guess-"+strings.Repeat("x", i), nil); st != 401 {
			t.Fatalf("guess %d: %d", i, st)
		}
	}
	st, hdr, body := h.internalRun("another-guess", nil)
	if st != 429 || hdr.Get("Retry-After") == "" || !bytes.Contains(body, []byte("RATE_LIMITED")) {
		t.Fatalf("4th request: %d %v %s", st, hdr, body)
	}
	// even the right secret waits its turn, so the limit cannot be used to test a guess
	if st, _, _ := h.internalRun(goodSecret, nil); st != 429 {
		t.Errorf("right secret after the budget is spent: %d", st)
	}
}
