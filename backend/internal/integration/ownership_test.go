package integration

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/workspaces"
	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/server"
)

type ownWorld struct {
	*tenants
	clock *liveClock
}

func newOwnWorld(t *testing.T) *ownWorld {
	t.Helper()
	clock := &liveClock{}
	x := newTenantsWith(t, func(d *server.Deps) {
		d.Clock = clock.now
		d.Limiter = ratelimit.New(ratelimit.Config{Rules: map[string]ratelimit.Rule{
			ratelimit.ClassInvite: {PerSecond: 1000, Burst: 1000}, ratelimit.ClassAccept: {PerSecond: 1000, Burst: 1000},
			ratelimit.ClassWorkspace: {PerSecond: 1000, Burst: 1000}, ratelimit.ClassMe: {PerSecond: 1000, Burst: 1000},
		}})
	})
	return &ownWorld{tenants: x, clock: clock}
}

// join invites a new user to workspace ws (owned by owner) with a role and has them accept.
func (w *ownWorld) join(t *testing.T, owner user, ws, name, role string) user {
	t.Helper()
	u := w.h.newUser(name)
	w.h.me(u)
	tok := decode[struct{ Token string }](t, w.h.expect(201, owner, "POST", "/workspaces/"+ws+"/invites", map[string]any{"email": u.email, "role": role})).Token
	w.h.expect(200, u, "POST", "/invites/accept", map[string]any{"token": tok})
	return u
}

func (w *ownWorld) offer(t *testing.T, owner user, ws, to string) string {
	t.Helper()
	return decode[struct{ Offer struct{ ID string } }](t, w.h.expect(201, owner, "POST", "/workspaces/"+ws+"/ownership-offers", map[string]any{"userId": to})).Offer.ID
}

func (w *ownWorld) role(ws, userID string) string {
	var r string
	_ = w.h.pool.QueryRow(context.Background(), `SELECT role::text FROM workspace_members WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, ws, userID).Scan(&r)
	return r
}

func (w *ownWorld) owners(ws string) int {
	return w.h.count(`SELECT count(*) FROM workspace_members WHERE workspace_id = $1::uuid AND role = 'owner'`, ws)
}

func TestAnOwnerHandsAWorkspaceToAnAdmin(t *testing.T) {
	w := newOwnWorld(t)
	ws := w.wsA.ID
	ada := w.join(t, w.a, ws, "ada", "admin")
	id := w.offer(t, w.a, ws, ada.id)

	// the target is told in /me, the owner sees it in settings
	me := decode[struct {
		OwnershipOffers []struct{ ID, WorkspaceName, FromEmail string }
	}](t, w.h.expect(200, ada, "GET", "/me", nil))
	if len(me.OwnershipOffers) != 1 || me.OwnershipOffers[0].ID != id || me.OwnershipOffers[0].FromEmail != w.a.email {
		t.Fatalf("/me: %+v", me.OwnershipOffers)
	}
	open := decode[struct{ Offer *struct{ ID, ToEmail string } }](t, w.h.expect(200, w.a, "GET", "/workspaces/"+ws+"/ownership-offers", nil))
	if open.Offer == nil || open.Offer.ID != id || open.Offer.ToEmail != ada.email {
		t.Fatalf("owner's view: %+v", open.Offer)
	}
	// the offer changes nothing until it is accepted
	if w.role(ws, w.a.id) != "owner" || w.role(ws, ada.id) != "admin" {
		t.Fatal("roles changed by an offer")
	}

	w.h.expect(200, ada, "POST", "/ownership-offers/"+id+"/accept", nil)
	if w.role(ws, ada.id) != "owner" || w.role(ws, w.a.id) != "admin" || w.owners(ws) != 1 {
		t.Fatalf("roles after: alice %s, ada %s, owners %d", w.role(ws, w.a.id), w.role(ws, ada.id), w.owners(ws))
	}
	if got := w.h.dbString2(`SELECT owner_id::text FROM workspaces WHERE id = $1::uuid`, ws); got != ada.id {
		t.Errorf("workspaces.owner_id = %s, want the new owner", got)
	}
	for _, action := range []string{"ownership.offer", "ownership.accept"} {
		if n := w.h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = $2`, ws, action); n != 1 {
			t.Errorf("%s audit entries: %d", action, n)
		}
	}
	// no longer listed; cannot be accepted twice
	if me := decode[struct{ OwnershipOffers []struct{ ID string } }](t, w.h.expect(200, ada, "GET", "/me", nil)); len(me.OwnershipOffers) != 0 {
		t.Errorf("a used offer is still listed")
	}
	w.h.expect(409, ada, "POST", "/ownership-offers/"+id+"/accept", nil)
	// the new owner has an owner's powers, the old one an admin's
	w.h.expect(201, ada, "POST", "/workspaces/"+ws+"/ownership-offers", map[string]any{"userId": w.a.id})
	if st, _ := w.h.call(w.a, "PATCH", "/workspaces/"+ws+"/members/"+ada.id, map[string]any{"role": "editor"}); st != 403 {
		t.Errorf("the old owner demoted the new one: %d", st)
	}
	// the old owner may now leave; the new (last) owner may not
	w.h.expect(204, w.a, "DELETE", "/workspaces/"+ws+"/members/"+w.a.id, nil)
	if st, b := w.h.call(ada, "DELETE", "/workspaces/"+ws+"/members/"+ada.id, nil); st != 409 || !bytes.Contains(b, []byte("LAST_OWNER")) {
		t.Errorf("the last owner left: %d %s", st, b)
	}
}

