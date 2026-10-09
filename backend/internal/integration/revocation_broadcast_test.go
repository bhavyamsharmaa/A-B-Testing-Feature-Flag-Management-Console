package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/redisx"
	"helios/backend/internal/server"
)

// evaluateAt calls /evaluate on a specific instance.
func evaluateAt(t *testing.T, base, sdkKey string, flagKeys ...string) int {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"context": map[string]any{"subjectKey": "u1"}, "flagKeys": flagKeys})
	req, _ := http.NewRequest("POST", base+"/evaluate", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Helios-SDK-Key", sdkKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func openStreamAt(t *testing.T, base, sdkKey string) (closed chan struct{}, cancel context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/sdk/stream", nil)
	req.Header.Set("X-Helios-SDK-Key", sdkKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("open stream at %s: %v %v", base, err, res)
	}
	closed = make(chan struct{})
	go func() {
		defer close(closed)
		defer res.Body.Close()
		buf := make([]byte, 512)
		for {
			if _, err := res.Body.Read(buf); err != nil {
				return
			}
		}
	}()
	return closed, cancel
}

// revokeOnInstance1RejectedOnInstance2 is the scenario: instance 2 has the key
// cached for an hour and an open stream whose database re-check is an hour
// away, so only the broadcast can make it let go.
func revocationScenario(t *testing.T, h *harness, x *tenants, instance2 *httptest.Server) {
	t.Helper()
	var keyID string
	if err := h.pool.QueryRow(context.Background(), `SELECT id::text FROM api_keys WHERE workspace_id = $1::uuid`, x.wsA.ID).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if evaluateAt(t, instance2.URL, x.sdkKeyA, secretFlag) != 200 { // warms instance 2's cache
		t.Fatal("the key does not work on instance 2 before revocation")
	}
	closed, _ := openStreamAt(t, instance2.URL, x.sdkKeyA)

	start := time.Now()
	h.expect(204, x.a, "DELETE", "/environments/"+x.devA+"/sdk-keys/"+keyID, nil) // revoked via instance 1

	deadline := time.Now().Add(3 * time.Second)
	for evaluateAt(t, instance2.URL, x.sdkKeyA, secretFlag) != 401 {
		if time.Now().After(deadline) {
			t.Fatalf("instance 2 still accepts the revoked key after %v", time.Since(start))
		}
		time.Sleep(10 * time.Millisecond)
	}
	rejected := time.Since(start)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatalf("instance 2's stream for the revoked key is still open after %v", time.Since(start))
	}
	t.Logf("instance 2 rejected the key %v and closed its stream %v after the revoke call on instance 1 (cache TTL 1h, stream re-check 1h)", rejected, time.Since(start))
	if rejected > time.Second {
		t.Errorf("rejected after %v, want about a second or less", rejected)
	}
}

func slowFallbacks(d *server.Deps) {
	d.SDKKeyCacheTTL = time.Hour
	d.StreamRecheck = time.Hour // only the broadcast can act in time
}

// Two instances sharing one event bus (a faithful in-process stand-in for Redis).
func TestRevocationReachesOtherInstancesImmediately(t *testing.T) {
	x := newTenants(t)
	h := x.h
	instance2 := h.newInstance(slowFallbacks) // same MemoryBus as instance 1
	revocationScenario(t, h, x, instance2)
}

// deafToRevocations is a bus on which the revocation channel is unreachable
// (subscribing fails) while everything else works: a partial Redis outage.
type deafToRevocations struct{ *events.MemoryBus }

func (d deafToRevocations) Subscribe(ctx context.Context, channel string) (<-chan string, func() error, error) {
	if channel == events.RevocationChannel {
		return nil, nil, errors.New("redis: connection refused")
	}
	return d.MemoryBus.Subscribe(ctx, channel)
}

