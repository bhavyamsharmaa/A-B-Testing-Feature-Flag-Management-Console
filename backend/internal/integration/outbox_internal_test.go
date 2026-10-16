package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/server"
)

func (h *harness) outboxRun(secret string, headers map[string]string) (int, http.Header, []byte) {
	h.t.Helper()
	req, _ := http.NewRequest("POST", h.srv.URL+"/internal/outbox/run", strings.NewReader("{}"))
	if secret != "" {
		req.Header.Set("X-Helios-Outbox-Secret", secret)
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

func outboxHarness(t *testing.T, secret string, rules map[string]ratelimit.Rule) *harness {
	t.Helper()
	return newHarnessWith(t, func(d *server.Deps) {
		d.OutboxSecret = secret
		if rules != nil {
			d.Limiter = ratelimit.New(ratelimit.Config{Rules: rules})
		}
	})
}

func TestOutboxEndpointNeedsTheSecret(t *testing.T) {
	h := outboxHarness(t, goodSecret, nil)
	for name, c := range map[string]struct {
		secret  string
		headers map[string]string
	}{
		"no secret":              {"", nil},
		"wrong":                  {"not-the-secret", nil},
		"prefix":                 {goodSecret[:20], nil},
		"longer":                 {goodSecret + "x", nil},
		"the scheduler's secret": {"", map[string]string{"X-Helios-Scheduler-Secret": goodSecret}},
		"from a browser":         {goodSecret, map[string]string{"Origin": "https://console.example.com"}},
	} {
		st, _, body := h.outboxRun(c.secret, c.headers)
		if st != 401 || string(body) != refused {
			t.Errorf("%s: status %d body %q", name, st, body)
		}
	}
	st, hdr, body := h.outboxRun(goodSecret, nil)
	if st != 200 || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("right secret: %d %s", st, body)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"claimed": true, "reclaimed": true, "delivered": true, "retrying": true, "dead": true, "leaseLost": true, "purged": true}
	for k, v := range m {
		if !want[k] {
			t.Errorf("unexpected field %q in the answer", k)
		}
		if _, ok := v.(float64); !ok {
			t.Errorf("field %q is %T, want a count", k, v)
		}
	}
}

func TestOutboxEndpointIsDisabledWhenUnsetOrShort(t *testing.T) {
	for name, secret := range map[string]string{"unset": "", "too short": "short", "31 characters": strings.Repeat("a", 31)} {
		h := outboxHarness(t, secret, nil)
		for _, send := range []string{"", secret, "anything"} {
			if st, _, body := h.outboxRun(send, nil); st != 404 || strings.TrimSpace(string(body)) != "404 page not found" {
				t.Errorf("%s (sent %q): %d %q", name, send, st, body)
			}
		}
	}
}

func TestOutboxEndpointIsRateLimitedBeforeTheSecretIsChecked(t *testing.T) {
	h := outboxHarness(t, goodSecret, map[string]ratelimit.Rule{ratelimit.ClassOutboxRun: {PerSecond: 0.001, Burst: 2}})
	for i := 0; i < 2; i++ {
		if st, _, _ := h.outboxRun("guess", nil); st != 401 {
			t.Fatalf("guess %d: %d", i, st)
		}
	}
	st, hdr, _ := h.outboxRun(goodSecret, nil) // even the right secret is refused once the budget of guesses is spent
	if st != 429 || hdr.Get("Retry-After") == "" {
		t.Errorf("after the budget: %d", st)
	}
}

func TestOutboxEndpointDeliversAndReportsCountsOnly(t *testing.T) {
	w := newHookWorld(t)
	h := newHarnessWith(t, func(d *server.Deps) {
		d.OutboxSecret = goodSecret
		d.Delivery.Guard = w.h.deps.Delivery.Guard
	})
	x := &tenants{h: h, a: h.newUser("alice")}
	x.wsA = h.me(x.a).Workspaces[0]
	x.devA = envID(t, x.wsA, "dev")
	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", boolFlag("viahttp"))
	h.expect(201, x.a, "POST", "/workspaces/"+x.wsA.ID+"/webhooks", map[string]any{"url": w.recv.url(), "eventTypes": []string{"flag.updated"}})
	h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/viahttp", map[string]any{"enabled": true})
	_, _, b := h.outboxRun(goodSecret, nil)
	if !strings.Contains(string(b), `"delivered":1`) || strings.Contains(string(b), "viahttp") || strings.Contains(string(b), w.recv.url()) {
		t.Errorf("answer: %s", b)
	}
	if len(w.recv.requests()) != 1 {
		t.Errorf("%d requests", len(w.recv.requests()))
	}
}
