package integration

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/platform/apitoken"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/platform/redisx"
	"helios/backend/internal/server"
)

// ---- helpers ------------------------------------------------------------------

type tokWorld struct {
	*tenants
	clock   *fakeClock
	staging string
	prod    string
}

// newTokWorld: two tenants, a controllable clock (starting now, so token expiry
// set by the database and read by the verifier agree), write limits out of the way.
func newTokWorld(t *testing.T, rules map[string]ratelimit.Rule, adjust ...func(*server.Deps)) *tokWorld {
	t.Helper()
	clock := &fakeClock{t: time.Now().UTC()}
	r := map[string]ratelimit.Rule{
		ratelimit.ClassTokenWrite: {PerSecond: 1000, Burst: 1000}, ratelimit.ClassScheduleWrite: {PerSecond: 1000, Burst: 1000},
		ratelimit.ClassSegmentWrite: {PerSecond: 1000, Burst: 1000},
	}
	for k, v := range rules {
		r[k] = v
	}
	x := newTenantsWith(t, func(d *server.Deps) {
		d.Clock = clock.now
		d.Limiter = ratelimit.New(ratelimit.Config{Rules: r, StreamsPerKey: 100})
		for _, a := range adjust {
			a(d)
		}
	})
	return &tokWorld{tenants: x, clock: clock, staging: envID(t, x.wsA, "staging"), prod: envID(t, x.wsA, "production")}
}

type minted struct {
	ID, Plaintext, Prefix string
}

func (w *tokWorld) mint(t *testing.T, u user, ws, name, role string, envs ...string) minted {
	t.Helper()
	body := map[string]any{"name": name, "role": role}
	if len(envs) > 0 {
		body["environments"] = envs
	}
	b := w.h.expect(201, u, "POST", "/workspaces/"+ws+"/api-tokens", body)
	r := decode[struct {
		Token     struct{ ID, Prefix string }
		Plaintext string
	}](t, b)
	return minted{ID: r.Token.ID, Plaintext: r.Plaintext, Prefix: r.Token.Prefix}
}

func (h *harness) tokenCall(token, method, path string, body any) (int, http.Header, []byte) {
	h.t.Helper()
	return h.tokenCallAt(h.srv.URL, token, method, path, body)
}

func (h *harness) tokenCallAt(base, token, method, path string, body any) (int, http.Header, []byte) {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	h.mu.Lock()
	h.bodies = append(h.bodies, b)
	h.mu.Unlock()
	return res.StatusCode, res.Header, b
}

func (w *tokWorld) tokExpect(t *testing.T, want int, token, method, path string, body any) []byte {
	t.Helper()
	st, _, b := w.h.tokenCall(token, method, path, body)
	if st != want {
		t.Fatalf("%s %s with a token: status %d, want %d; body %s", method, path, st, want, b)
	}
	return b
}

func (w *tokWorld) dbCount(query string, args ...any) int { return w.h.count(query, args...) }

const genericBody = `{"code":"UNAUTHORIZED","message":"invalid or expired token"}` + "\n"

// ---- the allowlist -------------------------------------------------------------------

// Every registered route is classified for tokens, and the map names no route that is gone.
func TestEveryRouteIsClassifiedForTokens(t *testing.T) {
	w := newTokWorld(t, nil)
	missing, stale := server.UnclassifiedRoutes(w.h.routes)
	if len(missing) > 0 || len(stale) > 0 {
		t.Fatalf("routes with no token classification (decide allowed or denied in server/tokenaccess.go): %v; classifications naming no route: %v", missing, stale)
	}
	for _, rt := range w.h.routes {
		if !server.TokenClassified(rt) {
			t.Errorf("%s %s is not classified", rt.Method, rt.Path)
		}
	}
	// The check itself works: a route added without a classification is reported, and is not token-allowed.
	future := append([]server.Route{}, w.h.routes...)
	future = append(future, server.Route{Method: "GET", Path: "/environments/{envId}/brand-new", Scope: server.ScopeEnv})
	if m, _ := server.UnclassifiedRoutes(future); len(m) != 1 || m[0] != "GET /environments/{envId}/brand-new" {
		t.Errorf("an unclassified route was not reported: %v", m)
	}
	if server.TokenClassified(server.Route{Method: "GET", Path: "/environments/{envId}/brand-new"}) {
		t.Error("an unknown route reads as classified")
	}
	// The allowlist is exactly what a pipeline needs: nothing that mints, manages or deletes.
	forbiddenForTokens := []string{"api-tokens", "/members", "/invites", "/sdk-keys", "/switch", "/internal/", "/me", "POST /workspaces\n"}
	for _, rt := range w.h.routes {
		if !rt.TokenAllowed {
			continue
		}
		label := rt.Method + " " + rt.Path
		for _, bad := range forbiddenForTokens {
			if strings.Contains(label+"\n", bad) && bad != "/me" {
				t.Errorf("%s is token-allowed", label)
			}
		}
		if rt.Method == "DELETE" && !strings.Contains(rt.Path, "/segments/") {
			t.Errorf("%s: a token may delete only segments", label)
		}
		if rt.Scope != server.ScopeEnv && rt.Scope != server.ScopeWorkspace {
			t.Errorf("%s: a %s route is token-allowed", label, rt.Scope)
		}
	}
}

// Over every registered route: a token reaches exactly the allowed ones; the rest are refused with
// 403 TOKEN_NOT_ALLOWED whatever its role; and a viewer-ceiling token is refused by role on allowed writes.
func TestTokenAllowlistAcrossEveryRoute(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	h.expect(201, w.a, "POST", segPath(w.wsA.ID), segReq("allow-seg", []string{"u"}))
	h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags/"+secretFlag+"/scheduled-changes", map[string]any{
		"idempotencyKey": "allow-1", "kind": "disable", "runAt": w.clock.now().Add(48 * time.Hour).Format(time.RFC3339)})
	var changeID string
	_ = h.pool.QueryRow(context.Background(), `SELECT id::text FROM scheduled_changes WHERE idempotency_key = 'allow-1'`).Scan(&changeID)
	adminTok := w.mint(t, w.a, w.wsA.ID, "all-routes", "admin")
	viewerTok := w.mint(t, w.a, w.wsA.ID, "read-only", "viewer")
	fill := func(path string) string {
		key := secretFlag
		if strings.Contains(path, "/experiments/") {
			key = "acme-secret-exp"
		}
		return strings.NewReplacer("{envId}", w.devA, "{wsId}", w.wsA.ID, "{userId}", w.a.id, "{inviteId}", "00000000-0000-4000-8000-000000000001",
			"{keyId}", "00000000-0000-4000-8000-000000000002", "{segKey}", "allow-seg", "{changeId}", changeID,
			"{tokenId}", adminTok.ID, "{key}", key).Replace(path)
	}
	allowed, denied := 0, 0
	for _, rt := range h.routes {
		if rt.Scope != server.ScopeEnv && rt.Scope != server.ScopeWorkspace && rt.Scope != server.ScopeSelf {
			continue
		}
		label := rt.Method + " " + rt.Path
		path := fill(rt.Path)
		st, _, body := h.tokenCall(adminTok.Plaintext, rt.Method, path, map[string]any{})
		if rt.TokenAllowed {
			allowed++
			if st == 401 || errCode(t, body) == "TOKEN_NOT_ALLOWED" {
				t.Errorf("%s is classified allowed but a token got %d %s", label, st, body)
			}
			// a viewer-ceiling token: reads pass, writes that need more are refused by role
			vst, _, vbody := h.tokenCall(viewerTok.Plaintext, rt.Method, path, map[string]any{})
			if rt.MinRole != "viewer" && vst != 403 {
				t.Errorf("%s as a viewer-ceiling token: %d %s, want 403 (needs %s)", label, vst, vbody, rt.MinRole)
			}
			if rt.MinRole == "viewer" && (vst == 401 || vst == 403) {
				t.Errorf("%s as a viewer-ceiling token: %d %s, but it is a read", label, vst, vbody)
			}
		} else {
			denied++
			if st != 403 || errCode(t, body) != "TOKEN_NOT_ALLOWED" {
				t.Errorf("%s is not allowed for tokens but a token got %d %s", label, st, body)
			}
		}
		// the same route with no credential at all is 401, never anything softer
		if st, _, _ := h.tokenCall("", rt.Method, path, nil); st != 401 {
			t.Errorf("%s without a token: %d", label, st)
		}
	}
	if allowed < 20 || denied < 15 {
		t.Errorf("only %d allowed and %d denied routes were exercised", allowed, denied)
	}
	t.Logf("%d token-allowed and %d token-denied control-plane routes exercised", allowed, denied)
	// non-control-plane routes ignore tokens: the health probe still answers, the SDK routes want their own key
	if st, _, _ := h.tokenCall(adminTok.Plaintext, "GET", "/healthz", nil); st != 200 {
		t.Errorf("/healthz: %d", st)
	}
	if st, _, _ := h.tokenCall(adminTok.Plaintext, "POST", "/evaluate", map[string]any{"context": map[string]any{"subjectKey": "u"}, "flagKeys": []string{"x"}}); st != 401 {
		t.Errorf("/evaluate with an API token as if it were an SDK key: %d", st)
	}
}

