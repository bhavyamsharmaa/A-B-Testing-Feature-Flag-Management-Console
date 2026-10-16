package integration

import (
	"strings"
	"testing"
	"time"
)

// joinAs adds a new member with the given workspace role to A's workspace.
func (x *tenants) joinAs(name, role string) user {
	x.h.t.Helper()
	u := x.h.newUser(name)
	x.h.me(u)
	tok := decode[struct{ Token string }](x.h.t, x.h.expect(201, x.a, "POST", "/workspaces/"+x.wsA.ID+"/invites", map[string]any{"email": u.email, "role": role})).Token
	x.h.expect(200, u, "POST", "/invites/accept", map[string]any{"token": tok})
	return u
}

type promoPreview struct {
	FlagKey        string `json:"flagKey"`
	Source, Target struct {
		Environment string
		Version     int64
		Enabled     bool
	}
	Parts []struct {
		Part              string
		Selected, Changed bool
	}
	DefaultParts    []string
	Problems        []string
	NothingToApply  bool
	CanApply        bool
	ExpectedVersion int64
	SourceVersion   int64
}

func (p promoPreview) part(name string) (selected, changed bool) {
	for _, q := range p.Parts {
		if q.Part == name {
			return q.Selected, q.Changed
		}
	}
	return false, false
}

func promoPath(env, flag string, rest string) string {
	return "/environments/" + env + "/flags/" + flag + "/promotions" + rest
}

// promoWorld: a flag configured in dev (rules, rollout, fallthrough, enabled) and untouched in staging and production.
func promoWorld(t *testing.T) (*tenants, string, string) {
	x := newSegTenants(t)
	h := x.h
	staging, prod := envID(t, x.wsA, "staging"), envID(t, x.wsA, "production")
	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", boolFlag("promo"))
	h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/promo", map[string]any{
		"enabled": true, "fallthroughVariationId": "off",
		"rollout":        map[string]any{"on": 25000, "off": 75000},
		"targetingRules": []map[string]any{{"clauses": []map[string]any{clause("plan", "equals", "pro")}, "variationId": "on"}},
	})
	return x, staging, prod
}

