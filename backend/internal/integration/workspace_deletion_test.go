package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/controlplane/scheduling"
	"helios/backend/internal/controlplane/workspaces"
	"helios/backend/internal/controlplane/wspurge"
	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/server"
)

type delWorld struct {
	*tenants
	clock *liveClock
}

func newDelWorld(t *testing.T) *delWorld {
	t.Helper()
	clock := &liveClock{}
	x := newTenantsWith(t, func(d *server.Deps) {
		d.Clock = clock.now
		d.SDKKeyCacheTTL = time.Hour // only a deliberate drop can end a key's life
		d.StreamRecheck = time.Hour
		d.Limiter = ratelimit.New(ratelimit.Config{Rules: map[string]ratelimit.Rule{
			ratelimit.ClassInvite: {PerSecond: 1000, Burst: 1000}, ratelimit.ClassAccept: {PerSecond: 1000, Burst: 1000},
			ratelimit.ClassWorkspace: {PerSecond: 1000, Burst: 1000}, ratelimit.ClassMe: {PerSecond: 1000, Burst: 1000},
			ratelimit.ClassTokenWrite: {PerSecond: 1000, Burst: 1000}, ratelimit.ClassWebhookWrite: {PerSecond: 1000, Burst: 1000},
			ratelimit.ClassSegmentWrite: {PerSecond: 1000, Burst: 1000}, ratelimit.ClassScheduleWrite: {PerSecond: 1000, Burst: 1000},
		}, StreamsPerKey: 100})
	})
	return &delWorld{tenants: x, clock: clock}
}

func (w *delWorld) join(t *testing.T, name, role string) user {
	t.Helper()
	return (&ownWorld{tenants: w.tenants, clock: w.clock}).join(t, w.a, w.wsA.ID, name, role)
}

func (w *delWorld) schedule(t *testing.T, want int) []byte {
	t.Helper()
	return w.h.expect(want, w.a, "POST", "/workspaces/"+w.wsA.ID+"/schedule-deletion", map[string]any{"confirmName": w.wsA.Name})
}

// graceOver pretends the 14 days have passed (the purge and the audit trigger use the database clock).
func (w *delWorld) graceOver(t *testing.T) {
	t.Helper()
	if err := w.h.rawExec(`UPDATE workspaces SET purge_after = now() - interval '1 minute' WHERE id = $1::uuid`, w.wsA.ID); err != nil {
		t.Fatal(err)
	}
}

func (w *delWorld) purger(cfg wspurge.Config) *wspurge.Purger {
	cfg.Pub = w.h.bus
	return wspurge.New(w.h.pool, cfg)
}

// ---- soft delete: invisible everywhere -------------------------------------------------------------

func TestOnlyAnOwnerSchedulesDeletionAfterTypingTheName(t *testing.T) {
	w := newDelWorld(t)
	ws := w.wsA.ID
	adm := w.join(t, "adm", "admin")
	url := "/workspaces/" + ws + "/schedule-deletion"
	if st, _ := w.h.call(adm, "POST", url, map[string]any{"confirmName": w.wsA.Name}); st != 403 {
		t.Errorf("an admin deleting: %d", st)
	}
	for name, body := range map[string]map[string]any{"no name": {}, "wrong": {"confirmName": "nope"}, "wrong case": {"confirmName": strings.ToUpper(w.wsA.Name)}, "empty": {"confirmName": ""}} {
		if st, b := w.h.call(w.a, "POST", url, body); st != 400 || !bytes.Contains(b, []byte("NAME_MISMATCH")) {
			t.Errorf("%s: %d %s", name, st, b)
		}
	}
	if w.h.dbString2(`SELECT coalesce(deleted_at::text, '') FROM workspaces WHERE id = $1::uuid`, ws) != "" {
		t.Fatal("refused requests scheduled a deletion")
	}
	tok := decode[struct{ Plaintext string }](t, w.h.expect(201, w.a, "POST", "/workspaces/"+ws+"/api-tokens", map[string]any{"name": "ci", "role": "admin"})).Plaintext
	if st, _, b := w.h.tokenCall(tok, "POST", url, map[string]any{"confirmName": w.wsA.Name}); st != 403 || !bytes.Contains(b, []byte("TOKEN_NOT_ALLOWED")) {
		t.Errorf("a token deleting: %d %s", st, b)
	}
	w.schedule(t, 200)
	if got := w.h.dbString2(`SELECT purge_after::date - deleted_at::date FROM workspaces WHERE id = $1::uuid`, ws); got != "14" {
		t.Errorf("grace period: %s days", got)
	}
	// a second request: the workspace is already gone
	if st, _ := w.h.call(w.a, "POST", url, map[string]any{"confirmName": w.wsA.Name}); st != 404 {
		t.Errorf("deleting twice: %d", st)
	}
	for _, action := range []string{"workspace.delete_scheduled"} {
		if n := w.h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = $2`, ws, action); n != 1 {
			t.Errorf("%s audit entries: %d", action, n)
		}
		if n := w.h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = $2 AND resource_id = $3`, workspaces.PlatformWorkspaceID, action, ws); n != 1 {
			t.Errorf("platform %s entries: %d", action, n)
		}
	}
}

