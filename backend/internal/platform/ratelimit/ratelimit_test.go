package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"helios/backend/internal/platform/auth"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTest(rules map[string]Rule) (*Limiter, *clock) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	l := New(Config{Rules: rules})
	l.now = c.now
	return l, c
}

func TestBucketAllowsABurstThenRefills(t *testing.T) {
	l, c := newTest(map[string]Rule{ClassMe: {PerSecond: 2, Burst: 5}})
	for i := 0; i < 5; i++ {
		if ok, _ := l.Take(ClassMe, "u1"); !ok {
			t.Fatalf("request %d of the burst was refused", i+1)
		}
	}
	ok, wait := l.Take(ClassMe, "u1")
	if ok || wait != 500*time.Millisecond {
		t.Fatalf("6th request: ok=%v wait=%v, want refused with 500ms", ok, wait)
	}
	c.advance(500 * time.Millisecond) // one token back at 2/s
	if ok, _ := l.Take(ClassMe, "u1"); !ok {
		t.Error("a refilled token was not usable")
	}
	if ok, _ := l.Take(ClassMe, "u1"); ok {
		t.Error("only one token should have come back")
	}
	c.advance(time.Hour) // never above the burst
	for i := 0; i < 5; i++ {
		if ok, _ := l.Take(ClassMe, "u1"); !ok {
			t.Fatalf("after a long rest request %d was refused", i+1)
		}
	}
	if ok, _ := l.Take(ClassMe, "u1"); ok {
		t.Error("the bucket grew past its burst")
	}
}

func TestKeysAndClassesAreIndependent(t *testing.T) {
	l, _ := newTest(map[string]Rule{ClassMe: {PerSecond: 1, Burst: 1}, ClassInvite: {PerSecond: 1, Burst: 1}})
	l.Take(ClassMe, "alice")
	if ok, _ := l.Take(ClassMe, "alice"); ok {
		t.Error("alice's second request passed")
	}
	if ok, _ := l.Take(ClassMe, "bob"); !ok {
		t.Error("alice's usage limited bob")
	}
	if ok, _ := l.Take(ClassInvite, "alice"); !ok {
		t.Error("the me bucket limited invites")
	}
}

func TestPeekDoesNotSpend(t *testing.T) {
	l, _ := newTest(map[string]Rule{ClassSDKBad: {PerSecond: 1, Burst: 2}})
	for i := 0; i < 10; i++ {
		if ok, _ := l.Peek(ClassSDKBad, "1.2.3.4"); !ok {
			t.Fatal("Peek spent tokens")
		}
	}
	l.Take(ClassSDKBad, "1.2.3.4")
	l.Take(ClassSDKBad, "1.2.3.4")
	if ok, _ := l.Peek(ClassSDKBad, "1.2.3.4"); ok {
		t.Error("Peek reported a token in an empty bucket")
	}
}

func TestDisabledLimiterNeverRefuses(t *testing.T) {
	l := New(Config{Disabled: true, Rules: map[string]Rule{ClassMe: {PerSecond: 0.001, Burst: 1}}})
	for i := 0; i < 100; i++ {
		if ok, _ := l.Take(ClassMe, "u"); !ok {
			t.Fatal("a disabled limiter refused")
		}
	}
	if _, ok := l.Acquire("k"); !ok {
		t.Error("a disabled limiter refused a stream slot")
	}
}

func TestStreamSlotsAreCappedAndReleased(t *testing.T) {
	l := New(Config{StreamsPerKey: 2})
	r1, ok1 := l.Acquire("key-a")
	_, ok2 := l.Acquire("key-a")
	_, ok3 := l.Acquire("key-a")
	if !ok1 || !ok2 || ok3 {
		t.Fatalf("slots: %v %v %v, want true true false", ok1, ok2, ok3)
	}
	if _, ok := l.Acquire("key-b"); !ok {
		t.Error("another key shares the cap")
	}
	r1()
	r1() // releasing twice must not free two slots
	if _, ok := l.Acquire("key-a"); !ok {
		t.Error("a released slot was not reusable")
	}
	if _, ok := l.Acquire("key-a"); ok {
		t.Error("a double release freed an extra slot")
	}
}

