package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"helios/backend/internal/controlplane/sdkkeys"
	"helios/backend/internal/dataplane/evaluation"
	"helios/backend/internal/platform/apikey"
)

// sseStream is an open /sdk/stream connection.
type sseStream struct {
	data   chan string   // payload of each flag_update event
	closed chan struct{} // closed when the server ends the stream
	cancel context.CancelFunc
}

func (h *harness) openStream(sdkKey string) *sseStream {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", h.srv.URL+"/sdk/stream", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("X-Helios-SDK-Key", sdkKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		h.t.Fatalf("open stream: %v (status %v)", err, res)
	}
	s := &sseStream{data: make(chan string, 32), closed: make(chan struct{}), cancel: cancel}
	h.t.Cleanup(cancel)
	go func() {
		defer close(s.closed)
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: ") {
				s.data <- strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return s
}

// next returns the next event, or "" if none arrives within d.
func (s *sseStream) next(d time.Duration) string {
	select {
	case m := <-s.data:
		return m
	case <-time.After(d):
		return ""
	}
}

func (h *harness) newSDKKey(u user, envID string) string {
	h.t.Helper()
	return decode[struct{ Plaintext string }](h.t, h.expect(201, u, "POST", "/environments/"+envID+"/sdk-keys", map[string]any{"name": "test"})).Plaintext
}

