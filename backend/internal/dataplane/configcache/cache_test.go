package configcache

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"helios/backend/internal/dataplane/evaluation"
	"helios/backend/internal/platform/events"
)

type clock struct{ t atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.t.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *clock) now() time.Time          { return time.Unix(0, c.t.Load()).UTC() }
func (c *clock) advance(d time.Duration) { c.t.Add(int64(d)) }

// snap makes a snapshot whose flags are at the given versions.
func snap(versions map[string]int64) *evaluation.Snapshot {
	s := &evaluation.Snapshot{Configs: map[string]*evaluation.FlagConfig{}, Versions: map[string]int64{}, Experiments: map[string]string{}, Bytes: 1000}
	for k, v := range versions {
		s.Configs[k] = &evaluation.FlagConfig{Key: k, Enabled: true}
		s.Versions[k] = v
	}
	return s
}

// fakeDB is a loader over a mutable "database".
type fakeDB struct {
	mu       sync.Mutex
	versions map[string]int64
	loads    atomic.Int64
	err      error
	delay    time.Duration
	gate     chan struct{} // if set, loads wait for it after reading
}

func (d *fakeDB) load(ctx context.Context, ws, env string) (*evaluation.Snapshot, error) {
	d.loads.Add(1)
	d.mu.Lock()
	err := d.err
	read := map[string]int64{}
	for k, v := range d.versions {
		read[k] = v
	}
	gate, delay := d.gate, d.delay
	d.mu.Unlock()
	if gate != nil {
		<-gate // the read happened; the result is delivered later
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	if err != nil {
		return nil, err
	}
	return snap(read), nil
}

func (d *fakeDB) set(flag string, v int64) { d.mu.Lock(); d.versions[flag] = v; d.mu.Unlock() }

func newCache(db *fakeDB, ttl time.Duration, c *clock, mod ...func(*Options)) *Cache {
	opt := Options{TTL: ttl, Now: c.now}
	for _, m := range mod {
		m(&opt)
	}
	return New(db.load, opt)
}

func version(t *testing.T, c *Cache, flag string) int64 {
	t.Helper()
	s, err := c.Snapshot(context.Background(), "w", "e")
	if err != nil {
		t.Fatal(err)
	}
	return s.Versions[flag]
}

func TestServedFromMemoryUntilTheTTLEnds(t *testing.T) {
	clk := newClock()
	db := &fakeDB{versions: map[string]int64{"f": 1}}
	c := newCache(db, 10*time.Second, clk)
	for i := 0; i < 50; i++ {
		if version(t, c, "f") != 1 {
			t.Fatal("wrong version")
		}
	}
	if db.loads.Load() != 1 {
		t.Fatalf("%d loads for 50 requests", db.loads.Load())
	}
	db.set("f", 2) // a writer that does not publish: only the TTL can heal this
	clk.advance(9 * time.Second)
	if version(t, c, "f") != 1 {
		t.Error("expired early")
	}
	clk.advance(2 * time.Second)
	if version(t, c, "f") != 2 {
		t.Error("the TTL did not bring the new version in")
	}
	if db.loads.Load() != 2 {
		t.Errorf("loads: %d", db.loads.Load())
	}
}

func TestDisabledCacheReadsEveryTime(t *testing.T) {
	db := &fakeDB{versions: map[string]int64{"f": 1}}
	c := newCache(db, 0, newClock())
	for i := 0; i < 5; i++ {
		version(t, c, "f")
	}
	if db.loads.Load() != 5 || c.Enabled() {
		t.Errorf("loads %d enabled %v", db.loads.Load(), c.Enabled())
	}
}

func TestInvalidateDropsAtOnce(t *testing.T) {
	clk := newClock()
	db := &fakeDB{versions: map[string]int64{"f": 1}}
	c := newCache(db, time.Hour, clk)
	version(t, c, "f")
	db.set("f", 2)
	c.Invalidate("w", "e", "f", 2)
	if got := version(t, c, "f"); got != 2 {
		t.Errorf("after invalidation: version %d", got)
	}
	// another environment is untouched
	if _, err := c.Snapshot(context.Background(), "w", "other"); err != nil {
		t.Fatal(err)
	}
	c.Invalidate("w", "e", "", 0)
	if !c.Has("w", "other") || c.Has("w", "e") {
		t.Errorf("Has(other)=%v Has(e)=%v", c.Has("w", "other"), c.Has("w", "e"))
	}
}

func TestConcurrentMissesShareOneLoad(t *testing.T) {
	db := &fakeDB{versions: map[string]int64{"f": 1}, delay: 50 * time.Millisecond}
	c := newCache(db, time.Hour, newClock())
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); version(t, c, "f") }()
	}
	wg.Wait()
	if n := db.loads.Load(); n != 1 {
		t.Fatalf("%d loads for 200 concurrent cold requests, want 1 (stampede)", n)
	}
}

