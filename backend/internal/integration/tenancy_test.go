package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

const secretFlag = "acme-secret-flag"

// setup: A and B each get a personal workspace. A creates (and enables) a
// flag, an experiment and an SDK key in their dev environment.
type tenants struct {
	h        *harness
	a, b     user
	wsA, wsB meWorkspace
	devA     string // A's dev environment id
	devB     string
	sdkKeyA  string // plaintext, shown once
}

func newTenants(t *testing.T) *tenants {
	h := newHarness(t)
	x := &tenants{h: h, a: h.newUser("alice"), b: h.newUser("bob")}
	x.wsA = h.me(x.a).Workspaces[0]
	x.wsB = h.me(x.b).Workspaces[0]
	x.devA, x.devB = envID(t, x.wsA, "dev"), envID(t, x.wsB, "dev")

	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", boolFlag(secretFlag))
	h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/"+secretFlag, map[string]any{"enabled": true})
	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/experiments", map[string]any{
		"flagKey": secretFlag, "key": "acme-secret-exp", "name": "Secret experiment",
		"metrics": []map[string]any{{"name": "M", "eventName": "e", "type": "conversion", "isPrimary": true}},
	})
	key := decode[struct{ Plaintext string }](t, h.expect(201, x.a, "POST", "/environments/"+x.devA+"/sdk-keys", map[string]any{"name": "alice key"}))
	x.sdkKeyA = key.Plaintext
	return x
}

func TestFirstMeCreatesOnePersonalWorkspace(t *testing.T) {
	h := newHarness(t)
	u := h.newUser("carol")

	// Several first requests at once (two tabs, or more) must yield ONE workspace.
	var wg sync.WaitGroup
	statuses := make([]int, 8)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", h.srv.URL+"/me", nil)
			req.Header.Set("X-Test-User", u.id+"|"+u.email)
			if res, err := http.DefaultClient.Do(req); err == nil {
				statuses[i] = res.StatusCode
				res.Body.Close()
			}
		}()
	}
	wg.Wait()
	for i, s := range statuses {
		if s != 200 {
			t.Errorf("request %d: status %d", i, s)
		}
	}
	if n := h.count(`SELECT count(*) FROM workspaces WHERE owner_id = $1::uuid`, u.id); n != 1 {
		t.Fatalf("%d workspaces created for one user, want 1", n)
	}

	me := h.me(u)
	if len(me.Workspaces) != 1 || me.ActiveWorkspaceID != me.Workspaces[0].ID {
		t.Fatalf("me = %+v", me)
	}
	w := me.Workspaces[0]
	if w.Role != "owner" || len(w.Environments) != 3 || !strings.HasPrefix(w.Name, "carol") {
		t.Errorf("workspace = %+v", w)
	}
	for _, key := range []string{"dev", "staging", "production"} {
		envID(t, w, key)
	}
	if n := h.count(`SELECT count(*) FROM audit_logs WHERE action = 'workspace.create' AND workspace_id = $1::uuid`, w.ID); n != 1 {
		t.Errorf("workspace.create audit rows = %d", n)
	}
	// No other tenant's data appears for a fresh user.
	h.expect(200, u, "GET", "/environments/"+envID(t, w, "dev")+"/flags", nil)
}

