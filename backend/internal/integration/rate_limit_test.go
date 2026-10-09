package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/server"
)

func limitedHarness(t *testing.T, streams int, rules map[string]ratelimit.Rule) *harness {
	t.Helper()
	return newHarnessWith(t, func(d *server.Deps) {
		d.Limiter = ratelimit.New(ratelimit.Config{Rules: rules, StreamsPerKey: streams})
	})
}

// slow refill: within a test a spent bucket stays spent
func tiny(burst int) ratelimit.Rule { return ratelimit.Rule{PerSecond: 0.01, Burst: burst} }

func (h *harness) expect429(u user, method, path string, body any) http.Header {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, jsonBody(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if u.id != "" {
		req.Header.Set("X-Test-User", u.header())
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	if res.StatusCode != http.StatusTooManyRequests {
		h.t.Fatalf("%s %s: status %d, want 429; body %s", method, path, res.StatusCode, buf.String())
	}
	secs, err := strconv.Atoi(res.Header.Get("Retry-After"))
	if err != nil || secs < 1 {
		h.t.Fatalf("%s %s: Retry-After %q is not a positive number of seconds", method, path, res.Header.Get("Retry-After"))
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"code":"RATE_LIMITED"`)) {
		h.t.Fatalf("%s %s: body %s", method, path, buf.String())
	}
	return res.Header
}

func TestBootstrapIsRateLimitedPerUser(t *testing.T) {
	h := limitedHarness(t, 0, map[string]ratelimit.Rule{ratelimit.ClassMe: {PerSecond: 1, Burst: 3}})
	u1, u2 := h.newUser("busy"), h.newUser("calm")
	for i := 0; i < 3; i++ {
		h.expect(200, u1, "GET", "/me", nil)
	}
	h.expect429(u1, "GET", "/me", nil)
	h.expect429(u1, "GET", "/workspaces", nil) // the same bootstrap bucket
	h.expect(200, u2, "GET", "/me", nil)       // another user is unaffected
	time.Sleep(1100 * time.Millisecond)
	h.expect(200, u1, "GET", "/me", nil) // and it refills
}

func TestInviteKeyAndWorkspaceCreationAreRateLimited(t *testing.T) {
	h := limitedHarness(t, 0, map[string]ratelimit.Rule{
		ratelimit.ClassInvite: tiny(2), ratelimit.ClassKeyCreate: tiny(2), ratelimit.ClassWorkspace: tiny(2),
	})
	u := h.newUser("owner")
	ws := h.me(u).Workspaces[0]
	dev := envID(t, ws, "dev")

	for i := 0; i < 2; i++ {
		h.expect(201, u, "POST", "/workspaces/"+ws.ID+"/invites", map[string]any{"email": fmt.Sprintf("p%d@example.com", i), "role": "viewer"})
		h.expect(201, u, "POST", "/environments/"+dev+"/sdk-keys", map[string]any{"name": "k"})
		h.expect(201, u, "POST", "/workspaces", map[string]any{"name": fmt.Sprintf("w%d", i)})
	}
	h.expect429(u, "POST", "/workspaces/"+ws.ID+"/invites", map[string]any{"email": "p9@example.com", "role": "viewer"})
	h.expect429(u, "POST", "/environments/"+dev+"/sdk-keys", map[string]any{"name": "k"})
	h.expect429(u, "POST", "/workspaces", map[string]any{"name": "w9"})
	// reading is not touched by those limits
	h.expect(200, u, "GET", "/workspaces/"+ws.ID+"/invites", nil)
	h.expect(200, u, "GET", "/environments/"+dev+"/sdk-keys", nil)
}

// Invite tokens are secrets someone might guess at: accepting is limited per
// user and per client IP, so rotating accounts doesn't help.
func TestAcceptingInvitesIsLimitedPerUserAndPerIP(t *testing.T) {
	h := limitedHarness(t, 0, map[string]ratelimit.Rule{ratelimit.ClassAccept: tiny(3)})
	a, b := h.newUser("guesser1"), h.newUser("guesser2")
	for i := 0; i < 3; i++ {
		h.expect(404, a, "POST", "/invites/accept", map[string]any{"token": "hinv_" + fmt.Sprint(i)})
	}
	h.expect429(a, "POST", "/invites/accept", map[string]any{"token": "hinv_x"})
	// A different user from the same address has no tokens left either: the IP bucket is spent too.
	h.expect429(b, "POST", "/invites/accept", map[string]any{"token": "hinv_y"})
}

func TestSDKEndpointsAreLimitedPerKeyAndBadKeysPerIP(t *testing.T) {
	h := limitedHarness(t, 2, map[string]ratelimit.Rule{
		ratelimit.ClassSDKKey: tiny(6), ratelimit.ClassSDKBad: tiny(3), ratelimit.ClassStream: tiny(50),
	})
	x := &tenants{h: h, a: h.newUser("alice"), b: h.newUser("bob")}
	x.wsA, x.wsB = h.me(x.a).Workspaces[0], h.me(x.b).Workspaces[0]
	devA, devB := envID(t, x.wsA, "dev"), envID(t, x.wsB, "dev")
	keyA, keyB := h.newSDKKey(x.a, devA), h.newSDKKey(x.b, devB)

	// Per key: A's key gets its burst, then 429 with Retry-After; B's key is its own bucket.
	for i := 0; i < 6; i++ {
		if st := h.evaluateStatus(keyA, "f"); st != 200 {
			t.Fatalf("evaluation %d: status %d", i+1, st)
		}
	}
	if st := h.evaluateStatus(keyA, "f"); st != 429 {
		t.Errorf("A's 7th evaluation: status %d, want 429", st)
	}
	if st := h.evaluateStatus(keyB, "f"); st != 200 {
		t.Errorf("B's key was limited by A's usage: %d", st)
	}

	// Concurrent streams per key: 2 allowed, the 3rd is refused, and a closed one frees its slot.
	s1, s2 := h.openStream(keyB), h.openStream(keyB)
	req, _ := http.NewRequest("GET", h.srv.URL+"/sdk/stream", nil)
	req.Header.Set("X-Helios-SDK-Key", keyB)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 429 || res.Header.Get("Retry-After") == "" {
		t.Errorf("3rd stream: status %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	s1.cancel()
	<-s1.closed
	deadline := time.Now().Add(2 * time.Second)
	for {
		req, _ := http.NewRequest("GET", h.srv.URL+"/sdk/stream", nil)
		req.Header.Set("X-Helios-SDK-Key", keyB)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode == 200 {
			res.Body.Close()
			break
		}
		res.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("a closed stream never freed its slot")
		}
		time.Sleep(50 * time.Millisecond)
	}
	s2.cancel()

	// Bad keys: three failures per IP, then the IP is refused BEFORE any hashing; cached good keys still work.
	for i := 0; i < 3; i++ {
		if st := h.evaluateStatus(fmt.Sprintf("hsdk_%08x_%s", i, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), "f"); st != 401 {
			t.Fatalf("bad key %d: status %d", i, st)
		}
	}
	if st := h.evaluateStatus("hsdk_deadbeef_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "f"); st != 429 {
		t.Errorf("4th bad key: status %d, want 429", st)
	}
	if st := h.evaluateStatus("not even shaped like a key", "f"); st != 429 {
		t.Errorf("malformed key from the same IP: status %d, want 429", st)
	}
	// B's key is cached (it was verified above), so the exhausted bad-key bucket does not touch it.
	if st := h.evaluateStatus(keyB, "f"); st != 200 {
		t.Errorf("a cached valid key was refused by the bad-key limit: %d", st)
	}
}

func TestHealthChecksAreNeverLimited(t *testing.T) {
	h := limitedHarness(t, 1, map[string]ratelimit.Rule{
		ratelimit.ClassIP: tiny(1), ratelimit.ClassUser: tiny(1), ratelimit.ClassSDKBad: tiny(1),
	})
	for i := 0; i < 300; i++ {
		req, _ := http.NewRequest("GET", h.srv.URL+"/healthz", nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("health check %d: status %d", i, res.StatusCode)
		}
	}
	for _, r := range h.routes {
		if (r.Scope == server.ScopePublic) != (len(r.Limits) == 0) {
			t.Errorf("%s %s: scope %q has limits %v; only public routes may be unlimited", r.Method, r.Path, r.Scope, r.Limits)
		}
	}
}

// Defaults are generous: a person using the console hard, or an SDK polling
// quickly, never sees a 429.
func TestNormalTrafficIsNeverLimited(t *testing.T) {
	x := newTenants(t)
	h := x.h
	for i := 0; i < 25; i++ { // the console asks /me once per page load
		h.expect(200, x.a, "GET", "/me", nil)
	}
	for i := 0; i < 120; i++ { // 240 requests back to back, far more than a person clicking
		h.expect(200, x.a, "GET", "/environments/"+x.devA+"/flags", nil)
		h.expect(200, x.a, "GET", "/environments/"+x.devA+"/audit-logs", nil)
	}
	for i := 0; i < 400; i++ {
		if st := h.evaluateStatus(x.sdkKeyA, secretFlag); st != 200 {
			t.Fatalf("SDK evaluation %d: status %d", i+1, st)
		}
	}
}

func TestLimitsCanBeSwitchedOff(t *testing.T) {
	h := newHarnessWith(t, func(d *server.Deps) {
		d.Limiter = ratelimit.New(ratelimit.Config{Disabled: true, Rules: map[string]ratelimit.Rule{ratelimit.ClassMe: tiny(1), ratelimit.ClassIP: tiny(1)}})
	})
	u := h.newUser("free")
	for i := 0; i < 30; i++ {
		h.expect(200, u, "GET", "/me", nil)
	}
}

func jsonBody(v any) *bytes.Reader {
	if v == nil {
		return bytes.NewReader(nil)
	}
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

// The general limits sit in front of every control-plane route: the per-IP one
// even before authentication (so hammering with bad or no credentials is
// stopped), the per-user one right after.
func TestGeneralPerIPAndPerUserLimits(t *testing.T) {
	h := limitedHarness(t, 0, map[string]ratelimit.Rule{ratelimit.ClassIP: tiny(5)})
	for i := 0; i < 5; i++ {
		h.expect(401, user{}, "GET", "/me", nil) // no credentials at all, still counted
	}
	h.expect429(user{}, "GET", "/me", nil)
	h.expect429(h.newUser("anyone"), "GET", "/environments/"+envPlaceholder+"/flags", nil)

	h2 := limitedHarness(t, 0, map[string]ratelimit.Rule{ratelimit.ClassUser: tiny(4)})
	u, other := h2.newUser("loud"), h2.newUser("quiet")
	ws := h2.me(u).Workspaces[0] // 1
	for i := 0; i < 3; i++ {     // 2, 3, 4
		h2.expect(200, u, "GET", "/environments/"+envID(t, ws, "dev")+"/flags", nil)
	}
	h2.expect429(u, "GET", "/environments/"+envID(t, ws, "dev")+"/flags", nil)
	h2.expect(200, other, "GET", "/me", nil)
}

const envPlaceholder = "00000000-0000-4000-8000-000000000000"