// ---- authority --------------------------------------------------------------------------

func TestEffectiveRoleIsTheLowerOfTheTokenAndItsCreator(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	admin := w.joinAs("adam", "admin")
	tok := w.mint(t, admin, w.wsA.ID, "adams-ci", "admin")
	flagPath := "/environments/" + w.devA + "/flags/" + secretFlag
	patch := map[string]any{"fallthroughVariationId": "off"}

	w.tokExpect(t, 200, tok.Plaintext, "PATCH", flagPath, patch) // admin creator, admin token: may edit

	// the creator is demoted to viewer: the token is capped AT ONCE (nothing cached about authority)
	h.expect(200, w.a, "PATCH", "/workspaces/"+w.wsA.ID+"/members/"+admin.id, map[string]any{"role": "viewer"})
	w.tokExpect(t, 200, tok.Plaintext, "GET", flagPath, nil)
	b := w.tokExpect(t, 403, tok.Plaintext, "PATCH", flagPath, patch)
	if !strings.Contains(errMessage(b), "viewer") {
		t.Errorf("the refusal does not say why: %s", b)
	}
	w.tokExpect(t, 403, tok.Plaintext, "POST", flagPath+"/kill", nil)
	w.tokExpect(t, 403, tok.Plaintext, "POST", segPath(w.wsA.ID), segReq("by-demoted", nil)) // workspace-level routes are capped too
	// ...and restored when the creator is: the token never held more than the person did
	h.expect(200, w.a, "PATCH", "/workspaces/"+w.wsA.ID+"/members/"+admin.id, map[string]any{"role": "editor"})
	w.tokExpect(t, 200, tok.Plaintext, "PATCH", flagPath, patch)
	w.tokExpect(t, 201, tok.Plaintext, "POST", segPath(w.wsA.ID), segReq("by-editor", nil))
	w.tokExpect(t, 403, tok.Plaintext, "PATCH", "/environments/"+w.prod+"/flags/"+secretFlag, patch) // an editor cannot change production
	h.expect(200, w.a, "PATCH", "/workspaces/"+w.wsA.ID+"/members/"+admin.id, map[string]any{"role": "admin"})
	w.tokExpect(t, 200, tok.Plaintext, "PATCH", "/environments/"+w.prod+"/flags/"+secretFlag, patch)

	// the creator is REMOVED: every request with the token fails like a stranger's, on environment and workspace routes alike
	h.expect(204, w.a, "DELETE", "/workspaces/"+w.wsA.ID+"/members/"+admin.id, nil)
	for _, c := range []struct{ method, path, code string }{
		{"GET", flagPath, "ENVIRONMENT_NOT_FOUND"},
		{"PATCH", flagPath, "ENVIRONMENT_NOT_FOUND"},
		{"GET", "/environments/" + w.devA + "/audit-logs", "ENVIRONMENT_NOT_FOUND"},
		{"GET", segPath(w.wsA.ID), "WORKSPACE_NOT_FOUND"},
		{"GET", "/workspaces/" + w.wsA.ID + "/environments", "WORKSPACE_NOT_FOUND"},
	} {
		st, _, body := h.tokenCall(tok.Plaintext, c.method, c.path, map[string]any{})
		if st != 404 || errCode(t, body) != c.code {
			t.Errorf("removed creator, %s %s: %d %s, want 404 %s", c.method, c.path, st, body, c.code)
		}
	}
	// the token is still "valid" as a credential; it has simply no authority left, and the flag was not changed by it
	if got := w.dbCount(`SELECT count(*) FROM api_tokens WHERE id = $1::uuid AND revoked_at IS NULL`, tok.ID); got != 1 {
		t.Error("removing the creator should not itself rewrite the token")
	}
}

func TestATokenCanNeverExceedItsCreatorOrBeRaised(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	admin := w.joinAs("adam", "admin")
	editor := w.joinAs("eddie", "editor")
	viewer := w.joinAs("vera", "viewer")
	create := func(u user, role string) (int, []byte) {
		return h.call(u, "POST", "/workspaces/"+w.wsA.ID+"/api-tokens", map[string]any{"name": "t-" + role, "role": role})
	}
	// only admins and owners create tokens
	for _, u := range []user{editor, viewer} {
		for _, role := range []string{"viewer", "editor", "admin"} {
			if st, b := create(u, role); st != 403 {
				t.Errorf("%s creating a %s token: %d %s", u.email, role, st, b)
			}
		}
	}
	// an admin and an owner may create up to admin; "owner" is not a token role at all
	for _, u := range []user{admin, w.a} {
		for _, role := range []string{"viewer", "editor", "admin"} {
			if st, b := create(u, role); st != 201 {
				t.Errorf("%s creating a %s token: %d %s", u.email, role, st, b)
			}
		}
		if st, b := create(u, "owner"); st != 400 {
			t.Errorf("a token with the owner role: %d %s", st, b)
		}
		if st, _ := create(u, ""); st != 400 {
			t.Errorf("a token with no role: %d", st)
		}
	}
	// there is no way to change a token's role: no such route, for a session or for the token itself
	tok := w.mint(t, w.a, w.wsA.ID, "fixed", "viewer")
	for _, u := range []user{w.a, admin} {
		for _, m := range []string{"PATCH", "PUT"} {
			if st, _ := h.call(u, m, "/workspaces/"+w.wsA.ID+"/api-tokens/"+tok.ID, map[string]any{"role": "admin"}); st != 405 {
				t.Errorf("%s on a token: %d, want 405 (a token's role cannot be raised: create a new one)", m, st)
			}
		}
	}
	if st, _, _ := h.tokenCall(tok.Plaintext, "PATCH", "/workspaces/"+w.wsA.ID+"/api-tokens/"+tok.ID, map[string]any{"role": "admin"}); st != 405 && st != 403 {
		t.Errorf("the token raising itself: %d", st)
	}
	var role string
	_ = h.pool.QueryRow(t.Context(), `SELECT role FROM api_tokens WHERE id = $1::uuid`, tok.ID).Scan(&role)
	if role != "viewer" {
		t.Errorf("the token's role is now %q", role)
	}
	// the database says so too: only these three roles exist
	if _, err := h.pool.Exec(t.Context(), `UPDATE api_tokens SET role = 'owner' WHERE id = $1::uuid`, tok.ID); err == nil {
		t.Error("the database accepted an owner token")
	}
}