func TestMemoryIsBounded(t *testing.T) {
	l, c := newTest(map[string]Rule{ClassIP: {PerSecond: 1, Burst: 1}})
	for i := 0; i < maxBuckets+50; i++ {
		l.Take(ClassIP, strconv.Itoa(i))
		if i == maxBuckets/2 {
			c.advance(time.Hour) // the first half goes idle and can be swept
		}
	}
	if len(l.buckets) > maxBuckets {
		t.Errorf("%d buckets, over the %d bound", len(l.buckets), maxBuckets)
	}
}

func TestParseConfig(t *testing.T) {
	cfg, err := ParseConfig("", "me=5/60, accept=2/50", "1", "7")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Rules[ClassMe] != (Rule{5, 60}) || cfg.Rules[ClassAccept] != (Rule{2, 50}) || cfg.TrustedProxyHops != 1 || cfg.StreamsPerKey != 7 || cfg.Disabled {
		t.Errorf("cfg = %+v", cfg)
	}
	if c, _ := ParseConfig("TRUE", "", "", ""); !c.Disabled {
		t.Error("RATE_LIMIT_DISABLED=TRUE was not honoured")
	}
	if l := New(cfg); l.Rule(ClassMe) != (Rule{5, 60}) || l.Rule(ClassIP) != DefaultRules()[ClassIP] {
		t.Error("overrides must replace only the classes they name")
	}
	for _, bad := range []struct{ rules, hops, streams string }{
		{"me", "", ""}, {"me=5", "", ""}, {"nope=1/1", "", ""}, {"me=0/5", "", ""}, {"me=5/0", "", ""}, {"me=x/y", "", ""},
		{"", "9", ""}, {"", "-1", ""}, {"", "x", ""}, {"", "", "0"}, {"", "", "x"},
	} {
		if _, err := ParseConfig("", bad.rules, bad.hops, bad.streams); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestClientIP(t *testing.T) {
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	for name, tc := range map[string]struct {
		r    *http.Request
		hops int
		want string
	}{
		"socket peer":                       {req("203.0.113.9:5555", ""), 0, "203.0.113.9"},
		"a spoofed header is ignored":       {req("203.0.113.9:5555", "1.1.1.1"), 0, "203.0.113.9"},
		"one trusted proxy: the last entry": {req("10.0.0.1:80", "6.6.6.6, 198.51.100.7"), 1, "198.51.100.7"},
		"two trusted proxies":               {req("10.0.0.1:80", "6.6.6.6, 198.51.100.7, 10.0.0.9"), 2, "198.51.100.7"},
		"header shorter than the hops":      {req("203.0.113.9:5555", "198.51.100.7"), 2, "203.0.113.9"},
		"garbage in the header":             {req("203.0.113.9:5555", "not-an-ip"), 1, "203.0.113.9"},
		"ipv6 grouped by /64":               {req("[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:1", ""), 0, "2001:db8:1:2::/64"},
		"ipv6 neighbour, same group":        {req("[2001:db8:1:2:1111:2222:3333:4444]:1", ""), 0, "2001:db8:1:2::/64"},
	} {
		if got := ClientIP(tc.r, tc.hops); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestMiddlewareAnswers429WithRetryAfter(t *testing.T) {
	l, _ := newTest(map[string]Rule{ClassMe: {PerSecond: 0.5, Burst: 2}})
	var served int
	h := l.ByUser(ClassMe)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served++ }))
	do := func(userID string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/me", nil)
		req = req.WithContext(auth.WithUser(req.Context(), auth.User{ID: userID}))
		h.ServeHTTP(rec, req)
		return rec
	}
	do("u1")
	do("u1")
	rec := do("u1")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("status %d, Retry-After %q, want 429 and 2", rec.Code, rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), `"code":"RATE_LIMITED"`) || served != 2 {
		t.Errorf("body %s, served %d", rec.Body.String(), served)
	}
	if rec := do("u2"); rec.Code != http.StatusOK {
		t.Errorf("another user was limited: %d", rec.Code)
	}
}
