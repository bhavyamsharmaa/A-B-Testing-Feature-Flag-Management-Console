package integration

import (
	"context"
	"testing"
	"time"
)

// Removing a member takes their workspace access away at once (membership is
// checked on every request, so an unexpired JWT does not help them), but SDK
// keys belong to the WORKSPACE, not to the person who created them: a key they
// made, and any stream open on it, keeps working until an admin revokes it.
// This pins that behaviour so it is a decision, not an accident.
func TestRemovedMembersLoseAccessButTheirKeysStayTheWorkspaces(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := "/workspaces/" + x.wsA.ID
	leaver := h.newUser("leaver")
	h.me(leaver)
	tok := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": leaver.email, "role": "admin"})).Token
	h.expect(200, leaver, "POST", "/invites/accept", map[string]any{"token": tok})

	theirKey := h.newSDKKey(leaver, x.devA)
	stream := h.openStream(theirKey)
	h.expect(200, leaver, "GET", "/environments/"+x.devA+"/sdk-keys", nil)

	h.expect(204, x.a, "DELETE", ws+"/members/"+leaver.id, nil)

	// Their own requests: gone, immediately.
	for _, p := range []string{"/environments/" + x.devA + "/flags", "/environments/" + x.devA + "/sdk-keys", ws + "/members"} {
		h.expect(404, leaver, "GET", p, nil)
	}
	if me := h.me(leaver); len(me.Workspaces) != 1 || me.Workspaces[0].ID == x.wsA.ID {
		t.Errorf("the removed member still sees the workspace: %+v", me.Workspaces)
	}

	// The key they created still belongs to the workspace.
	if st := h.evaluateStatus(theirKey, secretFlag); st != 200 {
		t.Errorf("the workspace's key stopped working when its creator left: %d", st)
	}
	select {
	case <-stream.closed:
		t.Error("a stream on the workspace's key was closed by a member's removal")
	case <-time.After(500 * time.Millisecond): // several stream re-check intervals
	}
	// An admin who wants it gone revokes it, and then it is gone everywhere.
	var keyID string
	if err := h.pool.QueryRow(context.Background(), `SELECT id::text FROM api_keys WHERE workspace_id = $1::uuid AND key_prefix = $2`, x.wsA.ID, theirKey[:len("hsdk_")+8]).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	h.expect(204, x.a, "DELETE", "/environments/"+x.devA+"/sdk-keys/"+keyID, nil)
	select {
	case <-stream.closed:
	case <-time.After(3 * time.Second):
		t.Error("revoking the key did not close its stream")
	}
	if st := h.evaluateStatus(theirKey, secretFlag); st != 401 {
		t.Errorf("revoked key: %d", st)
	}
}