// If the broadcast does not arrive (Redis trouble), the fallbacks still bound
// the delay: the cache TTL for requests, the periodic re-check for streams.
func TestRevocationFallsBackToTheTTLWhenTheBroadcastIsLost(t *testing.T) {
	x := newTenants(t)
	h := x.h
	instance2 := h.newInstance(func(d *server.Deps) {
		d.Subscriber = deafToRevocations{h.bus}
		d.SDKKeyCacheTTL = 400 * time.Millisecond
		d.StreamRecheck = 200 * time.Millisecond
	})
	var keyID string
	if err := h.pool.QueryRow(context.Background(), `SELECT id::text FROM api_keys WHERE workspace_id = $1::uuid`, x.wsA.ID).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if evaluateAt(t, instance2.URL, x.sdkKeyA, secretFlag) != 200 {
		t.Fatal("the key does not work before revocation")
	}
	closed, _ := openStreamAt(t, instance2.URL, x.sdkKeyA)
	start := time.Now()
	h.expect(204, x.a, "DELETE", "/environments/"+x.devA+"/sdk-keys/"+keyID, nil)
	deadline := time.Now().Add(3 * time.Second)
	for evaluateAt(t, instance2.URL, x.sdkKeyA, secretFlag) != 401 {
		if time.Now().After(deadline) {
			t.Fatal("the fallback never rejected the key")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the fallback never closed the stream")
	}
	t.Logf("broadcast lost: key rejected and stream closed within %v (cache TTL 400ms, re-check 200ms)", time.Since(start))
}

// The same scenario against a REAL Redis (HELIOS_TEST_REDIS_URL, e.g.
// redis://127.0.0.1:56379/0): this exercises internal/platform/redisx, the code
// production runs, for flag-change fan-out and for revocations.
func TestRealRedisRevocationAndChannelNames(t *testing.T) {
	url := os.Getenv("HELIOS_TEST_REDIS_URL")
	if url == "" {
		t.Skip("HELIOS_TEST_REDIS_URL not set; skipping the Redis-backed test")
	}
	if !strings.Contains(url, "127.0.0.1") && !strings.Contains(url, "localhost") {
		t.Fatalf("HELIOS_TEST_REDIS_URL must be a local Redis, got %q", url)
	}
	ctx := t.Context()
	client, err := redisx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// Everything published during this test, whatever its name.
	mon := client.PSubscribe(ctx, "*")
	t.Cleanup(func() { _ = mon.Close() })
	if _, err := mon.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	seen := make(chan *redis.Message, 256)
	go func() {
		for m := range mon.Channel() {
			seen <- m
		}
	}()

	h := newHarnessWith(t, func(d *server.Deps) {
		d.Publisher, d.Subscriber = redisx.Publisher{Client: client}, redisx.Subscriber{Client: client}
		slowFallbacks(d)
	})
	x := &tenants{h: h, a: h.newUser("alice"), b: h.newUser("bob")}
	x.wsA, x.wsB = h.me(x.a).Workspaces[0], h.me(x.b).Workspaces[0]
	x.devA, x.devB = envID(t, x.wsA, "dev"), envID(t, x.wsB, "dev")
	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", boolFlag(secretFlag))
	x.sdkKeyA = h.newSDKKey(x.a, x.devA)
	keyB := h.newSDKKey(x.b, x.devB)
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/flags", boolFlag("bobs-flag"))

	// Flag changes cross a real Redis and still reach only the right stream.
	sA, sB := h.openStream(x.sdkKeyA), h.openStream(keyB)
	time.Sleep(200 * time.Millisecond) // Redis subscriptions settle
	h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/"+secretFlag, map[string]any{"enabled": true})
	if got := sA.next(3 * time.Second); !strings.Contains(got, secretFlag) {
		t.Errorf("A's stream got %q", got)
	}
	if got := sB.next(500 * time.Millisecond); got != "" {
		t.Errorf("A's change reached B's stream through Redis: %q", got)
	}

	// Revocation reaches a second instance that shares only Redis and the database.
	instance2 := h.newInstance(func(d *server.Deps) {
		d.Publisher, d.Subscriber = redisx.Publisher{Client: client}, redisx.Subscriber{Client: client}
		slowFallbacks(d)
	})
	time.Sleep(300 * time.Millisecond) // instance 2's watcher subscribes
	revocationScenario(t, h, x, instance2)

	// Every channel anything published to is one of the three expected names.
	chanA := "flags:" + x.wsA.ID + ":" + x.devA
	chanB := "flags:" + x.wsB.ID + ":" + x.devB
	deadline := time.After(500 * time.Millisecond)
collect:
	for {
		select {
		case m := <-seen:
			if m.Channel != chanA && m.Channel != chanB && m.Channel != events.RevocationChannel {
				t.Errorf("unexpected Redis channel %q", m.Channel)
			}
		case <-deadline:
			break collect
		}
	}
}
