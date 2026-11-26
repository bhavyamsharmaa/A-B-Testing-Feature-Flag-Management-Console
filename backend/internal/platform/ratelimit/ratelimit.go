// Package ratelimit is an in-memory token-bucket limiter for the API's
// unauthenticated and expensive paths: per client IP, per user, and per SDK
// key. Each key has a bucket that holds up to Burst tokens and refills at
// PerSecond; a request takes one. An empty bucket answers 429 with a
// Retry-After.
//
// It is per process, which is right for one instance (what Helios runs on
// Render today). With several instances each would allow its own share, so the
// effective limit is the configured one times the number of instances. Moving
// to a shared store (Redis) is only worth it once there are several; see
// docs/SECURITY_REVIEW.md.
package ratelimit

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/httpx"
)

// Rule is one bucket's shape.
type Rule struct {
	PerSecond float64 // refill rate
	Burst     int     // capacity: how many requests can arrive at once
}

// Classes of limit. A route names the classes that apply to it.
const (
	ClassIP        = "ip"             // every control-plane request, per client IP, before authentication
	ClassUser      = "user"           // every authenticated control-plane request, per user
	ClassMe        = "me"             // GET /me and GET /workspaces: the bootstrap that creates workspaces
	ClassWorkspace = "workspace"      // POST /workspaces
	ClassInvite    = "invite"         // creating and revoking invites
	ClassAccept    = "accept"         // POST /invites/accept, per user AND per IP (token guessing)
	ClassKeyCreate = "sdk_key_create" // creating SDK keys
	ClassSDKKey    = "sdk_key"        // /evaluate and /sdk/stream, per SDK key
	ClassSDKBad    = "sdk_invalid"    // failed SDK key verifications, per client IP
	ClassStream    = "stream_open"    // opening /sdk/stream, per SDK key
)

// DefaultRules are deliberately generous: normal use of the console and of
// SDKs never gets near them; they stop loops, scrapers and guessing.
func DefaultRules() map[string]Rule {
	return map[string]Rule{
		ClassIP:        {PerSecond: 50, Burst: 500}, // an office behind one NAT address shares this
		ClassUser:      {PerSecond: 30, Burst: 300},
		ClassMe:        {PerSecond: 2, Burst: 30},
		ClassWorkspace: {PerSecond: 0.2, Burst: 10},
		ClassInvite:    {PerSecond: 0.5, Burst: 20},
		ClassAccept:    {PerSecond: 0.5, Burst: 20},
		ClassKeyCreate: {PerSecond: 0.2, Burst: 10},
		ClassSDKKey:    {PerSecond: 100, Burst: 500},
		ClassSDKBad:    {PerSecond: 0.5, Burst: 30},
		ClassStream:    {PerSecond: 0.5, Burst: 20},
	}
}

// Config is the limiter's settings, normally read from the environment.
type Config struct {
	// Disabled turns every limit off (RATE_LIMIT_DISABLED=true).
	Disabled bool
	// Rules override DefaultRules per class (RATE_LIMITS="me=5/60,accept=2/50").
	Rules map[string]Rule
	// TrustedProxyHops is how many reverse proxies sit in front of the API and
	// append to X-Forwarded-For (Render: 1). 0 means "use the socket address".
	TrustedProxyHops int
	// StreamsPerKey caps concurrent /sdk/stream connections per SDK key.
	StreamsPerKey int
}

// ParseConfig builds a Config from the environment's strings. rules looks like
// "me=5/60,accept=2/50" (per second / burst); unknown classes are an error so a
// typo can't silently leave a limit at its default.
func ParseConfig(disabled string, rules string, hops string, streams string) (Config, error) {
	cfg := Config{Rules: map[string]Rule{}, StreamsPerKey: 100}
	if d := strings.ToLower(strings.TrimSpace(disabled)); d == "true" || d == "1" {
		cfg.Disabled = true
	}
	known := DefaultRules()
	for _, part := range strings.Split(rules, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		class, spec, ok := strings.Cut(part, "=")
		perSec, burst, ok2 := strings.Cut(spec, "/")
		if !ok || !ok2 {
			return cfg, fmt.Errorf("RATE_LIMITS: %q is not class=perSecond/burst", part)
		}
		if _, isClass := known[strings.TrimSpace(class)]; !isClass {
			return cfg, fmt.Errorf("RATE_LIMITS: unknown class %q", class)
		}
		rate, err1 := strconv.ParseFloat(strings.TrimSpace(perSec), 64)
		b, err2 := strconv.Atoi(strings.TrimSpace(burst))
		if err1 != nil || err2 != nil || rate <= 0 || b < 1 {
			return cfg, fmt.Errorf("RATE_LIMITS: %q needs a positive rate and burst", part)
		}
		cfg.Rules[strings.TrimSpace(class)] = Rule{PerSecond: rate, Burst: b}
	}
	if h := strings.TrimSpace(hops); h != "" {
		n, err := strconv.Atoi(h)
		if err != nil || n < 0 || n > 5 {
			return cfg, fmt.Errorf("TRUSTED_PROXY_HOPS must be 0-5, got %q", h)
		}
		cfg.TrustedProxyHops = n
	}
	if s := strings.TrimSpace(streams); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("STREAMS_PER_KEY must be a positive number, got %q", s)
		}
		cfg.StreamsPerKey = n
	}
	return cfg, nil
}

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter holds every bucket. Safe for concurrent use.
type Limiter struct {
	cfg   Config
	rules map[string]Rule
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket

	slotsMu sync.Mutex
	slots   map[string]int
}

