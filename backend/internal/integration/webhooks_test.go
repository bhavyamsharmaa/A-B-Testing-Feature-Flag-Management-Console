package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/controlplane/scheduling"
	"helios/backend/internal/controlplane/webhooks"
	"helios/backend/internal/platform/netguard"
	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/server"
)

// ---- a world with a local receiver ------------------------------------------------------

// liveClock is real time plus an offset the test can move: rows are stamped with the real
// clock when they are queued, so the worker's clock must start at "now" and only move forward.
type liveClock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *liveClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *liveClock) advance(d time.Duration) {
	c.mu.Lock()
	c.off += d
	c.mu.Unlock()
}

type received struct {
	Header http.Header
	Body   []byte
}

// receiver is a customer's server: it records what it is sent and answers as told.
type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []received
	status atomic.Int64
	conns  atomic.Int64
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{}
	r.status.Store(200)
	r.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, received{Header: req.Header.Clone(), Body: b})
		r.mu.Unlock()
		w.WriteHeader(int(r.status.Load()))
		_, _ = w.Write([]byte("receiver says hi"))
	}))
	r.srv.Start()
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) requests() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received{}, r.got...)
}

func (r *receiver) url() string { return r.srv.URL + "/hook" }

type hookWorld struct {
	*tenants
	clock *liveClock
	recv  *receiver
}

// newHookWorld: two tenants and a receiver on loopback. The development switch is ON for the
// guard here so the receiver is reachable; the SSRF tests use a strict world.
func newHookWorld(t *testing.T) *hookWorld {
	t.Helper()
	clock := &liveClock{}
	x := newTenantsWith(t, func(d *server.Deps) {
		d.Delivery.Guard = netguard.New(netguard.Policy{AllowLocalHTTP: true})
		d.Clock = clock.now
		// these tests make many management calls; the limits have their own test
		d.Limiter = ratelimit.New(ratelimit.Config{Rules: map[string]ratelimit.Rule{
			ratelimit.ClassWebhookWrite: {PerSecond: 1000, Burst: 1000}, ratelimit.ClassWebhookTest: {PerSecond: 1000, Burst: 1000},
		}})
	})
	return &hookWorld{tenants: x, clock: clock, recv: newReceiver(t)}
}

func (w *hookWorld) worker(cfg outbox.Config) *outbox.Worker {
	return w.h.deliveryWorker(w.clock.now, cfg)
}

