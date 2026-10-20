package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"helios/backend/internal/controlplane/scheduling"
	"helios/backend/internal/dataplane/evaluation"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/platform/redisx"
	"helios/backend/internal/server"
)

// These tests run TWO API instances over one database and a REAL Redis, each with its own flag configuration cache.
// The cache TTL is an hour unless a test is about the TTL, so only invalidation (or the TTL fallback under test) can
// explain a fresh answer. The bounds asserted here are the ones documented in docs/PERFORMANCE.md.

type ccWorld struct {
	*delWorld
	i2     *httptest.Server
	r1, r2 *server.Router
	prod   string
	stage  string
}

// noPatterns hides the pattern-subscription capability: an instance behind it never hears flag-channel messages
// (a lost broker connection, as far as the cache can tell). Revocations still work.
type noPatterns struct{ events.Subscriber }

func ccLimiter() *ratelimit.Limiter {
	big := ratelimit.Rule{PerSecond: 100000, Burst: 100000}
	return ratelimit.New(ratelimit.Config{Rules: map[string]ratelimit.Rule{
		ratelimit.ClassSDKKey: big, ratelimit.ClassScheduleWrite: big, ratelimit.ClassSegmentWrite: big,
		ratelimit.ClassInvite: big, ratelimit.ClassAccept: big, ratelimit.ClassWorkspace: big, ratelimit.ClassMe: big,
	}, StreamsPerKey: 1000})
}

func newCCWorld(t *testing.T, ttl time.Duration, mod2 func(*server.Deps)) *ccWorld {
	t.Helper()
	return newCCWorldWith(t, ttl, nil, mod2)
}

// newCCWorldWith also adjusts instance 1 (mod1), e.g. to make it deaf to the broker.
func newCCWorldWith(t *testing.T, ttl time.Duration, mod1, mod2 func(*server.Deps)) *ccWorld {
	t.Helper()
	url := os.Getenv("HELIOS_TEST_REDIS_URL")
	if url == "" {
		t.Skip("HELIOS_TEST_REDIS_URL not set; skipping the Redis-backed cache test")
	}
	if !strings.Contains(url, "127.0.0.1") && !strings.Contains(url, "localhost") {
		t.Fatalf("HELIOS_TEST_REDIS_URL must be a local Redis, got %q", url)
	}
	client, err := redisx.Connect(t.Context(), url)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	clock := &liveClock{}
	common := func(d *server.Deps) {
		d.Publisher, d.Subscriber = redisx.Publisher{Client: client}, redisx.Subscriber{Client: client}
		d.ConfigCacheTTL = ttl
		d.SDKKeyCacheTTL = time.Hour
		d.StreamRecheck = time.Hour
		d.Clock = clock.now
		d.Limiter = ccLimiter()
	}
	x := newTenantsWith(t, func(d *server.Deps) {
		common(d)
		if mod1 != nil {
			mod1(d)
		}
	})
	w := &ccWorld{delWorld: &delWorld{tenants: x, clock: clock}}
	w.stage, w.prod = envID(t, x.wsA, "staging"), envID(t, x.wsA, "production")
	w.i2, w.r2 = x.h.newInstanceRouter(func(d *server.Deps) {
		common(d)
		if mod2 != nil {
			mod2(d)
		}
	})
	w.r1 = x.h.router
	time.Sleep(400 * time.Millisecond) // both instances' subscriptions settle
	return w
}

type ccEval struct{ Variation, Reason, Experiment string }