// maxBuckets bounds memory: an attacker rotating keys (IPs, SDK key prefixes)
// can't grow the table without limit.
const maxBuckets = 100_000

func New(cfg Config) *Limiter {
	rules := DefaultRules()
	for class, r := range cfg.Rules {
		rules[class] = r
	}
	if cfg.StreamsPerKey < 1 {
		cfg.StreamsPerKey = 100
	}
	return &Limiter{cfg: cfg, rules: rules, now: time.Now, buckets: map[string]*bucket{}, slots: map[string]int{}}
}

// Rule returns the rule in force for a class.
func (l *Limiter) Rule(class string) Rule { return l.rules[class] }

// Disabled reports whether limiting is switched off.
func (l *Limiter) Disabled() bool { return l.cfg.Disabled }

func (l *Limiter) refill(b *bucket, r Rule, now time.Time) {
	b.tokens = math.Min(float64(r.Burst), b.tokens+now.Sub(b.last).Seconds()*r.PerSecond)
	b.last = now
}

func (l *Limiter) get(class, key string, r Rule, now time.Time) *bucket {
	id := class + "\x00" + key
	b := l.buckets[id]
	if b == nil {
		if len(l.buckets) >= maxBuckets {
			l.sweep(now)
		}
		b = &bucket{tokens: float64(r.Burst), last: now}
		l.buckets[id] = b
	}
	return b
}

// sweep drops buckets that have been idle long enough to be full again; if
// that frees nothing the table is reset (rare, and only ever more lenient).
func (l *Limiter) sweep(now time.Time) {
	for id, b := range l.buckets {
		if now.Sub(b.last) > 10*time.Minute {
			delete(l.buckets, id)
		}
	}
	if len(l.buckets) >= maxBuckets {
		l.buckets = map[string]*bucket{}
	}
}

func retryAfter(b *bucket, r Rule) time.Duration {
	need := 1 - b.tokens
	return time.Duration(math.Ceil(need/r.PerSecond*1000)) * time.Millisecond
}

// Take spends one token of the (class, key) bucket. When there is none it
// returns false and how long until there is.
func (l *Limiter) Take(class, key string) (bool, time.Duration) {
	if l.cfg.Disabled {
		return true, 0
	}
	r := l.rules[class]
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.get(class, key, r, now)
	l.refill(b, r, now)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, retryAfter(b, r)
}

// Peek is Take without spending: is there a token right now?
func (l *Limiter) Peek(class, key string) (bool, time.Duration) {
	if l.cfg.Disabled {
		return true, 0
	}
	r := l.rules[class]
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.get(class, key, r, now)
	l.refill(b, r, now)
	if b.tokens >= 1 {
		return true, 0
	}
	return false, retryAfter(b, r)
}

// Acquire takes one of the key's concurrent-stream slots. The release function
// must be called when the connection ends.
func (l *Limiter) Acquire(key string) (release func(), ok bool) {
	if l.cfg.Disabled {
		return func() {}, true
	}
	l.slotsMu.Lock()
	defer l.slotsMu.Unlock()
	if l.slots[key] >= l.cfg.StreamsPerKey {
		return nil, false
	}
	l.slots[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.slotsMu.Lock()
			defer l.slotsMu.Unlock()
			if l.slots[key]--; l.slots[key] <= 0 {
				delete(l.slots, key)
			}
		})
	}, true
}

// Reject answers 429 with a Retry-After header (whole seconds, at least 1).
func Reject(w http.ResponseWriter, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	httpx.WriteError(w, http.StatusTooManyRequests, "RATE_LIMITED",
		fmt.Sprintf("too many requests; try again in %d second(s)", secs))
}

// ClientIP is the address limits are keyed by. Behind trusted proxies it is the
// entry the nearest trusted proxy appended to X-Forwarded-For; otherwise the
// socket peer. IPv6 addresses are grouped by /64 so rotating within a subnet
// doesn't dodge the limit.
func ClientIP(r *http.Request, trustedHops int) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if trustedHops > 0 {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if len(parts) >= trustedHops {
			if cand := strings.TrimSpace(parts[len(parts)-trustedHops]); net.ParseIP(cand) != nil {
				host = cand
			}
		}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// ByIP limits a handler per client IP.
func (l *Limiter) ByIP(class string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ok, wait := l.Take(class, ClientIP(r, l.cfg.TrustedProxyHops)); !ok {
				Reject(w, wait)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ByUser limits a handler per authenticated user; run it after authentication.
func (l *Limiter) ByUser(class string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if user, ok := auth.FromContext(r.Context()); ok {
				if ok, wait := l.Take(class, user.ID); !ok {
					Reject(w, wait)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIPOf is ClientIP with this limiter's proxy setting.
func (l *Limiter) ClientIPOf(r *http.Request) string { return ClientIP(r, l.cfg.TrustedProxyHops) }