func TestATokenLimitedToEnvironmentsSeesNoOthers(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags", boolFlag("lim"))
	h.expect(200, w.a, "PATCH", "/environments/"+w.staging+"/flags/lim", map[string]any{"enabled": true})
	h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/lim", map[string]any{"enabled": true, "rollout": map[string]any{"on": 10000, "off": 90000}})
	h.expect(201, w.a, "POST", segPath(w.wsA.ID), segReq("lim-seg", nil))
	for _, env := range []string{w.devA, w.staging, w.prod} {
		h.expect(200, w.a, "PATCH", "/environments/"+env+"/flags/lim", map[string]any{"targetingRules": []map[string]any{{"clauses": []map[string]any{inSeg("lim-seg")}, "variationId": "on"}}})
	}
	tok := w.mint(t, w.a, w.wsA.ID, "dev-only", "admin", "dev")

	// its own environment works
	w.tokExpect(t, 200, tok.Plaintext, "GET", "/environments/"+w.devA+"/flags/lim", nil)
	w.tokExpect(t, 200, tok.Plaintext, "PATCH", "/environments/"+w.devA+"/flags/lim", map[string]any{"fallthroughVariationId": "off"})
	// every other environment is "not found", on every kind of route, lists and the audit log included
	for _, env := range []string{w.staging, w.prod} {
		for _, c := range []struct{ method, path string }{
			{"GET", "/environments/" + env + "/flags"}, {"GET", "/environments/" + env + "/flags/lim"},
			{"PATCH", "/environments/" + env + "/flags/lim"}, {"POST", "/environments/" + env + "/flags/lim/kill"},
			{"GET", "/environments/" + env + "/audit-logs"}, {"GET", "/environments/" + env + "/scheduled-changes"},
			{"GET", "/environments/" + env + "/experiments"}, {"POST", "/environments/" + env + "/flags/lim/promotions/preview"},
			{"POST", "/environments/" + env + "/flags/lim/scheduled-changes"},
		} {
			st, _, body := h.tokenCall(tok.Plaintext, c.method, c.path, map[string]any{"sourceEnvironment": "dev"})
			if st != 404 || errCode(t, body) != "ENVIRONMENT_NOT_FOUND" {
				t.Errorf("%s %s: %d %s, want 404 ENVIRONMENT_NOT_FOUND", c.method, c.path, st, body)
			}
		}
	}
	// the flag in staging was untouched by the attempts
	if got := decode[struct {
		Config struct {
			Enabled                bool
			FallthroughVariationID string
		}
	}](t, h.expect(200, w.a, "GET", "/environments/"+w.staging+"/flags/lim", nil)).Config; got.FallthroughVariationID != "on" {
		t.Errorf("staging changed: %+v", got)
	}
	// it cannot pull another environment's configuration through a promotion
	st, _, body := h.tokenCall(tok.Plaintext, "POST", "/environments/"+w.devA+"/flags/lim/promotions/preview", map[string]any{"sourceEnvironment": "staging"})
	if st != 404 || errCode(t, body) != "ENVIRONMENT_NOT_FOUND" {
		t.Errorf("promotion source outside the restriction: %d %s", st, body)
	}
	// the list of environments names only its own
	envs := decode[struct{ Environments []struct{ Key string } }](t, w.tokExpect(t, 200, tok.Plaintext, "GET", "/workspaces/"+w.wsA.ID+"/environments", nil)).Environments
	if len(envs) != 1 || envs[0].Key != "dev" {
		t.Errorf("a dev-only token sees environments %+v", envs)
	}
	if all := decode[struct{ Environments []struct{ Key string } }](t, h.expect(200, w.a, "GET", "/workspaces/"+w.wsA.ID+"/environments", nil)).Environments; len(all) != 3 {
		t.Errorf("a person sees %d environments", len(all))
	}
	// workspace-level reads work, but say nothing about other environments...
	usage := decode[struct {
		Flags []struct {
			FlagKey      string
			Environments []struct{ Key string }
		}
	}](t, w.tokExpect(t, 200, tok.Plaintext, "GET", segPath(w.wsA.ID, "/lim-seg/usage"), nil))
	if len(usage.Flags) != 1 || len(usage.Flags[0].Environments) != 1 || usage.Flags[0].Environments[0].Key != "dev" {
		t.Errorf("segment usage for a dev-only token: %+v", usage)
	}
	// ...and workspace-level CHANGES are refused (they would reach every environment)
	for _, c := range []struct{ method, path string }{{"POST", segPath(w.wsA.ID)}, {"PATCH", segPath(w.wsA.ID, "/lim-seg")}, {"DELETE", segPath(w.wsA.ID, "/lim-seg?confirm=true")}} {
		if st, _, body := h.tokenCall(tok.Plaintext, c.method, c.path, segReq("tok-seg", nil)); st != 403 || !strings.Contains(errMessage(body), "limited to some environments") {
			t.Errorf("%s %s with a restricted token: %d %s", c.method, c.path, st, body)
		}
	}
	if len(x0(t, w).Segments) != 1 {
		t.Error("a restricted token changed a segment")
	}
	// two environments, listed by key or id
	two := w.mint(t, w.a, w.wsA.ID, "dev-and-staging", "editor", "dev", w.staging)
	w.tokExpect(t, 200, two.Plaintext, "GET", "/environments/"+w.staging+"/flags", nil)
	w.tokExpect(t, 404, two.Plaintext, "GET", "/environments/"+w.prod+"/flags", nil)
	// the database keeps a restriction inside the token's own workspace
	if _, err := h.pool.Exec(t.Context(), `INSERT INTO api_token_environments (workspace_id, token_id, environment_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, w.wsA.ID, tok.ID, w.devB); err == nil {
		t.Error("the database accepted a token restricted to another workspace's environment")
	}
	// restricting to an unknown or another workspace's environment is refused when creating
	for _, env := range []string{"nowhere", w.devB} {
		if st, b := h.call(w.a, "POST", "/workspaces/"+w.wsA.ID+"/api-tokens", map[string]any{"name": "x", "role": "viewer", "environments": []string{env}}); st != 400 {
			t.Errorf("environment %q: %d %s", env, st, b)
		}
	}
}

func x0(t *testing.T, w *tokWorld) struct{ Segments []any } {
	return decode[struct{ Segments []any }](t, w.h.expect(200, w.a, "GET", segPath(w.wsA.ID), nil))
}

// A token cannot get around the approval gate: production edits need an approver, whoever holds the token.
func TestProductionApprovalGateAppliesThroughATokenToo(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	prodFlag := "/environments/" + w.prod + "/flags/" + secretFlag
	editorTok := w.mint(t, w.a, w.wsA.ID, "editor-ci", "editor")
	adminTok := w.mint(t, w.a, w.wsA.ID, "admin-ci", "admin")
	viewerTok := w.mint(t, w.a, w.wsA.ID, "viewer-ci", "viewer")
	patch := map[string]any{"fallthroughVariationId": "off"}

	// a token whose role is editor (even from an owner) is an editor: not production
	w.tokExpect(t, 403, editorTok.Plaintext, "PATCH", prodFlag, patch)
	w.tokExpect(t, 403, viewerTok.Plaintext, "PATCH", prodFlag, patch)
	w.tokExpect(t, 200, editorTok.Plaintext, "PATCH", "/environments/"+w.staging+"/flags/"+secretFlag, patch)
	// kill is editor-and-above in every environment, as for people
	w.tokExpect(t, 200, editorTok.Plaintext, "POST", prodFlag+"/kill", nil)
	w.tokExpect(t, 403, viewerTok.Plaintext, "POST", prodFlag+"/kill", nil)
	// promotion into production, and scheduling a production change, go through the same gate
	pv := decode[promoPreview](t, w.tokExpect(t, 200, editorTok.Plaintext, "POST", promoPath(w.prod, secretFlag, "/preview"), map[string]any{"sourceEnvironment": "dev"}))
	w.tokExpect(t, 403, editorTok.Plaintext, "POST", promoPath(w.prod, secretFlag, ""), map[string]any{"sourceEnvironment": "dev", "expectedVersion": pv.ExpectedVersion, "sourceVersion": pv.SourceVersion})
	w.tokExpect(t, 403, editorTok.Plaintext, "POST", prodFlag+"/scheduled-changes", map[string]any{"idempotencyKey": "k1", "kind": "enable", "runAt": w.clock.now().Add(time.Hour).Format(time.RFC3339)})
	w.tokExpect(t, 201, editorTok.Plaintext, "POST", prodFlag+"/scheduled-changes", map[string]any{"idempotencyKey": "k2", "kind": "kill", "runAt": w.clock.now().Add(time.Hour).Format(time.RFC3339)})
	// an admin-role token held by an owner IS an approver
	w.tokExpect(t, 200, adminTok.Plaintext, "PATCH", prodFlag, patch)
	pv2 := decode[promoPreview](t, w.tokExpect(t, 200, adminTok.Plaintext, "POST", promoPath(w.prod, secretFlag, "/preview"), map[string]any{"sourceEnvironment": "dev"}))
	if pv2.CanApply {
		w.tokExpect(t, 200, adminTok.Plaintext, "POST", promoPath(w.prod, secretFlag, ""), map[string]any{"sourceEnvironment": "dev", "expectedVersion": pv2.ExpectedVersion, "sourceVersion": pv2.SourceVersion})
	}
	// the refused attempts changed nothing and left no "applied" audit row
	if n := h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND resource_id = $2 AND action = 'flag.update' AND actor_email = 'editor-ci (' || $3 || ')' AND environment_id = $4::uuid`,
		w.wsA.ID, secretFlag, editorTok.Prefix, w.prod); n != 0 {
		t.Errorf("%d production edits recorded for the editor token", n)
	}
}

// ---- management ----------------------------------------------------------------------------

