// Package configcache keeps the compiled flag configuration of each (workspace, environment) in memory, per
// instance, so /evaluate does not read Postgres for every request.
//
// Correctness is the whole job, so the rules are stated here and tested one by one (docs/PERFORMANCE.md has the
// staleness table):
//
//   - INVALIDATION. Every flag mutation already publishes to the flag-change channel after it commits. The cache
//     hears it two ways: in process, at once, through InvalidatingPublisher (so an edit made on this instance is
//     visible to the next request on it, with Redis down or absent), and from other instances through one pattern
//     subscription on flags:* (Run). Any message for an environment drops that environment's entry. A KILL takes
//     exactly this path and never waits for anything else.
//   - TTL FALLBACK. An entry also expires after TTL (default 10 s) whatever happens, so a lost message, a broker that
//     was down, or a writer that does not publish (a direct SQL fix) heals by itself. That TTL is the worst-case
//     staleness of an edit when the broker is unhealthy.
//   - NO STALE LOAD WINS. A load that began before an invalidation is marked stale and not stored (its callers still
//     get what they asked for); requests arriving after the invalidation start a new load instead of joining it. A
//     message that carries a flag's version sets a floor: a snapshot below it is not stored. A snapshot older than
//     the one already held (by any flag's version) never replaces it.
//   - ONE LOADER PER KEY. Concurrent misses for an environment share one database read (stampede protection); the
//     load runs detached from any one caller's context, so a client that gives up does not fail the others.
//   - BOUNDED. At most MaxEntries environments and MaxBytes (estimated); the least recently used go first.
//   - NO NEGATIVE CACHING WORTH THE NAME. Errors are never cached. An environment with no flags (also what a deleted
//     workspace yields) is cached for EmptyTTL (2 s) only.
//   - WORKSPACE DROPS. Deleting a workspace announces it on the revocation channel (and drops locally); the cache
//     forgets every environment of it. Revoked SDK keys never reach the cache at all: the key is verified first.
//
// Segment membership is deliberately NOT cached: it is per subject and unbounded, and a stale membership would silently
// change who gets a feature. /evaluate still reads it live (one indexed probe, only for flags whose rules use segments).
package configcache

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"helios/backend/internal/dataplane/evaluation"
	"helios/backend/internal/platform/events"
)

// Loader reads one environment's snapshot from the source of truth.
type Loader func(ctx context.Context, workspaceID, environmentID string) (*evaluation.Snapshot, error)

// Options tune a Cache. The zero value means the defaults, except TTL: zero disables caching.
type Options struct {
	TTL         time.Duration // how long a snapshot may be served; <= 0 disables the cache (every Snapshot call loads)
	EmptyTTL    time.Duration // for an environment with no flags; default 2 s
	MaxEntries  int           // default 1000 environments
	MaxBytes    int64         // default 32 MiB (estimated)
	LoadTimeout time.Duration // one database read; default 10 s
	Now         func() time.Time
}

type key struct{ ws, env string }

type entry struct {
	snap     *evaluation.Snapshot
	expires  time.Time
	floor    map[string]int64 // flag key -> lowest acceptable version, from invalidation messages
	lastUsed atomic.Int64
}

type call struct {
	done  chan struct{}
	snap  *evaluation.Snapshot
	err   error
	stale bool // an invalidation arrived while it was loading: never store it
}

// Stats are cumulative counters (for tests, logs and the load test).
type Stats struct {
	Hits, Misses, Loads, LoadErrors, Invalidations, Evictions, Stale uint64
	Entries                                                          int
	Bytes                                                            int64
}

// Cache is safe for concurrent use.
type Cache struct {
	loader Loader
	opt    Options

	mu      sync.RWMutex
	entries map[key]*entry
	calls   map[key]*call
	bytes   int64

	hits, misses, loads, loadErrors, invalidations, evictions, stale atomic.Uint64
}

func New(loader Loader, opt Options) *Cache {
	if opt.EmptyTTL <= 0 {
		opt.EmptyTTL = 2 * time.Second
	}
	if opt.MaxEntries <= 0 {
		opt.MaxEntries = 1000
	}
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = 32 << 20
	}
	if opt.LoadTimeout <= 0 {
		opt.LoadTimeout = 10 * time.Second
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Cache{loader: loader, opt: opt, entries: map[key]*entry{}, calls: map[key]*call{}}
}

// Enabled reports whether the cache serves anything (TTL > 0).
func (c *Cache) Enabled() bool { return c.opt.TTL > 0 }