// Every route that resolves a workspace answers 404 once it is scheduled for deletion: walked from the router's own
// registry, as a member and as an API token, and compared with the same walk before, so the test cannot pass vacuously.
func TestEveryWorkspaceRouteAnswersNotFoundOnceTheWorkspaceIsScheduledForDeletion(t *testing.T) {
	w := newDelWorld(t)
	h := w.h
	ws := w.wsA.ID
	tok := decode[struct{ Plaintext string }](t, h.expect(201, w.a, "POST", "/workspaces/"+ws+"/api-tokens", map[string]any{"name": "ci", "role": "admin"})).Plaintext
	member := w.join(t, "mem", "viewer")
	fill := func(p string) string {
		return strings.NewReplacer("{envId}", w.devA, "{wsId}", ws, "{key}", "k", "{userId}", w.a.id, "{inviteId}", "11111111-1111-4111-8111-111111111111",
			"{keyId}", "11111111-1111-4111-8111-111111111111", "{segKey}", "s", "{changeId}", "11111111-1111-4111-8111-111111111111",
			"{tokenId}", "11111111-1111-4111-8111-111111111111", "{endpointId}", "11111111-1111-4111-8111-111111111111",
			"{deliveryId}", "11111111-1111-4111-8111-111111111111", "{offerId}", "11111111-1111-4111-8111-111111111111").Replace(p)
	}
	type probe struct {
		label, method, path, want string
		token                     bool
	}
	var probes []probe
	for _, rt := range h.routes {
		var want string
		switch rt.Scope {
		case server.ScopeEnv:
			want = "ENVIRONMENT_NOT_FOUND"
		case server.ScopeWorkspace:
			want = "WORKSPACE_NOT_FOUND"
		default:
			continue
		}
		probes = append(probes, probe{rt.Method + " " + rt.Path, rt.Method, fill(rt.Path), want, rt.TokenAllowed})
	}
	if len(probes) < 40 {
		t.Fatalf("only %d workspace/environment routes: the registry is not being walked", len(probes))
	}
	code := func(b []byte) string {
		var e struct{ Code string }
		_ = json.Unmarshal(b, &e)
		return e.Code
	}
	call := func(p probe, asToken bool, u user) string {
		var st int
		var b []byte
		if asToken {
			st, _, b = h.tokenCall(tok, p.method, p.path, map[string]any{})
		} else {
			st, b = h.call(u, p.method, p.path, map[string]any{})
		}
		_ = st
		return code(b)
	}
	// BEFORE: the workspace resolves, so no probe is turned away as "not found" by the resolver.
	for _, p := range probes {
		if got := call(p, false, w.a); got == p.want {
			t.Errorf("before deletion, %s was already refused as %s", p.label, got)
		}
	}
	w.schedule(t, 200)
	for _, p := range probes {
		for name, u := range map[string]user{"the owner": w.a, "a member": member} {
			if got := call(p, false, u); got != p.want {
				t.Errorf("%s as %s after deletion: code %q, want %s", p.label, name, got, p.want)
			}
		}
		if p.token {
			if got := call(p, true, w.a); got != p.want {
				t.Errorf("%s with an API token after deletion: code %q, want %s", p.label, got, p.want)
			}
		}
	}
	t.Logf("walked %d routes", len(probes))
}