// B must get 404 for every one of A's resources, and no response B ever
// receives may contain anything of A's.
func TestCrossTenantIsolation(t *testing.T) {
	x := newTenants(t)
	h := x.h
	var seen [][]byte // every body B receives

	envA := "/environments/" + x.devA
	for _, c := range []struct{ method, path string }{
		{"GET", envA + "/flags"},
		{"GET", envA + "/flags/" + secretFlag},
		{"PATCH", envA + "/flags/" + secretFlag},
		{"POST", envA + "/flags/" + secretFlag + "/kill"},
		{"DELETE", envA + "/flags/" + secretFlag + "?force=true"},
		{"POST", envA + "/flags"},
		{"GET", envA + "/experiments"},
		{"GET", envA + "/experiments/acme-secret-exp"},
		{"POST", envA + "/experiments/acme-secret-exp/start"},
		{"POST", envA + "/experiments/acme-secret-exp/stop"},
		{"POST", envA + "/experiments"},
		{"GET", envA + "/audit-logs"},
		{"GET", envA + "/sdk-keys"},
		{"POST", envA + "/sdk-keys"},
		{"DELETE", envA + "/sdk-keys/" + x.devA}, // any id: still the environment guard that answers
		{"GET", "/workspaces/" + x.wsA.ID + "/members"},
		{"GET", "/workspaces/" + x.wsA.ID + "/invites"},
		{"POST", "/workspaces/" + x.wsA.ID + "/invites"},
		{"PATCH", "/workspaces/" + x.wsA.ID},
		{"POST", "/workspaces/" + x.wsA.ID + "/switch"},
		{"PATCH", "/workspaces/" + x.wsA.ID + "/members/" + x.a.id},
		{"DELETE", "/workspaces/" + x.wsA.ID + "/members/" + x.a.id},
	} {
		// Valid-looking bodies, so a 404 can only come from the tenant check.
		var body any
		switch c.method {
		case "PATCH", "POST", "DELETE":
			body = map[string]any{"enabled": false, "name": "x", "role": "viewer", "email": "x@example.com"}
			if strings.HasSuffix(c.path, "/flags") {
				body = boolFlag("intruder")
			}
			if c.method == "DELETE" || strings.HasSuffix(c.path, "/kill") || strings.HasSuffix(c.path, "/start") || strings.HasSuffix(c.path, "/stop") {
				body = nil
			}
		}
		status, b := h.call(x.b, c.method, c.path, body)
		seen = append(seen, b)
		if status != 404 {
			t.Errorf("B %s %s: status %d, want 404; body %s", c.method, c.path, status, b)
		}
	}

	// A bare, non-UUID environment name (the old key-based routes) is not a thing.
	for _, p := range []string{"/environments/dev/flags", "/environments/production/audit-logs"} {
		if status, b := h.call(x.b, "GET", p, nil); status != 404 {
			t.Errorf("GET %s: status %d, body %s", p, status, b)
		} else {
			seen = append(seen, b)
		}
	}

	// B's own data never contains A's.
	for _, p := range []string{
		"/environments/" + x.devB + "/flags",
		"/environments/" + x.devB + "/experiments",
		"/environments/" + x.devB + "/audit-logs",
		"/environments/" + x.devB + "/sdk-keys",
		"/workspaces", "/me", "/workspaces/" + x.wsB.ID + "/members", "/workspaces/" + x.wsB.ID + "/invites",
	} {
		seen = append(seen, h.expect(200, x.b, "GET", p, nil))
	}
	forbidden := []string{secretFlag, "acme-secret-exp", x.a.email, x.a.id, x.wsA.ID, x.devA, x.wsA.Name, "alice key", x.sdkKeyA[:12]}
	for _, b := range seen {
		for _, f := range forbidden {
			if bytes.Contains(b, []byte(f)) {
				t.Errorf("a response to B leaked %q: %s", f, b)
			}
		}
	}

	// B's own flag list is empty, and keys are unique per workspace.
	flags := decode[struct{ Flags []json.RawMessage }](t, h.expect(200, x.b, "GET", "/environments/"+x.devB+"/flags", nil))
	if len(flags.Flags) != 0 {
		t.Errorf("B sees %d flags", len(flags.Flags))
	}
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/flags", boolFlag(secretFlag))
	// ...and B can't build an experiment on A's flag, even by name, from B's own environment.
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/flags", boolFlag("b-flag"))
	status, b := h.call(x.b, "POST", "/environments/"+x.devB+"/experiments", map[string]any{
		"flagKey": "no-such-flag", "key": "e1", "name": "n",
		"metrics": []map[string]any{{"name": "M", "eventName": "e", "type": "conversion", "isPrimary": true}},
	})
	if status != 404 || !bytes.Contains(b, []byte("FLAG_NOT_FOUND")) {
		t.Errorf("experiment on unknown flag: %d %s", status, b)
	}

	// A is untouched by all of it.
	got := decode[struct {
		Config struct{ Enabled bool }
	}](t, h.expect(200, x.a, "GET", envA+"/flags/"+secretFlag, nil))
	if !got.Config.Enabled {
		t.Error("A's flag was changed")
	}
	if n := h.count(`SELECT count(*) FROM experiments WHERE workspace_id = $1::uuid AND status = 'draft'`, x.wsA.ID); n != 1 {
		t.Errorf("A's experiment count/status changed: %d", n)
	}
}