// Snapshot returns the environment's configuration: from memory when it is fresh, else from one shared load.
func (c *Cache) Snapshot(ctx context.Context, workspaceID, environmentID string) (*evaluation.Snapshot, error) {
	if !c.Enabled() {
		return c.loader(ctx, workspaceID, environmentID)
	}
	k := key{workspaceID, environmentID}
	now := c.opt.Now()

	c.mu.RLock()
	if e := c.entries[k]; e != nil && e.snap != nil && now.Before(e.expires) {
		snap := e.snap
		e.lastUsed.Store(now.UnixNano())
		c.mu.RUnlock()
		c.hits.Add(1)
		return snap, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	if e := c.entries[k]; e != nil && e.snap != nil && now.Before(e.expires) { // filled while we waited for the lock
		snap := e.snap
		e.lastUsed.Store(now.UnixNano())
		c.mu.Unlock()
		c.hits.Add(1)
		return snap, nil
	}
	cl := c.calls[k]
	start := cl == nil
	if start {
		cl = &call{done: make(chan struct{})}
		c.calls[k] = cl
	}
	c.mu.Unlock()
	c.misses.Add(1)
	if start {
		go c.run(k, cl)
	}
	select {
	case <-cl.done:
		return cl.snap, cl.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Cache) run(k key, cl *call) {
	ctx, cancel := context.WithTimeout(context.Background(), c.opt.LoadTimeout)
	snap, err := c.loader(ctx, k.ws, k.env)
	cancel()
	c.loads.Add(1)

	c.mu.Lock()
	if c.calls[k] == cl {
		delete(c.calls, k)
	}
	switch {
	case err != nil:
		c.loadErrors.Add(1) // never cached
	case cl.stale:
		c.stale.Add(1)
	default:
		c.storeLocked(k, snap)
	}
	cl.snap, cl.err = snap, err
	c.mu.Unlock()
	close(cl.done)
}

// storeLocked keeps snap unless it is older than what the cache already knows. Caller holds c.mu.
func (c *Cache) storeLocked(k key, snap *evaluation.Snapshot) {
	e := c.entries[k]
	if e == nil {
		e = &entry{}
		c.entries[k] = e
	}
	if !satisfies(snap, e.floor) || olderThan(snap, e.snap) {
		c.stale.Add(1)
		return
	}
	c.bytes += int64(snap.Bytes)
	if e.snap != nil {
		c.bytes -= int64(e.snap.Bytes)
	}
	ttl := c.opt.TTL
	if len(snap.Configs) == 0 {
		ttl = c.opt.EmptyTTL
	}
	now := c.opt.Now()
	e.snap, e.expires, e.floor = snap, now.Add(ttl), nil
	e.lastUsed.Store(now.UnixNano())
	c.evictLocked(k)
}

// satisfies: every flag with a floor is at or above it (a flag absent from the snapshot was deleted, which no floor refutes).
func satisfies(snap *evaluation.Snapshot, floor map[string]int64) bool {
	for flag, min := range floor {
		if v, ok := snap.Versions[flag]; ok && v < min {
			return false
		}
	}
	return true
}

// olderThan: some flag the two share is at a lower version in snap than in held.
func olderThan(snap, held *evaluation.Snapshot) bool {
	if held == nil {
		return false
	}
	for flag, v := range snap.Versions {
		if hv, ok := held.Versions[flag]; ok && v < hv {
			return true
		}
	}
	return false
}

func (c *Cache) evictLocked(keep key) {
	for len(c.entries) > c.opt.MaxEntries || c.bytes > c.opt.MaxBytes {
		var victim key
		var oldest int64 = 1<<63 - 1
		for k, e := range c.entries {
			if k == keep {
				continue
			}
			if l := e.lastUsed.Load(); l < oldest {
				oldest, victim = l, k
			}
		}
		if oldest == 1<<63-1 {
			return // only the entry just stored is left
		}
		c.dropLocked(victim)
		c.evictions.Add(1)
	}
}

func (c *Cache) dropLocked(k key) {
	if e := c.entries[k]; e != nil {
		if e.snap != nil {
			c.bytes -= int64(e.snap.Bytes)
		}
		delete(c.entries, k)
	}
}

// Invalidate forgets one environment now. flagKey and version, when the message carries them, set a floor so that a
// snapshot read from behind them is not stored. A load in flight is marked stale and detached.
func (c *Cache) Invalidate(workspaceID, environmentID, flagKey string, version int64) {
	k := key{workspaceID, environmentID}
	c.invalidations.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl := c.calls[k]; cl != nil {
		cl.stale = true
		delete(c.calls, k) // requests from now on start a load of their own
	}
	e := c.entries[k]
	if e == nil {
		return
	}
	if e.snap != nil {
		c.bytes -= int64(e.snap.Bytes)
		e.snap = nil
	}
	if flagKey != "" && version > 0 {
		if e.floor == nil {
			e.floor = map[string]int64{}
		}
		if version > e.floor[flagKey] {
			e.floor[flagKey] = version
		}
		return // keep the (empty) entry: it carries the floor
	}
	delete(c.entries, k)
}

// InvalidateWorkspace forgets every environment of a workspace (deleted, or its keys dropped).
func (c *Cache) InvalidateWorkspace(workspaceID string) {
	c.invalidations.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, cl := range c.calls {
		if k.ws == workspaceID {
			cl.stale = true
			delete(c.calls, k)
		}
	}
	for k := range c.entries {
		if k.ws == workspaceID {
			c.dropLocked(k)
		}
	}
}

// Flush forgets everything (after the broker connection was down: messages may have been missed).
func (c *Cache) Flush() {
	c.invalidations.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cl := range c.calls {
		cl.stale = true
	}
	c.calls = map[key]*call{}
	c.entries = map[key]*entry{}
	c.bytes = 0
}

// Has reports whether a fresh snapshot of the environment is held (for tests).
func (c *Cache) Has(workspaceID, environmentID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e := c.entries[key{workspaceID, environmentID}]
	return e != nil && e.snap != nil && c.opt.Now().Before(e.expires)
}

func (c *Cache) Stats() Stats {
	c.mu.RLock()
	n, b := len(c.entries), c.bytes
	c.mu.RUnlock()
	return Stats{Hits: c.hits.Load(), Misses: c.misses.Load(), Loads: c.loads.Load(), LoadErrors: c.loadErrors.Load(),
		Invalidations: c.invalidations.Load(), Evictions: c.evictions.Load(), Stale: c.stale.Load(), Entries: n, Bytes: b}
}

// ---- hearing about changes ------------------------------------------------------------------------------

const flagsPrefix = "flags:"

// splitChannel undoes events.FlagsChannel.
func splitChannel(channel string) (ws, env string, ok bool) {
	rest, found := strings.CutPrefix(channel, flagsPrefix)
	if !found {
		return "", "", false
	}
	ws, env, ok = strings.Cut(rest, ":")
	return ws, env, ok && ws != "" && env != ""
}

// Apply handles one flag-channel message: any message for an environment drops it; a version in the payload sets a floor.
func (c *Cache) Apply(channel, payload string) {
	ws, env, ok := splitChannel(channel)
	if !ok {
		return
	}
	var m struct {
		FlagKey string `json:"flagKey"`
		Version int64  `json:"version"`
	}
	_ = json.Unmarshal([]byte(payload), &m) // an unparseable payload still invalidates
	c.Invalidate(ws, env, m.FlagKey, m.Version)
}

// InvalidatingPublisher is the Publisher the handlers use: it invalidates this instance's cache first, synchronously
// (an edit is visible to the very next request here, broker or no broker), then forwards to the real broker.
type InvalidatingPublisher struct {
	Inner events.Publisher
	Cache *Cache
}

func (p InvalidatingPublisher) Publish(ctx context.Context, channel string, payload []byte) error {
	if p.Cache != nil {
		p.Cache.Apply(channel, string(payload))
	}
	if p.Inner == nil {
		return nil
	}
	return p.Inner.Publish(ctx, channel, payload)
}

// Run listens for changes made elsewhere until ctx ends: flag-channel messages (one pattern subscription) and workspace
// drops on the revocation channel. Each subscription is retried with backoff, and the cache is flushed whenever one is
// (re)established or lost, because messages in the gap are gone; the TTL covers whatever slips through. With a
// subscriber that cannot do patterns (no broker) it returns at once and the cache lives on its TTL and local invalidation.
func (c *Cache) Run(ctx context.Context, sub events.Subscriber) {
	ps, ok := sub.(events.PatternSubscriber)
	if !ok {
		log.Printf("config cache: no broker; entries expire after %s and are dropped by changes made on this instance", c.opt.TTL)
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		backoffLoop(ctx, "flag changes", func() (bool, error) {
			msgs, closeFn, err := ps.SubscribePattern(ctx, events.FlagsPattern)
			if err != nil {
				return false, err
			}
			c.Flush()
			stop := context.AfterFunc(ctx, func() { _ = closeFn() }) // shutting down ends the range below
			for m := range msgs {
				c.Apply(m.Channel, m.Payload)
			}
			stop()
			_ = closeFn()
			c.Flush()
			return true, nil
		})
	}()
	go func() {
		defer wg.Done()
		backoffLoop(ctx, "workspace drops", func() (bool, error) {
			msgs, closeFn, err := sub.Subscribe(ctx, events.RevocationChannel)
			if err != nil {
				return false, err
			}
			c.Flush()
			stop := context.AfterFunc(ctx, func() { _ = closeFn() })
			for payload := range msgs {
				var m struct {
					WorkspaceID string `json:"workspaceId"`
				}
				if json.Unmarshal([]byte(payload), &m) == nil && m.WorkspaceID != "" {
					c.InvalidateWorkspace(m.WorkspaceID)
				}
			}
			stop()
			_ = closeFn()
			c.Flush()
			return true, nil
		})
	}()
	wg.Wait()
}

func backoffLoop(ctx context.Context, what string, once func() (bool, error)) {
	backoff := time.Second
	for ctx.Err() == nil {
		_, err := once()
		if errors.Is(err, events.ErrUnavailable) {
			return
		}
		if err != nil {
			log.Printf("config cache: cannot listen for %s (%v); retrying; entries expire on their TTL meanwhile", what, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}