func TestTokensCannotManageTokensAndAdminsCan(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	admin := w.joinAs("adam", "admin")
	editor := w.joinAs("eddie", "editor")
	ownerTok := w.mint(t, w.a, w.wsA.ID, "owners", "admin")
	adminsTok := w.mint(t, admin, w.wsA.ID, "admins", "admin")
	base := "/workspaces/" + w.wsA.ID + "/api-tokens"

	// a token, even an admin-role one, cannot create, list or revoke tokens: not even itself
	for _, tok := range []minted{ownerTok, adminsTok} {
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"POST", base, map[string]any{"name": "minted-by-token", "role": "viewer"}},
			{"GET", base, nil},
			{"DELETE", base + "/" + tok.ID, nil},
			{"DELETE", base + "/" + ownerTok.ID, nil},
		} {
			st, _, body := h.tokenCall(tok.Plaintext, c.method, c.path, c.body)
			if st != 403 || errCode(t, body) != "TOKEN_NOT_ALLOWED" {
				t.Errorf("%s %s with a token: %d %s", c.method, c.path, st, body)
			}
		}
	}
	if w.dbCount(`SELECT count(*) FROM api_tokens WHERE name = 'minted-by-token'`) != 0 || w.dbCount(`SELECT count(*) FROM api_tokens WHERE revoked_at IS NOT NULL`) != 0 {
		t.Error("a token minted or revoked a token")
	}
	// non-admins cannot list or revoke either
	h.expect(403, editor, "GET", base, nil)
	h.expect(403, editor, "DELETE", base+"/"+ownerTok.ID, nil)
	// admins list everything and can revoke any of the workspace's tokens, theirs or not
	list := decode[struct {
		Tokens []struct{ ID, Name, Status, Role string }
	}](t, h.expect(200, admin, "GET", base, nil)).Tokens
	if len(list) < 2 {
		t.Fatalf("list: %+v", list)
	}
	h.expect(204, admin, "DELETE", base+"/"+ownerTok.ID, nil) // an owner's token, revoked by an admin
	h.expect(204, admin, "DELETE", base+"/"+ownerTok.ID, nil) // revoking again is a no-op
	h.expect(204, w.a, "DELETE", base+"/"+adminsTok.ID, nil)  // an admin's token, revoked by the owner
	h.expect(404, w.a, "DELETE", base+"/00000000-0000-4000-8000-000000000009", nil)
	h.expect(404, w.a, "DELETE", base+"/not-a-uuid", nil)
	for _, tok := range []minted{ownerTok, adminsTok} {
		if st, _, b := h.tokenCall(tok.Plaintext, "GET", "/environments/"+w.devA+"/flags", nil); st != 401 || string(b) != genericBody {
			t.Errorf("a revoked token: %d %s", st, b)
		}
	}
	// the list never carries the secret or the hash
	_, raw := h.call(w.a, "GET", base, nil)
	for _, secret := range []string{ownerTok.Plaintext, adminsTok.Plaintext, "token_hash", "plaintext"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("the token list contains %q", secret)
		}
	}
	// expiry is required and bounded: there is no "never"
	for name, body := range map[string]map[string]any{
		"zero days": {"name": "e", "role": "viewer", "expiresInDays": 0}, "negative": {"name": "e", "role": "viewer", "expiresInDays": -1},
		"over a year": {"name": "e", "role": "viewer", "expiresInDays": 367}, "huge": {"name": "e", "role": "viewer", "expiresInDays": 100000},
		"an absolute date": {"name": "e", "role": "viewer", "expiresAt": "2099-01-01T00:00:00Z"}, "never": {"name": "e", "role": "viewer", "never": true},
		"no name": {"role": "viewer"}, "long name": {"name": strings.Repeat("n", 61), "role": "viewer"}, "control characters": {"name": "a\nb", "role": "viewer"},
	} {
		if st, b := h.call(w.a, "POST", base, body); st != 400 {
			t.Errorf("%s: %d %s", name, st, b)
		}
	}
	var days float64
	for _, d := range []any{nil, 1, 90, 366} {
		body := map[string]any{"name": "ok", "role": "viewer"}
		if d != nil {
			body["expiresInDays"] = d
		}
		created := decode[struct {
			Token struct{ ExpiresAt, CreatedAt time.Time }
		}](t, h.expect(201, w.a, "POST", base, body)).Token
		days = created.ExpiresAt.Sub(created.CreatedAt).Hours() / 24
		want := 90.0
		if d != nil {
			want = float64(d.(int))
		}
		if days < want-0.01 || days > want+0.01 {
			t.Errorf("expiresInDays %v: expires in %.2f days", d, days)
		}
	}
	// the database refuses a token that never expires or lives too long
	for _, expr := range []string{"NULL", "now() + interval '400 days'", "now() - interval '1 day'"} {
		if _, err := h.pool.Exec(t.Context(), fmt.Sprintf(`INSERT INTO api_tokens (workspace_id, name, token_prefix, token_hash, role, created_by_email, expires_at)
			VALUES ($1::uuid, 'x', 'hlt_00000000', decode(repeat('ab', 32), 'hex'), 'viewer', 'x', %s)`, expr), w.wsA.ID); err == nil {
			t.Errorf("the database accepted expires_at = %s", expr)
		}
	}
}

// ---- lifecycle -------------------------------------------------------------------------------

func TestExpiryAndTheCacheBound(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	tok := w.mint(t, w.a, w.wsA.ID, "short", "viewer")
	path := "/environments/" + w.devA + "/flags"
	w.tokExpect(t, 200, tok.Plaintext, "GET", path, nil) // verified and cached
	// revoked directly in the database (no announcement, as if the message were lost): the cache is the bound
	h.pool.Exec(t.Context(), `UPDATE api_tokens SET revoked_at = now() WHERE id = $1::uuid`, tok.ID)
	w.clock.advance(14 * time.Second)
	w.tokExpect(t, 200, tok.Plaintext, "GET", path, nil) // inside the 15-second cache: still trusted
	w.clock.advance(2 * time.Second)
	if st, _, b := h.tokenCall(tok.Plaintext, "GET", path, nil); st != 401 || string(b) != genericBody {
		t.Errorf("after the cache TTL: %d %s", st, b)
	}

	// expiry is exact even for a cached token (it does not wait for the cache)
	exp := w.mint(t, w.a, w.wsA.ID, "expiring", "viewer")
	var expiresAt time.Time
	_ = h.pool.QueryRow(t.Context(), `SELECT expires_at FROM api_tokens WHERE id = $1::uuid`, exp.ID).Scan(&expiresAt)
	w.clock.t = expiresAt.Add(-time.Hour)
	w.tokExpect(t, 200, exp.Plaintext, "GET", path, nil)
	w.clock.t = expiresAt.Add(-time.Second)
	w.tokExpect(t, 200, exp.Plaintext, "GET", path, nil)
	w.clock.t = expiresAt.Add(time.Second)
	if st, _, b := h.tokenCall(exp.Plaintext, "GET", path, nil); st != 401 || string(b) != genericBody {
		t.Errorf("one second after expiry, cached: %d %s", st, b)
	}
	// ...and the database agrees: a token past its expiry is listed as expired
	h.pool.Exec(t.Context(), `UPDATE api_tokens SET created_at = now() - interval '100 days', expires_at = now() - interval '10 days' WHERE id = $1::uuid`, exp.ID)
	list := decode[struct{ Tokens []struct{ ID, Status string } }](t, h.expect(200, w.a, "GET", "/workspaces/"+w.wsA.ID+"/api-tokens", nil)).Tokens
	for _, tk := range list {
		if tk.ID == exp.ID && tk.Status != "expired" {
			t.Errorf("status %q", tk.Status)
		}
		if tk.ID == tok.ID && tk.Status != "revoked" {
			t.Errorf("status %q", tk.Status)
		}
	}
}

func TestLastUsedIsWrittenAtMostOncePerMinute(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	tok := w.mint(t, w.a, w.wsA.ID, "touchy", "viewer")
	other := w.mint(t, w.a, w.wsA.ID, "other", "viewer")
	h.pool.Exec(t.Context(), `
		CREATE TABLE token_writes (token_id uuid, at timestamptz DEFAULT now());
		CREATE FUNCTION count_token_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO token_writes (token_id) VALUES (NEW.id); RETURN NEW; END $$;
		CREATE TRIGGER count_token_write AFTER UPDATE ON api_tokens FOR EACH ROW EXECUTE FUNCTION count_token_write();`)
	writes := func(id string) int { return h.count(`SELECT count(*) FROM token_writes WHERE token_id = $1::uuid`, id) }
	path := "/environments/" + w.devA + "/flags"
	var last *time.Time
	if err := h.pool.QueryRow(t.Context(), `SELECT last_used_at FROM api_tokens WHERE id = $1::uuid`, tok.ID).Scan(&last); err != nil || last != nil {
		t.Fatalf("a new token has been used: %v %v", last, err)
	}
	for i := 0; i < 50; i++ { // fifty requests within a minute...
		w.tokExpect(t, 200, tok.Plaintext, "GET", path, nil)
		w.clock.advance(time.Second)
	}
	if n := writes(tok.ID); n != 1 {
		t.Fatalf("%d database writes for 50 requests in a minute, want 1", n)
	}
	w.clock.advance(20 * time.Second) // 70 seconds since the first
	w.tokExpect(t, 200, tok.Plaintext, "GET", path, nil)
	if n := writes(tok.ID); n != 2 {
		t.Errorf("after a minute: %d writes, want 2", n)
	}
	// another token is tracked on its own
	w.tokExpect(t, 200, other.Plaintext, "GET", path, nil)
	if writes(other.ID) != 1 || writes(tok.ID) != 2 {
		t.Errorf("writes: other %d, first %d", writes(other.ID), writes(tok.ID))
	}
	// the list reports it honestly: set once used, "never" before
	list := decode[struct {
		Tokens []struct {
			ID         string
			LastUsedAt *time.Time
		}
	}](t, h.expect(200, w.a, "GET", "/workspaces/"+w.wsA.ID+"/api-tokens", nil)).Tokens
	for _, tk := range list {
		if (tk.ID == tok.ID || tk.ID == other.ID) && tk.LastUsedAt == nil {
			t.Errorf("token %s was used but shows no last use", tk.ID)
		}
	}
	// a refused request records no use
	bad := w.mint(t, w.a, w.wsA.ID, "unused", "viewer")
	h.tokenCall(bad.Plaintext, "GET", "/me", nil) // token-denied route
	if writes(bad.ID) != 0 {
		t.Error("a refused request counted as a use")
	}
}