func TestACallerThatGivesUpDoesNotFailTheOthers(t *testing.T) {
	db := &fakeDB{versions: map[string]int64{"f": 1}, delay: 100 * time.Millisecond}
	c := newCache(db, time.Hour, newClock())
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := c.Snapshot(ctx, "w", "e"); first <- err }()
	time.Sleep(20 * time.Millisecond)
	second := make(chan error, 1)
	go func() { _, err := c.Snapshot(context.Background(), "w", "e"); second <- err }()
	time.Sleep(10 * time.Millisecond)
	cancel() // the caller that started the load leaves
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Errorf("first: %v", err)
	}
	if err := <-second; err != nil {
		t.Errorf("the other caller failed because the first gave up: %v", err)
	}
	if db.loads.Load() != 1 {
		t.Errorf("loads %d", db.loads.Load())
	}
}

// A load that began before an invalidation must not be stored, and a request after the invalidation must not join it.
func TestALoadStartedBeforeAnInvalidationIsNeverStored(t *testing.T) {
	db := &fakeDB{versions: map[string]int64{"f": 1}, gate: make(chan struct{})}
	c := newCache(db, time.Hour, newClock())
	old := make(chan int64, 1)
	go func() { s, _ := c.Snapshot(context.Background(), "w", "e"); old <- s.Versions["f"] }()
	for db.loads.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	// the write commits and is announced while the old read is still on its way back
	db.set("f", 2)
	c.Invalidate("w", "e", "f", 2)

	fresh := make(chan int64, 1)
	go func() { s, _ := c.Snapshot(context.Background(), "w", "e"); fresh <- s.Versions["f"] }()
	for db.loads.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	close(db.gate) // both reads finish
	if got := <-old; got != 1 {
		t.Errorf("the caller that asked before the change got %d", got)
	}
	if got := <-fresh; got != 2 {
		t.Errorf("a request after the invalidation joined the old load and got version %d", got)
	}
	if got := version(t, c, "f"); got != 2 {
		t.Errorf("the cache holds version %d, the stale load won", got)
	}
}

func TestASnapshotBelowTheAnnouncedVersionIsNotStored(t *testing.T) {
	db := &fakeDB{versions: map[string]int64{"f": 5}}
	c := newCache(db, time.Hour, newClock())
	version(t, c, "f")
	// a replica or a racing reader hands back version 5 although version 7 was announced
	c.Invalidate("w", "e", "f", 7)
	if got := version(t, c, "f"); got != 5 {
		t.Fatalf("got %d", got)
	}
	if c.Has("w", "e") {
		t.Error("a snapshot below the announced version was stored")
	}
	db.set("f", 7)
	version(t, c, "f")
	if !c.Has("w", "e") {
		t.Error("the snapshot at the announced version was not stored")
	}
}

func TestAnOlderSnapshotNeverReplacesANewerOne(t *testing.T) {
	clk := newClock()
	db := &fakeDB{versions: map[string]int64{"f": 9}}
	c := newCache(db, time.Second, clk)
	version(t, c, "f") // holds 9
	clk.advance(2 * time.Second)
	db.set("f", 4) // an older read comes back after the entry expired
	if got := version(t, c, "f"); got != 4 {
		t.Fatalf("served %d", got)
	}
	// the entry still carries version 9's snapshot (expired); the older one must not have replaced it as the held state
	c.mu.RLock()
	held := c.entries[key{"w", "e"}].snap.Versions["f"]
	c.mu.RUnlock()
	if held != 9 {
		t.Errorf("held version %d: an older snapshot replaced a newer one", held)
	}
}

