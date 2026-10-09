package integration

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// newUserWithEmail registers a user whose JWT email claim may differ in case
// from what is stored, as it can with a real identity provider.
func (h *harness) newUserWithEmail(stored, claim string) user {
	h.t.Helper()
	var id string
	if err := h.pool.QueryRow(context.Background(), `INSERT INTO auth.users (email) VALUES ($1) RETURNING id::text`, stored).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return user{id: id, email: claim}
}

func TestInviteTokensAreRandomSingleUseHashedAndExpire(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := "/workspaces/" + x.wsA.ID

	// Random: many invites, no repeats, no relation to the email, enough entropy.
	seen := map[string]bool{}
	for i := 0; i < 15; i++ {
		email := "person" + string(rune('a'+i)) + "@example.com"
		tok := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": email, "role": "viewer"})).Token
		if !strings.HasPrefix(tok, "hinv_") || len(tok) < 40 || strings.Contains(tok, strings.Split(email, "@")[0]) || seen[tok] {
			t.Fatalf("token %q is short, repeated or derived from the email", tok)
		}
		seen[tok] = true
		if n := h.count(`SELECT count(*) FROM workspace_invites WHERE token_hash = $1`, tok); n != 0 {
			t.Fatal("the token itself is stored")
		}
		if n := h.count(`SELECT count(*) FROM workspace_invites WHERE email = $1 AND length(token_hash) = 64 AND token_hash ~ '^[0-9a-f]+$'`, email); n != 1 {
			t.Fatalf("the stored value for %s is not a sha-256 hex digest", email)
		}
	}
	// Nothing else stores or returns it: not the audit log, not the listings.
	for tok := range seen {
		if n := h.count(`SELECT count(*) FROM audit_logs WHERE diff_after::text LIKE '%' || $1 || '%' OR diff_before::text LIKE '%' || $1 || '%'`, tok); n != 0 {
			t.Fatalf("an invite token is in the audit log")
		}
		for _, p := range []string{ws + "/invites", ws + "/members", "/environments/" + x.devA + "/audit-logs", "/me"} {
			if b := h.expect(200, x.a, "GET", p, nil); bytes.Contains(b, []byte(tok)) {
				t.Fatalf("GET %s returns an invite token", p)
			}
		}
		break // one is enough: they are all stored the same way
	}
}

