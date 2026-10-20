package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"helios/backend/internal/controlplane/scheduling"
)

type histEntry struct {
	ID        int64
	CreatedAt string
	Action    string
	Summary   string
	Source    string
	Actor     struct{ Type, Label, OnBehalfOf string }
	Changes   []struct {
		Field, Label  string
		Before, After []string
	}
}

type histPage struct {
	Entries    []histEntry
	NextCursor *int64
}

func (w *hookWorld) history(t *testing.T, u user, env, flag, query string) histPage {
	t.Helper()
	return decode[histPage](t, w.h.expect(200, u, "GET", "/environments/"+env+"/flags/"+flag+"/history"+query, nil))
}

func (e histEntry) change(field string) (before, after string, ok bool) {
	for _, c := range e.Changes {
		if c.Field == field {
			return strings.Join(c.Before, " | "), strings.Join(c.After, " | "), true
		}
	}
	return "", "", false
}

func TestAFlagsHistoryShowsWhoChangedWhatAndWhen(t *testing.T) {
	w := newHookWorld(t)
	h := w.h
	ws := w.wsA.ID
	dev := "/environments/" + w.devA
	staging := envID(t, w.wsA, "staging")
	h.expect(201, w.a, "POST", dev+"/flags", boolFlag("hist"))
	h.expect(200, w.a, "PATCH", dev+"/flags/hist", map[string]any{"enabled": true, "rollout": map[string]any{"on": 25000, "off": 75000}})
	h.expect(200, w.a, "PATCH", dev+"/flags/hist", map[string]any{"targetingRules": []map[string]any{{"clauses": []map[string]any{clause("plan", "equals", "pro")}, "variationId": "on"}}, "fallthroughVariationId": "off"})
	// an API token's edit
	tok := decode[struct{ Plaintext string }](t, h.expect(201, w.a, "POST", "/workspaces/"+ws+"/api-tokens", map[string]any{"name": "deploy-bot", "role": "editor"})).Plaintext
	if st, _, b := h.tokenCall(tok, "PATCH", dev+"/flags/hist", map[string]any{"enabled": false}); st != 200 {
		t.Fatalf("token edit: %d %s", st, b)
	}
	// a scheduled change, run later
	sw := &schedWorld{tenants: w.tenants, clock: &fakeClock{t: time.Now().UTC()}, staging: staging}
	sw.scheduleOK(t, w.a, w.devA, "hist", "sch", "enable", 5*time.Minute, nil)
	sw.clock.advance(6 * time.Minute)
	if _, err := scheduling.NewRunner(h.pool, h.bus, sw.clock.now, scheduling.Config{WorkerID: "hist"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// the kill switch, and an edit of another flag and another environment that must not appear
	h.expect(200, w.a, "POST", dev+"/flags/hist/kill", nil)
	h.expect(200, w.a, "PATCH", dev+"/flags/"+secretFlag, map[string]any{"enabled": false})
	h.expect(200, w.a, "PATCH", "/environments/"+staging+"/flags/hist", map[string]any{"enabled": true})
	// a promotion into dev from staging
	pre := decode[promoPreview](t, h.expect(200, w.a, "POST", promoPath(w.devA, "hist", "/preview"), map[string]any{"sourceEnvironment": "staging", "parts": []string{"enabled"}}))
	h.expect(200, w.a, "POST", promoPath(w.devA, "hist", ""), map[string]any{"sourceEnvironment": "staging", "parts": []string{"enabled"}, "expectedVersion": pre.ExpectedVersion, "sourceVersion": pre.SourceVersion})

	// a viewer can read it
	ow := &ownWorld{tenants: w.tenants, clock: &liveClock{}}
	vw := ow.join(t, w.a, ws, "vw", "viewer")
	page := w.history(t, vw, w.devA, "hist", "")
	if len(page.Entries) != 7 {
		t.Fatalf("%d entries, want 7 (create, 2 edits, token, schedule, kill, promotion): %+v", len(page.Entries), page.Entries)
	}
	// newest first
	for i := 1; i < len(page.Entries); i++ {
		if page.Entries[i].ID >= page.Entries[i-1].ID {
			t.Fatal("not newest first")
		}
	}
	by := map[string]histEntry{}
	for _, e := range page.Entries {
		switch {
		case strings.HasPrefix(e.Source, "promotion"):
			by["promotion"] = e
		case e.Actor.Type == "api_token":
			by["token"] = e
		case e.Actor.Type == "schedule":
			by["schedule"] = e
		case e.Action == "flag.kill":
			by["kill"] = e
		case e.Action == "flag.create":
			by["create"] = e
		default:
			if _, ok := by["edit2"]; ok {
				by["edit1"] = e
			} else if _, _, ok := e.change("targetingRules"); ok {
				by["edit2"] = e
			} else {
				by["edit1"] = e
			}
		}
	}
	if e := by["token"]; e.Actor.Label != "deploy-bot ("+tokenPrefix(tok)+")" || e.Actor.OnBehalfOf != w.a.email {
		t.Errorf("token actor: %+v", e.Actor)
	}
	if b, a, ok := by["token"].change("enabled"); !ok || b != "on" || a != "off" {
		t.Errorf("token change: %v %v", b, a)
	}
	if e := by["schedule"]; e.Actor.OnBehalfOf != w.a.email || e.Source != "scheduled change" {
		t.Errorf("schedule actor: %+v / %q", e.Actor, e.Source)
	}
	if e := by["promotion"]; e.Source != "promotion from staging" || e.Summary != "Configuration promoted" {
		t.Errorf("promotion: %+v", e)
	}
	if b, a, ok := by["kill"].change("enabled"); !ok || b != "on" || a != "off" || by["kill"].Summary != "Kill switch used" {
		t.Errorf("kill: %v %v %+v", b, a, by["kill"])
	}
	if e := by["create"]; e.Actor.Label != w.a.email || e.Actor.Type != "user" || e.Summary != "Flag created" {
		t.Errorf("create: %+v", e)
	}
	e2 := by["edit2"]
	if b, a, ok := e2.change("targetingRules"); !ok || b != "no rules" || !strings.Contains(a, `plan equals "pro" → on`) {
		t.Errorf("rules change: %q → %q", b, a)
	}
	if b, a, ok := e2.change("fallthroughVariationId"); !ok || b != "on" || a != "off" {
		t.Errorf("fallthrough change: %q → %q", b, a)
	}
	if _, _, ok := e2.change("enabled"); ok {
		t.Error("an unchanged field is listed")
	}
	if b, a, ok := by["edit1"].change("rollout"); !ok || b != "none" || a != "off: 75.0% | on: 25.0%" {
		t.Errorf("rollout change: %q → %q", b, a)
	}
	// times carry their zone (UTC, RFC 3339), so the console can show it
	for _, e := range page.Entries {
		if _, err := time.Parse(time.RFC3339, e.CreatedAt); err != nil || !strings.HasSuffix(e.CreatedAt, "Z") {
			t.Errorf("created at %q", e.CreatedAt)
		}
	}
	// nothing of the other flag or the other environment
	raw, _ := json.Marshal(page)
	if bytes.Contains(raw, []byte(secretFlag)) {
		t.Error("another flag's entries appear")
	}
	if n := len(w.history(t, w.a, staging, "hist", "").Entries); n != 2 { // the workspace-wide creation, and the one staging edit
		t.Errorf("staging history has %d entries, want 2", n)
	}
}

func tokenPrefix(plain string) string { return plain[:12] }

func TestHistoryIsPagedAndIsolated(t *testing.T) {
	w := newHookWorld(t)
	h := w.h
	dev := "/environments/" + w.devA
	for i := 0; i < 7; i++ {
		h.expect(200, w.a, "PATCH", dev+"/flags/"+secretFlag, map[string]any{"enabled": i%2 == 0})
	}
	all := w.history(t, w.a, w.devA, secretFlag, "?limit=100")
	if len(all.Entries) != 9 || all.NextCursor != nil { // the flag's creation, the setup's enabling, and 7 edits
		t.Fatalf("%d entries, cursor %v", len(all.Entries), all.NextCursor)
	}
	seen := map[int64]bool{}
	q := "?limit=3"
	pages := 0
	for {
		p := w.history(t, w.a, w.devA, secretFlag, q)
		pages++
		for _, e := range p.Entries {
			if seen[e.ID] {
				t.Fatalf("entry %d on two pages", e.ID)
			}
			seen[e.ID] = true
		}
		if p.NextCursor == nil {
			break
		}
		q = "?limit=3&before=" + itoa64(*p.NextCursor)
	}
	if pages != 3 || len(seen) != 9 {
		t.Errorf("%d pages, %d entries", pages, len(seen))
	}
	for _, bad := range []string{"?limit=0", "?limit=101", "?limit=x", "?before=0", "?before=x"} {
		if st, _ := h.call(w.a, "GET", dev+"/flags/"+secretFlag+"/history"+bad, nil); st != 400 {
			t.Errorf("%s: %d", bad, st)
		}
	}
	// another tenant, and a flag that does not exist
	if st, _ := h.call(w.b, "GET", dev+"/flags/"+secretFlag+"/history", nil); st != 404 {
		t.Errorf("another tenant: %d", st)
	}
	if p := w.history(t, w.a, w.devA, "no-such-flag", ""); len(p.Entries) != 0 {
		t.Errorf("a made-up flag has %d entries", len(p.Entries))
	}
	// B's own flag of the same key shows only B's
	h.expect(201, w.b, "POST", "/environments/"+w.devB+"/flags", boolFlag(secretFlag))
	for _, e := range w.history(t, w.b, w.devB, secretFlag, "").Entries {
		if e.Actor.Label == w.a.email {
			t.Error("A's entries in B's history")
		}
	}
	// read only: no way to restore through this part
	for _, rt := range h.routes {
		if strings.Contains(rt.Path, "history") && rt.Method != "GET" {
			t.Errorf("%s %s: history is read only", rt.Method, rt.Path)
		}
		if strings.Contains(rt.Path, "restore") && strings.Contains(rt.Path, "flags") {
			t.Errorf("%s %s: no restore of flag versions in this part", rt.Method, rt.Path)
		}
	}
}

func TestHistoryNeverShowsASecret(t *testing.T) {
	w := newHookWorld(t)
	secret := "hlt_0123abcd_" + strings.Repeat("Z", 43)
	if err := w.h.rawExec(`INSERT INTO audit_logs (workspace_id, environment_id, actor_email, action, resource_type, resource_id, diff_before, diff_after)
		VALUES ($1::uuid, $2::uuid, $3, 'flag.update', 'flag', 'leaky', '{"enabled":false}'::jsonb, $4::jsonb)`,
		w.wsA.ID, w.devA, "who "+secret, `{"enabled":true,"fallthroughVariationId":"`+secret+`","targetingRules":[{"clauses":[{"attribute":"k","operator":"equals","value":"`+secret+`"}],"variationId":"on"}]}`); err != nil {
		t.Fatal(err)
	}
	raw := w.h.expect(200, w.a, "GET", "/environments/"+w.devA+"/flags/leaky/history", nil)
	if bytes.Contains(raw, []byte(secret)) {
		t.Errorf("a secret-shaped value appears: %s", raw)
	}
	if !bytes.Contains(raw, []byte("[REDACTED]")) {
		t.Error("nothing was redacted")
	}
}

func itoa64(n int64) string {
	return strings.TrimSpace(strings.Join(strings.Fields(strings.Repeat(" ", 0)+fmtInt(n)), ""))
}

func fmtInt(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