func TestWhatALiveWorkspaceDoesStopsWhileItIsScheduledForDeletion(t *testing.T) {
	w := newDelWorld(t)
	h := w.h
	ws := w.wsA.ID
	// an SDK key that works, an open stream, a scheduled change, a webhook delivery, an invite
	if h.evaluateStatus(w.sdkKeyA, secretFlag) != 200 {
		t.Fatal("setup: the key does not work")
	}
	closed, _ := openStreamAt(t, h.srv.URL, w.sdkKeyA)
	h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags", boolFlag("sched"))
	h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags/sched/scheduled-changes", map[string]any{
		"idempotencyKey": "k", "kind": "enable", "runAt": w.clock.now().Add(5 * time.Minute).Format(time.RFC3339)})
	h.expect(201, w.a, "POST", "/workspaces/"+ws+"/webhooks", map[string]any{"url": "https://hooks.example.com/in", "eventTypes": []string{"flag.updated"}})
	h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": false})
	inviteTok := decode[struct{ Token string }](t, h.expect(201, w.a, "POST", "/workspaces/"+ws+"/invites", map[string]any{"email": w.b.email, "role": "viewer"})).Token
	if h.count(`SELECT count(*) FROM outbox WHERE workspace_id = $1::uuid AND status = 'pending'`, ws) < 2 {
		t.Fatal("setup: nothing queued")
	}

	w.schedule(t, 200)

	// SDK key: rejected at once on this instance, stream closed
	if st := h.evaluateStatus(w.sdkKeyA, secretFlag); st != 401 {
		t.Errorf("the SDK key still evaluates: %d", st)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Error("the stream is still open")
	}
	// a new stream is refused
	req, _ := http.NewRequest("GET", h.srv.URL+"/sdk/stream", nil)
	req.Header.Set("X-Helios-SDK-Key", w.sdkKeyA)
	if res, err := http.DefaultClient.Do(req); err == nil {
		res.Body.Close()
		if res.StatusCode != 401 {
			t.Errorf("a new stream: %d", res.StatusCode)
		}
	}
	// the scheduler does not run its changes, and does not mark them missed either
	w.clock.advance(10 * time.Minute)
	st, err := scheduling.NewRunner(h.pool, h.bus, w.clock.now, scheduling.Config{WorkerID: "del"}).Run(context.Background())
	if err != nil || st.Claimed != 0 || st.Missed != 0 {
		t.Errorf("scheduler: %+v %v", st, err)
	}
	if got := h.dbString2(`SELECT status FROM scheduled_changes WHERE workspace_id = $1::uuid`, ws); got != "pending" {
		t.Errorf("scheduled change status %s", got)
	}
	// the outbox sends nothing for it
	ost, err := h.deliveryWorker(w.clock.now, outbox.Config{}).Run(context.Background())
	if err != nil || ost.Claimed != 0 {
		t.Errorf("outbox: %+v %v", ost, err)
	}
	// the invite cannot be accepted, by link or by id
	bob := w.b
	h.expect(404, bob, "POST", "/invites/accept", map[string]any{"token": inviteTok})
	if me := decode[struct{ Invites []struct{ ID string } }](t, h.expect(200, bob, "GET", "/me", nil)); len(me.Invites) != 0 {
		t.Errorf("a pending invite for a deleted workspace is listed in /me")
	}
	// /me: the owner sees it as pending; nobody else does; it is not among the workspaces
	me := decode[struct {
		Workspaces      []struct{ ID string }
		PendingDeletion []struct{ ID, Name string }
	}](t, h.expect(200, w.a, "GET", "/me", nil))
	for _, x := range me.Workspaces {
		if x.ID == ws {
			t.Error("the workspace is still in the owner's workspace list")
		}
	}
	if len(me.PendingDeletion) != 1 || me.PendingDeletion[0].ID != ws || me.PendingDeletion[0].Name != w.wsA.Name {
		t.Errorf("pendingDeletion: %+v", me.PendingDeletion)
	}

}