func evalAt(t *testing.T, base, sdkKey, flag string) (int, ccEval) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"context": map[string]any{"subjectKey": "u1"}, "flagKeys": []string{flag}})
	req, _ := http.NewRequest("POST", base+"/evaluate", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Helios-SDK-Key", sdkKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Evaluations []struct {
			VariationID  *string `json:"variationId"`
			Reason       string  `json:"reason"`
			ExperimentID *string `json:"experimentId"`
		} `json:"evaluations"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	var e ccEval
	if len(out.Evaluations) == 1 {
		e.Reason = out.Evaluations[0].Reason
		if v := out.Evaluations[0].VariationID; v != nil {
			e.Variation = *v
		}
		if x := out.Evaluations[0].ExperimentID; x != nil {
			e.Experiment = *x
		}
	}
	return res.StatusCode, e
}

// until polls f (every 2 ms) and returns how long it took; it fails the test after max.
func until(t *testing.T, max time.Duration, what string, f func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !f() {
		if time.Since(start) > max {
			t.Fatalf("%s: still not true after %v", what, max)
		}
		time.Sleep(2 * time.Millisecond)
	}
	return time.Since(start)
}

func (w *ccWorld) flag(t *testing.T, key string, patch map[string]any) {
	t.Helper()
	w.h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags", boolFlag(key))
	if patch != nil {
		w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+key, patch)
	}
}

func (w *ccWorld) warm(t *testing.T, flag string) {
	t.Helper()
	for _, base := range []string{w.h.srv.URL, w.i2.URL} {
		if st, _ := evalAt(t, base, w.sdkKeyA, flag); st != 200 {
			t.Fatalf("warm-up on %s: %d", base, st)
		}
	}
}

func summarize(ds []time.Duration) string {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return fmt.Sprintf("n=%d p50=%v max=%v", len(ds), ds[len(ds)/2].Round(time.Millisecond), ds[len(ds)-1].Round(time.Millisecond))
}

// ---- the cache is really in use ------------------------------------------------------------------------

func TestConfigCacheServesRepeatedEvaluationsFromMemory(t *testing.T) {
	w := newCCWorld(t, time.Hour, nil)
	w.flag(t, "cc", map[string]any{"enabled": true, "fallthroughVariationId": "on"})
	before := w.r1.ConfigCache.Stats()
	for i := 0; i < 50; i++ {
		if st, e := evalAt(t, w.h.srv.URL, w.sdkKeyA, "cc"); st != 200 || e.Variation != "on" {
			t.Fatalf("%d %+v", st, e)
		}
	}
	st := w.r1.ConfigCache.Stats()
	if loads := st.Loads - before.Loads; loads != 1 || st.Hits-before.Hits < 49 {
		t.Errorf("50 evaluations: %d loads, %d hits (%+v)", loads, st.Hits-before.Hits, st)
	}
}

// ---- edits, kills and the other ways a configuration changes -------------------------------------------

func TestAnEditReachesTheOtherInstanceWithinMillisecondsAndThisOneAtOnce(t *testing.T) {
	w := newCCWorld(t, time.Hour, nil)
	w.flag(t, "cc", map[string]any{"enabled": true, "fallthroughVariationId": "on"})
	w.warm(t, "cc")
	var others []time.Duration
	for i := 0; i < 12; i++ {
		want := []string{"off", "on"}[i%2]
		w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/cc", map[string]any{"fallthroughVariationId": want})
		// the instance that took the edit: the very next request sees it (no waiting at all)
		if _, e := evalAt(t, w.h.srv.URL, w.sdkKeyA, "cc"); e.Variation != want {
			t.Fatalf("edit %d: the instance that handled it served %q, want %q", i, e.Variation, want)
		}
		d := until(t, 2*time.Second, "the other instance serves the edit", func() bool {
			_, e := evalAt(t, w.i2.URL, w.sdkKeyA, "cc")
			return e.Variation == want
		})
		others = append(others, d)
	}
	t.Logf("edit -> other instance: %s (cache TTL 1h, so only the Redis message explains it)", summarize(others))
	if max := others[len(others)-1]; max > 500*time.Millisecond { // others was sorted by summarize
		t.Errorf("slowest propagation %v, want well under half a second", max)
	}
	hitsBefore := w.r2.ConfigCache.Stats().Hits
	for i := 0; i < 20; i++ {
		evalAt(t, w.i2.URL, w.sdkKeyA, "cc")
	}
	if st := w.r2.ConfigCache.Stats(); st.Hits-hitsBefore < 19 {
		t.Errorf("the other instance does not serve from memory between edits: %+v", st)
	}
}

func TestAKillIsFastEverywhereAndNeverWaitsForTheTTL(t *testing.T) {
	w := newCCWorld(t, time.Hour, nil)
	w.flag(t, "cc", map[string]any{"enabled": true, "fallthroughVariationId": "on"})
	w.warm(t, "cc")
	var others []time.Duration
	for i := 0; i < 12; i++ {
		w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/cc", map[string]any{"enabled": true})
		until(t, 2*time.Second, "re-enabled on the other instance", func() bool { _, e := evalAt(t, w.i2.URL, w.sdkKeyA, "cc"); return e.Reason != "FLAG_DISABLED" })
		w.h.expect(200, w.a, "POST", "/environments/"+w.devA+"/flags/cc/kill", nil)
		if _, e := evalAt(t, w.h.srv.URL, w.sdkKeyA, "cc"); e.Reason != "FLAG_DISABLED" {
			t.Fatalf("kill %d: the instance that took it still serves the flag (%+v)", i, e)
		}
		others = append(others, until(t, 2*time.Second, "the other instance honours the kill", func() bool {
			_, e := evalAt(t, w.i2.URL, w.sdkKeyA, "cc")
			return e.Reason == "FLAG_DISABLED"
		}))
	}
	t.Logf("kill -> other instance: %s (TTL 1h)", summarize(others))
	if others[len(others)-1] > 500*time.Millisecond {
		t.Errorf("slowest kill propagation %v", others[len(others)-1])
	}
}

func TestADeletedFlagIsGoneEverywhereAtOnce(t *testing.T) {
	w := newCCWorld(t, time.Hour, nil)
	w.flag(t, "cc", map[string]any{"enabled": true, "fallthroughVariationId": "on"})
	w.warm(t, "cc")
	w.h.expect(204, w.a, "DELETE", "/environments/"+w.devA+"/flags/cc?force=true", nil)
	if _, e := evalAt(t, w.h.srv.URL, w.sdkKeyA, "cc"); e.Reason != "FLAG_NOT_FOUND" {
		t.Errorf("this instance: %+v", e)
	}
	until(t, 2*time.Second, "the other instance forgets the flag", func() bool { _, e := evalAt(t, w.i2.URL, w.sdkKeyA, "cc"); return e.Reason == "FLAG_NOT_FOUND" })
}

func TestAScheduledChangeRunByAnotherProcessReachesBothInstances(t *testing.T) {
	w := newCCWorld(t, time.Hour, nil)
	w.flag(t, "cc", nil) // created disabled
	w.warm(t, "cc")
	if _, e := evalAt(t, w.i2.URL, w.sdkKeyA, "cc"); e.Reason != "FLAG_DISABLED" {
		t.Fatalf("setup: %+v", e)
	}
	body := map[string]any{"idempotencyKey": "s1", "kind": "enable", "runAt": w.clock.now().Add(90 * time.Second).Format(time.RFC3339)}
	w.h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags/cc/scheduled-changes", body)
	w.clock.advance(2 * time.Minute)
	// cmd/scheduler is its own process: it publishes to Redis and never touches either API instance's cache
	client := w.redis(t)
	runner := scheduling.NewRunner(w.h.pool, redisx.Publisher{Client: client}, w.clock.now, scheduling.Config{WorkerID: "ext"})
	if st, err := runner.Run(context.Background()); err != nil || st.Succeeded != 1 {
		t.Fatalf("scheduler: %+v %v", st, err)
	}
	for name, base := range map[string]string{"instance 1": w.h.srv.URL, "instance 2": w.i2.URL} {
		d := until(t, 2*time.Second, name+" serves the scheduled enable", func() bool { _, e := evalAt(t, base, w.sdkKeyA, "cc"); return e.Reason != "FLAG_DISABLED" })
		t.Logf("scheduled change -> %s: %v", name, d.Round(time.Millisecond))
	}
}

func (w *ccWorld) redis(t *testing.T) *redis.Client {
	t.Helper()
	c, err := redisx.Connect(t.Context(), os.Getenv("HELIOS_TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestAPromotedConfigurationReachesTheOtherInstance(t *testing.T) {
	w := newCCWorld(t, time.Hour, nil)
	h := w.h
	h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags", boolFlag("promo"))
	h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/promo", map[string]any{"enabled": true, "fallthroughVariationId": "off"})
	h.expect(200, w.a, "PATCH", "/environments/"+w.stage+"/flags/promo", map[string]any{"enabled": true, "fallthroughVariationId": "on"})
	stageKey := h.newSDKKey(w.a, w.stage)
	for _, base := range []string{h.srv.URL, w.i2.URL} {
		if _, e := evalAt(t, base, stageKey, "promo"); e.Variation != "on" {
			t.Fatalf("setup on %s: %+v", base, e)
		}
	}
	preview := decode[promoPreview](t, h.expect(200, w.a, "POST", promoPath(w.stage, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev"}))
	h.expect(200, w.a, "POST", promoPath(w.stage, "promo", ""), map[string]any{
		"sourceEnvironment": "dev", "expectedVersion": preview.ExpectedVersion, "sourceVersion": preview.SourceVersion})
	until(t, 2*time.Second, "instance 2 serves the promoted fallthrough", func() bool { _, e := evalAt(t, w.i2.URL, stageKey, "promo"); return e.Variation == "off" })
	if _, e := evalAt(t, h.srv.URL, stageKey, "promo"); e.Variation != "off" {
		t.Errorf("instance 1: %+v", e)
	}
}

func TestStartingAndStoppingAnExperimentReachesTheOtherInstance(t *testing.T) {
	w := newCCWorld(t, time.Hour, nil)
	// the tenants fixture already has an experiment "acme-secret-exp" on secretFlag (draft)
	w.warm(t, secretFlag)
	if _, e := evalAt(t, w.i2.URL, w.sdkKeyA, secretFlag); e.Experiment != "" {
		t.Fatalf("setup: %+v", e)
	}
	w.h.expect(200, w.a, "POST", "/environments/"+w.devA+"/experiments/acme-secret-exp/start", nil)
	var running string
	until(t, 2*time.Second, "instance 2 reports the running experiment", func() bool {
		_, e := evalAt(t, w.i2.URL, w.sdkKeyA, secretFlag)
		running = e.Experiment
		return running != ""
	})
	if _, e := evalAt(t, w.h.srv.URL, w.sdkKeyA, secretFlag); e.Experiment != running {
		t.Errorf("instance 1: %+v", e)
	}
	w.h.expect(200, w.a, "POST", "/environments/"+w.devA+"/experiments/acme-secret-exp/stop", nil)
	until(t, 2*time.Second, "instance 2 stops reporting it", func() bool { _, e := evalAt(t, w.i2.URL, w.sdkKeyA, secretFlag); return e.Experiment == "" })
	if _, e := evalAt(t, w.h.srv.URL, w.sdkKeyA, secretFlag); e.Experiment != "" {
		t.Errorf("instance 1 still reports the stopped experiment: %+v", e)
	}
}

// ---- never serve what must not be served --------------------------------------------------------------

func TestARevokedKeyIsNeverServedFromTheCache(t *testing.T) {
	w := newCCWorld(t, time.Hour, nil)
	w.flag(t, "cc", map[string]any{"enabled": true, "fallthroughVariationId": "on"})
	w.warm(t, "cc")
	var keyID string
	if err := w.h.pool.QueryRow(t.Context(), `SELECT id::text FROM api_keys WHERE workspace_id = $1::uuid`, w.wsA.ID).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	w.h.expect(204, w.a, "DELETE", "/environments/"+w.devA+"/sdk-keys/"+keyID, nil)
	until(t, 2*time.Second, "instance 2 rejects the revoked key", func() bool { st, _ := evalAt(t, w.i2.URL, w.sdkKeyA, "cc"); return st == 401 })
	for i := 0; i < 10; i++ {
		for _, base := range []string{w.h.srv.URL, w.i2.URL} {
			// 401, or 429 once the per-client limit on failed keys kicks in: never a 200
			if st, e := evalAt(t, base, w.sdkKeyA, "cc"); st != 401 && st != 429 || e.Variation != "" {
				t.Fatalf("the revoked key got %d %+v from %s", st, e, base)
			}
		}
	}
	// the environment's configuration is still cached on instance 2: the KEY check is what protects, and it comes first
	if !w.r2.ConfigCache.Has(w.wsA.ID, w.devA) {
		t.Log("note: instance 2 no longer holds the entry (fine either way)")
	}
	// a new key works straight away, from the same cache
	fresh := w.h.newSDKKey(w.a, w.devA)
	if st, e := evalAt(t, w.i2.URL, fresh, "cc"); st != 200 || e.Variation != "on" {
		t.Errorf("a new key: %d %+v", st, e)
	}
}

func TestADeletedWorkspaceIsDroppedFromEveryCachePromptlyAndRestoredFresh(t *testing.T) {
	w := newCCWorld(t, time.Hour, nil)
	w.flag(t, "cc", map[string]any{"enabled": true, "fallthroughVariationId": "on"})
	w.warm(t, "cc")
	if !w.r1.ConfigCache.Has(w.wsA.ID, w.devA) || !w.r2.ConfigCache.Has(w.wsA.ID, w.devA) {
		t.Fatal("setup: not cached on both instances")
	}
	w.schedule(t, 200)
	if w.r1.ConfigCache.Has(w.wsA.ID, w.devA) {
		t.Error("instance 1 still holds the deleted workspace's configuration")
	}
	until(t, 2*time.Second, "instance 2 drops the workspace", func() bool { return !w.r2.ConfigCache.Has(w.wsA.ID, w.devA) })
	for _, base := range []string{w.h.srv.URL, w.i2.URL} {
		if st, _ := evalAt(t, base, w.sdkKeyA, "cc"); st != 401 {
			t.Errorf("%s answers %d for a deleted workspace", base, st)
		}
	}
	w.h.expect(200, w.a, "POST", "/workspaces/restore", map[string]any{"workspaceId": w.wsA.ID})
	for _, base := range []string{w.h.srv.URL, w.i2.URL} {
		until(t, 2*time.Second, base+" serves the restored workspace", func() bool { st, e := evalAt(t, base, w.sdkKeyA, "cc"); return st == 200 && e.Variation == "on" })
	}
}

// ---- when the broker does not deliver ------------------------------------------------------------------

func TestWhenTheMessageIsLostTheTTLHealsItWithinTheTTL(t *testing.T) {
	const ttl = 1200 * time.Millisecond
	w := newCCWorld(t, ttl, func(d *server.Deps) { d.Subscriber = noPatterns{redisx.Subscriber{Client: w2client(t)}} })
	w.flag(t, "cc", map[string]any{"enabled": true, "fallthroughVariationId": "on"})
	w.warm(t, "cc")
	w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/cc", map[string]any{"fallthroughVariationId": "off"})
	start := time.Now()
	if _, e := evalAt(t, w.i2.URL, w.sdkKeyA, "cc"); e.Variation != "on" {
		t.Fatalf("instance 2 already saw the edit (%+v): the message was not lost, so this tests nothing", e)
	}
	took := until(t, ttl+time.Second, "the TTL brings the edit to the deaf instance", func() bool {
		_, e := evalAt(t, w.i2.URL, w.sdkKeyA, "cc")
		return e.Variation == "off"
	})
	t.Logf("message lost: the deaf instance served the old value for %v (TTL %v)", time.Since(start).Round(time.Millisecond), ttl)
	if took > ttl+500*time.Millisecond {
		t.Errorf("healed after %v with a TTL of %v", took, ttl)
	}
	// and a kill is no exception when the message is lost: the TTL bounds it too
}

func w2client(t *testing.T) *redis.Client {
	c, err := redisx.Connect(t.Context(), os.Getenv("HELIOS_TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// A writer that never publishes (a direct SQL fix) is bounded by the TTL on every instance, including the one it ran near.
func TestAWriterThatDoesNotPublishIsBoundedByTheTTL(t *testing.T) {
	const ttl = 900 * time.Millisecond
	w := newCCWorld(t, ttl, nil)
	w.flag(t, "cc", map[string]any{"enabled": true, "fallthroughVariationId": "on"})
	w.warm(t, "cc")
	if err := w.h.rawExec(`UPDATE flag_configs SET fallthrough_variation_id = 'off', version = version + 1 WHERE flag_id = (SELECT id FROM flags WHERE key = 'cc' AND workspace_id = $1::uuid)`, w.wsA.ID); err != nil {
		t.Fatal(err)
	}
	if _, e := evalAt(t, w.h.srv.URL, w.sdkKeyA, "cc"); e.Variation != "on" {
		t.Skip("the cache expired before the check; timing-dependent on a slow machine")
	}
	took := until(t, ttl+time.Second, "the TTL picks up the direct write", func() bool { _, e := evalAt(t, w.h.srv.URL, w.sdkKeyA, "cc"); return e.Variation == "off" })
	if took > ttl+400*time.Millisecond {
		t.Errorf("took %v with a TTL of %v", took, ttl)
	}
}

// ---- ordering and concurrency --------------------------------------------------------------------------

// A reader never goes back to an older state after serving a newer one, and ends on the last edit, while edits stream in.
func TestNoInstanceEverServesAnOlderEditAfterANewerOne(t *testing.T) {
	w := newCCWorld(t, time.Hour, nil)
	h := w.h
	vars := make([]map[string]any, 20)
	for i := range vars {
		vars[i] = map[string]any{"id": fmt.Sprintf("v%02d", i), "value": i}
	}
	h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags", map[string]any{"key": "seq", "name": "seq", "variationType": "number", "variations": vars})
	h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/seq", map[string]any{"enabled": true, "fallthroughVariationId": "v00"})
	w.warm(t, "seq")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	seen := map[string][]string{}
	var mu sync.Mutex
	for name, base := range map[string]string{"instance 1": h.srv.URL, "instance 2": w.i2.URL, "instance 2b": w.i2.URL} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last string
			var n int
			for {
				select {
				case <-stop:
					mu.Lock()
					seen[name] = []string{last, fmt.Sprint(n)}
					mu.Unlock()
					return
				default:
				}
				_, e := evalAt(t, base, w.sdkKeyA, "seq")
				if e.Variation < last { // v00..v19 sort in edit order
					t.Errorf("%s served %s after %s", name, e.Variation, last)
					return
				}
				last = e.Variation
				n++
			}
		}()
	}
	for i := 1; i < 20; i++ {
		h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/seq", map[string]any{"fallthroughVariationId": fmt.Sprintf("v%02d", i)})
		time.Sleep(15 * time.Millisecond)
	}
	until(t, 2*time.Second, "instance 2 reaches the last edit", func() bool { _, e := evalAt(t, w.i2.URL, w.sdkKeyA, "seq"); return e.Variation == "v19" })
	close(stop)
	wg.Wait()
	for name, s := range seen {
		if s[0] != "v19" {
			t.Errorf("%s ended on %s", name, s[0])
		}
		t.Logf("%s: %s requests, ended on %s", name, s[1], s[0])
	}
}

func TestAColdInstanceHitByAHundredRequestsReadsTheDatabaseOnce(t *testing.T) {
	w := newCCWorld(t, time.Hour, nil)
	w.flag(t, "cc", map[string]any{"enabled": true, "fallthroughVariationId": "on"})
	// warm the SDK-key verification (a different cache) with a different flag, then measure config loads only
	evalAt(t, w.i2.URL, w.sdkKeyA, "nothing")
	w.r2.ConfigCache.Invalidate(w.wsA.ID, w.devA, "", 0)
	before := w.r2.ConfigCache.Stats().Loads
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if st, e := evalAt(t, w.i2.URL, w.sdkKeyA, "cc"); st != 200 || e.Variation != "on" {
				t.Errorf("%d %+v", st, e)
			}
		}()
	}
	wg.Wait()
	if n := w.r2.ConfigCache.Stats().Loads - before; n != 1 {
		t.Errorf("%d database loads for 100 concurrent cold requests, want 1", n)
	}
}

// The loader the cache fills itself from reads nothing for a workspace that is scheduled for deletion, so the cache
// cannot be refilled from a deleted workspace even by a request that was already past the key check.
func TestTheSnapshotLoaderReadsNothingForAWorkspaceScheduledForDeletion(t *testing.T) {
	w := newDelWorld(t)
	w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": true})
	snap, err := evaluation.LoadSnapshot(context.Background(), w.h.pool, w.wsA.ID, w.devA)
	if err != nil || len(snap.Configs) != 1 || snap.Versions[secretFlag] == 0 {
		t.Fatalf("before: %+v %v", snap, err)
	}
	// another workspace's environment is not readable through this workspace's id (checked while A is alive)
	other, err := evaluation.LoadSnapshot(context.Background(), w.h.pool, w.wsB.ID, w.devA)
	if err != nil || len(other.Configs) != 0 {
		t.Errorf("environment of workspace A read under workspace B's id: %+v %v", other, err)
	}
	w.schedule(t, 200)
	snap, err = evaluation.LoadSnapshot(context.Background(), w.h.pool, w.wsA.ID, w.devA)
	if err != nil || len(snap.Configs) != 0 || len(snap.Experiments) != 0 {
		t.Errorf("a deleted workspace's configuration was loaded: %+v %v", snap, err)
	}
}

// Instance 1 hears nothing from the broker (no pattern subscription, so no echo of its own messages either): what it
// does itself must still drop its own entries at once.
func TestTheInstanceThatTookAChangeDropsItsOwnEntriesEvenWhenItHearsNothingFromTheBroker(t *testing.T) {
	url := os.Getenv("HELIOS_TEST_REDIS_URL")
	if url == "" {
		t.Skip("HELIOS_TEST_REDIS_URL not set")
	}
	w := newCCWorldWith(t, time.Hour, func(d *server.Deps) { d.Subscriber = noPatterns{d.Subscriber} }, nil)
	w.flag(t, "cc", map[string]any{"enabled": true, "fallthroughVariationId": "on"})
	if _, e := evalAt(t, w.h.srv.URL, w.sdkKeyA, "cc"); e.Variation != "on" {
		t.Fatal(e)
	}
	w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/cc", map[string]any{"fallthroughVariationId": "off"})
	if _, e := evalAt(t, w.h.srv.URL, w.sdkKeyA, "cc"); e.Variation != "off" {
		t.Errorf("after its own edit the deaf instance served %+v", e)
	}
	w.h.expect(200, w.a, "POST", "/environments/"+w.devA+"/flags/cc/kill", nil)
	if _, e := evalAt(t, w.h.srv.URL, w.sdkKeyA, "cc"); e.Reason != "FLAG_DISABLED" {
		t.Errorf("after its own kill the deaf instance served %+v", e)
	}
	if !w.r1.ConfigCache.Has(w.wsA.ID, w.devA) {
		t.Fatal("setup: nothing cached on instance 1")
	}
	w.schedule(t, 200)
	if w.r1.ConfigCache.Has(w.wsA.ID, w.devA) {
		t.Error("the deaf instance kept the deleted workspace's configuration")
	}
}
