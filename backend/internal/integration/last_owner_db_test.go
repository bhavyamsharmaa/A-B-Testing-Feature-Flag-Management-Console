package integration

import (
	"context"
	"strings"
	"testing"
)

// The application checks the last-owner rule, and so does the database
// (migration 0007). These tests go around the application with raw SQL.
func (h *harness) rawExec(sql string, args ...any) error {
	_, err := h.pool.Exec(context.Background(), sql, args...)
	return err
}

func (h *harness) ownersOf(wsID string) int {
	return h.count(`SELECT count(*) FROM workspace_members WHERE workspace_id = $1::uuid AND role = 'owner'`, wsID)
}

func mustRefuse(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s was allowed", what)
	} else if !strings.Contains(err.Error(), "must keep at least one owner") {
		t.Errorf("%s failed, but not because of the owner rule: %v", what, err)
	}
}

func TestDatabaseRefusesAWorkspaceWithoutAnOwner(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := x.wsA.ID
	b := h.newUser("second")
	h.me(b)
	tok := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", "/workspaces/"+ws+"/invites", map[string]any{"email": b.email, "role": "admin"})).Token
	h.expect(200, b, "POST", "/invites/accept", map[string]any{"token": tok})

	// Raw SQL, no application in the way: the only owner can't be removed or demoted.
	mustRefuse(t, "DELETE of the last owner", h.rawExec(`DELETE FROM workspace_members WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, ws, x.a.id))
	mustRefuse(t, "demoting the last owner to admin", h.rawExec(`UPDATE workspace_members SET role = 'admin' WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, ws, x.a.id))
	mustRefuse(t, "demoting the last owner to viewer", h.rawExec(`UPDATE workspace_members SET role = 'viewer' WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, ws, x.a.id))
	mustRefuse(t, "deleting every owner row at once", h.rawExec(`DELETE FROM workspace_members WHERE workspace_id = $1::uuid AND role = 'owner'`, ws))
	mustRefuse(t, "moving the owner row to another workspace id", h.rawExec(`UPDATE workspace_members SET workspace_id = $2::uuid WHERE workspace_id = $1::uuid AND user_id = $3::uuid`, ws, x.wsB.ID, x.a.id))
	if n := h.ownersOf(ws); n != 1 {
		t.Fatalf("owners = %d after the refused statements", n)
	}

	// Inside one transaction ownership can be handed over, in either order.
	if err := h.rawExec(`BEGIN;
		UPDATE workspace_members SET role = 'admin' WHERE workspace_id = '` + ws + `' AND user_id = '` + x.a.id + `';
		UPDATE workspace_members SET role = 'owner' WHERE workspace_id = '` + ws + `' AND user_id = '` + b.id + `';
		COMMIT;`); err != nil {
		t.Errorf("demote-then-promote in one transaction was refused: %v", err)
	}
	if err := h.rawExec(`BEGIN;
		UPDATE workspace_members SET role = 'owner' WHERE workspace_id = '` + ws + `' AND user_id = '` + x.a.id + `';
		UPDATE workspace_members SET role = 'admin' WHERE workspace_id = '` + ws + `' AND user_id = '` + b.id + `';
		COMMIT;`); err != nil {
		t.Errorf("promote-then-demote in one transaction was refused: %v", err)
	}
	// ...but a transaction that ends without an owner is refused at COMMIT, and rolled back.
	mustRefuse(t, "a transaction that demotes the owner and promotes nobody", h.rawExec(`BEGIN;
		UPDATE workspace_members SET role = 'viewer' WHERE workspace_id = '`+ws+`' AND user_id = '`+x.a.id+`';
		COMMIT;`))
	if n := h.ownersOf(ws); n != 1 {
		t.Fatalf("owners = %d", n)
	}

	// Things that are not "losing the last owner" keep working.
	if err := h.rawExec(`UPDATE workspace_members SET last_active_at = now() WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, ws, x.a.id); err != nil {
		t.Errorf("touching the owner row: %v", err)
	}
	if err := h.rawExec(`INSERT INTO workspace_members (user_id, workspace_id, role) VALUES ($1::uuid, $2::uuid, 'owner')
		ON CONFLICT (user_id, workspace_id) DO UPDATE SET last_active_at = now()`, x.a.id, ws); err != nil {
		t.Errorf("the accept-invite upsert on an owner: %v", err)
	}
	if err := h.rawExec(`DELETE FROM workspace_members WHERE workspace_id = $1::uuid AND role <> 'owner'`, ws); err != nil {
		t.Errorf("removing non-owners: %v", err)
	}
}

func TestDatabaseRuleAllowsDeletingWorkspacesAndAbandonedOnes(t *testing.T) {
	x := newTenants(t)
	h := x.h

	// Deleting a workspace removes its members through the cascade: allowed.
	if err := h.rawExec(`DELETE FROM workspaces WHERE id = $1::uuid`, x.wsB.ID); err != nil {
		t.Errorf("deleting a workspace with its owner: %v", err)
	}
	if n := h.count(`SELECT count(*) FROM workspace_members WHERE workspace_id = $1::uuid`, x.wsB.ID); n != 0 {
		t.Errorf("%d members survived the workspace", n)
	}

	// A user who is the only member of a workspace can be deleted (it is left abandoned, not ownerless).
	solo := h.newUser("solo")
	soloWS := h.me(solo).Workspaces[0].ID
	if err := h.rawExec(`DELETE FROM auth.users WHERE id = $1::uuid`, solo.id); err != nil {
		t.Errorf("deleting the only member's account: %v", err)
	}
	if n := h.count(`SELECT count(*) FROM workspaces WHERE id = $1::uuid`, soloWS); n != 1 {
		t.Error("the abandoned workspace should remain")
	}

	// A user who is the last owner of a workspace with other members can NOT be deleted.
	ws := x.wsA.ID
	m := h.newUser("member")
	h.me(m)
	tok := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", "/workspaces/"+ws+"/invites", map[string]any{"email": m.email, "role": "viewer"})).Token
	h.expect(200, m, "POST", "/invites/accept", map[string]any{"token": tok})
	mustRefuse(t, "deleting the account of the last owner of a workspace with members", h.rawExec(`DELETE FROM auth.users WHERE id = $1::uuid`, x.a.id))
	// After transferring ownership it works.
	h.expect(200, x.a, "PATCH", "/workspaces/"+ws+"/members/"+m.id, map[string]any{"role": "owner"})
	if err := h.rawExec(`DELETE FROM auth.users WHERE id = $1::uuid`, x.a.id); err != nil {
		t.Errorf("deleting an owner who is not the last: %v", err)
	}
	if h.ownersOf(ws) != 1 {
		t.Error("the workspace should keep its remaining owner")
	}
}