// A's SDK key evaluates only A's flags; B's key only B's.
func TestSDKKeysAreWorkspaceScoped(t *testing.T) {
	x := newTenants(t)
	h := x.h
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/flags", boolFlag("b-only-flag"))
	h.expect(200, x.b, "PATCH", "/environments/"+x.devB+"/flags/b-only-flag", map[string]any{"enabled": true})
	keyB := decode[struct{ Plaintext string }](t, h.expect(201, x.b, "POST", "/environments/"+x.devB+"/sdk-keys", map[string]any{"name": "bob key"})).Plaintext

	type eval struct {
		FlagKey string
		Value   any
		Reason  string
	}
	evaluate := func(sdkKey string, keys ...string) (int, map[string]eval) {
		b, _ := json.Marshal(map[string]any{"context": map[string]any{"subjectKey": "user-1"}, "flagKeys": keys})
		req, _ := http.NewRequest("POST", h.srv.URL+"/evaluate", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Helios-SDK-Key", sdkKey)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct{ Evaluations []eval }
		_ = json.NewDecoder(res.Body).Decode(&out)
		m := map[string]eval{}
		for _, e := range out.Evaluations {
			m[e.FlagKey] = e
		}
		return res.StatusCode, m
	}

	status, got := evaluate(x.sdkKeyA, secretFlag, "b-only-flag")
	if status != 200 || got[secretFlag].Value != true || got["b-only-flag"].Reason != "FLAG_NOT_FOUND" || got["b-only-flag"].Value != nil {
		t.Errorf("A's key: status %d, %+v", status, got)
	}
	status, got = evaluate(keyB, secretFlag, "b-only-flag")
	if status != 200 || got["b-only-flag"].Value != true || got[secretFlag].Reason != "FLAG_NOT_FOUND" || got[secretFlag].Value != nil {
		t.Errorf("B's key: status %d, %+v", status, got)
	}

	// Revoking A's key stops it; B can't revoke it.
	var keyID string
	if err := h.pool.QueryRow(context.Background(), `SELECT id::text FROM api_keys WHERE workspace_id = $1::uuid`, x.wsA.ID).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	h.expect(404, x.b, "DELETE", "/environments/"+x.devA+"/sdk-keys/"+keyID, nil)
	if status, _ := evaluate(x.sdkKeyA, secretFlag); status != 200 {
		t.Errorf("B's attempt revoked A's key (status %d)", status)
	}
	h.expect(204, x.a, "DELETE", "/environments/"+x.devA+"/sdk-keys/"+keyID, nil)
	h.expect(204, x.a, "DELETE", "/environments/"+x.devA+"/sdk-keys/"+keyID, nil) // idempotent
	if status, _ := evaluate(x.sdkKeyA, secretFlag); status != 401 {
		t.Errorf("revoked key: status %d, want 401", status)
	}

	// The key list shows prefixes only.
	list := h.expect(200, x.a, "GET", "/environments/"+x.devA+"/sdk-keys", nil)
	if bytes.Contains(list, []byte(x.sdkKeyA)) || !bytes.Contains(list, []byte(`"revokedAt":"`)) {
		t.Errorf("key list: %s", list)
	}
}

func TestInviteAcceptAndViewerIsReadOnly(t *testing.T) {
	x := newTenants(t)
	h := x.h
	envA := "/environments/" + x.devA

	inv := decode[struct {
		Invite struct{ ID, Email, Role string }
		Token  string
	}](t, h.expect(201, x.a, "POST", "/workspaces/"+x.wsA.ID+"/invites", map[string]any{"email": "Bob@Example.com", "role": "viewer"}))
	if inv.Invite.Email != "bob@example.com" || inv.Invite.Role != "viewer" || !strings.HasPrefix(inv.Token, "hinv_") {
		t.Fatalf("invite = %+v", inv)
	}
	if n := h.count(`SELECT count(*) FROM workspace_invites WHERE token_hash = $1`, inv.Token); n != 0 {
		t.Error("the token itself is stored")
	}

	// Not accepted yet: B still sees nothing of A's, but is told about the invite (no token).
	h.expect(404, x.b, "GET", envA+"/flags", nil)
	me := h.me(x.b)
	if len(me.Invites) != 1 || me.Invites[0].WorkspaceName != x.wsA.Name || me.Invites[0].Role != "viewer" {
		t.Fatalf("B's invites = %+v", me.Invites)
	}

	// Someone else can't use B's token; B can't use it on someone else's behalf either.
	carol := h.newUser("carol")
	h.me(carol)
	h.expect(403, carol, "POST", "/invites/accept", map[string]any{"token": inv.Token})
	h.expect(404, carol, "POST", "/invites/accept", map[string]any{"inviteId": inv.Invite.ID})

	// B accepts by token.
	h.expect(200, x.b, "POST", "/invites/accept", map[string]any{"token": inv.Token})
	h.expect(404, x.b, "POST", "/invites/accept", map[string]any{"token": inv.Token}) // single use

	me = h.me(x.b)
	if len(me.Workspaces) != 2 || me.ActiveWorkspaceID != x.wsA.ID || len(me.Invites) != 0 {
		t.Fatalf("after accepting, B's me = %+v", me)
	}
	var asViewer meWorkspace
	for _, w := range me.Workspaces {
		if w.ID == x.wsA.ID {
			asViewer = w
		}
	}
	if asViewer.Role != "viewer" {
		t.Fatalf("role = %q", asViewer.Role)
	}

	// Read-only: B sees A's flag, and every write is refused with 403.
	list := h.expect(200, x.b, "GET", envA+"/flags", nil)
	if !bytes.Contains(list, []byte(secretFlag)) {
		t.Errorf("viewer can't see the flag: %s", list)
	}
	h.expect(200, x.b, "GET", envA+"/experiments", nil)
	h.expect(200, x.b, "GET", envA+"/audit-logs", nil)
	h.expect(200, x.b, "GET", "/workspaces/"+x.wsA.ID+"/members", nil)
	for _, c := range []struct{ method, path string }{
		{"PATCH", envA + "/flags/" + secretFlag},
		{"POST", envA + "/flags"},
		{"POST", envA + "/flags/" + secretFlag + "/kill"},
		{"DELETE", envA + "/flags/" + secretFlag},
		{"POST", envA + "/experiments"},
		{"POST", envA + "/experiments/acme-secret-exp/start"},
		{"GET", envA + "/sdk-keys"},
		{"POST", envA + "/sdk-keys"},
		{"GET", "/workspaces/" + x.wsA.ID + "/invites"},
		{"POST", "/workspaces/" + x.wsA.ID + "/invites"},
		{"PATCH", "/workspaces/" + x.wsA.ID},
		{"PATCH", "/workspaces/" + x.wsA.ID + "/members/" + x.a.id},
		{"DELETE", "/workspaces/" + x.wsA.ID + "/members/" + x.a.id}, // removing someone else
	} {
		var body any
		if c.method != "DELETE" && !strings.HasSuffix(c.path, "/kill") && !strings.HasSuffix(c.path, "/start") {
			body = map[string]any{"enabled": false, "name": "x", "role": "viewer", "email": "z@example.com"}
			if strings.HasSuffix(c.path, "/flags") {
				body = boolFlag("viewer-flag")
			}
		}
		if status, b := h.call(x.b, c.method, c.path, body); status != 403 {
			t.Errorf("viewer %s %s: status %d, want 403; body %s", c.method, c.path, status, b)
		}
	}

	// B leaves; A's workspace goes back to being invisible.
	h.expect(204, x.b, "DELETE", "/workspaces/"+x.wsA.ID+"/members/"+x.b.id, nil)
	h.expect(404, x.b, "GET", envA+"/flags", nil)
	if me := h.me(x.b); len(me.Workspaces) != 1 {
		t.Errorf("after leaving, B has %d workspaces", len(me.Workspaces))
	}
}

func TestInviteEdgeCases(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := "/workspaces/" + x.wsA.ID

	h.expect(400, x.a, "POST", ws+"/invites", map[string]any{"email": "not-an-email", "role": "viewer"})
	h.expect(400, x.a, "POST", ws+"/invites", map[string]any{"email": "z@example.com", "role": "root"})
	h.expect(403, x.a, "POST", ws+"/invites", map[string]any{"email": "z@example.com", "role": "owner"})
	h.expect(409, x.a, "POST", ws+"/invites", map[string]any{"email": x.a.email, "role": "viewer"}) // already a member

	first := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": "dave@example.com", "role": "editor"}))
	h.expect(409, x.a, "POST", ws+"/invites", map[string]any{"email": "dave@example.com", "role": "viewer"}) // already open

	// An expired invite can't be accepted, doesn't count as open, and doesn't block a new one.
	if _, err := h.pool.Exec(context.Background(), `UPDATE workspace_invites SET expires_at = now() - interval '1 hour' WHERE email = 'dave@example.com'`); err != nil {
		t.Fatal(err)
	}
	dave := h.newUser("dave")
	h.me(dave)
	h.expect(410, dave, "POST", "/invites/accept", map[string]any{"token": first.Token})
	listed := decode[struct{ Invites []json.RawMessage }](t, h.expect(200, x.a, "GET", ws+"/invites", nil))
	if len(listed.Invites) != 0 {
		t.Errorf("expired invite is listed: %d", len(listed.Invites))
	}
	second := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": "dave@example.com", "role": "editor"}))
	h.expect(200, dave, "POST", "/invites/accept", map[string]any{"token": second.Token})

	// Revoking: only an open invite of this workspace; and it can no longer be used.
	third := decode[struct {
		Invite struct{ ID string }
		Token  string
	}](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": "erin@example.com", "role": "viewer"}))
	h.expect(204, x.a, "DELETE", ws+"/invites/"+third.Invite.ID, nil)
	h.expect(404, x.a, "DELETE", ws+"/invites/"+third.Invite.ID, nil)
	erin := h.newUser("erin")
	h.me(erin)
	h.expect(404, erin, "POST", "/invites/accept", map[string]any{"token": third.Token})
	// B can't revoke A's invites (nor see them).
	other := decode[struct{ Invite struct{ ID string } }](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": "frank@example.com", "role": "viewer"}))
	h.expect(404, x.b, "DELETE", ws+"/invites/"+other.Invite.ID, nil)

	// Neither a token nor an id, or both: a 400.
	h.expect(400, dave, "POST", "/invites/accept", map[string]any{})
	h.expect(400, dave, "POST", "/invites/accept", map[string]any{"token": "x", "inviteId": "y"})
}

func TestRolesAndLastOwner(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := "/workspaces/" + x.wsA.ID
	joinAs := func(u user, role string) {
		t.Helper()
		h.me(u)
		inv := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": u.email, "role": role}))
		h.expect(200, u, "POST", "/invites/accept", map[string]any{"token": inv.Token})
	}
	adminU, editorU, viewerU := h.newUser("adam"), h.newUser("edith"), h.newUser("vera")
	joinAs(adminU, "admin")
	joinAs(editorU, "editor")
	joinAs(viewerU, "viewer")

	// The workspace role decides access in every environment: editors write outside production only.
	envEditorDev, envProd := x.devA, envID(t, x.wsA, "production")
	h.expect(201, editorU, "POST", "/environments/"+envEditorDev+"/flags", boolFlag("editor-flag"))
	h.expect(200, editorU, "PATCH", "/environments/"+envEditorDev+"/flags/editor-flag", map[string]any{"enabled": true})
	h.expect(403, editorU, "PATCH", "/environments/"+envProd+"/flags/editor-flag", map[string]any{"enabled": true})
	h.expect(200, editorU, "POST", "/environments/"+envEditorDev+"/flags/editor-flag/kill", nil)
	h.expect(403, editorU, "DELETE", "/environments/"+envEditorDev+"/flags/editor-flag", nil)
	h.expect(200, adminU, "PATCH", "/environments/"+envProd+"/flags/editor-flag", map[string]any{"enabled": true})
	h.expect(204, adminU, "DELETE", "/environments/"+envEditorDev+"/flags/editor-flag", nil)

	// Admins manage members but not owners.
	h.expect(200, adminU, "PATCH", ws+"/members/"+viewerU.id, map[string]any{"role": "editor"})
	h.expect(403, adminU, "PATCH", ws+"/members/"+x.a.id, map[string]any{"role": "viewer"})
	h.expect(403, adminU, "PATCH", ws+"/members/"+viewerU.id, map[string]any{"role": "owner"})
	h.expect(403, adminU, "DELETE", ws+"/members/"+x.a.id, nil)
	h.expect(200, adminU, "PATCH", ws, map[string]any{"name": "Renamed by admin"})
	h.expect(403, editorU, "PATCH", ws, map[string]any{"name": "Nope"})
	if got := h.me(x.a).Workspaces[0].Name; got != "Renamed by admin" {
		t.Errorf("name = %q", got)
	}
	h.expect(400, adminU, "PATCH", ws, map[string]any{"name": "   "})
	h.expect(400, x.a, "PATCH", ws+"/members/"+viewerU.id, map[string]any{"role": "superuser"})
	h.expect(404, x.a, "PATCH", ws+"/members/"+x.b.id, map[string]any{"role": "viewer"}) // B is not a member

	// The only owner can neither leave, be demoted, nor be removed.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"DELETE", ws + "/members/" + x.a.id, nil},
		{"PATCH", ws + "/members/" + x.a.id, map[string]any{"role": "admin"}},
	} {
		b := h.expect(409, x.a, c.method, c.path, c.body)
		if !bytes.Contains(b, []byte("LAST_OWNER")) {
			t.Errorf("%s: %s", c.method, b)
		}
	}
	// ...until someone else is an owner.
	h.expect(200, x.a, "PATCH", ws+"/members/"+adminU.id, map[string]any{"role": "owner"})
	h.expect(204, x.a, "DELETE", ws+"/members/"+x.a.id, nil)
	h.expect(404, x.a, "GET", "/environments/"+x.devA+"/flags", nil)
	h.expect(200, adminU, "GET", "/environments/"+x.devA+"/flags", nil)

	// Removing a member takes their access with them, immediately.
	h.expect(204, adminU, "DELETE", ws+"/members/"+editorU.id, nil)
	h.expect(404, editorU, "GET", "/environments/"+x.devA+"/flags", nil)
}

func TestWorkspacesCreateSwitchAndLimits(t *testing.T) {
	h := newHarness(t)
	u := h.newUser("gina")
	personal := h.me(u).Workspaces[0]

	created := decode[meWorkspace](t, h.expect(201, u, "POST", "/workspaces", map[string]any{"name": "  Side project  "}))
	if created.Name != "Side project" || created.Role != "owner" || len(created.Environments) != 3 {
		t.Fatalf("created = %+v", created)
	}
	me := h.me(u)
	if len(me.Workspaces) != 2 || me.ActiveWorkspaceID != created.ID {
		t.Fatalf("me = %+v", me)
	}
	// The two workspaces share nothing: same environment keys, different ids and flags.
	devPersonal, devSide := envID(t, personal, "dev"), envID(t, created, "dev")
	if devPersonal == devSide {
		t.Fatal("workspaces share an environment id")
	}
	h.expect(201, u, "POST", "/environments/"+devSide+"/flags", boolFlag("side-flag"))
	if b := h.expect(200, u, "GET", "/environments/"+devPersonal+"/flags", nil); bytes.Contains(b, []byte("side-flag")) {
		t.Error("a flag from one workspace shows up in another")
	}

	// Switching changes the active workspace; a stranger can't switch into it.
	h.expect(200, u, "POST", "/workspaces/"+personal.ID+"/switch", nil)
	if got := h.me(u).ActiveWorkspaceID; got != personal.ID {
		t.Errorf("active = %s, want %s", got, personal.ID)
	}
	h.expect(404, h.newUser("hank"), "POST", "/workspaces/"+personal.ID+"/switch", nil)
	h.expect(400, u, "POST", "/workspaces", map[string]any{"name": ""})

	// A user owns at most 5 workspaces (the personal one counts).
	for i := 0; i < 3; i++ {
		h.expect(201, u, "POST", "/workspaces", map[string]any{"name": fmt.Sprintf("extra %d", i)})
	}
	b := h.expect(409, u, "POST", "/workspaces", map[string]any{"name": "one too many"})
	if !bytes.Contains(b, []byte("QUOTA_EXCEEDED")) {
		t.Errorf("body %s", b)
	}
}

func TestFlagQuotaPerWorkspace(t *testing.T) {
	h := newHarness(t)
	u := h.newUser("ivy")
	ws := h.me(u).Workspaces[0]
	dev := envID(t, ws, "dev")
	for i := 0; i < 50; i++ {
		h.expect(201, u, "POST", "/environments/"+dev+"/flags", boolFlag(fmt.Sprintf("flag-%02d", i)))
	}
	b := h.expect(409, u, "POST", "/environments/"+dev+"/flags", boolFlag("flag-50"))
	if !bytes.Contains(b, []byte("QUOTA_EXCEEDED")) {
		t.Errorf("body %s", b)
	}
	// Another workspace's quota is its own.
	other := decode[meWorkspace](t, h.expect(201, u, "POST", "/workspaces", map[string]any{"name": "fresh"}))
	h.expect(201, u, "POST", "/environments/"+envID(t, other, "dev")+"/flags", boolFlag("flag-00"))
}

// Audit rows are visible only inside their workspace, including the rows that
// belong to no single environment (flag.create, invites, ...).
func TestAuditLogIsWorkspaceScoped(t *testing.T) {
	x := newTenants(t)
	h := x.h
	h.expect(201, x.a, "POST", "/workspaces/"+x.wsA.ID+"/invites", map[string]any{"email": "zed@example.com", "role": "viewer"})
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/flags", boolFlag("bobs-flag"))

	logA := string(h.expect(200, x.a, "GET", "/environments/"+x.devA+"/audit-logs", nil))
	logB := string(h.expect(200, x.b, "GET", "/environments/"+x.devB+"/audit-logs", nil))
	for _, want := range []string{secretFlag, "flag.create", "invite.create", "workspace.create", "zed@example.com"} {
		if !strings.Contains(logA, want) {
			t.Errorf("A's log lacks %q", want)
		}
	}
	for _, leaked := range []string{secretFlag, "alice@example.com", "invite.create", "zed@example.com"} {
		if strings.Contains(logB, leaked) {
			t.Errorf("B's log leaks %q", leaked)
		}
	}
	if !strings.Contains(logB, "bobs-flag") || strings.Contains(logA, "bobs-flag") {
		t.Error("each log should contain only its own workspace's flag")
	}
	if n := h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id IS NULL`); n != 0 {
		t.Errorf("%d audit rows without a workspace", n)
	}
}