func TestMaximumActiveTokensQuota(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	base := "/workspaces/" + w.wsA.ID + "/api-tokens"
	post := func(u user, name string) (int, []byte) {
		return h.call(u, "POST", base, map[string]any{"name": name, "role": "viewer"})
	}
	var ids []string
	for i := 0; i < quota.MaxActiveAPITokensPerUser; i++ {
		ids = append(ids, w.mint(t, w.a, w.wsA.ID, fmt.Sprintf("a%d", i), "viewer").ID)
	}
	st, b := post(w.a, "one-too-many")
	if st != 409 || errCode(t, b) != "QUOTA_EXCEEDED" || !strings.Contains(errMessage(b), "per person") {
		t.Fatalf("per-person cap: %d %s", st, b)
	}
	// revoked and expired tokens do not count
	h.expect(204, w.a, "DELETE", base+"/"+ids[0], nil)
	if st, b := post(w.a, "after-revoke"); st != 201 {
		t.Errorf("after a revoke: %d %s", st, b)
	}
	h.pool.Exec(t.Context(), `UPDATE api_tokens SET created_at = now() - interval '100 days', expires_at = now() - interval '1 day' WHERE id = $1::uuid`, ids[1])
	if st, b := post(w.a, "after-expiry"); st != 201 {
		t.Errorf("after an expiry: %d %s", st, b)
	}
	// the workspace cap: other admins fill it
	for i := 0; w.dbCount(`SELECT count(*) FROM api_tokens WHERE workspace_id = $1::uuid AND revoked_at IS NULL AND expires_at > now()`, w.wsA.ID) < quota.MaxActiveAPITokensPerWorkspace; i++ {
		u := w.joinAs(fmt.Sprintf("filler%d", i), "admin")
		for j := 0; j < quota.MaxActiveAPITokensPerUser-1 && w.dbCount(`SELECT count(*) FROM api_tokens WHERE workspace_id = $1::uuid AND revoked_at IS NULL AND expires_at > now()`, w.wsA.ID) < quota.MaxActiveAPITokensPerWorkspace; j++ {
			w.mint(t, u, w.wsA.ID, fmt.Sprintf("f%d-%d", i, j), "viewer")
		}
	}
	extra := w.joinAs("lastone", "admin")
	st, b = post(extra, "over-the-workspace-cap")
	if st != 409 || !strings.Contains(errMessage(b), "per workspace") {
		t.Errorf("per-workspace cap: %d %s", st, b)
	}
	// another workspace is unaffected
	if st, _ := h.call(w.b, "POST", "/workspaces/"+w.wsB.ID+"/api-tokens", map[string]any{"name": "b", "role": "viewer"}); st != 201 {
		t.Errorf("workspace B: %d", st)
	}
}

func TestTokenCapHoldsUnderConcurrency(t *testing.T) {
	w := newTokWorld(t, nil)
	var wg sync.WaitGroup
	statuses := make([]int, quota.MaxActiveAPITokensPerUser+6)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], _ = w.h.call(w.a, "POST", "/workspaces/"+w.wsA.ID+"/api-tokens", map[string]any{"name": fmt.Sprintf("p%d", i), "role": "viewer"})
		}()
	}
	wg.Wait()
	created := 0
	for _, s := range statuses {
		if s == 201 {
			created++
		} else if s != 409 {
			t.Errorf("status %d", s)
		}
	}
	if created != quota.MaxActiveAPITokensPerUser {
		t.Errorf("%d created in parallel, want exactly %d", created, quota.MaxActiveAPITokensPerUser)
	}
}