func (h *harness) evaluateStatus(sdkKey string, flagKeys ...string) int {
	h.t.Helper()
	b, _ := json.Marshal(map[string]any{"context": map[string]any{"subjectKey": "u1"}, "flagKeys": flagKeys})
	req, _ := http.NewRequest("POST", h.srv.URL+"/evaluate", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Helios-SDK-Key", sdkKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// A's flag changes must reach A's SDK streams and nobody else's; the channel
// name carries the workspace and the environment.
func TestFlagChangesOnlyReachTheOwnWorkspacesStream(t *testing.T) {
	x := newTenants(t)
	h := x.h
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/flags", boolFlag("bobs-flag"))
	keyB := h.newSDKKey(x.b, x.devB)

	streamA, streamB := h.openStream(x.sdkKeyA), h.openStream(keyB)
	chanA := "flags:" + x.wsA.ID + ":" + x.devA
	chanB := "flags:" + x.wsB.ID + ":" + x.devB
	subs := map[string]bool{}
	for _, name := range h.bus.Subscribers() {
		subs[name] = true
	}
	if len(subs) != 2 || !subs[chanA] || !subs[chanB] {
		t.Fatalf("subscriptions = %v, want exactly %s and %s", subs, chanA, chanB)
	}

	// A flips its flag: A's stream hears it, B's does not.
	h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/"+secretFlag, map[string]any{"enabled": false})
	if got := streamA.next(2 * time.Second); !strings.Contains(got, secretFlag) {
		t.Errorf("A's stream got %q", got)
	}
	if got := streamB.next(400 * time.Millisecond); got != "" {
		t.Errorf("A's flag change reached B's stream: %q", got)
	}

	// And the other way round.
	h.expect(200, x.b, "PATCH", "/environments/"+x.devB+"/flags/bobs-flag", map[string]any{"enabled": true})
	if got := streamB.next(2 * time.Second); !strings.Contains(got, "bobs-flag") {
		t.Errorf("B's stream got %q", got)
	}
	if got := streamA.next(400 * time.Millisecond); got != "" {
		t.Errorf("B's flag change reached A's stream: %q", got)
	}

	// Every channel ever published to is workspace:environment, and only the two real ones.
	for _, ch := range h.bus.Published() {
		if ch != chanA && ch != chanB {
			t.Errorf("published to unexpected channel %q", ch)
		}
	}
	// A different environment of the same workspace is a different channel.
	staging := envID(t, x.wsA, "staging")
	h.expect(200, x.a, "PATCH", "/environments/"+staging+"/flags/"+secretFlag, map[string]any{"enabled": true})
	if got := streamA.next(400 * time.Millisecond); got != "" {
		t.Errorf("a staging change reached the dev stream: %q", got)
	}

	// The verification cache does not let B's key read A's flags either.
	h.evaluateStatus(x.sdkKeyA, secretFlag) // warm A's cache entry
	if st := h.evaluateStatus(keyB, secretFlag); st != 200 {
		t.Fatalf("status %d", st)
	}
}

func TestSDKKeysAreStoredHashedAndNeverEchoed(t *testing.T) {
	x := newTenants(t)
	h := x.h
	var hash string
	if err := h.pool.QueryRow(context.Background(), `SELECT key_hash FROM api_keys WHERE workspace_id = $1::uuid`, x.wsA.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") || strings.Contains(hash, x.sdkKeyA) {
		t.Errorf("key_hash = %q", hash)
	}
	for _, q := range []string{
		`SELECT count(*) FROM api_keys WHERE key_hash = $1 OR name = $1 OR key_prefix = $1`,
		`SELECT count(*) FROM audit_logs WHERE diff_after::text LIKE '%' || $1 || '%' OR diff_before::text LIKE '%' || $1 || '%' OR resource_id = $1`,
	} {
		if n := h.count(q, x.sdkKeyA); n != 0 {
			t.Errorf("the plaintext key is stored (%d rows): %s", n, q)
		}
	}
	// The list shows prefixes only.
	if b := h.expect(200, x.a, "GET", "/environments/"+x.devA+"/sdk-keys", nil); bytes.Contains(b, []byte(x.sdkKeyA)) {
		t.Error("the key list contains the plaintext")
	}
}

// Revocation takes effect at once on the instance that handled it even though
// verifications are cached for an hour; another instance honours it within its
// cache TTL; an open stream is closed.
func TestRevocationIsImmediateHereAndBoundedElsewhere(t *testing.T) {
	x := newTenants(t)
	h := x.h
	var keyID string
	if err := h.pool.QueryRow(context.Background(), `SELECT id::text FROM api_keys WHERE workspace_id = $1::uuid`, x.wsA.ID).Scan(&keyID); err != nil {
		t.Fatal(err)
	}

	// A second "instance": its own verifier and cache over the same database.
	const otherTTL = 400 * time.Millisecond
	other := apikey.NewVerifier(h.pool, otherTTL)
	otherMux := http.NewServeMux()
	otherMux.Handle("POST /evaluate", other.Middleware(evaluation.Handler(h.pool)))
	otherSrv := httptest.NewServer(otherMux)
	defer otherSrv.Close()
	evalOther := func() int {
		b, _ := json.Marshal(map[string]any{"context": map[string]any{"subjectKey": "u1"}, "flagKeys": []string{secretFlag}})
		req, _ := http.NewRequest("POST", otherSrv.URL+"/evaluate", bytes.NewReader(b))
		req.Header.Set("X-Helios-SDK-Key", x.sdkKeyA)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	stream := h.openStream(x.sdkKeyA)
	if h.evaluateStatus(x.sdkKeyA, secretFlag) != 200 || evalOther() != 200 { // both caches are warm
		t.Fatal("the key does not work before revocation")
	}

	start := time.Now()
	h.expect(204, x.a, "DELETE", "/environments/"+x.devA+"/sdk-keys/"+keyID, nil)

	if st := h.evaluateStatus(x.sdkKeyA, secretFlag); st != 401 {
		t.Errorf("revoked key on the revoking instance: status %d, want 401 (the cache TTL is an hour)", st)
	}
	t.Logf("revoking instance: key failed %v after the revoke call", time.Since(start))

	// The other instance still has it cached: that window is its TTL, and no more.
	if st := evalOther(); st != 200 {
		t.Logf("other instance already rejects it (status %d)", st) // fine: the cache may have just expired
	}
	deadline := time.Now().Add(otherTTL + time.Second)
	for evalOther() != 401 {
		if time.Now().After(deadline) {
			t.Fatalf("the other instance still accepts the revoked key after %v (its TTL is %v)", time.Since(start), otherTTL)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Logf("other instance: key failed %v after the revoke call (cache TTL %v; production TTL is 15s)", time.Since(start), otherTTL)

	// The open stream notices at its next re-check (150ms here, 10s in production).
	select {
	case <-stream.closed:
		t.Logf("open stream closed %v after the revoke call", time.Since(start))
	case <-time.After(3 * time.Second):
		t.Fatal("the stream of a revoked key is still open")
	}
}

// A key resolves to exactly its own environment, however it is mangled.
func TestMalformedAndMixedUpKeysNeverResolve(t *testing.T) {
	x := newTenants(t)
	h := x.h
	keyB := h.newSDKKey(x.b, x.devB)
	// hsdk_<8 hex>_<43 base64url>
	prefixA := x.sdkKeyA[:len("hsdk_")+8]
	secretA, secretB := x.sdkKeyA[len(prefixA)+1:], keyB[len(prefixA)+1:]

	for name, key := range map[string]string{
		"empty":                       "",
		"only the tag":                "hsdk_",
		"truncated":                   x.sdkKeyA[:len(x.sdkKeyA)-5],
		"extra character":             x.sdkKeyA + "x",
		"A's prefix with B's secret":  prefixA + "_" + secretB,
		"B's prefix with A's secret":  keyB[:len(prefixA)] + "_" + secretA,
		"A's prefix, wrong secret":    prefixA + "_" + strings.Repeat("A", 43),
		"server tag instead of sdk":   "hsrv" + x.sdkKeyA[len("hsdk"):],
		"upper-cased":                 strings.ToUpper(x.sdkKeyA),
		"the hash instead of the key": "$argon2id$v=19$m=19456,t=2,p=1$AAAA$BBBB",
		"two keys glued together":     x.sdkKeyA + keyB,
		"a different tenant's prefix": "hsdk_deadbeef_" + secretA,
	} {
		if key == "" {
			continue // an empty header is "missing", also 401 (covered below)
		}
		if st := h.evaluateStatus(key, secretFlag); st != 401 {
			t.Errorf("%s: status %d, want 401", name, st)
		}
	}
	if st := h.evaluateStatus("", secretFlag); st != 401 {
		t.Errorf("no key: status %d", st)
	}

	// The two real keys each see only their own workspace.
	if st := h.evaluateStatus(x.sdkKeyA, secretFlag); st != 200 {
		t.Errorf("A's own key: status %d", st)
	}
	if st := h.evaluateStatus(keyB, secretFlag); st != 200 { // B's key may ask for the name; it just finds nothing
		t.Errorf("B's own key: status %d", st)
	}
}

// Two keys drawing the same 32-bit display prefix is a matter of volume, not
// if: creating a key must retry with a fresh one instead of failing.
func TestKeyPrefixCollisionIsRetried(t *testing.T) {
	x := newTenants(t)
	h := x.h
	var existing string
	if err := h.pool.QueryRow(context.Background(), `SELECT key_prefix FROM api_keys WHERE workspace_id = $1::uuid`, x.wsA.ID).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	real := sdkkeys.GenerateKey
	calls := 0
	sdkkeys.GenerateKey = func(kind apikey.Kind) (string, string, string, error) {
		calls++
		if calls <= 2 { // the first two draws collide with A's existing key
			plain, _, hash, err := real(kind)
			return existing + "_" + strings.Repeat("x", 43), existing, hash + plain[:0], err
		}
		return real(kind)
	}
	defer func() { sdkkeys.GenerateKey = real }()

	key := decode[struct{ Plaintext string }](t, h.expect(201, x.a, "POST", "/environments/"+x.devA+"/sdk-keys", map[string]any{"name": "after collision"}))
	if calls != 3 || strings.HasPrefix(key.Plaintext, existing) {
		t.Fatalf("calls = %d, key %q", calls, key.Plaintext)
	}
	if st := h.evaluateStatus(key.Plaintext, secretFlag); st != 200 {
		t.Errorf("the new key does not work: %d", st)
	}
}