func (w *hookWorld) run(t *testing.T) outbox.Stats {
	t.Helper()
	st, err := w.worker(outbox.Config{}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

type endpointOut struct {
	Endpoint struct {
		ID, URL, SecretHint, Status string
		Enabled                     bool
		EventTypes                  []string
		Environment                 *struct{ ID, Key string }
		Health                      struct {
			ConsecutiveFailures int
			DisabledReason      *string
		}
		PreviousSecretExpiresAt *time.Time
	}
	Secret string
}

func (w *hookWorld) create(t *testing.T, body map[string]any) endpointOut {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if body["url"] == nil {
		body["url"] = w.recv.url()
	}
	if body["eventTypes"] == nil {
		body["eventTypes"] = webhooks.Types
	}
	return decode[endpointOut](t, w.h.expect(201, w.a, "POST", "/workspaces/"+w.wsA.ID+"/webhooks", body))
}

func (w *hookWorld) dbString(query string, args ...any) string {
	var s *string
	if err := w.h.pool.QueryRow(context.Background(), query, args...).Scan(&s); err != nil {
		w.h.t.Fatal(err)
	}
	if s == nil {
		return ""
	}
	return *s
}

// ---- tests ----------------------------------------------------------------------------------

func TestADeliveryIsSignedAndCarriesAStableDeliveryID(t *testing.T) {
	w := newHookWorld(t)
	ep := w.create(t, map[string]any{"eventTypes": []string{"flag.updated"}})
	w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": false})
	st := w.run(t)
	if st.Delivered != 1 {
		t.Fatalf("%+v", st)
	}
	got := w.recv.requests()
	if len(got) != 1 {
		t.Fatalf("%d requests", len(got))
	}
	r := got[0]
	delivery := r.Header.Get(webhooks.HeaderDeliveryID)
	if err := webhooks.Verify(ep.Secret, r.Header.Get(webhooks.HeaderSignature), r.Header.Get(webhooks.HeaderTimestamp), delivery, r.Body, time.Now(), webhooks.Tolerance); err != nil {
		t.Fatalf("the receiver cannot verify the signature: %v", err)
	}
	if n := w.h.count(`SELECT count(*) FROM outbox WHERE delivery_id = $1::uuid AND status = 'delivered'`, delivery); n != 1 {
		t.Errorf("the delivery id the receiver saw is not the outbox row's")
	}
	if r.Header.Get(webhooks.HeaderEventType) != "flag.updated" || r.Header.Get("Content-Type") != "application/json" || !strings.HasPrefix(r.Header.Get("User-Agent"), "Helios-Webhooks/") {
		t.Errorf("headers %v", r.Header)
	}
	var env struct {
		ID, Type  string
		Version   int
		Data      struct{ Flag struct{ Key string } }
		Actor     struct{ Type, ID string }
		Workspace string `json:"workspaceId"`
	}
	if err := json.Unmarshal(r.Body, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != "flag.updated" || env.Version != 1 || env.Data.Flag.Key != secretFlag || env.Actor.Type != "user" || env.Actor.ID != w.a.id || env.Workspace != w.wsA.ID || r.Header.Get(webhooks.HeaderEventID) != env.ID {
		t.Errorf("envelope %s", r.Body)
	}
	// someone replaying a captured request with another body or another delivery id fails verification
	if err := webhooks.Verify(ep.Secret, r.Header.Get(webhooks.HeaderSignature), r.Header.Get(webhooks.HeaderTimestamp), delivery, append(r.Body, ' '), time.Now(), webhooks.Tolerance); err == nil {
		t.Error("a modified body verified")
	}
}

func TestAtLeastOnceTheSameDeliveryIDOnEveryAttempt(t *testing.T) {
	w := newHookWorld(t)
	w.create(t, map[string]any{"eventTypes": []string{"flag.updated"}})
	w.recv.status.Store(500)
	w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": false})
	if st := w.run(t); st.Retrying != 1 {
		t.Fatalf("%+v", st)
	}
	w.clock.advance(2 * time.Minute)
	w.recv.status.Store(200)
	if st := w.run(t); st.Delivered != 1 {
		t.Fatalf("%+v", st)
	}
	got := w.recv.requests()
	if len(got) != 2 || got[0].Header.Get(webhooks.HeaderDeliveryID) != got[1].Header.Get(webhooks.HeaderDeliveryID) {
		t.Fatalf("delivery ids across attempts: %d requests", len(got))
	}
	if got[0].Header.Get(webhooks.HeaderSignature) == got[1].Header.Get(webhooks.HeaderSignature) && got[0].Header.Get(webhooks.HeaderTimestamp) != got[1].Header.Get(webhooks.HeaderTimestamp) {
		t.Error("two timestamps, one signature")
	}
}

func TestManagementNeedsAnAdminAndASessionAndNeverShowsTheSecretAgain(t *testing.T) {
	w := newHookWorld(t)
	h := w.h
	base := "/workspaces/" + w.wsA.ID + "/webhooks"
	editor, viewer := h.newUser("erin"), h.newUser("vic")
	for _, m := range []struct {
		u    user
		role string
	}{{editor, "editor"}, {viewer, "viewer"}} {
		h.me(m.u)
		tok := decode[struct{ Token string }](t, h.expect(201, w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites", map[string]any{"email": m.u.email, "role": m.role})).Token
		h.expect(200, m.u, "POST", "/invites/accept", map[string]any{"token": tok})
	}
	ep := w.create(t, nil)
	for _, u := range []user{editor, viewer} {
		for _, c := range []struct{ method, path string }{
			{"GET", base}, {"POST", base}, {"PATCH", base + "/" + ep.Endpoint.ID}, {"DELETE", base + "/" + ep.Endpoint.ID},
			{"POST", base + "/" + ep.Endpoint.ID + "/rotate-secret"}, {"POST", base + "/" + ep.Endpoint.ID + "/test"}, {"GET", base + "/" + ep.Endpoint.ID + "/deliveries"},
		} {
			if st, _ := h.call(u, c.method, c.path, map[string]any{}); st != 403 {
				t.Errorf("%s %s as a non-admin: %d, want 403", c.method, c.path, st)
			}
		}
	}
	// an API token, even an admin's, cannot touch any of it
	admin := w.mintToken(t, "ci", "admin")
	for _, c := range []struct{ method, path string }{
		{"GET", base}, {"POST", base}, {"PATCH", base + "/" + ep.Endpoint.ID}, {"DELETE", base + "/" + ep.Endpoint.ID},
		{"POST", base + "/" + ep.Endpoint.ID + "/rotate-secret"}, {"POST", base + "/" + ep.Endpoint.ID + "/test"}, {"GET", base + "/" + ep.Endpoint.ID + "/deliveries"},
	} {
		st, _, body := h.tokenCall(admin, c.method, c.path, map[string]any{})
		if st != 403 || !bytes.Contains(body, []byte("TOKEN_NOT_ALLOWED")) {
			t.Errorf("%s %s with an API token: %d %s", c.method, c.path, st, body)
		}
	}
	// the secret is in the creation response only
	if !strings.HasPrefix(ep.Secret, "whsec_") || len(ep.Secret) < 40 {
		t.Fatalf("secret %q", ep.Secret)
	}
	for _, path := range []string{base, base + "/" + ep.Endpoint.ID + "/deliveries"} {
		if b := h.expect(200, w.a, "GET", path, nil); bytes.Contains(b, []byte(ep.Secret)) || bytes.Contains(b, []byte(strings.TrimPrefix(ep.Secret, "whsec_"))) {
			t.Errorf("GET %s contains the secret", path)
		}
	}
	if !strings.HasSuffix(ep.Secret, strings.TrimPrefix(ep.Endpoint.SecretHint, "…")) {
		t.Errorf("hint %q does not match", ep.Endpoint.SecretHint)
	}
	// stored sealed, never plain
	var enc []byte
	_ = h.pool.QueryRow(context.Background(), `SELECT secret_enc FROM webhook_endpoints WHERE id = $1::uuid`, ep.Endpoint.ID).Scan(&enc)
	if len(enc) < 32 || bytes.Contains(enc, []byte(ep.Secret)) || bytes.Contains(enc, []byte(strings.TrimPrefix(ep.Secret, "whsec_"))) {
		t.Errorf("secret_enc is not ciphertext")
	}
	var anywhere int
	_ = h.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM audit_logs WHERE diff_after::text LIKE '%'||$1||'%' OR diff_before::text LIKE '%'||$1||'%') + (SELECT count(*) FROM outbox WHERE payload::text LIKE '%'||$1||'%' OR last_error LIKE '%'||$1||'%')`, strings.TrimPrefix(ep.Secret, "whsec_")).Scan(&anywhere)
	if anywhere != 0 {
		t.Errorf("the secret appears in %d audit/outbox rows", anywhere)
	}
}

// mintToken creates an API token for A and returns its plaintext.
func (w *hookWorld) mintToken(t *testing.T, name, role string) string {
	t.Helper()
	b := w.h.expect(201, w.a, "POST", "/workspaces/"+w.wsA.ID+"/api-tokens", map[string]any{"name": name, "role": role})
	return decode[struct{ Plaintext string }](t, b).Plaintext
}

func TestEndpointQuotaAndValidation(t *testing.T) {
	w := newHookWorld(t)
	h := w.h
	base := "/workspaces/" + w.wsA.ID + "/webhooks"
	for _, bad := range []map[string]any{
		{"url": w.recv.url(), "eventTypes": []string{}},
		{"url": w.recv.url(), "eventTypes": []string{"flag.exploded"}},
		{"url": w.recv.url(), "eventTypes": []string{"webhook.ping"}},
		{"url": "", "eventTypes": []string{"flag.updated"}},
		{"url": "ftp://example.com/x", "eventTypes": []string{"flag.updated"}},
		{"url": w.recv.url(), "eventTypes": []string{"flag.updated"}, "description": strings.Repeat("x", 201)},
		{"url": w.recv.url(), "eventTypes": []string{"flag.updated"}, "description": "a\nb"},
		{"url": w.recv.url(), "eventTypes": []string{"flag.updated"}, "environmentId": w.devB},
		{"url": w.recv.url(), "eventTypes": []string{"flag.updated"}, "environmentId": "not-a-uuid"},
		{"url": w.recv.url(), "eventTypes": []string{"flag.updated"}, "surprise": true},
	} {
		if st, b := h.call(w.a, "POST", base, bad); st != 400 {
			t.Errorf("%v: %d %s, want 400", bad, st, b)
		}
	}
	if n := h.count(`SELECT count(*) FROM webhook_endpoints`); n != 0 {
		t.Fatalf("%d endpoints exist after refusals", n)
	}
	for i := 0; i < 10; i++ {
		w.create(t, nil)
	}
	st, b := h.call(w.a, "POST", base, map[string]any{"url": w.recv.url(), "eventTypes": []string{"flag.updated"}})
	if st != 409 || !bytes.Contains(b, []byte("QUOTA_EXCEEDED")) {
		t.Errorf("the 11th endpoint: %d %s", st, b)
	}
	// B has its own allowance
	h.expect(201, w.b, "POST", "/workspaces/"+w.wsB.ID+"/webhooks", map[string]any{"url": w.recv.url(), "eventTypes": []string{"flag.updated"}})
}

func TestWithoutAKeyWebhooksFailClosed(t *testing.T) {
	x := newTenantsWith(t, func(d *server.Deps) {
		d.Delivery.Box = nil // server.New substitutes a box with no key
		d.Delivery.Guard = netguard.New(netguard.Policy{AllowLocalHTTP: true})
	})
	base := "/workspaces/" + x.wsA.ID + "/webhooks"
	st, b := x.h.call(x.a, "POST", base, map[string]any{"url": "http://127.0.0.1:9/h", "eventTypes": []string{"flag.updated"}})
	if st != 503 || !bytes.Contains(b, []byte("WEBHOOKS_UNAVAILABLE")) {
		t.Errorf("create without a key: %d %s", st, b)
	}
	if n := x.h.count(`SELECT count(*) FROM webhook_endpoints`); n != 0 {
		t.Errorf("an endpoint was stored without a key")
	}
	list := decode[struct{ Available bool }](t, x.h.expect(200, x.a, "GET", base, nil))
	if list.Available {
		t.Error("the list says webhooks are available")
	}
}

func TestAnEndpointOnlyReceivesItsOwnWorkspacesEvents(t *testing.T) {
	w := newHookWorld(t)
	recvB := newReceiver(t)
	w.create(t, map[string]any{"eventTypes": []string{"flag.created", "flag.updated"}})
	w.h.expect(201, w.b, "POST", "/workspaces/"+w.wsB.ID+"/webhooks", map[string]any{"url": recvB.url(), "eventTypes": []string{"flag.created", "flag.updated"}})
	w.h.expect(201, w.b, "POST", "/environments/"+w.devB+"/flags", boolFlag("b-private-flag"))
	w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": false})
	w.run(t)
	for _, r := range w.recv.requests() {
		if bytes.Contains(r.Body, []byte("b-private-flag")) || bytes.Contains(r.Body, []byte(w.wsB.ID)) {
			t.Errorf("A's endpoint received B's event: %s", r.Body)
		}
	}
	for _, r := range recvB.requests() {
		if bytes.Contains(r.Body, []byte(secretFlag)) || bytes.Contains(r.Body, []byte(w.wsA.ID)) {
			t.Errorf("B's endpoint received A's event: %s", r.Body)
		}
	}
	if len(w.recv.requests()) != 1 || len(recvB.requests()) != 1 {
		t.Errorf("deliveries: A %d, B %d, want 1 each", len(w.recv.requests()), len(recvB.requests()))
	}
	// B cannot read or change A's endpoint
	var id string
	_ = w.h.pool.QueryRow(context.Background(), `SELECT id::text FROM webhook_endpoints WHERE workspace_id = $1::uuid`, w.wsA.ID).Scan(&id)
	for _, c := range []struct{ method, path string }{
		{"PATCH", "/workspaces/" + w.wsB.ID + "/webhooks/" + id}, {"DELETE", "/workspaces/" + w.wsB.ID + "/webhooks/" + id},
		{"POST", "/workspaces/" + w.wsB.ID + "/webhooks/" + id + "/rotate-secret"}, {"GET", "/workspaces/" + w.wsB.ID + "/webhooks/" + id + "/deliveries"},
		{"POST", "/workspaces/" + w.wsB.ID + "/webhooks/" + id + "/test"},
	} {
		if st, b := w.h.call(w.b, c.method, c.path, map[string]any{}); st != 404 || !bytes.Contains(b, []byte("WEBHOOK_NOT_FOUND")) {
			t.Errorf("%s %s as B: %d %s", c.method, c.path, st, b)
		}
	}
	if len(recvB.requests()) != 1 || w.h.count(`SELECT count(*) FROM webhook_endpoints WHERE workspace_id = $1::uuid`, w.wsA.ID) != 1 {
		t.Error("B changed A's endpoint")
	}
}

func TestEventTypeAndEnvironmentFiltersApply(t *testing.T) {
	w := newHookWorld(t)
	staging := envID(t, w.wsA, "staging")
	w.create(t, map[string]any{"eventTypes": []string{"flag.updated"}, "environmentId": staging})
	killOnly := newReceiver(t)
	w.h.expect(201, w.a, "POST", "/workspaces/"+w.wsA.ID+"/webhooks", map[string]any{"url": killOnly.url(), "eventTypes": []string{"flag.killed"}})
	w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": false}) // dev: filtered out of the first
	w.h.expect(200, w.a, "PATCH", "/environments/"+staging+"/flags/"+secretFlag, map[string]any{"enabled": false})
	w.h.expect(200, w.a, "POST", "/environments/"+w.devA+"/flags/"+secretFlag+"/kill", nil)
	w.run(t)
	if got := w.recv.requests(); len(got) != 1 || !strings.Contains(string(got[0].Body), `"staging"`) {
		t.Errorf("the staging-only endpoint got %d request(s)", len(got))
	}
	if got := killOnly.requests(); len(got) != 1 || got[0].Header.Get(webhooks.HeaderEventType) != "flag.killed" {
		t.Errorf("the kill-only endpoint got %d request(s)", len(got))
	}
}

func TestSecretRotationSendsBothSignaturesDuringTheGrace(t *testing.T) {
	w := newHookWorld(t)
	ep := w.create(t, map[string]any{"eventTypes": []string{"flag.updated"}})
	rot := decode[endpointOut](t, w.h.expect(200, w.a, "POST", "/workspaces/"+w.wsA.ID+"/webhooks/"+ep.Endpoint.ID+"/rotate-secret", nil))
	if rot.Secret == ep.Secret || rot.Endpoint.PreviousSecretExpiresAt == nil {
		t.Fatalf("rotation: %+v", rot)
	}
	patch := func() {
		w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": false})
	}
	patch()
	w.run(t)
	r := w.recv.requests()[0]
	if n := strings.Count(r.Header.Get(webhooks.HeaderSignature), "v1="); n != 2 {
		t.Fatalf("signature header during the grace: %q", r.Header.Get(webhooks.HeaderSignature))
	}
	for name, secret := range map[string]string{"old": ep.Secret, "new": rot.Secret} {
		if err := webhooks.Verify(secret, r.Header.Get(webhooks.HeaderSignature), r.Header.Get(webhooks.HeaderTimestamp), r.Header.Get(webhooks.HeaderDeliveryID), r.Body, time.Now(), webhooks.Tolerance); err != nil {
			t.Errorf("the %s secret does not verify: %v", name, err)
		}
	}
	// after the grace only the new secret signs
	w.clock.advance(25 * time.Hour)
	patch()
	w.run(t)
	r = w.recv.requests()[1]
	if n := strings.Count(r.Header.Get(webhooks.HeaderSignature), "v1="); n != 1 {
		t.Fatalf("signature header after the grace: %q", r.Header.Get(webhooks.HeaderSignature))
	}
	now := w.clock.now()
	if err := webhooks.Verify(rot.Secret, r.Header.Get(webhooks.HeaderSignature), r.Header.Get(webhooks.HeaderTimestamp), r.Header.Get(webhooks.HeaderDeliveryID), r.Body, now, webhooks.Tolerance); err != nil {
		t.Errorf("new secret: %v", err)
	}
	if err := webhooks.Verify(ep.Secret, r.Header.Get(webhooks.HeaderSignature), r.Header.Get(webhooks.HeaderTimestamp), r.Header.Get(webhooks.HeaderDeliveryID), r.Body, now, webhooks.Tolerance); err == nil {
		t.Error("the old secret still verifies after the grace")
	}
}

func TestATestPingAndAResendAreSentAtOnceWithTheSameDeliveryID(t *testing.T) {
	w := newHookWorld(t)
	ep := w.create(t, nil)
	base := "/workspaces/" + w.wsA.ID + "/webhooks/" + ep.Endpoint.ID
	ping := decode[struct{ ID, EventType, Status string }](t, w.h.expect(200, w.a, "POST", base+"/test", nil))
	if ping.EventType != "webhook.ping" || ping.Status != "delivered" || len(w.recv.requests()) != 1 {
		t.Fatalf("ping %+v, %d requests", ping, len(w.recv.requests()))
	}
	// a failed real delivery, then a resend once the receiver is fixed
	w.recv.status.Store(503)
	w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": false})
	w.run(t)
	list := decode[struct {
		Deliveries []struct{ ID, Status, EventType string }
	}](t, w.h.expect(200, w.a, "GET", base+"/deliveries?status=failed", nil))
	if len(list.Deliveries) != 1 {
		t.Fatalf("failed deliveries: %+v", list)
	}
	failed := list.Deliveries[0]
	w.recv.status.Store(200)
	res := decode[struct{ ID, Status string }](t, w.h.expect(200, w.a, "POST", base+"/deliveries/"+failed.ID+"/resend", nil))
	if res.ID != failed.ID || res.Status != "delivered" {
		t.Errorf("resend: %+v", res)
	}
	got := w.recv.requests()
	if last := got[len(got)-1]; last.Header.Get(webhooks.HeaderDeliveryID) != failed.ID {
		t.Errorf("the resend carried delivery id %s, want %s", last.Header.Get(webhooks.HeaderDeliveryID), failed.ID)
	}
	// a delivered one cannot be resent
	if st, b := w.h.call(w.a, "POST", base+"/deliveries/"+failed.ID+"/resend", nil); st != 409 || !bytes.Contains(b, []byte("NOT_RESENDABLE")) {
		t.Errorf("resending a delivered one: %d %s", st, b)
	}
	// a made-up or another endpoint's delivery id is simply not found
	if st, _ := w.h.call(w.a, "POST", base+"/deliveries/11111111-1111-4111-8111-111111111111/resend", nil); st != 404 {
		t.Errorf("unknown delivery: %d", st)
	}
}

func TestPingsAndResendsAreRateLimited(t *testing.T) {
	x := newTenantsWith(t, func(d *server.Deps) {
		d.Delivery.Guard = netguard.New(netguard.Policy{AllowLocalHTTP: true})
		d.Limiter = ratelimit.New(ratelimit.Config{Rules: map[string]ratelimit.Rule{ratelimit.ClassWebhookTest: {PerSecond: 0.001, Burst: 2}}})
	})
	recv := newReceiver(t)
	ep := decode[endpointOut](t, x.h.expect(201, x.a, "POST", "/workspaces/"+x.wsA.ID+"/webhooks", map[string]any{"url": recv.url(), "eventTypes": []string{"flag.updated"}}))
	path := "/workspaces/" + x.wsA.ID + "/webhooks/" + ep.Endpoint.ID + "/test"
	x.h.expect(200, x.a, "POST", path, nil)
	x.h.expect(200, x.a, "POST", path, nil)
	st, hdr, b := x.h.callHdr(x.a, "POST", path, nil)
	if st != 429 || hdr.Get("Retry-After") == "" || !bytes.Contains(b, []byte("RATE_LIMITED")) {
		t.Errorf("third ping: %d %s", st, b)
	}
	if len(recv.requests()) != 2 {
		t.Errorf("%d requests reached the receiver", len(recv.requests()))
	}
}

func TestAnEndpointThatKeepsFailingIsDisabledAndAuditedAndCanBeReEnabled(t *testing.T) {
	w := newHookWorld(t)
	ep := w.create(t, map[string]any{"eventTypes": []string{"flag.updated"}})
	w.recv.status.Store(500)
	for i := 0; i < webhooks.AutoDisableAfterFailures+1; i++ {
		w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": i%2 == 0})
	}
	// each pass makes one attempt at every due row; enough passes to fail 50 times
	for i := 0; i < 60; i++ {
		w.run(t)
		w.clock.advance(13 * time.Hour)
		if w.h.count(`SELECT count(*) FROM webhook_endpoints WHERE enabled`) == 0 {
			break
		}
	}
	list := decode[struct {
		Endpoints []struct {
			Enabled bool
			Status  string
			Health  struct {
				ConsecutiveFailures int
				DisabledReason      *string
			}
		}
	}](t, w.h.expect(200, w.a, "GET", "/workspaces/"+w.wsA.ID+"/webhooks", nil))
	e := list.Endpoints[0]
	if e.Enabled || e.Status != "disabled" || e.Health.DisabledReason == nil || e.Health.ConsecutiveFailures < webhooks.AutoDisableAfterFailures {
		t.Fatalf("endpoint = %+v", e)
	}
	if n := w.h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'webhook_endpoint.auto_disable'`, w.wsA.ID); n != 1 {
		t.Errorf("auto-disable audit entries: %d", n)
	}
	// a disabled endpoint gets nothing new queued
	before := w.h.count(`SELECT count(*) FROM outbox`)
	w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": false})
	if after := w.h.count(`SELECT count(*) FROM outbox`); after != before {
		t.Errorf("an event was queued for a disabled endpoint")
	}
	// re-enabling gives it a fresh start
	w.recv.status.Store(200)
	var re2 struct {
		Enabled bool
		Health  struct {
			ConsecutiveFailures int
			DisabledReason      *string
		}
	}
	re2 = decode[struct {
		Enabled bool
		Health  struct {
			ConsecutiveFailures int
			DisabledReason      *string
		}
	}](t, w.h.expect(200, w.a, "PATCH", "/workspaces/"+w.wsA.ID+"/webhooks/"+ep.Endpoint.ID, map[string]any{"enabled": true}))
	if !re2.Enabled || re2.Health.ConsecutiveFailures != 0 || re2.Health.DisabledReason != nil {
		t.Errorf("after re-enabling: %+v", re2)
	}
}

// SSRF at delivery time: the endpoint was valid when created; the name later points inside.
func TestRebindingBetweenCreationAndDeliveryIsRefusedWithNoConnection(t *testing.T) {
	x := newTenants(t) // the strict guard with the stub resolver (a public address for any name)
	h := x.h
	conn := newReceiver(t) // a server on loopback that must never be contacted
	ep := decode[endpointOut](t, h.expect(201, x.a, "POST", "/workspaces/"+x.wsA.ID+"/webhooks",
		map[string]any{"url": "https://hooks.customer.example:8443/in", "eventTypes": []string{"flag.updated"}}))
	h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/"+secretFlag, map[string]any{"enabled": false})
	// DNS now answers with a loopback address, and then the metadata address
	for _, ip := range []string{"127.0.0.1", "169.254.169.254", "10.0.0.7", "::ffff:192.168.0.9"} {
		h.resolver.set("hooks.customer.example", ip)
		clock := &liveClock{}
		clock.advance(48 * time.Hour)
		st, err := h.deliveryWorker(clock.now, outbox.Config{}).Run(context.Background())
		if err != nil || st.Delivered != 0 {
			t.Fatalf("%s: %+v %v", ip, st, err)
		}
		msg := h.dbStringOne(`SELECT last_error FROM outbox WHERE endpoint_id = $1::uuid ORDER BY created_at DESC LIMIT 1`, ep.Endpoint.ID)
		if !strings.Contains(msg, "blocked") {
			t.Errorf("%s: last_error = %q", ip, msg)
		}
	}
	if len(conn.requests()) != 0 {
		t.Errorf("the receiver was contacted")
	}
}

func (h *harness) dbStringOne(query string, args ...any) string {
	var s *string
	if err := h.pool.QueryRow(context.Background(), query, args...).Scan(&s); err != nil {
		h.t.Fatal(err)
	}
	if s == nil {
		return ""
	}
	return *s
}

func TestHostileEndpointURLsAreRefusedAtCreationAndNothingIsStored(t *testing.T) {
	x := newTenants(t)
	h := x.h
	for _, u := range []string{
		"http://example.com/h", "https://127.0.0.1/h", "https://localhost/h", "https://169.254.169.254/latest/meta-data/", "https://10.0.0.1/h",
		"https://[::1]/h", "https://[::ffff:127.0.0.1]/h", "https://2130706433/h", "https://0x7f000001/h", "https://user:pw@example.com/h",
		"https://example.com:22/h", "https://db.internal/h", "file:///etc/passwd", "https://example.com/" + strings.Repeat("a", 2100),
	} {
		st, b := h.call(x.a, "POST", "/workspaces/"+x.wsA.ID+"/webhooks", map[string]any{"url": u, "eventTypes": []string{"flag.updated"}})
		if st != 400 {
			t.Errorf("%.60s: %d %s, want 400", u, st, b)
		}
		if bytes.Contains(b, []byte("pw@")) {
			t.Errorf("the refusal echoed the credentials: %s", b)
		}
	}
	// a name that resolves inside is refused too
	h.resolver.set("rebind.example.com", "127.0.0.1")
	if st, _ := h.call(x.a, "POST", "/workspaces/"+x.wsA.ID+"/webhooks", map[string]any{"url": "https://rebind.example.com/h", "eventTypes": []string{"flag.updated"}}); st != 400 {
		t.Errorf("a hostname resolving to loopback: %d", st)
	}
	if n := h.count(`SELECT count(*) FROM webhook_endpoints`); n != 0 {
		t.Errorf("%d endpoints stored", n)
	}
	// changing an existing endpoint's URL is validated the same way
	good := decode[endpointOut](t, h.expect(201, x.a, "POST", "/workspaces/"+x.wsA.ID+"/webhooks", map[string]any{"url": "https://ok.example.com/h", "eventTypes": []string{"flag.updated"}}))
	if st, _ := h.call(x.a, "PATCH", "/workspaces/"+x.wsA.ID+"/webhooks/"+good.Endpoint.ID, map[string]any{"url": "http://169.254.169.254/"}); st != 400 {
		t.Errorf("patching the URL to the metadata address: %d", st)
	}
	if u := h.dbStringOne(`SELECT url FROM webhook_endpoints WHERE id = $1::uuid`, good.Endpoint.ID); u != "https://ok.example.com/h" {
		t.Errorf("url is now %q", u)
	}
}

func TestNeitherTheSecretNorTheURLReachesTheLogs(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard)
	w := newHookWorld(t)
	// a path with a token in it, the sort of URL people really use
	ep := w.create(t, map[string]any{"url": w.recv.srv.URL + "/hooks/TOP-SECRET-PATH-TOKEN-123", "eventTypes": []string{"flag.updated"}})
	w.h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": false})
	w.recv.status.Store(500)
	w.run(t)
	w.recv.srv.Close() // connection refused on the next attempt
	w.clock.advance(time.Hour)
	w.run(t)
	out := buf.String()
	for _, s := range []string{ep.Secret, strings.TrimPrefix(ep.Secret, "whsec_"), "TOP-SECRET-PATH-TOKEN-123", "Helios-Signature"} {
		if strings.Contains(out, s) {
			t.Errorf("the logs contain %q", s)
		}
	}
	// nor the stored errors
	for _, e := range []string{w.dbString(`SELECT string_agg(coalesce(last_error,'') || coalesce(last_response_hint,''), ' ') FROM outbox`)} {
		if strings.Contains(e, "TOP-SECRET-PATH-TOKEN-123") || strings.Contains(e, ep.Secret) {
			t.Errorf("a stored error holds the secret or the URL path: %q", e)
		}
	}
}

// Every documented event is emitted at its choke point, in the documented shape, with nothing secret in it.
func TestEveryEventIsEmittedWithTheDocumentedShapeAndNothingSecret(t *testing.T) {
	w := newHookWorld(t)
	h := w.h
	staging := envID(t, w.wsA, "staging")
	apiTok := w.mintToken(t, "ci", "editor")
	w.create(t, nil) // every event type
	const secretRule = "SUPER-SECRET-RULE-VALUE"
	dev := "/environments/" + w.devA
	// flag.created
	h.expect(201, w.a, "POST", dev+"/flags", boolFlag("hooked"))
	// flag.updated, with targeting rules whose contents must not travel; made with an API token (actor = the token)
	if st, _, b := h.tokenCall(apiTok, "PATCH", dev+"/flags/hooked", map[string]any{
		"enabled": true, "rollout": map[string]any{"on": 25000, "off": 75000},
		"targetingRules": []map[string]any{{"clauses": []map[string]any{clause("plan", "equals", secretRule)}, "variationId": "on"}}}); st != 200 {
		t.Fatalf("patch with a token: %d %s", st, b)
	}
	// experiment.started / stopped
	h.expect(201, w.a, "POST", dev+"/experiments", map[string]any{"flagKey": "hooked", "key": "hook-exp", "name": "Hook experiment",
		"metrics": []map[string]any{{"name": "M", "eventName": "e", "type": "conversion", "isPrimary": true}}})
	h.expect(200, w.a, "POST", dev+"/experiments/hook-exp/start", nil)
	h.expect(200, w.a, "POST", dev+"/experiments/hook-exp/stop", nil)
	// promotion.applied
	pre := decode[promoPreview](t, h.expect(200, w.a, "POST", promoPath(staging, "hooked", "/preview"), map[string]any{"sourceEnvironment": "dev"}))
	h.expect(200, w.a, "POST", promoPath(staging, "hooked", ""), map[string]any{"sourceEnvironment": "dev", "expectedVersion": pre.ExpectedVersion, "sourceVersion": pre.SourceVersion})
	// scheduled_change.executed (and the flag.updated it makes) and scheduled_change.failed (a patch that conflicts)
	sw := &schedWorld{tenants: w.tenants, clock: &fakeClock{t: time.Now().UTC()}, staging: staging}
	ok := sw.scheduleOK(t, w.a, w.devA, "hooked", "ok", "disable", 5*time.Minute, nil)
	conflict := sw.scheduleOK(t, w.a, w.devA, "hooked", "bad", "patch", 6*time.Minute, map[string]any{"fallthroughVariationId": "off"})
	_ = conflict
	h.expect(200, w.a, "PATCH", dev+"/flags/hooked", map[string]any{"fallthroughVariationId": "off"}) // the conflict
	sw.clock.advance(10 * time.Minute)
	if _, err := scheduling.NewRunner(h.pool, h.bus, sw.clock.now, scheduling.Config{WorkerID: "ev"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = ok
	_ = conflict
	// flag.killed, flag.deleted
	h.expect(200, w.a, "POST", dev+"/flags/hooked/kill", nil)
	h.expect(204, w.a, "DELETE", dev+"/flags/hooked?force=true", nil)

	rows, err := h.pool.Query(context.Background(), `SELECT event_type, payload::text FROM outbox WHERE workspace_id = $1::uuid ORDER BY created_at`, w.wsA.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string][]string{}
	for rows.Next() {
		var typ, payload string
		if err := rows.Scan(&typ, &payload); err != nil {
			t.Fatal(err)
		}
		seen[typ] = append(seen[typ], payload)
	}
	for _, typ := range webhooks.Types {
		if len(seen[typ]) == 0 {
			t.Errorf("event %s was never emitted", typ)
		}
	}
	forbidden := []string{secretRule, w.a.email, "alice@", w.sdkKeyA, apiTok, "hlt_", "clauses", "attribute", "diff_after", "password", "secret", "tokenId", "token_"}
	for typ, payloads := range seen {
		for _, p := range payloads {
			if len(p) > webhooks.MaxEventBytes {
				t.Errorf("%s: %d bytes", typ, len(p))
			}
			for _, f := range forbidden {
				if strings.Contains(strings.ToLower(p), strings.ToLower(f)) {
					t.Errorf("%s payload contains %q: %s", typ, f, p)
				}
			}
			var env struct {
				ID, Type, CreatedAt, WorkspaceID string
				Version                          int
				Actor                            struct{ Type, ID, Name, Via string }
				Environment                      struct{ ID, Key string }
				Data                             map[string]any
			}
			if err := json.Unmarshal([]byte(p), &env); err != nil {
				t.Fatalf("%s: %v", typ, err)
			}
			if env.ID == "" || env.Type != typ || env.Version != 1 || env.CreatedAt == "" || env.WorkspaceID != w.wsA.ID || env.Environment.Key == "" || env.Data == nil || env.Actor.Type == "" || env.Actor.ID == "" {
				t.Errorf("%s: incomplete envelope %s", typ, p)
			}
		}
	}
	// the update made with an API token names the token as the actor, never the person
	var tokActor int
	for _, p := range seen["flag.updated"] {
		if strings.Contains(p, `"api_token"`) && strings.Contains(p, `"ci"`) && !strings.Contains(p, w.a.id) {
			tokActor++
		}
	}
	if tokActor != 1 {
		t.Errorf("flag.updated events attributed to the token: %d, want 1", tokActor)
	}
	// compact changes: enabled and rollout are there, rules only as a count
	for _, p := range seen["flag.updated"] {
		if strings.Contains(p, `"api_token"`) {
			for _, want := range []string{`"enabled"`, `"rollout"`, `"ruleCount"`, `"version"`} {
				if !strings.Contains(p, want) {
					t.Errorf("flag.updated lacks %s: %s", want, p)
				}
			}
		}
	}
	// a promotion is one event, promotion.applied, not also flag.updated for the target
	if len(seen["promotion.applied"]) != 1 {
		t.Errorf("promotion.applied events: %d", len(seen["promotion.applied"]))
	}
}