// Revocation reaches another instance at once through the announcement; if the announcement is lost, the cache TTL is the bound.
func TestRevocationReachesOtherInstances(t *testing.T) {
	for name, useRedis := range map[string]bool{"in-process bus": false, "real Redis": true} {
		t.Run(name, func(t *testing.T) {
			var adjust func(*server.Deps)
			if useRedis {
				url := os.Getenv("HELIOS_TEST_REDIS_URL")
				if url == "" {
					t.Skip("HELIOS_TEST_REDIS_URL not set; skipping the Redis-backed test")
				}
				if !strings.Contains(url, "127.0.0.1") && !strings.Contains(url, "localhost") {
					t.Fatalf("HELIOS_TEST_REDIS_URL must be a local Redis, got %q", url)
				}
				client, err := redisx.Connect(t.Context(), url)
				if err != nil {
					t.Fatalf("redis: %v", err)
				}
				t.Cleanup(func() { _ = client.Close() })
				adjust = func(d *server.Deps) {
					d.Publisher, d.Subscriber = redisx.Publisher{Client: client}, redisx.Subscriber{Client: client}
				}
			}
			// a one-hour cache: only the announcement can make instance 2 let go in time
			w := newTokWorld(t, nil, func(d *server.Deps) {
				d.APITokenCacheTTL = time.Hour
				if adjust != nil {
					adjust(d)
				}
			})
			h := w.h
			instance2 := h.newInstance(nil)
			tok := w.mint(t, w.a, w.wsA.ID, "two-instances", "viewer")
			path := "/environments/" + w.devA + "/flags"
			if st, _, _ := h.tokenCallAt(instance2.URL, tok.Plaintext, "GET", path, nil); st != 200 { // warms instance 2's cache
				t.Fatalf("instance 2 before revocation: %d", st)
			}
			start := time.Now()
			h.expect(204, w.a, "DELETE", "/workspaces/"+w.wsA.ID+"/api-tokens/"+tok.ID, nil) // via instance 1
			if st, _, _ := h.tokenCall(tok.Plaintext, "GET", path, nil); st != 401 {
				t.Errorf("instance 1 right after the revoke: %d", st) // dropped locally at once
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				if st, _, _ := h.tokenCallAt(instance2.URL, tok.Plaintext, "GET", path, nil); st == 401 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("instance 2 still accepts the revoked token after %v (cache TTL 1h)", time.Since(start))
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Logf("%s: instance 2 refused the revoked token %v after the revoke call on instance 1 (cache TTL 1h)", name, time.Since(start))
			if time.Since(start) > time.Second {
				t.Errorf("took %v, want about a second or less", time.Since(start))
			}
		})
	}
}

func TestRevocationFallsBackToTheCacheTTLWhenTheBroadcastIsLost(t *testing.T) {
	w := newTokWorld(t, nil, func(d *server.Deps) { d.APITokenCacheTTL = 400 * time.Millisecond })
	h := w.h
	instance2 := h.newInstance(func(d *server.Deps) {
		d.Subscriber = deafToRevocations{h.bus}
		d.APITokenCacheTTL = 400 * time.Millisecond
		d.Clock = nil
	})
	tok := w.mint(t, w.a, w.wsA.ID, "lost-message", "viewer")
	// instance 2 uses the real clock; the token was minted "now", so it is valid there too
	path := "/environments/" + w.devA + "/flags"
	if st, _, _ := h.tokenCallAt(instance2.URL, tok.Plaintext, "GET", path, nil); st != 200 {
		t.Fatalf("before: %d", st)
	}
	start := time.Now()
	h.expect(204, w.a, "DELETE", "/workspaces/"+w.wsA.ID+"/api-tokens/"+tok.ID, nil)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if st, _, _ := h.tokenCallAt(instance2.URL, tok.Plaintext, "GET", path, nil); st == 401 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the cache TTL never let go")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("broadcast lost: refused within %v (cache TTL 400ms)", time.Since(start))
}

// ---- security ----------------------------------------------------------------------------------

func TestATokenIsOnlyAcceptedInTheAuthorizationBearerHeader(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	tok := w.mint(t, w.a, w.wsA.ID, "header-only", "admin")
	path := "/environments/" + w.devA + "/flags"
	// in a query string, a cookie, another header, or the body: ignored, as no credential at all
	for name, req := range map[string]func() *http.Request{
		"query access_token": func() *http.Request {
			r, _ := http.NewRequest("GET", h.srv.URL+path+"?access_token="+tok.Plaintext, nil)
			return r
		},
		"query token": func() *http.Request {
			r, _ := http.NewRequest("GET", h.srv.URL+path+"?token="+tok.Plaintext, nil)
			return r
		},
		"cookie": func() *http.Request {
			r, _ := http.NewRequest("GET", h.srv.URL+path, nil)
			r.Header.Set("Cookie", "token="+tok.Plaintext)
			return r
		},
		"x-api-key header": func() *http.Request {
			r, _ := http.NewRequest("GET", h.srv.URL+path, nil)
			r.Header.Set("X-Api-Key", tok.Plaintext)
			r.Header.Set("X-Helios-SDK-Key", tok.Plaintext)
			return r
		},
		"json body": func() *http.Request {
			r, _ := http.NewRequest("PATCH", h.srv.URL+path+"/"+secretFlag, strings.NewReader(`{"token":"`+tok.Plaintext+`","fallthroughVariationId":"off"}`))
			r.Header.Set("Content-Type", "application/json")
			return r
		},
		"basic auth": func() *http.Request {
			r, _ := http.NewRequest("GET", h.srv.URL+path, nil)
			r.SetBasicAuth("x", tok.Plaintext)
			return r
		},
		"lowercase scheme": func() *http.Request {
			r, _ := http.NewRequest("GET", h.srv.URL+path, nil)
			r.Header.Set("Authorization", "bearer "+tok.Plaintext)
			return r
		},
		"Token scheme": func() *http.Request {
			r, _ := http.NewRequest("GET", h.srv.URL+path, nil)
			r.Header.Set("Authorization", "Token "+tok.Plaintext)
			return r
		},
	} {
		res, err := http.DefaultClient.Do(req())
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 401 {
			t.Errorf("%s: status %d, want 401", name, res.StatusCode)
		}
	}
	w.tokExpect(t, 200, tok.Plaintext, "GET", path, nil) // the header works
}

func TestMalformedTokensAreAllTheSameGenericRefusal(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	good := w.mint(t, w.a, w.wsA.ID, "real", "admin")
	flipped := []byte(good.Plaintext)
	flipped[len(flipped)-1] ^= 1 // one bit of the secret
	expired := w.mint(t, w.a, w.wsA.ID, "gone-by-expiry", "viewer")
	h.pool.Exec(t.Context(), `UPDATE api_tokens SET created_at = now() - interval '100 days', expires_at = now() - interval '1 day' WHERE id = $1::uuid`, expired.ID)
	revoked := w.mint(t, w.a, w.wsA.ID, "gone-by-revoke", "viewer")
	h.expect(204, w.a, "DELETE", "/workspaces/"+w.wsA.ID+"/api-tokens/"+revoked.ID, nil)
	unknown, _, _, _ := apitoken.Generate()

	cases := map[string]string{
		"unknown, well-formed": unknown, "expired": expired.Plaintext, "revoked": revoked.Plaintext, "one character changed": string(flipped),
		"truncated": good.Plaintext[:len(good.Plaintext)-1], "extra character": good.Plaintext + "A", "prefix only": "hlt_",
		"empty after prefix": "hlt_ ", "wrong prefix": "hlx_" + good.Plaintext[4:], "uppercase prefix": "HLT_" + good.Plaintext[4:],
		"uppercase id": good.Plaintext[:4] + strings.ToUpper(good.Plaintext[4:12]) + good.Plaintext[12:], "padding": good.Plaintext[:55] + "=",
		"unicode": good.Plaintext[:20] + "é" + good.Plaintext[22:], "spaces inside": good.Plaintext[:30] + " " + good.Plaintext[31:],
		"a separator missing": strings.Replace(good.Plaintext, "_", "-", 2), // not even the prefix: the user-JWT path "two tokens": good.Plaintext + good.Plaintext,
		"long garbage":        "hlt_" + strings.Repeat("x", 5000), "sql": "hlt_'; DROP TABLE api_tokens;--", "nul": "hlt_\x00",
	}
	var first []byte
	for name, tok := range cases {
		if strings.ContainsAny(tok, "\x00\r\n") {
			continue // not sendable in a header
		}
		st, hdr, body := h.tokenCall(tok, "GET", "/environments/"+w.devA+"/flags", nil)
		viaJWT := name == "wrong prefix" || name == "uppercase prefix" || name == "a separator missing" // not "hlt_...": the user-JWT path
		if st != 401 && !viaJWT {
			t.Errorf("%s: status %d %s", name, st, body)
		}
		if st == 401 && !viaJWT {
			if first == nil {
				first = body
			}
			if string(body) != genericBody || string(body) != string(first) {
				t.Errorf("%s: body %q is not the one generic refusal", name, body)
			}
			if hdr.Get("Retry-After") != "" || hdr.Get("WWW-Authenticate") != "" {
				t.Errorf("%s: extra headers %v", name, hdr)
			}
		}
		if st == 401 && strings.Contains(string(body), "expired") && name == "expired" && string(body) != genericBody {
			t.Errorf("an expired token is told apart: %s", body)
		}
	}
	for _, other := range []string{"wrong prefix", "uppercase prefix", "a separator missing"} {
		if st, _, _ := h.tokenCall(cases[other], "GET", "/environments/"+w.devA+"/flags", nil); st != 401 {
			t.Errorf("%s: %d", other, st)
		}
	}
	// a creator removed from the workspace is the one case that is not a 401: the credential is fine, the
	// person is a stranger (404), exactly as for a user session
	// (covered in TestEffectiveRoleIsTheLowerOfTheTokenAndItsCreator)
	w.tokExpect(t, 200, good.Plaintext, "GET", "/environments/"+w.devA+"/flags", nil)
}

func TestFailedTokenAttemptsAreLimitedPerIPBeforeAnyHashingOrDatabaseWork(t *testing.T) {
	w := newTokWorld(t, map[string]ratelimit.Rule{ratelimit.ClassTokenBad: tiny(3)})
	h := w.h
	good := w.mint(t, w.a, w.wsA.ID, "real", "viewer")
	path := "/environments/" + w.devA + "/flags"
	unknown, _, _, _ := apitoken.Generate()
	// the database would be consulted for a well-formed token: take its table away to prove it is not, once the bucket is empty
	h.pool.Exec(t.Context(), `ALTER TABLE api_tokens RENAME TO api_tokens_away`)
	if st, _, _ := h.tokenCall(unknown, "GET", path, nil); st != 500 {
		t.Fatalf("with the table gone a well-formed token should reach the database and fail: %d", st)
	}
	h.pool.Exec(t.Context(), `ALTER TABLE api_tokens_away RENAME TO api_tokens`)
	// three failures spend the budget: malformed, unknown, malformed
	for _, tok := range []string{"hlt_x", unknown, "hlt_y"} {
		if st, _, _ := h.tokenCall(tok, "GET", path, nil); st != 401 {
			t.Fatalf("failure %.8s: %d", tok, st)
		}
	}
	st, hdr, body := h.tokenCall("hlt_z", "GET", path, nil)
	if st != 429 || hdr.Get("Retry-After") == "" || !bytes.Contains(body, []byte("RATE_LIMITED")) {
		t.Fatalf("after the budget: %d %v %s", st, hdr, body)
	}
	// from now on, nothing reaches the hash or the database: even a well-formed token gets 429 with the table gone
	h.pool.Exec(t.Context(), `ALTER TABLE api_tokens RENAME TO api_tokens_away`)
	for _, tok := range []string{unknown, good.Plaintext, "hlt_garbage", "hlt_" + strings.Repeat("z", 5000)} {
		if st, _, b := h.tokenCall(tok, "GET", path, nil); st != 429 {
			t.Errorf("token %.12s…: status %d %s, want 429 before any database work", tok, st, b)
		}
	}
	h.pool.Exec(t.Context(), `ALTER TABLE api_tokens_away RENAME TO api_tokens`)
	// a valid token does not spend the failure budget, and a user session is not touched by it
	h.expect(200, w.a, "GET", path, nil)
}

// A pipeline's traffic is counted against the token, never against the person who made it.
func TestATokensTrafficDoesNotThrottleItsCreator(t *testing.T) {
	w := newTokWorld(t, map[string]ratelimit.Rule{ratelimit.ClassUser: {PerSecond: 0.01, Burst: 40}})
	tok := w.mint(t, w.a, w.wsA.ID, "chatty", "viewer")
	path := "/environments/" + w.devA + "/flags"
	for i := 0; i < 40; i++ {
		w.tokExpect(t, 200, tok.Plaintext, "GET", path, nil)
	}
	st, _, _ := w.h.tokenCall(tok.Plaintext, "GET", path, nil)
	if st != 429 {
		t.Fatalf("the 41st token request: %d, want 429 from the token's own bucket", st)
	}
	for i := 0; i < 10; i++ { // the person is untouched
		w.h.expect(200, w.a, "GET", path, nil)
	}
}

func TestEachTokenHasItsOwnRateLimitAndTheCreatorKeepsTheirs(t *testing.T) {
	w := newTokWorld(t, map[string]ratelimit.Rule{ratelimit.ClassToken: tiny(3)})
	h := w.h
	a := w.mint(t, w.a, w.wsA.ID, "busy", "viewer")
	b := w.mint(t, w.a, w.wsA.ID, "calm", "viewer")
	path := "/environments/" + w.devA + "/flags"
	for i := 0; i < 3; i++ {
		w.tokExpect(t, 200, a.Plaintext, "GET", path, nil)
	}
	st, hdr, body := h.tokenCall(a.Plaintext, "GET", path, nil)
	if st != 429 || hdr.Get("Retry-After") == "" || errCode(t, body) != "RATE_LIMITED" {
		t.Fatalf("4th request: %d %v %s", st, hdr, body)
	}
	w.tokExpect(t, 200, b.Plaintext, "GET", path, nil) // another token is not affected
	for i := 0; i < 10; i++ {                          // the creator's own session is not throttled by their token
		h.expect(200, w.a, "GET", path, nil)
	}
}

// The comparison and the header are code-level properties, checked in the source.
func TestTokenCodeUsesConstantTimeComparisonAndOnlyTheHeader(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	tokenSrc := read("../platform/apitoken/apitoken.go")
	verifierSrc := read("../platform/apitoken/verifier.go")
	if !strings.Contains(tokenSrc, "subtle.ConstantTimeCompare(") {
		t.Error("apitoken.Equal does not use crypto/subtle")
	}
	if !strings.Contains(verifierSrc, "!Equal(stored, digest[:])") {
		t.Error("the stored hash is not compared with the constant-time Equal")
	}
	for _, src := range []string{tokenSrc, verifierSrc} {
		for _, bad := range []string{"bytes.Equal", "strings.EqualFold", "reflect.DeepEqual", "== stored", "stored ==", "bytes.Compare"} {
			if strings.Contains(src, bad) {
				t.Errorf("token code uses %s, which is not constant time", bad)
			}
		}
	}
	// the credential is read from Authorization only: never from the URL, a form or the body
	for _, bad := range []string{"URL.Query", "FormValue", "ParseForm", "PostForm", "r.Body", "Cookie("} {
		if strings.Contains(verifierSrc, bad) {
			t.Errorf("verifier.go reads the credential from somewhere other than the Authorization header (%s)", bad)
		}
	}
	// no raw token, hash or header is ever handed to a logger
	for _, line := range strings.Split(verifierSrc, "\n") {
		if strings.Contains(line, "log.Printf") && (strings.Contains(line, "raw") || strings.Contains(line, "digest") || strings.Contains(line, "Authorization") || strings.Contains(line, "stored")) {
			t.Errorf("a log call mentions the credential: %s", strings.TrimSpace(line))
		}
	}
}

func TestNoTokenHashOrAuthorizationHeaderReachesTheLogs(t *testing.T) {
	logs := captureLogs(t)
	w := newTokWorld(t, nil)
	h := w.h
	good := w.mint(t, w.a, w.wsA.ID, "logged", "admin")
	expired := w.mint(t, w.a, w.wsA.ID, "logged-expired", "viewer")
	h.pool.Exec(t.Context(), `UPDATE api_tokens SET created_at = now() - interval '100 days', expires_at = now() - interval '1 day' WHERE id = $1::uuid`, expired.ID)
	revoked := w.mint(t, w.a, w.wsA.ID, "logged-revoked", "viewer")
	h.expect(204, w.a, "DELETE", "/workspaces/"+w.wsA.ID+"/api-tokens/"+revoked.ID, nil)
	unknown, _, _, _ := apitoken.Generate()
	path := "/environments/" + w.devA + "/flags/" + secretFlag
	for _, tok := range []string{good.Plaintext, expired.Plaintext, revoked.Plaintext, unknown, "hlt_garbage", good.Plaintext[:30]} {
		h.tokenCall(tok, "GET", path, nil)
		h.tokenCall(tok, "PATCH", path, map[string]any{"fallthroughVariationId": "off"})
		h.tokenCall(tok, "GET", "/me", nil)
	}
	// also a failure inside the handlers
	h.pool.Exec(t.Context(), `ALTER TABLE api_tokens RENAME TO api_tokens_away`)
	h.tokenCall(unknown, "GET", path, nil)
	h.pool.Exec(t.Context(), `ALTER TABLE api_tokens_away RENAME TO api_tokens`)
	out := logs.String()
	for _, tok := range []string{good.Plaintext, expired.Plaintext, revoked.Plaintext, unknown} {
		secretPart := tok[13:]
		for _, bad := range []string{tok, secretPart, secretPart[:20], hex.EncodeToString(apitoken.Hash(tok)), "Bearer " + tok} {
			if strings.Contains(out, bad) {
				t.Errorf("the logs contain %.16q…", bad)
			}
		}
	}
	if strings.Contains(out, "Authorization") || strings.Contains(out, "Bearer ") {
		t.Errorf("the logs mention the Authorization header:\n%s", out)
	}
	// and no response echoes a credential back
	for _, body := range h.bodies {
		for _, tok := range []string{good.Plaintext[13:], unknown[13:]} {
			if bytes.Contains(body, []byte(tok)) && !bytes.Contains(body, []byte(`"plaintext"`)) {
				t.Errorf("a response echoed a token: %s", body)
			}
		}
	}
}

// ---- tenants ---------------------------------------------------------------------------------------

func TestATokenOfOneWorkspaceReachesNothingOfAnother(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	secretB := "bobs-secret-flag"
	h.expect(201, w.b, "POST", "/environments/"+w.devB+"/flags", boolFlag(secretB))
	h.expect(201, w.b, "POST", segPath(w.wsB.ID), segReq("bobs-seg", []string{"bobs-member"}))
	tokA := w.mint(t, w.a, w.wsA.ID, "alice-admin", "admin")
	tokB := w.mint(t, w.b, w.wsB.ID, "bob-admin", "admin")
	stagingB := envID(t, w.wsB, "staging")

	type call struct {
		method, path string
		body         any
	}
	against := []call{
		{"GET", "/environments/" + w.devB + "/flags", nil}, {"GET", "/environments/" + w.devB + "/flags/" + secretB, nil},
		{"PATCH", "/environments/" + w.devB + "/flags/" + secretB, map[string]any{"enabled": true}},
		{"POST", "/environments/" + w.devB + "/flags/" + secretB + "/kill", nil},
		{"GET", "/environments/" + w.devB + "/audit-logs", nil}, {"GET", "/environments/" + w.devB + "/scheduled-changes", nil},
		{"POST", "/environments/" + w.devB + "/flags/" + secretB + "/scheduled-changes", map[string]any{"idempotencyKey": "x", "kind": "enable", "runAt": w.clock.now().Add(time.Hour).Format(time.RFC3339)}},
		{"POST", "/environments/" + w.devB + "/flags/" + secretB + "/promotions/preview", map[string]any{"sourceEnvironment": "staging"}},
		{"GET", "/environments/" + w.devB + "/experiments", nil},
	}
	for _, c := range against {
		st, _, body := h.tokenCall(tokA.Plaintext, c.method, c.path, c.body)
		if st != 404 || errCode(t, body) != "ENVIRONMENT_NOT_FOUND" || bytes.Contains(body, []byte("bob")) {
			t.Errorf("A's token, %s %s: %d %s", c.method, c.path, st, body)
		}
	}
	for _, c := range []call{
		{"GET", segPath(w.wsB.ID), nil}, {"GET", segPath(w.wsB.ID, "/bobs-seg"), nil}, {"GET", segPath(w.wsB.ID, "/bobs-seg/usage"), nil},
		{"POST", segPath(w.wsB.ID), segReq("planted", nil)}, {"PATCH", segPath(w.wsB.ID, "/bobs-seg"), map[string]any{"name": "mine"}},
		{"DELETE", segPath(w.wsB.ID, "/bobs-seg?confirm=true"), nil}, {"GET", "/workspaces/" + w.wsB.ID + "/environments", nil},
	} {
		st, _, body := h.tokenCall(tokA.Plaintext, c.method, c.path, c.body)
		if st != 404 || errCode(t, body) != "WORKSPACE_NOT_FOUND" || bytes.Contains(body, []byte("bob")) {
			t.Errorf("A's token, %s %s: %d %s", c.method, c.path, st, body)
		}
	}
	// the promotion source can only be an environment of the target's own workspace
	for _, src := range []string{stagingB, w.devB, w.staging} {
		st, _, body := h.tokenCall(tokA.Plaintext, "POST", "/environments/"+w.devA+"/flags/"+secretFlag+"/promotions/preview", map[string]any{"sourceEnvironment": src})
		if src != w.staging && (st != 404 || errCode(t, body) != "ENVIRONMENT_NOT_FOUND") {
			t.Errorf("A's token pulling from B's environment %s: %d %s", src, st, body)
		}
	}
	// B's data is exactly as it was: nothing read, nothing written
	if got := decode[struct {
		Config struct {
			Enabled bool
			Version int64
		}
	}](t, h.expect(200, w.b, "GET", "/environments/"+w.devB+"/flags/"+secretB, nil)).Config; got.Enabled || got.Version != 1 {
		t.Errorf("B's flag changed: %+v", got)
	}
	if got := decode[segView](t, h.expect(200, w.b, "GET", segPath(w.wsB.ID, "/bobs-seg"), nil)); got.Name != "Segment bobs-seg" || got.MemberCount != 1 {
		t.Errorf("B's segment changed: %+v", got)
	}
	if n := w.dbCount(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND actor_type = 'api_token'`, w.wsB.ID); n != 0 {
		t.Errorf("%d audit rows in B's workspace were written by A's token", n)
	}
	// A creator who belongs to BOTH workspaces: the token is still confined to the workspace it was made in
	bTok := h.newUser("both")
	h.me(bTok)
	inviteA := decode[struct{ Token string }](t, h.expect(201, w.a, "POST", "/workspaces/"+w.wsA.ID+"/invites", map[string]any{"email": bTok.email, "role": "admin"})).Token
	h.expect(200, bTok, "POST", "/invites/accept", map[string]any{"token": inviteA})
	inviteB := decode[struct{ Token string }](t, h.expect(201, w.b, "POST", "/workspaces/"+w.wsB.ID+"/invites", map[string]any{"email": bTok.email, "role": "admin"})).Token
	h.expect(200, bTok, "POST", "/invites/accept", map[string]any{"token": inviteB})
	both := w.mint(t, bTok, w.wsA.ID, "made-for-A", "admin")
	w.tokExpect(t, 200, both.Plaintext, "GET", "/environments/"+w.devA+"/flags", nil)
	for _, c := range []struct{ method, path string }{
		{"GET", "/environments/" + w.devB + "/flags"}, {"PATCH", "/environments/" + w.devB + "/flags/" + secretB},
		{"GET", segPath(w.wsB.ID)}, {"GET", "/workspaces/" + w.wsB.ID + "/environments"},
	} {
		st, _, body := h.tokenCall(both.Plaintext, c.method, c.path, map[string]any{"enabled": true})
		if st != 404 || bytes.Contains(body, []byte("bob")) {
			t.Errorf("a token made for A by someone who is also in B, %s %s: %d %s", c.method, c.path, st, body)
		}
	}
	// A's token on A's own side works, and B's token cannot touch A's
	w.tokExpect(t, 200, tokA.Plaintext, "GET", "/environments/"+w.devA+"/flags/"+secretFlag, nil)
	w.tokExpect(t, 404, tokB.Plaintext, "GET", "/environments/"+w.devA+"/flags/"+secretFlag, nil)
	w.tokExpect(t, 404, tokB.Plaintext, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": false})
	if got := decode[struct{ Config struct{ Enabled bool } }](t, h.expect(200, w.a, "GET", "/environments/"+w.devA+"/flags/"+secretFlag, nil)).Config; !got.Enabled {
		t.Error("B's token changed A's flag")
	}
	// B's token list and A's are separate; B's admins cannot revoke A's token
	h.expect(404, w.b, "DELETE", "/workspaces/"+w.wsB.ID+"/api-tokens/"+tokA.ID, nil)
	h.expect(404, w.b, "DELETE", "/workspaces/"+w.wsA.ID+"/api-tokens/"+tokA.ID, nil)
	w.tokExpect(t, 200, tokA.Plaintext, "GET", "/environments/"+w.devA+"/flags", nil)
	if list := decode[struct{ Tokens []struct{ Name string } }](t, h.expect(200, w.b, "GET", "/workspaces/"+w.wsB.ID+"/api-tokens", nil)).Tokens; len(list) != 1 || list[0].Name != "bob-admin" {
		t.Errorf("B's token list: %+v", list)
	}
}

// ---- audit -------------------------------------------------------------------------------------------

func TestAuditEntriesShowTheTokenAsTheActor(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	admin := w.joinAs("adam", "admin")
	tok := w.mint(t, admin, w.wsA.ID, "deploy-bot", "editor", "dev")
	flag := "/environments/" + w.devA + "/flags/" + secretFlag
	w.tokExpect(t, 200, tok.Plaintext, "PATCH", flag, map[string]any{"enabled": true})
	w.tokExpect(t, 200, tok.Plaintext, "POST", flag+"/kill", nil)
	w.tokExpect(t, 201, tok.Plaintext, "POST", flag+"/scheduled-changes", map[string]any{"idempotencyKey": "tok-sched", "kind": "enable", "runAt": w.clock.now().Add(time.Hour).Format(time.RFC3339)})
	h.expect(201, w.a, "POST", segPath(w.wsA.ID), segReq("user-seg", nil))

	type row struct {
		Action, ActorEmail, ActorType, Severity string
		ActorDetail                             map[string]string
	}
	entries := decode[struct{ Entries []row }](t, h.expect(200, w.a, "GET", "/environments/"+w.devA+"/audit-logs?limit=200", nil)).Entries
	find := func(action string) *row {
		for i := range entries {
			if entries[i].Action == action {
				return &entries[i]
			}
		}
		return nil
	}
	label := "deploy-bot (" + tok.Prefix + ")"
	for _, action := range []string{"flag.update", "flag.kill", "scheduled_change.create"} {
		r := find(action)
		if r == nil {
			t.Errorf("no %s entry", action)
			continue
		}
		if r.ActorType != "api_token" || r.ActorEmail != label || r.ActorDetail["tokenId"] != tok.ID || r.ActorDetail["name"] != "deploy-bot" ||
			r.ActorDetail["prefix"] != tok.Prefix || r.ActorDetail["createdBy"] != admin.email || r.ActorDetail["createdById"] != admin.id {
			t.Errorf("%s: %+v", action, r)
		}
		if strings.Contains(r.ActorEmail, tok.Plaintext) || r.ActorEmail == admin.email {
			t.Errorf("%s: the token's action is recorded as the person's or leaks: %q", action, r.ActorEmail)
		}
	}
	// the rows themselves, and the actor id is the token's, never the person's
	var actorID string
	if err := h.pool.QueryRow(t.Context(), `SELECT actor_id::text FROM audit_logs WHERE action = 'flag.update' AND actor_type = 'api_token'`).Scan(&actorID); err != nil || actorID != tok.ID {
		t.Errorf("actor_id %q (%v), want the token's id", actorID, err)
	}
	// a person's actions are unchanged: type user, no detail
	if r := find("segment.create"); r == nil || r.ActorType != "user" || r.ActorEmail != w.a.email || r.ActorDetail != nil {
		t.Errorf("a person's entry: %+v", r)
	}
	// creating and revoking a token are audited, as the person, and never contain the secret
	h.expect(204, w.a, "DELETE", "/workspaces/"+w.wsA.ID+"/api-tokens/"+tok.ID, nil)
	var creator, revoker, detail string
	h.pool.QueryRow(t.Context(), `SELECT actor_email, diff_after::text FROM audit_logs WHERE action = 'api_token.create' AND resource_id = $1`, tok.ID).Scan(&creator, &detail)
	h.pool.QueryRow(t.Context(), `SELECT actor_email FROM audit_logs WHERE action = 'api_token.revoke' AND resource_id = $1`, tok.ID).Scan(&revoker)
	if creator != admin.email || revoker != w.a.email {
		t.Errorf("token create/revoke actors: %q %q", creator, revoker)
	}
	if strings.Contains(detail, tok.Plaintext) || !strings.Contains(detail, tok.Prefix) || !strings.Contains(detail, "[REDACTED]") {
		t.Errorf("api_token.create audit: %s", detail)
	}
	if h.count(`SELECT count(*) FROM audit_logs WHERE diff_after::text LIKE '%' || $1 || '%' OR diff_before::text LIKE '%' || $1 || '%'`, tok.Plaintext[13:]) != 0 {
		t.Error("a token's secret is in an audit entry")
	}
}

// An expired or revoked token being presented is audited (at most once an hour per token), and still gets only the generic answer.
func TestExpiredAndRevokedUseIsAuditedOncePerHour(t *testing.T) {
	w := newTokWorld(t, nil)
	h := w.h
	exp := w.mint(t, w.a, w.wsA.ID, "stale-ci", "viewer")
	rev := w.mint(t, w.a, w.wsA.ID, "revoked-ci", "viewer")
	h.pool.Exec(t.Context(), `UPDATE api_tokens SET created_at = now() - interval '100 days', expires_at = now() - interval '1 day' WHERE id = $1::uuid`, exp.ID)
	h.expect(204, w.a, "DELETE", "/workspaces/"+w.wsA.ID+"/api-tokens/"+rev.ID, nil)
	rows := func(id string) (n int, reason, actorType, sev string) {
		n = h.count(`SELECT count(*) FROM audit_logs WHERE action = 'api_token.rejected' AND resource_id = $1`, id)
		h.pool.QueryRow(t.Context(), `SELECT diff_after->>'reason', actor_type, severity FROM audit_logs WHERE action = 'api_token.rejected' AND resource_id = $1 ORDER BY id LIMIT 1`, id).Scan(&reason, &actorType, &sev)
		return
	}
	path := "/environments/" + w.devA + "/flags"
	for i := 0; i < 5; i++ {
		if st, _, b := h.tokenCall(exp.Plaintext, "GET", path, nil); st != 401 || string(b) != genericBody {
			t.Fatalf("expired: %d %s", st, b)
		}
		h.tokenCall(rev.Plaintext, "GET", path, nil)
	}
	if n, reason, typ, sev := rows(exp.ID); n != 1 || reason != "expired" || typ != "api_token" || sev != "info" {
		t.Errorf("expired use: %d rows, reason %q type %q severity %q", n, reason, typ, sev)
	}
	if n, reason, _, sev := rows(rev.ID); n != 1 || reason != "revoked" || sev != "critical" {
		t.Errorf("revoked use: %d rows, reason %q severity %q", n, reason, sev)
	}
	w.clock.advance(61 * time.Minute)
	h.tokenCall(exp.Plaintext, "GET", path, nil)
	if n, _, _, _ := rows(exp.ID); n != 2 {
		t.Errorf("an hour later: %d rows, want 2", n)
	}
	// unknown tokens have no workspace: nothing to audit, nothing to learn from the answer
	unknown, _, _, _ := apitoken.Generate()
	h.tokenCall(unknown, "GET", path, nil)
	if h.count(`SELECT count(*) FROM audit_logs WHERE action = 'api_token.rejected'`) != 3 {
		t.Error("an unknown token was audited")
	}
}

// A revocation takes effect at once on the instance that handled it, even when
// no broadcast ever arrives and the cache would otherwise keep the token for an hour.
func TestRevocationIsImmediateOnTheInstanceThatHandledItEvenWithoutABroadcast(t *testing.T) {
	w := newTokWorld(t, nil, func(d *server.Deps) {
		d.APITokenCacheTTL = time.Hour
		d.Subscriber = deafToRevocations{events.NewMemoryBus()}
	})
	tok := w.mint(t, w.a, w.wsA.ID, "local-drop", "viewer")
	path := "/environments/" + w.devA + "/flags"
	w.tokExpect(t, 200, tok.Plaintext, "GET", path, nil)
	w.h.expect(204, w.a, "DELETE", "/workspaces/"+w.wsA.ID+"/api-tokens/"+tok.ID, nil)
	w.tokExpect(t, 401, tok.Plaintext, "GET", path, nil)
}