// The announcement reaches other instances: their cached key and their open stream go at once, with a cache TTL and a
// stream re-check of an hour (so only the broadcast can explain it).
func TestAnotherInstanceDropsTheKeyAndTheStreamPromptly(t *testing.T) {
	w := newDelWorld(t)
	h := w.h
	instance2 := h.newInstance(slowFallbacks)
	if evaluateAt(t, instance2.URL, w.sdkKeyA, secretFlag) != 200 {
		t.Fatal("the key does not work on instance 2")
	}
	closed, _ := openStreamAt(t, instance2.URL, w.sdkKeyA)
	start := time.Now()
	w.schedule(t, 200)
	deadline := time.Now().Add(3 * time.Second)
	for evaluateAt(t, instance2.URL, w.sdkKeyA, secretFlag) != 401 {
		if time.Now().After(deadline) {
			t.Fatalf("instance 2 still accepts the key after %v", time.Since(start))
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatalf("instance 2's stream is still open after %v", time.Since(start))
	}
	t.Logf("instance 2 dropped the key and the stream %v after the request (cache TTL 1h)", time.Since(start))
}

func TestWhenTheBroadcastIsLostTheStreamsRecheckStillEndsThem(t *testing.T) {
	w := newDelWorld(t)
	h := w.h
	instance2 := h.newInstance(func(d *server.Deps) {
		d.Subscriber = deafToRevocations{h.bus}
		d.SDKKeyCacheTTL = 300 * time.Millisecond
		d.StreamRecheck = 150 * time.Millisecond
	})
	if evaluateAt(t, instance2.URL, w.sdkKeyA, secretFlag) != 200 {
		t.Fatal("setup")
	}
	closed, _ := openStreamAt(t, instance2.URL, w.sdkKeyA)
	w.schedule(t, 200)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream outlived the workspace with the broadcast lost")
	}
	deadline := time.Now().Add(3 * time.Second)
	for evaluateAt(t, instance2.URL, w.sdkKeyA, secretFlag) != 401 {
		if time.Now().After(deadline) {
			t.Fatal("the cache TTL never let go")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---- restore -----------------------------------------------------------------------------------------

// tableDump: for every table with a workspace_id column except the audit log (which gains the delete and restore
// entries), a deterministic dump of the workspace's rows.
func (w *delWorld) dump(t *testing.T, ws string) string {
	t.Helper()
	tables := w.catalogTables(t)
	var parts []string
	for _, tb := range tables {
		if tb == "audit_logs" || tb == "workspace_tombstones" {
			continue
		}
		var s *string
		q := fmt.Sprintf(`SELECT string_agg(row_to_json(t)::text, E'\n' ORDER BY row_to_json(t)::text) FROM %s t WHERE workspace_id = $1::uuid`, tb)
		if err := w.h.pool.QueryRow(context.Background(), q, ws).Scan(&s); err != nil {
			t.Fatalf("%s: %v", tb, err)
		}
		if s != nil {
			parts = append(parts, tb+"\n"+*s)
		}
	}
	var wsRow string
	_ = w.h.pool.QueryRow(context.Background(), `SELECT (to_jsonb(w) - 'deleted_at' - 'deleted_by' - 'purge_after')::text FROM workspaces w WHERE id = $1::uuid`, ws).Scan(&wsRow)
	return wsRow + "\n" + strings.Join(parts, "\n---\n")
}

func (w *delWorld) catalogTables(t *testing.T) []string {
	t.Helper()
	rows, err := w.h.pool.Query(context.Background(), `
		SELECT c.table_name FROM information_schema.columns c JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND c.column_name = 'workspace_id' AND t.table_type = 'BASE TABLE' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestRestoringWithinTheGraceBringsEverythingBackExactly(t *testing.T) {
	w := newDelWorld(t)
	h := w.h
	ws := w.wsA.ID
	adm := w.join(t, "adm", "admin")
	h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags", boolFlag("rich"))
	h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/rich", map[string]any{"enabled": true, "rollout": map[string]any{"on": 30000, "off": 70000}})
	h.expect(201, w.a, "POST", "/workspaces/"+ws+"/segments", map[string]any{"key": "vip", "name": "VIP", "members": []string{"a", "b"}})
	h.expect(201, w.a, "POST", "/workspaces/"+ws+"/webhooks", map[string]any{"url": "https://hooks.example.com/in", "eventTypes": []string{"flag.updated"}})
	tok := decode[struct{ Plaintext string }](t, h.expect(201, w.a, "POST", "/workspaces/"+ws+"/api-tokens", map[string]any{"name": "ci", "role": "editor"})).Plaintext
	h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags/rich/scheduled-changes", map[string]any{
		"idempotencyKey": "k", "kind": "disable", "runAt": w.clock.now().Add(48 * time.Hour).Format(time.RFC3339)})
	before := w.dump(t, ws)

	w.schedule(t, 200)
	if st, _, _ := h.tokenCall(tok, "GET", "/environments/"+w.devA+"/flags", nil); st != 404 {
		t.Errorf("a token while deleted: %d", st)
	}
	// only an owner can restore
	for _, u := range []user{adm, w.b} {
		if st, _ := h.call(u, "POST", "/workspaces/restore", map[string]any{"workspaceId": ws}); st != 404 {
			t.Errorf("%s restoring: %d, want 404", u.email, st)
		}
	}
	if st, _, _ := h.tokenCall(tok, "POST", "/workspaces/restore", map[string]any{"workspaceId": ws}); st != 403 {
		t.Errorf("a token restoring: %d", st)
	}
	h.expect(200, w.a, "POST", "/workspaces/restore", map[string]any{"workspaceId": ws})

	if after := w.dump(t, ws); after != before {
		t.Errorf("the workspace is not exactly as it was:\n%s", firstDifferences(before, after))
	}
	// and it works again for everyone
	if h.evaluateStatus(w.sdkKeyA, secretFlag) != 200 {
		t.Error("the SDK key does not work after the restore")
	}
	if st, _, _ := h.tokenCall(tok, "GET", "/environments/"+w.devA+"/flags", nil); st != 200 {
		t.Errorf("the token after the restore: %d", st)
	}
	h.expect(200, adm, "GET", "/environments/"+w.devA+"/flags", nil)
	if me := decode[struct{ PendingDeletion []struct{ ID string } }](t, h.expect(200, w.a, "GET", "/me", nil)); len(me.PendingDeletion) != 0 {
		t.Error("still listed as pending deletion")
	}
	for _, action := range []string{"workspace.delete_scheduled", "workspace.restored"} {
		if n := h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = $2`, ws, action); n != 1 {
			t.Errorf("%s audit entries: %d", action, n)
		}
	}
	// it can be deleted again
	w.schedule(t, 200)
}

func TestRestoreIsRefusedOnceTheGraceIsOverAndForAWorkspaceThatIsNotDeleted(t *testing.T) {
	w := newDelWorld(t)
	ws := w.wsA.ID
	if st, _ := w.h.call(w.a, "POST", "/workspaces/restore", map[string]any{"workspaceId": ws}); st != 404 {
		t.Errorf("restoring a live workspace: %d", st)
	}
	w.schedule(t, 200)
	w.graceOver(t)
	st, b := w.h.call(w.a, "POST", "/workspaces/restore", map[string]any{"workspaceId": ws})
	if st != 410 || !bytes.Contains(b, []byte("GRACE_PERIOD_ENDED")) {
		t.Errorf("restoring after the grace: %d %s", st, b)
	}
	// and /me no longer offers it
	if me := decode[struct{ PendingDeletion []struct{ ID string } }](t, w.h.expect(200, w.a, "GET", "/me", nil)); len(me.PendingDeletion) != 0 {
		t.Error("an expired restore is still offered")
	}
}

func TestADeletedOnlyWorkspaceDoesNotBecomeANewPersonalWorkspaceBehindTheOwnersBack(t *testing.T) {
	x := newDelWorld(t)
	carol := x.h.newUser("carol")
	ws := x.h.me(carol).Workspaces[0]
	x.h.expect(200, carol, "POST", "/workspaces/"+ws.ID+"/schedule-deletion", map[string]any{"confirmName": ws.Name})
	me := decode[struct {
		Workspaces      []struct{ ID string }
		PendingDeletion []struct{ ID string }
	}](t, x.h.expect(200, carol, "GET", "/me", nil))
	if len(me.Workspaces) != 0 || len(me.PendingDeletion) != 1 {
		t.Fatalf("/me: %+v", me)
	}
	if n := x.h.count(`SELECT count(*) FROM workspace_members WHERE user_id = $1::uuid`, carol.id); n != 1 {
		t.Errorf("carol is a member of %d workspaces", n)
	}
	x.h.expect(200, carol, "POST", "/workspaces/restore", map[string]any{"workspaceId": ws.ID})
	if me := decode[struct{ Workspaces []struct{ ID string } }](t, x.h.expect(200, carol, "GET", "/me", nil)); len(me.Workspaces) != 1 {
		t.Errorf("after restoring: %+v", me)
	}
}

func TestAPendingDeletionStillCountsTowardTheOwnedWorkspacesLimitUntilPurged(t *testing.T) {
	w := newDelWorld(t)
	for i := 0; i < 4; i++ {
		w.h.expect(201, w.b, "POST", "/workspaces", map[string]any{"name": fmt.Sprintf("b %d", i)})
	}
	var extra struct{ ID, Name string }
	for _, ws := range w.h.me(w.b).Workspaces {
		if strings.HasPrefix(ws.Name, "b ") {
			extra.ID, extra.Name = ws.ID, ws.Name
			break
		}
	}
	w.h.expect(200, w.b, "POST", "/workspaces/"+extra.ID+"/schedule-deletion", map[string]any{"confirmName": extra.Name})
	if st, b := w.h.call(w.b, "POST", "/workspaces", map[string]any{"name": "sixth"}); st != 409 || !bytes.Contains(b, []byte("QUOTA_EXCEEDED")) {
		t.Fatalf("creating while one is pending deletion: %d %s", st, b)
	}
	// purged: the slot is free
	w.h.rawExec(`UPDATE workspaces SET purge_after = now() - interval '1 minute' WHERE id = $1::uuid`, extra.ID)
	if _, err := wspurge.New(w.h.pool, wspurge.Config{Pub: w.h.bus}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.h.expect(201, w.b, "POST", "/workspaces", map[string]any{"name": "sixth"})
}

func TestScheduledDeletionCancelsAnOpenOwnershipOffer(t *testing.T) {
	w := newDelWorld(t)
	ws := w.wsA.ID
	ada := w.join(t, "ada", "admin")
	id := (&ownWorld{tenants: w.tenants, clock: w.clock}).offer(t, w.a, ws, ada.id)
	w.schedule(t, 200)
	if st, _ := w.h.call(ada, "POST", "/ownership-offers/"+id+"/accept", nil); st != 404 && st != 409 {
		t.Errorf("accepting an offer of a deleted workspace: %d", st)
	}
	if w.owners(ws) != 1 {
		t.Error("ownership changed")
	}
}

func (w *delWorld) owners(ws string) int {
	return w.h.count(`SELECT count(*) FROM workspace_members WHERE workspace_id = $1::uuid AND role = 'owner'`, ws)
}

// ---- the purge -------------------------------------------------------------------------------------------

// fill makes a workspace with plenty of rows in the big tables.
func (w *delWorld) fill(t *testing.T, ws, env string, events int) {
	t.Helper()
	h := w.h
	h.expect(201, w.a, "POST", "/workspaces/"+ws+"/segments", map[string]any{"key": "vip", "name": "VIP", "members": []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7"}})
	h.expect(201, w.a, "POST", "/workspaces/"+ws+"/webhooks", map[string]any{"url": "https://hooks.example.com/in", "eventTypes": []string{"flag.updated"}})
	for i := 0; i < 5; i++ {
		h.expect(200, w.a, "PATCH", "/environments/"+env+"/flags/"+secretFlag, map[string]any{"enabled": i%2 == 0})
	}
	if err := h.rawExec(`INSERT INTO events (workspace_id, environment_id, event_id, event_name, subject_key, occurred_at)
		SELECT $1::uuid, $2::uuid, 'e' || g, 'page', 'u' || g, now() FROM generate_series(1, $3) g`, ws, env, events); err != nil {
		t.Fatal(err)
	}
}

func (w *delWorld) leftover(t *testing.T, ws string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, tb := range w.catalogTables(t) {
		var n int
		if err := w.h.pool.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %s WHERE workspace_id = $1::uuid`, tb), ws).Scan(&n); err != nil {
			t.Fatalf("%s: %v", tb, err)
		}
		if n > 0 {
			out[tb] = n
		}
	}
	return out
}

func TestThePurgeLeavesNoRowsForTheWorkspaceInAnyTableAndKeepsOnlyATombstone(t *testing.T) {
	w := newDelWorld(t)
	h := w.h
	ws := w.wsA.ID
	w.join(t, "adm", "admin")
	w.fill(t, ws, w.devA, 95)
	// B's workspace must be untouched
	bBefore := w.dump(t, w.wsB.ID)
	bAudit := h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid`, w.wsB.ID)
	if got := w.leftover(t, ws); len(got) < 8 {
		t.Fatalf("setup: only %d tables have rows: %v", len(got), got)
	}
	w.schedule(t, 200)

	// not due yet: nothing happens
	st, err := w.purger(wspurge.Config{BatchSize: 10}).Run(context.Background())
	if err != nil || st.Purged != 0 || st.Rows != 0 {
		t.Fatalf("a purge before the grace is over: %+v %v", st, err)
	}
	// and the audit log still refuses deletion, for anyone, while the grace runs
	if err := h.rawExec(`DELETE FROM audit_logs WHERE workspace_id = $1::uuid`, ws); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("audit rows deleted before the purge date: %v", err)
	}

	w.graceOver(t)
	st, err = w.purger(wspurge.Config{BatchSize: 10}).Run(context.Background())
	if err != nil || st.Purged != 1 {
		t.Fatalf("purge: %+v %v", st, err)
	}
	if st.Batches < 10 {
		t.Errorf("only %d batches for 95+ events at a batch size of 10: the deletes are not batched", st.Batches)
	}
	if left := w.leftover(t, ws); len(left) != 1 || left["workspace_tombstones"] != 1 {
		t.Fatalf("rows left behind: %v", left)
	}
	if n := h.count(`SELECT count(*) FROM workspaces WHERE id = $1::uuid`, ws); n != 0 {
		t.Error("the workspace row is still there")
	}
	// the tombstone says who asked and when, and holds only counts
	var requestedBy *string
	var summary string
	if err := h.pool.QueryRow(context.Background(), `SELECT requested_by::text, summary::text FROM workspace_tombstones WHERE workspace_id = $1::uuid`, ws).Scan(&requestedBy, &summary); err != nil {
		t.Fatal(err)
	}
	if requestedBy == nil || *requestedBy != w.a.id {
		t.Errorf("requested_by = %v", requestedBy)
	}
	for _, leak := range []string{w.a.email, w.wsA.Name, secretFlag, "m1", "hooks.example.com"} {
		if strings.Contains(summary, leak) {
			t.Errorf("the tombstone summary contains %q: %s", leak, summary)
		}
	}
	// the final audit entry exists, under the platform id, without customer data
	var entry string
	if err := h.pool.QueryRow(context.Background(), `SELECT actor_email || ' ' || coalesce(diff_after::text, '') || ' ' || resource_id FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'workspace.purged' AND resource_id = $2`, workspaces.PlatformWorkspaceID, ws).Scan(&entry); err != nil {
		t.Fatalf("no final audit entry: %v", err)
	}
	for _, leak := range []string{w.a.email, w.wsA.Name, secretFlag} {
		if strings.Contains(entry, leak) {
			t.Errorf("the final audit entry contains %q: %s", leak, entry)
		}
	}
	// B is exactly as it was
	if after := w.dump(t, w.wsB.ID); after != bBefore {
		t.Error("another workspace changed")
	}
	if h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid`, w.wsB.ID) != bAudit {
		t.Error("another workspace's audit log changed")
	}
	// idempotent
	if st, err := w.purger(wspurge.Config{BatchSize: 10}).Run(context.Background()); err != nil || st.Purged != 0 {
		t.Errorf("a second run: %+v %v", st, err)
	}
}

func TestACrashedPurgeResumesAndFinishes(t *testing.T) {
	w := newDelWorld(t)
	h := w.h
	ws := w.wsA.ID
	w.fill(t, ws, w.devA, 60)
	w.schedule(t, 200)
	w.graceOver(t)
	batches := 0
	crash := fmt.Errorf("simulated crash")
	_, err := w.purger(wspurge.Config{BatchSize: 7, AfterBatch: func(table string, n int) error {
		batches++
		if batches == 4 {
			return crash
		}
		return nil
	}}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "simulated crash") {
		t.Fatalf("the run did not stop: %v", err)
	}
	// half way: the workspace exists, is still scheduled for deletion, invisible, has fewer events and nothing is lost that should remain
	if n := h.count(`SELECT count(*) FROM workspaces WHERE id = $1::uuid AND deleted_at IS NOT NULL`, ws); n != 1 {
		t.Fatal("the workspace row went before its data")
	}
	mid := h.count(`SELECT count(*) FROM events WHERE workspace_id = $1::uuid`, ws)
	if mid == 0 || mid >= 60 {
		t.Errorf("events left after 4 batches: %d (of 60)", mid)
	}
	if st, _ := h.call(w.a, "GET", "/environments/"+w.devA+"/flags", nil); st != 404 {
		t.Errorf("the half-purged workspace is visible: %d", st)
	}
	// the next run carries on from where it stopped
	st, err := w.purger(wspurge.Config{BatchSize: 7}).Run(context.Background())
	if err != nil || st.Purged != 1 {
		t.Fatalf("resumed run: %+v %v", st, err)
	}
	if left := w.leftover(t, ws); len(left) != 1 || left["workspace_tombstones"] != 1 {
		t.Fatalf("left behind: %v", left)
	}
	// a crash after everything was deleted but before the tombstone is also resumable (nothing to do but finish)
	if n := h.count(`SELECT count(*) FROM workspace_tombstones WHERE workspace_id = $1::uuid`, ws); n != 1 {
		t.Errorf("tombstones: %d", n)
	}
}

func TestARestoreCannotRaceAPurge(t *testing.T) {
	w := newDelWorld(t)
	w.fill(t, w.wsA.ID, w.devA, 20)
	w.schedule(t, 200)
	w.graceOver(t)
	// the purge has begun (a batch ran) when the owner tries to restore: too late
	_, err := w.purger(wspurge.Config{BatchSize: 5, AfterBatch: func(string, int) error { return fmt.Errorf("stop") }}).Run(context.Background())
	if err == nil {
		t.Fatal("expected a stop")
	}
	if st, _ := w.h.call(w.a, "POST", "/workspaces/restore", map[string]any{"workspaceId": w.wsA.ID}); st != 410 {
		t.Errorf("restoring a half-purged workspace: %d", st)
	}
}

// A table added to the schema later with a workspace_id column cannot be forgotten: it must be listed as batched,
// cascaded or exempt, or this test fails; and the purge test above fails if it leaves rows behind.
func TestEveryTableWithAWorkspaceIDIsAccountedForByThePurge(t *testing.T) {
	w := newDelWorld(t)
	covered := wspurge.Covered()
	var missing, stale []string
	seen := map[string]bool{}
	for _, tb := range w.catalogTables(t) {
		seen[tb] = true
		if _, ok := covered[tb]; !ok {
			missing = append(missing, tb)
		}
	}
	for tb := range covered {
		if !seen[tb] {
			stale = append(stale, tb)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("tables with a workspace_id column that the workspace purge does not account for (add them to wspurge.Batched, Cascaded or Exempt): %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("tables the purge lists that have no workspace_id column (any more): %v", stale)
	}
	// a cascaded table must really cascade: its foreign keys lead to the workspace with ON DELETE CASCADE, directly or through a parent.
	// (The purge test proves it end to end; this names the exemption list so it stays tiny.)
	if len(wspurge.Exempt) != 1 {
		t.Errorf("exemptions: %v", wspurge.Exempt)
	}
}

func TestTheAuditLogStaysAppendOnlyExceptForADuePurge(t *testing.T) {
	w := newDelWorld(t)
	h := w.h
	ws := w.wsA.ID
	for _, sql := range []string{
		`UPDATE audit_logs SET severity = 'info' WHERE workspace_id = $1::uuid`,
		`DELETE FROM audit_logs WHERE workspace_id = $1::uuid`,
		`TRUNCATE audit_logs`,
	} {
		if err := h.rawExec(sql, ws); err == nil || !strings.Contains(err.Error(), "append-only") {
			if strings.HasPrefix(sql, "TRUNCATE") {
				if err2 := h.rawExec(sql); err2 != nil && strings.Contains(err2.Error(), "append-only") {
					continue
				}
			}
			t.Errorf("%q: %v", sql, err)
		}
	}
	w.schedule(t, 200)
	if err := h.rawExec(`DELETE FROM audit_logs WHERE workspace_id = $1::uuid`, ws); err == nil {
		t.Error("deleted while the grace is running")
	}
	if err := h.rawExec(`DELETE FROM audit_logs WHERE workspace_id = $1::uuid`, w.wsB.ID); err == nil {
		t.Error("another workspace's audit rows were deletable during A's purge window")
	}
	w.graceOver(t)
	if err := h.rawExec(`UPDATE audit_logs SET severity = 'info' WHERE workspace_id = $1::uuid`, ws); err == nil {
		t.Error("updated during a due purge: only deletion is allowed")
	}
	if err := h.rawExec(`DELETE FROM audit_logs WHERE workspace_id = $1::uuid`, ws); err != nil {
		t.Errorf("a due purge could not delete: %v", err)
	}
	if err := h.rawExec(`DELETE FROM audit_logs WHERE workspace_id = $1::uuid`, w.wsB.ID); err == nil {
		t.Error("B's audit rows deletable")
	}
	if err := h.rawExec(`DELETE FROM audit_logs WHERE workspace_id = $1::uuid`, workspaces.PlatformWorkspaceID); err == nil {
		t.Error("the platform's audit rows deletable")
	}
}

func (h *harness) purgeRun(secret string, headers map[string]string) (int, []byte) {
	h.t.Helper()
	req, _ := http.NewRequest("POST", h.srv.URL+"/internal/workspaces/purge", strings.NewReader("{}"))
	if secret != "" {
		req.Header.Set("X-Helios-Purge-Secret", secret)
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
	return res.StatusCode, b
}

func TestThePurgeEndpointNeedsTheSecretAndIsOffWithoutOne(t *testing.T) {
	h := newHarnessWith(t, func(d *server.Deps) { d.PurgeSecret = goodSecret })
	for name, c := range map[string]struct {
		secret  string
		headers map[string]string
	}{"none": {"", nil}, "wrong": {"nope", nil}, "the outbox's header": {"", map[string]string{"X-Helios-Outbox-Secret": goodSecret}}, "browser": {goodSecret, map[string]string{"Origin": "https://x.example"}}} {
		if st, b := h.purgeRun(c.secret, c.headers); st != 401 || string(b) != refused {
			t.Errorf("%s: %d %q", name, st, b)
		}
	}
	st, b := h.purgeRun(goodSecret, nil)
	if st != 200 || !bytes.Contains(b, []byte(`"purged":0`)) {
		t.Errorf("right secret: %d %s", st, b)
	}
	off := newHarness(t)
	if st, b := off.purgeRun(goodSecret, nil); st != 404 || strings.TrimSpace(string(b)) != "404 page not found" {
		t.Errorf("unset: %d %q", st, b)
	}
}

// firstDifferences lists the lines that are in one dump and not the other.
func firstDifferences(a, b string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	var out []string
	for _, l := range strings.Split(a, "\n") {
		if !mb[l] {
			out = append(out, "- "+l)
		}
	}
	for _, l := range strings.Split(b, "\n") {
		if !ma[l] {
			out = append(out, "+ "+l)
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return strings.Join(out, "\n")
}