func TestPromotionPreviewAndApply(t *testing.T) {
	x, staging, _ := promoWorld(t)
	h := x.h
	preview := decode[promoPreview](t, h.expect(200, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev"}))
	if preview.Source.Environment != "dev" || preview.Target.Environment != "staging" || !preview.CanApply || preview.NothingToApply {
		t.Fatalf("preview = %+v", preview)
	}
	// by default the enabled state is NOT part of it, though it differs
	if sel, changed := preview.part("enabled"); sel || !changed {
		t.Errorf("enabled: selected=%v changed=%v, want unselected but different", sel, changed)
	}
	for _, p := range []string{"rules", "rollout", "fallthrough"} {
		if sel, changed := preview.part(p); !sel || !changed {
			t.Errorf("%s: selected=%v changed=%v", p, sel, changed)
		}
	}
	// the preview wrote nothing
	if got := decode[struct {
		Config struct {
			Version int64
			Enabled bool
		}
	}](t, h.expect(200, x.a, "GET", "/environments/"+staging+"/flags/promo", nil)).Config; got.Version != preview.ExpectedVersion || got.Enabled {
		t.Errorf("a preview changed the target: %+v", got)
	}

	stream := h.openStream(h.newSDKKey(x.a, staging))
	view := decode[struct {
		Config struct {
			Enabled                bool
			FallthroughVariationID string
			Version                int64
			TargetingRules         []map[string]any
			Rollout                map[string]float64
		}
	}](t, h.expect(200, x.a, "POST", promoPath(staging, "promo", ""), map[string]any{
		"sourceEnvironment": "dev", "expectedVersion": preview.ExpectedVersion, "sourceVersion": preview.SourceVersion}))
	c := view.Config
	if c.Enabled || c.FallthroughVariationID != "off" || len(c.TargetingRules) != 1 || c.Rollout["on"] != 25000 || c.Version != preview.ExpectedVersion+1 {
		t.Errorf("applied config = %+v", c)
	}
	// it was an ordinary edit: audited with the source named, and fanned out
	var action, actor, after string
	if err := h.pool.QueryRow(t.Context(), `
		SELECT action, actor_email, diff_after::text FROM audit_logs
		WHERE workspace_id = $1::uuid AND resource_id = 'promo' AND action = 'flag.update' AND diff_after::text LIKE '%promotedFrom%'`, x.wsA.ID).Scan(&action, &actor, &after); err != nil {
		t.Fatalf("no audit row for the promotion: %v", err)
	}
	if actor != x.a.email || !strings.Contains(after, `"promotedFrom": "dev"`) {
		t.Errorf("audit: actor %q after %s", actor, after)
	}
	if msg := stream.next(2 * time.Second); !strings.Contains(msg, `"flagKey":"promo"`) {
		t.Errorf("no flag event after a promotion: %q", msg)
	}
	// previewing again: nothing left to copy
	again := decode[promoPreview](t, h.expect(200, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev"}))
	if !again.NothingToApply || again.CanApply {
		t.Errorf("after applying: %+v", again)
	}
	h.expect(409, x.a, "POST", promoPath(staging, "promo", ""), map[string]any{
		"sourceEnvironment": "dev", "expectedVersion": again.ExpectedVersion, "sourceVersion": again.SourceVersion})
	// the enabled state only moves when asked for
	en := decode[promoPreview](t, h.expect(200, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev", "parts": []string{"enabled"}}))
	if !en.CanApply {
		t.Fatalf("enabled preview: %+v", en)
	}
	got := decode[struct{ Config struct{ Enabled bool } }](t, h.expect(200, x.a, "POST", promoPath(staging, "promo", ""), map[string]any{
		"sourceEnvironment": "dev", "parts": []string{"enabled"}, "expectedVersion": en.ExpectedVersion, "sourceVersion": en.SourceVersion}))
	if !got.Config.Enabled {
		t.Error("promoting the enabled part did not enable the flag")
	}
}

// Promotion is an edit of the target: the same role gate (an approver for production), the same audit.
func TestPromotionUsesTheEditGate(t *testing.T) {
	x, staging, prod := promoWorld(t)
	h := x.h
	editor := x.joinAs("eddie", "editor")
	viewer := x.joinAs("vera", "viewer")
	body := func(p promoPreview) map[string]any {
		return map[string]any{"sourceEnvironment": "dev", "expectedVersion": p.ExpectedVersion, "sourceVersion": p.SourceVersion}
	}
	pStaging := decode[promoPreview](t, h.expect(200, editor, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev"}))
	pProd := decode[promoPreview](t, h.expect(200, viewer, "POST", promoPath(prod, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev"})) // a viewer may look
	h.expect(403, viewer, "POST", promoPath(staging, "promo", ""), body(pStaging))
	h.expect(403, editor, "POST", promoPath(prod, "promo", ""), body(pProd)) // production needs an approver, as for PATCH
	h.expect(403, editor, "PATCH", "/environments/"+prod+"/flags/promo", map[string]any{"fallthroughVariationId": "off"})
	if h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND resource_id = 'promo' AND diff_after::text LIKE '%promotedFrom%'`, x.wsA.ID) != 0 {
		t.Fatal("a refused promotion was audited as applied")
	}
	h.expect(200, editor, "POST", promoPath(staging, "promo", ""), body(pStaging)) // an editor may edit staging
	h.expect(200, x.a, "POST", promoPath(prod, "promo", ""), body(pProd))          // the owner (an approver) may edit production
	if n := h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND resource_id = 'promo' AND diff_after::text LIKE '%promotedFrom%'`, x.wsA.ID); n != 2 {
		t.Errorf("%d promotion audit rows, want 2", n)
	}
	var who string
	_ = h.pool.QueryRow(t.Context(), `SELECT actor_email FROM audit_logs WHERE resource_id = 'promo' AND diff_after::text LIKE '%promotedFrom%' ORDER BY id LIMIT 1`).Scan(&who)
	if who != editor.email {
		t.Errorf("the staging promotion is recorded as %q, want the editor", who)
	}
}

func TestPromotionNeverOverwritesAConcurrentEdit(t *testing.T) {
	x, staging, _ := promoWorld(t)
	h := x.h
	p := decode[promoPreview](t, h.expect(200, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev"}))
	// someone edits the TARGET after the preview
	h.expect(200, x.a, "PATCH", "/environments/"+staging+"/flags/promo", map[string]any{"fallthroughVariationId": "on"})
	b := h.expect(409, x.a, "POST", promoPath(staging, "promo", ""), map[string]any{"sourceEnvironment": "dev", "expectedVersion": p.ExpectedVersion, "sourceVersion": p.SourceVersion})
	if errCode(t, b) != "VERSION_CONFLICT" {
		t.Errorf("target changed: %s", b)
	}
	cur := decode[struct {
		Config struct {
			FallthroughVariationID string
			TargetingRules         []any
		}
	}](t, h.expect(200, x.a, "GET", "/environments/"+staging+"/flags/promo", nil))
	if cur.Config.FallthroughVariationID != "on" || len(cur.Config.TargetingRules) != 0 {
		t.Errorf("the concurrent edit was overwritten: %+v", cur.Config)
	}
	// someone edits the SOURCE after the preview
	p2 := decode[promoPreview](t, h.expect(200, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev"}))
	h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/promo", map[string]any{"rollout": map[string]any{"on": 50000, "off": 50000}})
	if b := h.expect(409, x.a, "POST", promoPath(staging, "promo", ""), map[string]any{"sourceEnvironment": "dev", "expectedVersion": p2.ExpectedVersion, "sourceVersion": p2.SourceVersion}); errCode(t, b) != "SOURCE_CHANGED" {
		t.Errorf("source changed: %s", b)
	}
	// the versions are required
	h.expect(400, x.a, "POST", promoPath(staging, "promo", ""), map[string]any{"sourceEnvironment": "dev"})
}

func TestPromotionRefusesWhatAnEditWouldRefuse(t *testing.T) {
	x, staging, _ := promoWorld(t)
	h := x.h
	// segments are workspace-level: a rule naming one promotes fine...
	h.expect(201, x.a, "POST", segPath(x.wsA.ID), segReq("beta", []string{"u1"}))
	h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/promo", map[string]any{
		"targetingRules": []map[string]any{{"clauses": []map[string]any{inSeg("beta")}, "variationId": "on"}}})
	p := decode[promoPreview](t, h.expect(200, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev"}))
	if !p.CanApply || len(p.Problems) != 0 {
		t.Fatalf("a rule naming an existing segment: %+v", p)
	}
	// ...but not once the segment is gone (deleted with confirmation) and the source rule is stale
	h.expect(204, x.a, "DELETE", segPath(x.wsA.ID, "/beta?confirm=true"), nil)
	stale := decode[promoPreview](t, h.expect(200, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev"}))
	if stale.CanApply || len(stale.Problems) != 1 || !strings.Contains(stale.Problems[0], `segment "beta" does not exist`) {
		t.Fatalf("stale segment preview: %+v", stale)
	}
	before := decode[struct{ Config struct{ Version int64 } }](t, h.expect(200, x.a, "GET", "/environments/"+staging+"/flags/promo", nil)).Config.Version
	b := h.expect(400, x.a, "POST", promoPath(staging, "promo", ""), map[string]any{"sourceEnvironment": "dev", "expectedVersion": stale.ExpectedVersion, "sourceVersion": stale.SourceVersion})
	if !strings.Contains(errMessage(b), "does not exist") {
		t.Errorf("apply with a stale segment: %s", b)
	}
	// nothing was partly applied: the other parts did not move either
	after := decode[struct {
		Config struct {
			Version                int64
			FallthroughVariationID string
		}
	}](t, h.expect(200, x.a, "GET", "/environments/"+staging+"/flags/promo", nil)).Config
	if after.Version != before || after.FallthroughVariationID == "off" {
		t.Errorf("a refused promotion changed the target: %+v", after)
	}

	// a variation that does not exist (bypassing the API, as corrupt data would)
	h.pool.Exec(t.Context(), `UPDATE flag_configs SET targeting_rules = '[{"clauses":[{"attribute":"a","operator":"equals","value":"x"}],"variationId":"ghost"}]'::jsonb
		WHERE environment_id = $1::uuid AND flag_id = (SELECT id FROM flags WHERE key = 'promo' AND workspace_id = $2::uuid)`, x.devA, x.wsA.ID)
	vm := decode[promoPreview](t, h.expect(200, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev"}))
	if vm.CanApply || len(vm.Problems) == 0 || !strings.Contains(vm.Problems[0], "ghost") {
		t.Errorf("variation mismatch preview: %+v", vm)
	}
	h.expect(400, x.a, "POST", promoPath(staging, "promo", ""), map[string]any{"sourceEnvironment": "dev", "expectedVersion": vm.ExpectedVersion, "sourceVersion": vm.SourceVersion})
	// bad requests
	h.expect(400, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "staging"})
	h.expect(400, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{})
	h.expect(400, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev", "parts": []string{"salt"}})
	h.expect(404, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "nowhere"})
	h.expect(404, x.a, "POST", promoPath(staging, "nope", "/preview"), map[string]any{"sourceEnvironment": "dev"})
}

func TestPromotionIsTenantScoped(t *testing.T) {
	x, staging, _ := promoWorld(t)
	h := x.h
	stagingB := envID(t, x.wsB, "staging")
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/flags", boolFlag("promo"))
	// B cannot promote into A's environment, nor preview it
	h.expect(404, x.b, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": "dev"})
	h.expect(404, x.b, "POST", promoPath(staging, "promo", ""), map[string]any{"sourceEnvironment": "dev", "expectedVersion": 1, "sourceVersion": 1})
	// B cannot name A's environment as the SOURCE, by id or by key: it only resolves inside B's workspace
	for _, src := range []string{x.devA, strings.ToUpper(x.devA), staging} {
		b := h.expect(404, x.b, "POST", promoPath(stagingB, "promo", "/preview"), map[string]any{"sourceEnvironment": src})
		if errCode(t, b) != "ENVIRONMENT_NOT_FOUND" || strings.Contains(string(b), "acme") {
			t.Errorf("source %q: %s", src, b)
		}
	}
	// and A cannot pull from B's
	h.expect(404, x.a, "POST", promoPath(staging, "promo", "/preview"), map[string]any{"sourceEnvironment": x.devB})
}
