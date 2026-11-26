package integration

import (
	"bytes"
	"sync"
	"testing"

	"helios/backend/internal/platform/auth"
	"helios/backend/internal/server"
)

func (h *harness) workspaceRows(userID string) (workspaces, memberships, audits int) {
	h.t.Helper()
	return h.count(`SELECT count(*) FROM workspaces WHERE owner_id = $1::uuid`, userID),
		h.count(`SELECT count(*) FROM workspace_members WHERE user_id = $1::uuid`, userID),
		h.count(`SELECT count(*) FROM audit_logs WHERE actor_id = $1::uuid`, userID)
}

// A user whose token says the email is not confirmed gets no workspace, can't
// create one and can't accept an invite; once confirmed, the same user can.
func TestUnconfirmedEmailCannotBootstrapOrAcceptInvites(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := "/workspaces/" + x.wsA.ID

	u := h.newUser("unconfirmed")
	u.status = "unconfirmed"
	inv := decode[struct {
		Invite struct{ ID string }
		Token  string
	}](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": u.email, "role": "editor"}))

	expectUnconfirmed := func(method, path string, body any) {
		t.Helper()
		b := h.expect(403, u, method, path, body)
		if !bytes.Contains(b, []byte(`"code":"EMAIL_NOT_CONFIRMED"`)) || !bytes.Contains(b, []byte("confirm your email")) {
			t.Errorf("%s %s: %s", method, path, b)
		}
	}
	expectUnconfirmed("GET", "/me", nil)
	expectUnconfirmed("GET", "/workspaces", nil)
	expectUnconfirmed("POST", "/workspaces", map[string]any{"name": "sneaky"})
	expectUnconfirmed("POST", "/invites/accept", map[string]any{"token": inv.Token})
	expectUnconfirmed("POST", "/invites/accept", map[string]any{"inviteId": inv.Invite.ID})

	// Twenty at once, still nothing: no workspace, no membership, no audit row.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.call(u, "GET", "/me", nil)
		}()
	}
	wg.Wait()
	if w, m, a := h.workspaceRows(u.id); w+m+a != 0 {
		t.Errorf("an unconfirmed user left rows behind: %d workspaces, %d memberships, %d audit rows", w, m, a)
	}
	if n := h.count(`SELECT count(*) FROM workspace_invites WHERE email = $1 AND accepted_at IS NULL AND revoked_at IS NULL`, u.email); n != 1 {
		t.Errorf("the invite was consumed (%d open)", n)
	}
	// ...and the rest of the API gives them nothing either.
	h.expect(404, u, "GET", "/environments/"+x.devA+"/flags", nil)

	// Confirmed: the very same user now bootstraps and accepts.
	u.status = ""
	me := h.me(u)
	if len(me.Workspaces) != 1 || me.Workspaces[0].Role != "owner" {
		t.Fatalf("after confirming: %+v", me)
	}
	h.expect(200, u, "POST", "/invites/accept", map[string]any{"token": inv.Token})
	if w, _, _ := h.workspaceRows(u.id); w != 1 {
		t.Errorf("workspaces = %d", w)
	}
	h.expect(200, u, "GET", "/environments/"+x.devA+"/flags", nil)
}

// With no signal at all, "enforce" lets the user through (the project's tokens
// carry nothing to read) and "strict" refuses.
func TestUnknownEmailStatusDependsOnTheMode(t *testing.T) {
	enforce := newHarness(t)
	u := enforce.newUser("unknown1")
	u.status = "unknown"
	enforce.expect(200, u, "GET", "/me", nil)

	strict := newHarnessWith(t, func(d *server.Deps) { d.Email = auth.NewEmailPolicy(auth.EmailStrict, "", "", nil) })
	s := strict.newUser("unknown2")
	s.status = "unknown"
	strict.expect(403, s, "GET", "/me", nil)
	if w, m, _ := strict.workspaceRows(s.id); w+m != 0 {
		t.Error("strict mode created a workspace for an unconfirmed user")
	}
	s.status = ""
	strict.expect(200, s, "GET", "/me", nil)

	off := newHarnessWith(t, func(d *server.Deps) { d.Email = auth.NewEmailPolicy(auth.EmailOff, "", "", nil) })
	o := off.newUser("unconfirmed3")
	o.status = "unconfirmed"
	off.expect(200, o, "GET", "/me", nil)
}

// Where "Confirm email" can't be guaranteed, acceptance by the one-time link is
// the only way in that does not lean on the email being verified: INVITES_BY_ID=false
// turns the id path (and the open-invites list in /me) off.
func TestInvitesByIDCanBeSwitchedOff(t *testing.T) {
	h := newHarnessWith(t, func(d *server.Deps) { d.InvitesByID = false })
	owner, invitee := h.newUser("owner"), h.newUser("invitee")
	ws := h.me(owner).Workspaces[0]
	inv := decode[struct {
		Invite struct{ ID string }
		Token  string
	}](t, h.expect(201, owner, "POST", "/workspaces/"+ws.ID+"/invites", map[string]any{"email": invitee.email, "role": "viewer"}))

	if me := h.me(invitee); len(me.Invites) != 0 {
		t.Errorf("/me lists open invites although acceptance by id is off: %+v", me.Invites)
	}
	b := h.expect(403, invitee, "POST", "/invites/accept", map[string]any{"inviteId": inv.Invite.ID})
	if !bytes.Contains(b, []byte("INVITE_LINK_REQUIRED")) {
		t.Errorf("body %s", b)
	}
	if n := h.count(`SELECT count(*) FROM workspace_members WHERE user_id = $1::uuid AND workspace_id = $2::uuid`, invitee.id, ws.ID); n != 0 {
		t.Error("the invite was accepted by id")
	}
	h.expect(200, invitee, "POST", "/invites/accept", map[string]any{"token": inv.Token}) // the link still works
}