func (h *harness) dbString2(query string, args ...any) string { return h.dbStringOne(query, args...) }

func TestOnlyAnOwnerOffersAndOnlyToAnAdmin(t *testing.T) {
	w := newOwnWorld(t)
	ws := w.wsA.ID
	adm := w.join(t, w.a, ws, "adm", "admin")
	ed := w.join(t, w.a, ws, "ed", "editor")
	vw := w.join(t, w.a, ws, "vw", "viewer")
	base := "/workspaces/" + ws + "/ownership-offers"
	// an admin is not an owner
	if st, _ := w.h.call(adm, "POST", base, map[string]any{"userId": adm.id}); st != 403 {
		t.Errorf("an admin offering: %d", st)
	}
	for name, c := range map[string]struct {
		target string
		want   int
		code   string
	}{
		"an editor":     {ed.id, 409, "TARGET_NOT_ADMIN"},
		"a viewer":      {vw.id, 409, "TARGET_NOT_ADMIN"},
		"oneself":       {w.a.id, 400, "INVALID_REQUEST"},
		"a stranger":    {w.b.id, 404, "MEMBER_NOT_FOUND"},
		"not a uuid":    {"alice", 400, "INVALID_REQUEST"},
		"another owner": {"", 0, ""},
	} {
		if c.want == 0 {
			continue
		}
		st, b := w.h.call(w.a, "POST", base, map[string]any{"userId": c.target})
		if st != c.want || !bytes.Contains(b, []byte(c.code)) {
			t.Errorf("offer to %s: %d %s, want %d %s", name, st, b, c.want, c.code)
		}
	}
	if n := w.h.count(`SELECT count(*) FROM ownership_offers`); n != 0 {
		t.Fatalf("%d offers exist after refusals", n)
	}
	// session only: an API token, even an admin's, cannot
	tok := decode[struct{ Plaintext string }](t, w.h.expect(201, w.a, "POST", "/workspaces/"+ws+"/api-tokens", map[string]any{"name": "ci", "role": "admin"})).Plaintext
	for _, c := range []struct{ m, p string }{{"POST", base}, {"GET", base}, {"DELETE", base + "/11111111-1111-4111-8111-111111111111"}, {"POST", "/ownership-offers/11111111-1111-4111-8111-111111111111/accept"}} {
		st, _, b := w.h.tokenCall(tok, c.m, c.p, map[string]any{"userId": adm.id})
		if st != 403 || !bytes.Contains(b, []byte("TOKEN_NOT_ALLOWED")) {
			t.Errorf("%s %s with a token: %d %s", c.m, c.p, st, b)
		}
	}
	// one open offer at a time
	id := w.offer(t, w.a, ws, adm.id)
	if st, b := w.h.call(w.a, "POST", base, map[string]any{"userId": adm.id}); st != 409 || !bytes.Contains(b, []byte("OFFER_EXISTS")) {
		t.Errorf("a second open offer: %d %s", st, b)
	}
	// cancelled: gone, cannot be accepted, and a new one can be made
	w.h.expect(204, w.a, "DELETE", base+"/"+id, nil)
	if st, b := w.h.call(adm, "POST", "/ownership-offers/"+id+"/accept", nil); st != 409 || !bytes.Contains(b, []byte("OFFER_NOT_OPEN")) {
		t.Errorf("accepting a cancelled offer: %d %s", st, b)
	}
	w.h.expect(404, w.a, "DELETE", base+"/"+id, nil)
	id2 := w.offer(t, w.a, ws, adm.id)
	// only the addressee can answer
	for _, u := range []user{w.a, w.b, ed} {
		if st, _ := w.h.call(u, "POST", "/ownership-offers/"+id2+"/accept", nil); st != 404 {
			t.Errorf("%s accepting: %d, want 404", u.email, st)
		}
		if st, _ := w.h.call(u, "POST", "/ownership-offers/"+id2+"/decline", nil); st != 404 {
			t.Errorf("%s declining: %d, want 404", u.email, st)
		}
	}
	// declined
	w.h.expect(204, adm, "POST", "/ownership-offers/"+id2+"/decline", nil)
	if w.role(ws, w.a.id) != "owner" {
		t.Error("declining changed roles")
	}
	for _, action := range []string{"ownership.offer", "ownership.cancel", "ownership.decline"} {
		if n := w.h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = $2`, ws, action); n < 1 {
			t.Errorf("no %s audit entry", action)
		}
	}
}

func TestAnOfferExpiresAndIsAudited(t *testing.T) {
	w := newOwnWorld(t)
	ws := w.wsA.ID
	ada := w.join(t, w.a, ws, "ada", "admin")
	id := w.offer(t, w.a, ws, ada.id)
	w.clock.advance(6*24*time.Hour + 23*time.Hour)
	if me := decode[struct{ OwnershipOffers []struct{ ID string } }](t, w.h.expect(200, ada, "GET", "/me", nil)); len(me.OwnershipOffers) != 1 {
		t.Fatal("the offer should still be open before its time")
	}
	w.clock.advance(2 * time.Hour) // past seven days
	if me := decode[struct{ OwnershipOffers []struct{ ID string } }](t, w.h.expect(200, ada, "GET", "/me", nil)); len(me.OwnershipOffers) != 0 {
		t.Error("an expired offer is still listed")
	}
	if open := decode[struct{ Offer *struct{ ID string } }](t, w.h.expect(200, w.a, "GET", "/workspaces/"+ws+"/ownership-offers", nil)); open.Offer != nil {
		t.Error("an expired offer is shown as open to the owner")
	}
	st, b := w.h.call(ada, "POST", "/ownership-offers/"+id+"/accept", nil)
	if st != 410 || !bytes.Contains(b, []byte("OFFER_EXPIRED")) {
		t.Fatalf("accepting an expired offer: %d %s", st, b)
	}
	if w.role(ws, w.a.id) != "owner" || w.role(ws, ada.id) != "admin" {
		t.Error("an expired offer changed roles")
	}
	if got := w.h.dbString2(`SELECT status FROM ownership_offers WHERE id = $1::uuid`, id); got != "expired" {
		t.Errorf("status %s", got)
	}
	if n := w.h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'ownership.expire'`, ws); n != 1 {
		t.Errorf("expire audit entries: %d", n)
	}
	// a new offer can be made at once
	w.offer(t, w.a, ws, ada.id)

	// the sweep (the purge job's step) expires offers nobody touched, and audits them
	other := w.join(t, w.a, ws, "bea", "admin")
	w.h.expect(204, w.a, "DELETE", "/workspaces/"+ws+"/ownership-offers/"+w.h.dbString2(`SELECT id::text FROM ownership_offers WHERE status = 'pending'`), nil)
	w.offer(t, w.a, ws, other.id)
	w.clock.advance(8 * 24 * time.Hour)
	err := pgx.BeginFunc(context.Background(), w.h.pool, func(tx pgx.Tx) error {
		n, err := workspacesExpire(tx, w.clock.now())
		if err != nil || n != 1 {
			return fmt.Errorf("swept %d: %v", n, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := w.h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'ownership.expire'`, ws); n != 2 {
		t.Errorf("expire audit entries after the sweep: %d", n)
	}
}

func TestAcceptanceChecksEverythingAgain(t *testing.T) {
	w := newOwnWorld(t)
	ws := w.wsA.ID
	base := "/workspaces/" + ws

	// the target is demoted after the offer
	a1 := w.join(t, w.a, ws, "demoted", "admin")
	id := w.offer(t, w.a, ws, a1.id)
	w.h.expect(200, w.a, "PATCH", base+"/members/"+a1.id, map[string]any{"role": "editor"})
	if st, b := w.h.call(a1, "POST", "/ownership-offers/"+id+"/accept", nil); st != 409 || !bytes.Contains(b, []byte("OFFER_NO_LONGER_VALID")) {
		t.Errorf("target demoted: %d %s", st, b)
	}
	if w.role(ws, a1.id) != "editor" || w.role(ws, w.a.id) != "owner" {
		t.Error("roles changed by a refused acceptance")
	}
	w.h.expect(204, w.a, "DELETE", base+"/ownership-offers/"+id, nil)

	// the target is removed after the offer
	a2 := w.join(t, w.a, ws, "removed", "admin")
	id = w.offer(t, w.a, ws, a2.id)
	w.h.expect(204, w.a, "DELETE", base+"/members/"+a2.id, nil)
	if st, _ := w.h.call(a2, "POST", "/ownership-offers/"+id+"/accept", nil); st != 409 {
		t.Errorf("target removed: %d", st)
	}
	if w.owners(ws) != 1 || w.role(ws, w.a.id) != "owner" {
		t.Error("ownership changed")
	}
	w.h.expect(204, w.a, "DELETE", base+"/ownership-offers/"+id, nil)

	// the offerer is no longer an owner after the offer (a second owner demoted them)
	a3 := w.join(t, w.a, ws, "third", "admin")
	co := w.join(t, w.a, ws, "coowner", "admin")
	w.h.expect(200, w.a, "PATCH", base+"/members/"+co.id, map[string]any{"role": "owner"})
	id = w.offer(t, w.a, ws, a3.id)
	w.h.expect(200, co, "PATCH", base+"/members/"+w.a.id, map[string]any{"role": "editor"})
	if st, b := w.h.call(a3, "POST", "/ownership-offers/"+id+"/accept", nil); st != 409 || !bytes.Contains(b, []byte("OFFER_NO_LONGER_VALID")) {
		t.Errorf("offerer no longer owner: %d %s", st, b)
	}
	if w.role(ws, a3.id) != "admin" {
		t.Error("the target was promoted by a refused acceptance")
	}
}

func TestTheOwnedWorkspacesLimitAppliesToTheNewOwner(t *testing.T) {
	w := newOwnWorld(t)
	ws := w.wsA.ID
	ada := w.join(t, w.a, ws, "ada", "admin")
	// ada already owns the most she may (her personal workspace and four more)
	for i := 0; i < 4; i++ {
		w.h.expect(201, ada, "POST", "/workspaces", map[string]any{"name": fmt.Sprintf("ada %d", i)})
	}
	if n := w.h.count(`SELECT count(*) FROM workspaces WHERE owner_id = $1::uuid`, ada.id); n != 5 {
		t.Fatalf("setup: ada owns %d", n)
	}
	id := w.offer(t, w.a, ws, ada.id)
	st, b := w.h.call(ada, "POST", "/ownership-offers/"+id+"/accept", nil)
	if st != 409 || !bytes.Contains(b, []byte("QUOTA_EXCEEDED")) || !strings.Contains(string(b), "5") {
		t.Fatalf("accepting a sixth workspace: %d %s", st, b)
	}
	if w.role(ws, ada.id) != "admin" || w.role(ws, w.a.id) != "owner" || w.h.count(`SELECT count(*) FROM workspaces WHERE owner_id = $1::uuid`, ada.id) != 5 {
		t.Error("the limit was exceeded silently")
	}
}

// An owner and an admin racing: the admin accepts while the owner demotes them. Whatever order the
// database picks, the workspace always has exactly one owner and the outcomes agree with each other.
func TestAConcurrentDemotionDuringATransferNeverLeavesNoOwner(t *testing.T) {
	w := newOwnWorld(t)
	transferred, refused := 0, 0
	for i := 0; i < 20; i++ {
		owner := w.h.newUser(fmt.Sprintf("race-owner-%d", i))
		ws := w.h.me(owner).Workspaces[0].ID
		target := w.join(t, owner, ws, fmt.Sprintf("race-admin-%d", i), "admin")
		id := w.offer(t, owner, ws, target.id)

		var wg sync.WaitGroup
		var acceptStatus, demoteStatus int
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			acceptStatus, _ = w.h.call(target, "POST", "/ownership-offers/"+id+"/accept", nil)
		}()
		go func() {
			defer wg.Done()
			<-start
			time.Sleep(time.Duration(i%7) * 3 * time.Millisecond) // spread the race over both orders
			demoteStatus, _ = w.h.call(owner, "PATCH", "/workspaces/"+ws+"/members/"+target.id, map[string]any{"role": "editor"})
		}()
		close(start)
		wg.Wait()

		if n := w.owners(ws); n != 1 {
			t.Fatalf("round %d: %d owners (accept %d, demote %d)", i, n, acceptStatus, demoteStatus)
		}
		switch {
		case acceptStatus == 200:
			transferred++
			// the transfer won: the old owner is an admin now, so the demotion can only have come first (200) and been
			// refused afterwards... the only consistent world is: accept 200 and the target is the owner
			if w.role(ws, target.id) != "owner" || w.role(ws, owner.id) != "admin" {
				t.Fatalf("round %d: accept 200 but roles are %s/%s", i, w.role(ws, owner.id), w.role(ws, target.id))
			}
			if demoteStatus == 200 {
				t.Fatalf("round %d: the target was demoted and also promoted", i)
			}
		default:
			refused++
			if acceptStatus != 409 && acceptStatus != 404 {
				t.Fatalf("round %d: accept %d", i, acceptStatus)
			}
			if w.role(ws, owner.id) != "owner" || w.role(ws, target.id) != "editor" || demoteStatus != 200 {
				t.Fatalf("round %d: accept refused (%d) but roles are %s/%s, demote %d", i, acceptStatus, w.role(ws, owner.id), w.role(ws, target.id), demoteStatus)
			}
		}
	}
	t.Logf("20 races: %d transfers won, %d demotions won", transferred, refused)
}

// The database's own guarantee, underneath the application's: promote-then-demote passes, demote alone cannot commit.
func TestTheLastOwnerTriggerAcceptsAHandoverAndRefusesAnEmptyingDemotion(t *testing.T) {
	w := newOwnWorld(t)
	ws := w.wsA.ID
	ada := w.join(t, w.a, ws, "ada", "admin")
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, w.h.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE workspace_members SET role = 'owner' WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, ws, ada.id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE workspace_members SET role = 'admin' WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, ws, w.a.id)
		return err
	})
	if err != nil {
		t.Fatalf("a promote-then-demote handover was refused: %v", err)
	}
	err = pgx.BeginFunc(ctx, w.h.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE workspace_members SET role = 'admin' WHERE workspace_id = $1::uuid AND user_id = $2::uuid`, ws, ada.id)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "must keep at least one owner") {
		t.Fatalf("a demotion that leaves no owner committed: %v", err)
	}
	if w.owners(ws) != 1 {
		t.Errorf("%d owners", w.owners(ws))
	}
}

func workspacesExpire(tx pgx.Tx, now time.Time) (int, error) {
	return workspaces.ExpireOffers(context.Background(), tx, now)
}