func TestErrorsAreNeverCached(t *testing.T) {
	db := &fakeDB{versions: map[string]int64{"f": 1}, err: errors.New("db down")}
	c := newCache(db, time.Hour, newClock())
	for i := 0; i < 3; i++ {
		if _, err := c.Snapshot(context.Background(), "w", "e"); err == nil {
			t.Fatal("no error")
		}
	}
	if db.loads.Load() != 3 {
		t.Errorf("an error was cached: %d loads", db.loads.Load())
	}
	db.mu.Lock()
	db.err = nil
	db.mu.Unlock()
	if version(t, c, "f") != 1 {
		t.Error("did not recover")
	}
}

func TestAnEnvironmentWithNoFlagsIsCachedBrieflyOnly(t *testing.T) {
	clk := newClock()
	db := &fakeDB{versions: map[string]int64{}}
	c := newCache(db, time.Hour, clk)
	version(t, c, "f")
	version(t, c, "f")
	if db.loads.Load() != 1 {
		t.Fatalf("%d loads", db.loads.Load())
	}
	clk.advance(3 * time.Second) // EmptyTTL is 2 s
	db.set("f", 1)
	if version(t, c, "f") != 1 {
		t.Error("an empty result was cached beyond a few seconds")
	}
}

func TestMemoryIsBoundedByEntriesAndBytes(t *testing.T) {
	clk := newClock()
	db := &fakeDB{versions: map[string]int64{"f": 1}}
	c := newCache(db, time.Hour, clk, func(o *Options) { o.MaxEntries = 3 })
	for i := 0; i < 10; i++ {
		clk.advance(time.Millisecond)
		if _, err := c.Snapshot(context.Background(), "w", "env"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if st := c.Stats(); st.Entries > 3 || st.Evictions < 7 {
		t.Errorf("%+v", st)
	}
	// the most recently used survive
	if !c.Has("w", "env9") || c.Has("w", "env0") {
		t.Error("eviction did not drop the least recently used")
	}
	c2 := newCache(db, time.Hour, clk, func(o *Options) { o.MaxBytes = 2500 }) // two 1000-byte snapshots fit
	for i := 0; i < 6; i++ {
		clk.advance(time.Millisecond)
		_, _ = c2.Snapshot(context.Background(), "w", "env"+strconv.Itoa(i))
	}
	if st := c2.Stats(); st.Bytes > 2500 || st.Entries != 2 {
		t.Errorf("byte bound: %+v", st)
	}
}

func TestInvalidateWorkspaceDropsEveryEnvironmentOfIt(t *testing.T) {
	db := &fakeDB{versions: map[string]int64{"f": 1}}
	c := newCache(db, time.Hour, newClock())
	for _, w := range []string{"w1", "w2"} {
		for _, e := range []string{"dev", "prod"} {
			_, _ = c.Snapshot(context.Background(), w, e)
		}
	}
	c.InvalidateWorkspace("w1")
	if c.Has("w1", "dev") || c.Has("w1", "prod") || !c.Has("w2", "dev") || !c.Has("w2", "prod") {
		t.Errorf("entries after dropping w1: %+v", c.Stats())
	}
}

func TestApplyParsesTheChannelAndSurvivesGarbage(t *testing.T) {
	db := &fakeDB{versions: map[string]int64{"f": 1}}
	c := newCache(db, time.Hour, newClock())
	_, _ = c.Snapshot(context.Background(), "w", "e")
	c.Apply("not-a-flags-channel", `{}`)
	c.Apply("flags:w", `{}`)
	c.Apply("flags::e", `{}`)
	if !c.Has("w", "e") {
		t.Fatal("a message for another channel dropped the entry")
	}
	c.Apply(events.FlagsChannel("w", "e"), `not json`)
	if c.Has("w", "e") {
		t.Error("a garbage payload on the right channel must still invalidate")
	}
}

func TestTheInvalidatingPublisherDropsLocallyBeforeAndEvenWhenTheBrokerFails(t *testing.T) {
	db := &fakeDB{versions: map[string]int64{"f": 1}}
	c := newCache(db, time.Hour, newClock())
	_, _ = c.Snapshot(context.Background(), "w", "e")
	inner := &recordingPub{err: errors.New("redis down")}
	p := InvalidatingPublisher{Inner: inner, Cache: c}
	err := p.Publish(context.Background(), events.FlagsChannel("w", "e"), []byte(`{"flagKey":"f","version":2}`))
	if err == nil || len(inner.got) != 1 {
		t.Errorf("err %v forwarded %v", err, inner.got)
	}
	if c.Has("w", "e") {
		t.Error("a broker failure left the local entry in place")
	}
	if err := (InvalidatingPublisher{Cache: c}).Publish(context.Background(), "x", nil); err != nil {
		t.Errorf("no inner publisher: %v", err)
	}
}

type recordingPub struct {
	got []string
	err error
}

func (r *recordingPub) Publish(_ context.Context, ch string, _ []byte) error {
	r.got = append(r.got, ch)
	return r.err
}

func TestRunHearsOtherInstancesThroughOnePatternSubscription(t *testing.T) {
	bus := events.NewMemoryBus()
	db := &fakeDB{versions: map[string]int64{"f": 1}}
	c := newCache(db, time.Hour, newClock())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, bus); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool { return len(bus.Subscribers()) > 0 || true }, "")
	time.Sleep(100 * time.Millisecond) // subscriptions settle

	_, _ = c.Snapshot(context.Background(), "w", "e")
	_, _ = c.Snapshot(context.Background(), "w", "other")
	_ = bus.Publish(ctx, events.FlagsChannel("w", "e"), []byte(`{"flagKey":"f","version":2}`))
	waitFor(t, func() bool { return !c.Has("w", "e") }, "the flag message did not drop the entry")
	if !c.Has("w", "other") {
		t.Error("a message for one environment dropped another")
	}
	_ = bus.Publish(ctx, events.RevocationChannel, []byte(`{"workspaceId":"w"}`))
	waitFor(t, func() bool { return !c.Has("w", "other") }, "the workspace drop did not clear the cache")
}