func TestAcceptRequiresTheInvitedEmailCaseInsensitively(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := "/workspaces/" + x.wsA.ID
	inv := decode[struct {
		Invite struct{ Email string }
		Token  string
	}](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": "  Mixed.Case@Example.COM ", "role": "editor"}))
	if inv.Invite.Email != "mixed.case@example.com" {
		t.Fatalf("stored email %q", inv.Invite.Email)
	}

	// Wrong person: a different address, and one that merely contains the invited one.
	for _, wrong := range []string{"mixed.case@example.org", "xmixed.case@example.com", "mixed.case@example.com.evil.io"} {
		u := h.newUserWithEmail(wrong, wrong)
		h.me(u)
		if st, b := h.call(u, "POST", "/invites/accept", map[string]any{"token": inv.Token}); st != 403 {
			t.Errorf("accepted as %s: status %d %s", wrong, st, b)
		}
		if st, _ := h.call(u, "POST", "/invites/accept", map[string]any{"inviteId": "00000000-0000-4000-8000-000000000000"}); st != 404 {
			t.Errorf("accepting a made-up id as %s: status %d", wrong, st)
		}
	}
	// A user with a different token claim case is still the same address.
	right := h.newUserWithEmail("mixed.case@example.com", "MIXED.case@EXAMPLE.com")
	h.me(right)
	h.expect(200, right, "POST", "/invites/accept", map[string]any{"token": inv.Token})
	members := string(h.expect(200, x.a, "GET", ws+"/members", nil))
	if !strings.Contains(members, `"role":"editor"`) {
		t.Errorf("members: %s", members)
	}

	// An existing member with a different case of their address can't be re-invited.
	h.expect(409, x.a, "POST", ws+"/invites", map[string]any{"email": strings.ToUpper(x.a.email), "role": "viewer"})
}

func TestRolesCannotBeEscalated(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := "/workspaces/" + x.wsA.ID
	join := func(name, role string) user {
		u := h.newUser(name)
		h.me(u)
		tok := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": u.email, "role": role})).Token
		h.expect(200, u, "POST", "/invites/accept", map[string]any{"token": tok})
		return u
	}
	admin, editor, viewer := join("adm", "admin"), join("edt", "editor"), join("vwr", "viewer")

	// Nobody promotes themselves.
	h.expect(403, viewer, "PATCH", ws+"/members/"+viewer.id, map[string]any{"role": "editor"})
	h.expect(403, editor, "PATCH", ws+"/members/"+editor.id, map[string]any{"role": "admin"})
	h.expect(403, admin, "PATCH", ws+"/members/"+admin.id, map[string]any{"role": "owner"})
	// Nobody promotes anyone above their own role.
	h.expect(403, editor, "PATCH", ws+"/members/"+viewer.id, map[string]any{"role": "editor"})
	h.expect(403, admin, "PATCH", ws+"/members/"+editor.id, map[string]any{"role": "owner"})
	// An admin can't change or remove an owner, nor hand out ownership.
	h.expect(403, admin, "PATCH", ws+"/members/"+x.a.id, map[string]any{"role": "viewer"})
	h.expect(403, admin, "PATCH", ws+"/members/"+x.a.id, map[string]any{"role": "admin"})
	h.expect(403, admin, "DELETE", ws+"/members/"+x.a.id, nil)
	// ...nor invite an owner, or invite at all if they are an editor.
	h.expect(403, admin, "POST", ws+"/invites", map[string]any{"email": "o@example.com", "role": "owner"})
	h.expect(403, editor, "POST", ws+"/invites", map[string]any{"email": "o@example.com", "role": "viewer"})
	// Viewers and editors can't remove each other either.
	h.expect(403, viewer, "DELETE", ws+"/members/"+editor.id, nil)
	h.expect(403, editor, "DELETE", ws+"/members/"+viewer.id, nil)

	// None of that changed anything.
	got := string(h.expect(200, x.a, "GET", ws+"/members", nil))
	for _, want := range []string{`"role":"owner"`, `"role":"admin"`, `"role":"editor"`, `"role":"viewer"`} {
		if strings.Count(got, want) != 1 {
			t.Errorf("roles changed: %s", got)
			break
		}
	}

	// What they MAY do works: an admin demotes an admin-level peer and an editor.
	h.expect(200, admin, "PATCH", ws+"/members/"+editor.id, map[string]any{"role": "viewer"})
	h.expect(204, admin, "DELETE", ws+"/members/"+viewer.id, nil)
	h.expect(204, editor, "DELETE", ws+"/members/"+editor.id, nil) // leaving
}

func TestTheLastOwnerCannotBeRemovedDemotedOrLeave(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := "/workspaces/" + x.wsA.ID
	co := h.newUser("coowner")
	h.me(co)
	tok := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": co.email, "role": "admin"})).Token
	h.expect(200, co, "POST", "/invites/accept", map[string]any{"token": tok})
	h.expect(200, x.a, "PATCH", ws+"/members/"+co.id, map[string]any{"role": "owner"}) // two owners now

	// With two owners, one can step down or leave...
	h.expect(200, co, "PATCH", ws+"/members/"+co.id, map[string]any{"role": "admin"})
	h.expect(200, x.a, "PATCH", ws+"/members/"+co.id, map[string]any{"role": "owner"})
	h.expect(204, co, "DELETE", ws+"/members/"+co.id, nil)

	// ...and the remaining one is the last: can't leave, step down, or be removed.
	for _, c := range []struct {
		who          user
		method, path string
		body         any
	}{
		{x.a, "DELETE", ws + "/members/" + x.a.id, nil},
		{x.a, "PATCH", ws + "/members/" + x.a.id, map[string]any{"role": "admin"}},
		{x.a, "PATCH", ws + "/members/" + x.a.id, map[string]any{"role": "viewer"}},
	} {
		b := h.expect(409, c.who, c.method, c.path, c.body)
		if !bytes.Contains(b, []byte("LAST_OWNER")) {
			t.Errorf("%s %s: %s", c.method, c.path, b)
		}
	}
	if n := h.count(`SELECT count(*) FROM workspace_members WHERE workspace_id = $1::uuid AND role = 'owner'`, x.wsA.ID); n != 1 {
		t.Errorf("owners = %d", n)
	}
}

// Concurrent removals/demotions can't leave a workspace without an owner.
func TestConcurrentOwnerRemovalsLeaveAnOwner(t *testing.T) {
	x := newTenants(t)
	h := x.h
	h.slowDown("workspace_members", "DELETE", 300*time.Millisecond) // both leavers are inside their transactions at once
	ws := "/workspaces/" + x.wsA.ID
	co := h.newUser("coowner2")
	h.me(co)
	tok := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": co.email, "role": "admin"})).Token
	h.expect(200, co, "POST", "/invites/accept", map[string]any{"token": tok})
	h.expect(200, x.a, "PATCH", ws+"/members/"+co.id, map[string]any{"role": "owner"})

	var wg sync.WaitGroup
	for _, u := range []user{x.a, co} {
		wg.Add(1)
		go func() { // both owners leave at the same moment
			defer wg.Done()
			req, _ := http.NewRequest("DELETE", h.srv.URL+ws+"/members/"+u.id, nil)
			req.Header.Set("X-Test-User", u.id+"|"+u.email)
			if res, err := http.DefaultClient.Do(req); err == nil {
				res.Body.Close()
			}
		}()
	}
	wg.Wait()
	if n := h.count(`SELECT count(*) FROM workspace_members WHERE workspace_id = $1::uuid AND role = 'owner'`, x.wsA.ID); n != 1 {
		t.Errorf("after two owners left at once, owners = %d, want exactly 1", n)
	}
}

// 20 first requests of one new user at once: one workspace, one owner membership.
func TestTwentyParallelFirstRequestsCreateOneWorkspace(t *testing.T) {
	h := newHarness(t)
	h.slowDown("workspaces", "INSERT", 60*time.Millisecond) // all 20 transactions overlap
	u := h.newUser("racer")
	var wg sync.WaitGroup
	statuses := make([]int, 20)
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
	checks := map[string]int{
		`SELECT count(*) FROM workspaces WHERE owner_id = $1::uuid`:                                                  1,
		`SELECT count(*) FROM workspace_members WHERE user_id = $1::uuid`:                                            1,
		`SELECT count(*) FROM workspace_members WHERE user_id = $1::uuid AND role = 'owner'`:                         1,
		`SELECT count(*) FROM environments e JOIN workspaces w ON w.id = e.workspace_id WHERE w.owner_id = $1::uuid`: 3,
		`SELECT count(*) FROM audit_logs WHERE actor_id = $1::uuid AND action = 'workspace.create'`:                  1,
	}
	for q, want := range checks {
		if got := h.count(q, u.id); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
	// Every answer names the same workspace.
	first := h.me(u).Workspaces
	if len(first) != 1 {
		t.Errorf("workspaces = %d", len(first))
	}
}