func TestRunWithoutABrokerReturnsAndTheCacheStillWorks(t *testing.T) {
	db := &fakeDB{versions: map[string]int64{"f": 1}}
	c := newCache(db, time.Hour, newClock())
	done := make(chan struct{})
	go func() { c.Run(context.Background(), events.NoopSubscriber{}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run blocked without a broker")
	}
	if version(t, c, "f") != 1 {
		t.Error("cache broken")
	}
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Run with -race: readers, writers announcing, expiry and eviction all at once. The invariant checked is the one that
// matters: once the final state has been announced, no reader sees anything older than it.
func TestConcurrentReadersWritersAndInvalidationsStayConsistent(t *testing.T) {
	db := &fakeDB{versions: map[string]int64{"f": 1}}
	clk := newClock()
	c := newCache(db, 50*time.Millisecond, clk, func(o *Options) { o.MaxEntries = 4 })
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			env := "e" + strconv.Itoa(r%6) // more environments than MaxEntries: eviction churns
			var last int64
			for {
				select {
				case <-stop:
					return
				default:
				}
				s, err := c.Snapshot(context.Background(), "w", env)
				if err != nil {
					t.Error(err)
					return
				}
				if v := s.Versions["f"]; v < last {
					t.Errorf("a reader saw version %d after %d", v, last)
					return
				} else {
					last = v
				}
			}
		}(r)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for v := int64(2); v <= 200; v++ {
			db.set("f", v)
			for e := 0; e < 6; e++ {
				c.Invalidate("w", "e"+strconv.Itoa(e), "f", v)
			}
			clk.advance(10 * time.Millisecond)
			time.Sleep(200 * time.Microsecond)
		}
		close(stop)
	}()
	wg.Wait()
	for e := 0; e < 6; e++ {
		s, _ := c.Snapshot(context.Background(), "w", "e"+strconv.Itoa(e))
		if s.Versions["f"] != 200 {
			t.Errorf("env %d ends at version %d, want 200", e, s.Versions["f"])
		}
	}
	if st := c.Stats(); st.Entries > 4 {
		t.Errorf("%+v", st)
	}
	_ = fmt.Sprint(db.loads.Load())
}
