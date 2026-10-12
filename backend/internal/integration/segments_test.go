package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/dataplane/evaluation"
	"helios/backend/internal/platform/redisx"
	"helios/backend/internal/server"
)

// ---- helpers ------------------------------------------------------------

func segPath(ws string, rest ...string) string {
	return "/workspaces/" + ws + "/segments" + strings.Join(rest, "")
}

func segReq(key string, members []string, rules ...map[string]any) map[string]any {
	r := map[string]any{"key": key, "name": "Segment " + key, "description": "d", "members": members}
	if len(rules) > 0 {
		r["rules"] = rules
	}
	return r
}

func clause(attr, op string, value any) map[string]any {
	return map[string]any{"attribute": attr, "operator": op, "value": value}
}

func inSeg(key string) map[string]any { return map[string]any{"operator": "inSegment", "value": key} }
func notInSeg(key string) map[string]any {
	return map[string]any{"operator": "notInSegment", "value": key}
}

// ruleFlag creates an enabled boolean flag in A's dev environment that serves
// "on" when the clauses match and "off" otherwise.
func (x *tenants) ruleFlag(key string, clauses ...map[string]any) {
	x.ruleFlagAs(x.a, x.devA, key, clauses...)
}

func (x *tenants) ruleFlagAs(u user, env, key string, clauses ...map[string]any) {
	x.h.t.Helper()
	x.h.expect(201, u, "POST", "/environments/"+env+"/flags", boolFlag(key))
	x.h.expect(200, u, "PATCH", "/environments/"+env+"/flags/"+key, map[string]any{
		"enabled": true, "fallthroughVariationId": "off",
		"targetingRules": []map[string]any{{"clauses": clauses, "variationId": "on"}},
	})
}

type served struct{ Variation, Reason string }

func (x *tenants) serve(sdkKey, subject string, attrs map[string]any, flag string) served {
	x.h.t.Helper()
	raw := x.h.sdkPost(sdkKey, "/evaluate", map[string]any{"context": map[string]any{"subjectKey": subject, "attributes": attrs}, "flagKeys": []string{flag}})
	var out struct {
		Evaluations []struct {
			VariationID *string `json:"variationId"`
			Reason      string  `json:"reason"`
		} `json:"evaluations"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Evaluations) != 1 {
		x.h.t.Fatalf("evaluate: %v %s", err, raw)
	}
	s := served{Reason: out.Evaluations[0].Reason}
	if out.Evaluations[0].VariationID != nil {
		s.Variation = *out.Evaluations[0].VariationID
	}
	return s
}

func (x *tenants) wantServed(sdkKey, subject string, attrs map[string]any, flag, variation string) {
	x.h.t.Helper()
	if got := x.serve(sdkKey, subject, attrs, flag); got.Variation != variation {
		x.h.t.Errorf("%s (%v) on %s: served %+v, want %s", subject, attrs, flag, got, variation)
	}
}

type segView struct {
	Key         string           `json:"key"`
	Name        string           `json:"name"`
	Rules       []map[string]any `json:"rules"`
	MemberCount int              `json:"memberCount"`
	Members     []string         `json:"members"`
	Version     int64            `json:"version"`
}

func (x *tenants) getSeg(u user, ws, key string) segView {
	x.h.t.Helper()
	return decode[segView](x.h.t, x.h.expect(200, u, "GET", segPath(ws, "/", key), nil))
}

func errCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct{ Code, Message string }
	_ = json.Unmarshal(body, &e)
	return e.Code
}

func errMessage(body []byte) string {
	var e struct{ Code, Message string }
	_ = json.Unmarshal(body, &e)
	return e.Message
}

func flagVersion(t *testing.T, x *tenants, env, flag string) int64 {
	t.Helper()
	return decode[struct{ Config struct{ Version int64 } }](t, x.h.expect(200, x.a, "GET", "/environments/"+env+"/flags/"+flag, nil)).Config.Version
}

// ---- evaluation ----------------------------------------------------------

func TestSegmentEvaluationEndToEnd(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := x.wsA.ID
	h.expect(201, x.a, "POST", segPath(ws), segReq("beta", []string{"vip-1", "vip-2"},
		clause("plan", "equals", "pro"), clause("country", "equals", "IN")))
	x.ruleFlag("in-beta", inSeg("beta"))
	x.ruleFlag("not-in-beta", notInSeg("beta"))
	k := x.sdkKeyA

	for _, tc := range []struct {
		name    string
		subject string
		attrs   map[string]any
		in      bool
	}{
		{"explicit member without attributes", "vip-1", nil, true},
		{"explicit member who fails the rules", "vip-2", map[string]any{"plan": "free"}, true},
		{"rules match, not a member", "u9", map[string]any{"plan": "pro", "country": "IN"}, true},
		{"only one of the rules", "u9", map[string]any{"plan": "pro", "country": "US"}, false},
		{"neither", "u9", map[string]any{"plan": "free"}, false},
		{"member keys are exact", "VIP-1", nil, false},
	} {
		want, wantNot := "off", "on"
		if tc.in {
			want, wantNot = "on", "off"
		}
		if got := x.serve(k, tc.subject, tc.attrs, "in-beta"); got.Variation != want {
			t.Errorf("inSegment, %s: %+v, want %s", tc.name, got, want)
		}
		if got := x.serve(k, tc.subject, tc.attrs, "not-in-beta"); got.Variation != wantNot {
			t.Errorf("notInSegment, %s: %+v, want %s", tc.name, got, wantNot)
		}
	}
	if got := x.serve(k, "vip-1", nil, "in-beta"); got.Reason != "TARGETING_MATCH" {
		t.Errorf("reason = %s", got.Reason)
	}

	// Several flags referencing several segments in one request.
	h.expect(201, x.a, "POST", segPath(ws), segReq("gamma", []string{"u-gamma"}))
	x.ruleFlag("in-gamma", inSeg("gamma"))
	raw := h.sdkPost(k, "/evaluate", map[string]any{"context": map[string]any{"subjectKey": "u-gamma"}, "flagKeys": []string{"in-beta", "in-gamma", "not-in-beta"}})
	if !bytes.Contains(raw, []byte(`"flagKey":"in-gamma","variationId":"on"`)) || !bytes.Contains(raw, []byte(`"flagKey":"in-beta","variationId":"off"`)) {
		t.Errorf("batch evaluation: %s", raw)
	}
}

// The members lookup is one probe of the members table's primary key, never a
// scan, whatever the segment's size.
func TestMemberLookupUsesTheIndex(t *testing.T) {
	x := newTenants(t)
	h := x.h
	members := make([]string, quota.MaxSegmentMembers)
	for i := range members {
		members[i] = fmt.Sprintf("user-%05d", i)
	}
	h.expect(201, x.a, "POST", segPath(x.wsA.ID), segReq("big", members))
	rows, err := h.pool.Query(context.Background(), "EXPLAIN "+evaluation.SegmentsQuery, x.wsA.ID, []string{"big"}, "user-09999")
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		_ = rows.Scan(&line)
		plan.WriteString(line + "\n")
	}
	rows.Close()
	// The subject key itself must be part of the index condition: a probe that
	// only used the workspace and segment columns would read every member.
	if !strings.Contains(plan.String(), "segment_members_pkey") || strings.Contains(plan.String(), "Seq Scan") ||
		!strings.Contains(plan.String(), "(workspace_id = s.workspace_id) AND (segment_id = s.id) AND (subject_key = ") {
		t.Errorf("the membership probe is not a lookup of (workspace, segment, subject) in the primary key:\n%s", plan.String())
	}

	x.ruleFlag("in-big", inSeg("big"))
	x.wantServed(x.sdkKeyA, "user-09999", nil, "in-big", "on")
	x.wantServed(x.sdkKeyA, "user-10000", nil, "in-big", "off")
	start := time.Now()
	for i := 0; i < 50; i++ {
		x.serve(x.sdkKeyA, fmt.Sprintf("user-%05d", i*200), nil, "in-big")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("50 evaluations against a %d-member segment took %v", len(members), d)
	}
}

// ---- propagation and audit ---------------------------------------------------

func TestSegmentChangesPropagateAndAreAudited(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := x.wsA.ID
	members := make([]string, 400)
	for i := range members {
		members[i] = fmt.Sprintf("member-%03d", i)
	}
	h.expect(201, x.a, "POST", segPath(ws), segReq("beta", members))
	x.ruleFlag("gated", inSeg("beta"))
	x.ruleFlag("unrelated", clause("plan", "equals", "pro"))
	stream := h.openStream(x.sdkKeyA)
	k := x.sdkKeyA

	x.wantServed(k, "newcomer", nil, "gated", "off")
	gatedBefore, unrelatedBefore := flagVersion(t, x, x.devA, "gated"), flagVersion(t, x, x.devA, "unrelated")
	segBefore := x.getSeg(x.a, ws, "beta")

	// Adding a member changes the answer at once, and the stream hears about it.
	start := time.Now()
	h.expect(200, x.a, "PATCH", segPath(ws, "/beta"), map[string]any{"members": append([]string{"newcomer"}, members...), "expectedVersion": segBefore.Version})
	x.wantServed(k, "newcomer", nil, "gated", "on")
	msg := stream.next(2 * time.Second)
	if !strings.Contains(msg, `"action":"segment_updated"`) || !strings.Contains(msg, `"segmentKey":"beta"`) || !strings.Contains(msg, `"gated"`) {
		t.Fatalf("stream event = %q", msg)
	}
	if strings.Contains(msg, "unrelated") {
		t.Errorf("the event names a flag that does not use the segment: %s", msg)
	}
	t.Logf("a member added: evaluation changed and the stream event arrived within %v", time.Since(start))

	// The flag configs that use the segment got a new version; others did not.
	if v := flagVersion(t, x, x.devA, "gated"); v != gatedBefore+1 {
		t.Errorf("gated version %d, want %d", v, gatedBefore+1)
	}
	if v := flagVersion(t, x, x.devA, "unrelated"); v != unrelatedBefore {
		t.Errorf("an unrelated flag's version changed: %d -> %d", unrelatedBefore, v)
	}
	if after := x.getSeg(x.a, ws, "beta"); after.Version != segBefore.Version+1 || after.MemberCount != 401 {
		t.Errorf("segment after = version %d members %d", after.Version, after.MemberCount)
	}

	// Removing it flips the answer back; rules changes propagate too.
	h.expect(200, x.a, "PATCH", segPath(ws, "/beta"), map[string]any{"members": members})
	x.wantServed(k, "newcomer", nil, "gated", "off")
	if msg := stream.next(2 * time.Second); !strings.Contains(msg, "segment_updated") {
		t.Errorf("no event for the removal: %q", msg)
	}
	h.expect(200, x.a, "PATCH", segPath(ws, "/beta"), map[string]any{"rules": []map[string]any{clause("plan", "equals", "gold")}})
	x.wantServed(k, "someone", map[string]any{"plan": "gold"}, "gated", "on")
	if msg := stream.next(2 * time.Second); !strings.Contains(msg, "segment_updated") {
		t.Errorf("no event for the rules change: %q", msg)
	}

	// A rename or description change alters nobody's membership: no event, no flag version bump.
	v := flagVersion(t, x, x.devA, "gated")
	h.expect(200, x.a, "PATCH", segPath(ws, "/beta"), map[string]any{"name": "Renamed", "description": "new"})
	if msg := stream.next(400 * time.Millisecond); msg != "" {
		t.Errorf("a rename published %q", msg)
	}
	if flagVersion(t, x, x.devA, "gated") != v {
		t.Error("a rename bumped a flag version")
	}
	// A no-op update changes nothing at all.
	cur := x.getSeg(x.a, ws, "beta")
	h.expect(200, x.a, "PATCH", segPath(ws, "/beta"), map[string]any{"members": members})
	if x.getSeg(x.a, ws, "beta").Version != cur.Version {
		t.Error("an update that changed nothing bumped the version")
	}

	// Audit: who, what, before/after summaries, and never the member list.
	var n int
	var actor, severity, before, after string
	if err := h.pool.QueryRow(context.Background(), `
		SELECT count(*), max(actor_email), max(severity), max(diff_before::text), max(diff_after::text)
		FROM audit_logs WHERE workspace_id = $1::uuid AND resource_type = 'segment' AND resource_id = 'beta' AND action = 'segment.update'
		  AND diff_after::text LIKE '%"added": 1%'`, ws).Scan(&n, &actor, &severity, &before, &after); err != nil {
		t.Fatal(err)
	}
	if n != 1 || actor != x.a.email || severity != "info" {
		t.Fatalf("audit row: n=%d actor=%q severity=%q", n, actor, severity)
	}
	for _, want := range []string{`"memberCount": 401`, `"sampleAdded": ["newcomer"]`, `"flagConfigsUsing": 1`} {
		if !strings.Contains(after, want) {
			t.Errorf("audit diff_after lacks %s: %s", want, after)
		}
	}
	if !strings.Contains(before, `"memberCount": 400`) {
		t.Errorf("audit diff_before: %s", before)
	}
	var biggest int
	if err := h.pool.QueryRow(context.Background(), `
		SELECT max(length(coalesce(diff_before::text,'')) + length(coalesce(diff_after::text,''))) FROM audit_logs
		WHERE workspace_id = $1::uuid AND resource_type = 'segment'`, ws).Scan(&biggest); err != nil {
		t.Fatal(err)
	}
	if biggest > 4096 {
		t.Errorf("a segment audit row is %d bytes: it must summarise, not dump, the %d member keys", biggest, len(members))
	}
	if n := h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND resource_type = 'segment' AND (diff_before::text LIKE '%member-399%' OR diff_after::text LIKE '%member-399%')`, ws); n != 0 {
		t.Errorf("%d audit rows contain a member key beyond the sample", n)
	}
	if h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'segment.create' AND resource_id = 'beta'`, ws) != 1 {
		t.Error("segment.create was not audited")
	}
}

// ---- validation -------------------------------------------------------------

func TestSegmentValidation(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := x.wsA.ID
	post := func(want int, body any) []byte { return h.expect(want, x.a, "POST", segPath(ws), body) }

	post(201, segReq("ok", []string{"a"}))
	if code := errCode(t, post(409, segReq("ok", nil))); code != "SEGMENT_KEY_EXISTS" {
		t.Errorf("duplicate key: %s", code)
	}
	for name, body := range map[string]map[string]any{
		"bad key":                  segReq("-bad key", nil),
		"empty name":               {"key": "n1", "name": ""},
		"name too long":            {"key": "n2", "name": strings.Repeat("n", 121)},
		"unknown field":            {"key": "n3", "name": "n", "colour": "red"},
		"empty member":             segReq("m1", []string{"a", ""}),
		"member with newline":      segReq("m2", []string{"a\nb"}),
		"member with NUL":          segReq("m3", []string{"a\u0000b"}),
		"member too long":          segReq("m4", []string{strings.Repeat("m", 257)}),
		"unknown operator":         segReq("r1", nil, clause("a", "frobnicate", "x")),
		"invalid regex":            segReq("r2", nil, clause("a", "matchesRegex", "(")),
		"regex too long":           segReq("r3", nil, clause("a", "matchesRegex", strings.Repeat("a", 257))),
		"invalid semver":           segReq("r4", nil, clause("a", "semverGreaterThan", "1.2")),
		"invalid date":             segReq("r5", nil, clause("a", "dateBefore", "2024-01-01")),
		"clause without value":     segReq("r6", nil, map[string]any{"attribute": "a", "operator": "startsWith"}),
		"clause without attribute": segReq("r7", nil, map[string]any{"operator": "equals", "value": "x"}),
	} {
		b := post(400, body)
		if code := errCode(t, b); code != "INVALID_REQUEST" || errMessage(b) == "" {
			t.Errorf("%s: %s", name, b)
		}
	}
	// A nested reference is rejected, with a message that says why.
	for _, op := range []string{"inSegment", "notInSegment"} {
		b := post(400, segReq("nest-"+op, nil, map[string]any{"operator": op, "value": "ok"}))
		if !strings.Contains(errMessage(b), "segments cannot reference other segments") {
			t.Errorf("nested %s: %s", op, b)
		}
	}
	// ...at update time too, and nothing changed.
	b := h.expect(400, x.a, "PATCH", segPath(ws, "/ok"), map[string]any{"rules": []map[string]any{inSeg("ok")}})
	if !strings.Contains(errMessage(b), "cannot reference other segments") {
		t.Errorf("nested at update: %s", b)
	}
	if len(x.getSeg(x.a, ws, "ok").Rules) != 0 {
		t.Error("a rejected update changed the segment")
	}

	// Sizes are quota errors (409), not 400s.
	tooMany := make([]string, quota.MaxSegmentMembers+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("u%d", i)
	}
	if b := post(409, segReq("many", tooMany)); errCode(t, b) != "QUOTA_EXCEEDED" || !strings.Contains(errMessage(b), "explicit members") {
		t.Errorf("too many members: %s", b)
	}
	if b := h.expect(409, x.a, "PATCH", segPath(ws, "/ok"), map[string]any{"members": tooMany}); errCode(t, b) != "QUOTA_EXCEEDED" {
		t.Errorf("too many members at update: %s", b)
	}
	// Duplicates do not count twice.
	dups := make([]string, 0, 3*quota.MaxSegmentMembers)
	for i := 0; i < 3; i++ {
		dups = append(dups, tooMany[:quota.MaxSegmentMembers]...)
	}
	post(201, segReq("dups", dups))
	if got := x.getSeg(x.a, ws, "dups").MemberCount; got != quota.MaxSegmentMembers {
		t.Errorf("duplicates: %d members, want %d", got, quota.MaxSegmentMembers)
	}
	clauses := make([]map[string]any, quota.MaxSegmentRuleClauses+1)
	for i := range clauses {
		clauses[i] = clause("a", "exists", nil)
	}
	if b := post(409, segReq("clauses", nil, clauses...)); errCode(t, b) != "QUOTA_EXCEEDED" || !strings.Contains(errMessage(b), "rule clauses") {
		t.Errorf("too many clauses: %s", b)
	}
	bigList := make([]any, 220)
	for i := range bigList {
		bigList[i] = strings.Repeat("v", 40) + fmt.Sprint(i)
	}
	if b := post(409, segReq("bytes", nil, clause("a", "in", bigList))); errCode(t, b) != "QUOTA_EXCEEDED" || !strings.Contains(errMessage(b), "bytes of rule definition") {
		t.Errorf("rule definition too large: %s", b)
	}
	// An oversize body is refused before it is read in full.
	huge := strings.Repeat("x", 5<<20)
	if st, _ := h.call(x.a, "POST", segPath(ws), map[string]any{"key": "huge", "name": huge}); st != 413 {
		t.Errorf("a 5 MiB body: status %d, want 413", st)
	}
	// Optimistic concurrency.
	cur := x.getSeg(x.a, ws, "ok")
	h.expect(200, x.a, "PATCH", segPath(ws, "/ok"), map[string]any{"name": "first", "expectedVersion": cur.Version})
	if b := h.expect(409, x.a, "PATCH", segPath(ws, "/ok"), map[string]any{"name": "second", "expectedVersion": cur.Version}); errCode(t, b) != "STALE_SEGMENT" {
		t.Errorf("stale update: %s", b)
	}
	if b := h.expect(400, x.a, "PATCH", segPath(ws, "/ok"), map[string]any{}); errMessage(b) == "" {
		t.Error("an empty update")
	}
	h.expect(404, x.a, "GET", segPath(ws, "/nope"), nil)
	h.expect(404, x.a, "PATCH", segPath(ws, "/nope"), map[string]any{"name": "x"})
	h.expect(404, x.a, "DELETE", segPath(ws, "/nope"), nil)
	h.expect(404, x.a, "GET", segPath(ws, "/nope/usage"), nil)
}

func TestFlagRulesCannotReferenceUnknownSegmentsOrUseBadOperators(t *testing.T) {
	x := newTenants(t)
	h := x.h
	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", boolFlag("f"))
	patch := func(want int, rules ...map[string]any) []byte {
		return h.expect(want, x.a, "PATCH", "/environments/"+x.devA+"/flags/f",
			map[string]any{"targetingRules": []map[string]any{{"clauses": rules, "variationId": "on"}}})
	}
	b := patch(400, clause("plan", "equals", "x"), inSeg("ghost"))
	if !strings.Contains(errMessage(b), `targetingRules[0]: clause 1: segment "ghost" does not exist in this workspace`) {
		t.Errorf("unknown segment: %s", b)
	}
	patch(400, notInSeg("ghost"))
	patch(400, map[string]any{"attribute": "plan", "operator": "inSegment", "value": "ghost"}) // attribute on a segment clause
	patch(400, map[string]any{"operator": "inSegment"})
	patch(400, clause("v", "matchesRegex", "(a"))
	patch(400, clause("v", "semverEquals", "v1.2.3"))
	patch(400, clause("v", "dateAfter", "yesterday"))
	patch(400, map[string]any{"attribute": "v", "operator": "exists", "negate": true})
	patch(200, map[string]any{"attribute": "v", "operator": "startsWith", "value": "ab", "negate": true},
		clause("n", "lessThanOrEqual", 5), clause("v", "matchesRegex", `^a+$`), clause("ver", "semverGreaterThanOrEqual", "1.2.3-rc.1"),
		clause("when", "dateBefore", "2030-01-01T00:00:00Z"))
	// A rejected save left the stored rules alone.
	got := decode[struct {
		Config struct {
			TargetingRules []struct{ Clauses []map[string]any }
		}
	}](t, h.expect(200, x.a, "GET", "/environments/"+x.devA+"/flags/f", nil))
	if len(got.Config.TargetingRules) != 1 || len(got.Config.TargetingRules[0].Clauses) != 5 {
		t.Errorf("stored rules: %+v", got.Config.TargetingRules)
	}
}

func TestNewOperatorsEvaluateThroughTheAPI(t *testing.T) {
	x := newTenants(t)
	x.ruleFlag("ops",
		map[string]any{"attribute": "email", "operator": "endsWith", "value": "@helios.dev"},
		map[string]any{"attribute": "app", "operator": "semverGreaterThanOrEqual", "value": "2.0.0"},
		map[string]any{"attribute": "plan", "operator": "equals", "value": "free", "negate": true},
		clause("seen", "dateAfter", "2024-01-01T00:00:00Z"),
		clause("code", "matchesRegex", `^[A-Z]{2}-\d+$`),
		clause("age", "lessThan", 65))
	k := x.sdkKeyA
	ok := map[string]any{"email": "a@helios.dev", "app": "2.1.0", "plan": "pro", "seen": "2024-06-01T00:00:00+02:00", "code": "AB-12", "age": 30}
	x.wantServed(k, "u", ok, "ops", "on")
	for attr, bad := range map[string]any{"email": "a@helios.com", "app": "1.9.9", "plan": "free", "seen": "2023-06-01T00:00:00Z", "code": "ab-12", "age": 70} {
		attrs := map[string]any{}
		for kk, v := range ok {
			attrs[kk] = v
		}
		attrs[attr] = bad
		x.wantServed(k, "u", attrs, "ops", "off")
	}
	// Wrong types and garbage never error: every one is just "no match".
	for _, attrs := range []map[string]any{
		{"email": 5, "app": 2, "plan": []any{}, "seen": 1, "code": true, "age": "30"},
		{"email": nil, "app": "not.a.version", "plan": map[string]any{}, "seen": "2024-13-45", "code": strings.Repeat("A", 100000), "age": nil},
	} {
		if got := x.serve(k, "u", attrs, "ops"); got.Variation != "off" || got.Reason != "FALLTHROUGH" {
			t.Errorf("garbage attributes %v: %+v", attrs, got)
		}
	}
}

// ---- delete, usage, stale references ------------------------------------------------

func TestDeleteSegment(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := x.wsA.ID
	h.expect(201, x.a, "POST", segPath(ws), segReq("beta", []string{"vip"}))
	h.expect(201, x.a, "POST", segPath(ws), segReq("unused", []string{"a", "b"}))
	x.ruleFlag("uses-in", inSeg("beta"))
	x.ruleFlag("uses-not-in", notInSeg("beta"))
	k := x.sdkKeyA
	stream := h.openStream(k)
	before := flagVersion(t, x, x.devA, "uses-in")

	if u := decode[struct{ Flags []any }](t, h.expect(200, x.a, "GET", segPath(ws, "/unused/usage"), nil)); len(u.Flags) != 0 {
		t.Errorf("unused segment's usage = %+v", u)
	}

	// Unused: deleted at once, members gone with it, audited as an ordinary delete.
	h.expect(204, x.a, "DELETE", segPath(ws, "/unused"), nil)
	h.expect(404, x.a, "GET", segPath(ws, "/unused"), nil)
	if n := h.count(`SELECT count(*) FROM segment_members WHERE workspace_id = $1::uuid`, ws); n != 1 {
		t.Errorf("%d members rows left, want only beta's 1", n)
	}
	if h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'segment.delete' AND resource_id = 'unused' AND severity = 'info'`, ws) != 1 {
		t.Error("segment.delete not audited")
	}

	// The usage endpoint lists the flags (and environments) whose rules name the segment.
	usage := decode[struct {
		Flags []struct {
			FlagKey      string
			Environments []struct {
				Key     string
				Enabled bool
			}
		}
	}](t, h.expect(200, x.a, "GET", segPath(ws, "/beta/usage"), nil))
	if len(usage.Flags) != 2 || usage.Flags[0].FlagKey != "uses-in" || usage.Flags[1].FlagKey != "uses-not-in" ||
		len(usage.Flags[0].Environments) != 1 || usage.Flags[0].Environments[0].Key != "dev" || !usage.Flags[0].Environments[0].Enabled {
		t.Fatalf("usage = %+v", usage)
	}

	// In use: refused, and the message names the flags; nothing changed.
	b := h.expect(409, x.a, "DELETE", segPath(ws, "/beta"), nil)
	if errCode(t, b) != "SEGMENT_IN_USE" || !strings.Contains(errMessage(b), "uses-in (dev)") || !strings.Contains(errMessage(b), "uses-not-in (dev)") {
		t.Fatalf("delete in use: %s", b)
	}
	x.wantServed(k, "vip", nil, "uses-in", "on")
	if msg := stream.next(300 * time.Millisecond); msg != "" {
		t.Errorf("a refused delete published %q", msg)
	}

	// Confirmed: gone; critical audit; flag versions bumped; the stream is told.
	h.expect(204, x.a, "DELETE", segPath(ws, "/beta?confirm=true"), nil)
	if msg := stream.next(2 * time.Second); !strings.Contains(msg, `"action":"segment_deleted"`) || !strings.Contains(msg, "uses-in") {
		t.Errorf("stream event = %q", msg)
	}
	if h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'segment.force_delete' AND resource_id = 'beta' AND severity = 'critical'`, ws) != 1 {
		t.Error("segment.force_delete not audited as critical")
	}
	if v := flagVersion(t, x, x.devA, "uses-in"); v != before+1 {
		t.Errorf("flag version %d, want %d", v, before+1)
	}

	// The stored rules still name "beta" (stale), and now match nobody: inSegment
	// is false for the old member, and notInSegment is NOT true for strangers.
	for _, flag := range []string{"uses-in", "uses-not-in"} {
		for _, subject := range []string{"vip", "stranger"} {
			if got := x.serve(k, subject, nil, flag); got.Variation != "off" || got.Reason != "FALLTHROUGH" {
				t.Errorf("%s for %s after the segment was deleted: %+v", flag, subject, got)
			}
		}
	}
	// A new rule cannot reference it.
	h.expect(400, x.a, "PATCH", "/environments/"+x.devA+"/flags/uses-in", map[string]any{
		"targetingRules": []map[string]any{{"clauses": []map[string]any{inSeg("beta")}, "variationId": "on"}}})
	// Re-creating a segment with the same key makes the stale rule live again.
	h.expect(201, x.a, "POST", segPath(ws), segReq("beta", []string{"stranger"}))
	x.wantServed(k, "stranger", nil, "uses-in", "on")
}

// ---- tenants, roles, quotas -----------------------------------------------------------

func TestSegmentsAreTenantScoped(t *testing.T) {
	x := newTenants(t)
	h := x.h
	wsA, wsB := x.wsA.ID, x.wsB.ID
	h.expect(201, x.a, "POST", segPath(wsA), segReq("beta", []string{"u1", "acme-only-member"}, clause("plan", "equals", "acme-secret-plan")))
	x.ruleFlag("a-gated", inSeg("beta"))

	// B cannot reach A's segments through A's workspace id...
	for _, c := range []struct{ method, path string }{
		{"GET", segPath(wsA)}, {"GET", segPath(wsA, "/beta")}, {"GET", segPath(wsA, "/beta/usage")},
		{"POST", segPath(wsA)}, {"PATCH", segPath(wsA, "/beta")}, {"DELETE", segPath(wsA, "/beta?confirm=true")},
	} {
		st, body := h.call(x.b, c.method, c.path, map[string]any{"key": "z", "name": "z", "members": []string{"x"}})
		if st != 404 || errCode(t, body) != "WORKSPACE_NOT_FOUND" || bytes.Contains(body, []byte("acme")) {
			t.Errorf("B %s %s: %d %s", c.method, c.path, st, body)
		}
	}
	// ...nor through their own workspace.
	h.expect(404, x.b, "GET", segPath(wsB, "/beta"), nil)
	h.expect(404, x.b, "PATCH", segPath(wsB, "/beta"), map[string]any{"name": "mine now"})
	h.expect(404, x.b, "DELETE", segPath(wsB, "/beta"), nil)
	if list := decode[struct{ Segments []any }](t, h.expect(200, x.b, "GET", segPath(wsB), nil)); len(list.Segments) != 0 {
		t.Errorf("B lists %d segments", len(list.Segments))
	}
	// B cannot reference A's segment in a flag rule.
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/flags", boolFlag("b-flag"))
	b := h.expect(400, x.b, "PATCH", "/environments/"+x.devB+"/flags/b-flag", map[string]any{
		"targetingRules": []map[string]any{{"clauses": []map[string]any{inSeg("beta")}, "variationId": "on"}}})
	if !strings.Contains(errMessage(b), "does not exist in this workspace") || bytes.Contains(b, []byte("acme")) {
		t.Errorf("B referencing A's segment: %s", b)
	}
	// The same key in B's workspace is B's own segment, with B's own members: A's membership never leaks into it.
	h.expect(201, x.b, "POST", segPath(wsB), segReq("beta", []string{"only-bobs"}))
	x.ruleFlagAs(x.b, x.devB, "b-gated", inSeg("beta"))
	keyB := h.newSDKKey(x.b, x.devB)
	x.wantServed(keyB, "u1", map[string]any{"plan": "acme-secret-plan"}, "b-gated", "off") // A's member and A's rule
	x.wantServed(keyB, "only-bobs", nil, "b-gated", "on")
	x.wantServed(x.sdkKeyA, "only-bobs", nil, "a-gated", "off")
	x.wantServed(x.sdkKeyA, "u1", nil, "a-gated", "on")
	// B's SDK key asking for A's flag finds nothing; B's segment edits do not touch A's flag version.
	if got := x.serve(keyB, "u1", nil, "a-gated"); got.Reason != "FLAG_NOT_FOUND" {
		t.Errorf("B's key evaluating A's flag: %+v", got)
	}
	v := flagVersion(t, x, x.devA, "a-gated")
	h.expect(200, x.b, "PATCH", segPath(wsB, "/beta"), map[string]any{"members": []string{"someone"}})
	if flagVersion(t, x, x.devA, "a-gated") != v {
		t.Error("B's segment edit bumped A's flag")
	}
	// B's audit log shows nothing of A's segment.
	_, logs := h.call(x.b, "GET", "/environments/"+x.devB+"/audit-logs", nil)
	if bytes.Contains(logs, []byte("acme")) || bytes.Contains(logs, []byte(wsA)) {
		t.Errorf("B's audit log leaks A: %s", logs)
	}
	// Deleting a workspace's data never reaches the other: A's segment is intact.
	if got := x.getSeg(x.a, wsA, "beta"); got.MemberCount != 2 {
		t.Errorf("A's segment changed: %+v", got)
	}
	// Database level: a member cannot point at another workspace's segment.
	var segA string
	if err := h.pool.QueryRow(context.Background(), `SELECT id::text FROM segments WHERE workspace_id = $1::uuid AND key = 'beta'`, wsA).Scan(&segA); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(context.Background(), `INSERT INTO segment_members (workspace_id, segment_id, subject_key) VALUES ($1::uuid, $2::uuid, 'sneaky')`, wsB, segA); err == nil {
		t.Error("the database accepted a member in workspace B for a segment of workspace A")
	}
}

func TestSegmentRoles(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := x.wsA.ID
	h.expect(201, x.a, "POST", segPath(ws), segReq("beta", []string{"u1"}))
	join := func(name, role string) user {
		u := h.newUser(name)
		h.me(u)
		tok := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", "/workspaces/"+ws+"/invites", map[string]any{"email": u.email, "role": role})).Token
		h.expect(200, u, "POST", "/invites/accept", map[string]any{"token": tok})
		return u
	}
	viewer, editor := join("vera", "viewer"), join("eddie", "editor")

	h.expect(200, viewer, "GET", segPath(ws), nil)
	h.expect(200, viewer, "GET", segPath(ws, "/beta"), nil)
	h.expect(200, viewer, "GET", segPath(ws, "/beta/usage"), nil)
	h.expect(403, viewer, "POST", segPath(ws), segReq("v", nil))
	h.expect(403, viewer, "PATCH", segPath(ws, "/beta"), map[string]any{"name": "v"})
	h.expect(403, viewer, "DELETE", segPath(ws, "/beta"), nil)

	h.expect(201, editor, "POST", segPath(ws), segReq("by-editor", []string{"e"}))
	h.expect(200, editor, "PATCH", segPath(ws, "/by-editor"), map[string]any{"name": "edited"})
	h.expect(204, editor, "DELETE", segPath(ws, "/by-editor"), nil)
	if h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND resource_type = 'segment' AND actor_email = $2`, ws, editor.email) != 3 {
		t.Error("the editor's three changes were not all audited under their name")
	}
}

func TestSegmentCountQuota(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := x.wsA.ID
	for i := 0; i < quota.MaxSegments; i++ {
		h.expect(201, x.a, "POST", segPath(ws), segReq(fmt.Sprintf("s%02d", i), nil))
	}
	b := h.expect(409, x.a, "POST", segPath(ws), segReq("one-too-many", nil))
	if errCode(t, b) != "QUOTA_EXCEEDED" || !strings.Contains(errMessage(b), fmt.Sprintf("at most %d segments", quota.MaxSegments)) {
		t.Errorf("segment quota: %s", b)
	}
	// Another workspace is not affected, and deleting one frees a slot.
	h.expect(201, x.b, "POST", segPath(x.wsB.ID), segReq("s00", nil))
	h.expect(204, x.a, "DELETE", segPath(ws, "/s00"), nil)
	h.expect(201, x.a, "POST", segPath(ws), segReq("one-too-many", nil))
}

// Parallel creates cannot overshoot the quota (the workspace row lock serialises them).
func TestSegmentCountQuotaHoldsUnderConcurrency(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := x.wsA.ID
	for i := 0; i < quota.MaxSegments-2; i++ {
		h.expect(201, x.a, "POST", segPath(ws), segReq(fmt.Sprintf("s%02d", i), nil))
	}
	var wg sync.WaitGroup
	statuses := make([]int, 8)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], _ = h.call(x.a, "POST", segPath(ws), segReq(fmt.Sprintf("race%d", i), nil))
		}()
	}
	wg.Wait()
	created := 0
	for _, s := range statuses {
		switch s {
		case 201:
			created++
		case 409:
		default:
			t.Errorf("status %d", s)
		}
	}
	if created != 2 || h.count(`SELECT count(*) FROM segments WHERE workspace_id = $1::uuid`, ws) != quota.MaxSegments {
		t.Errorf("created %d, total %d; want exactly 2 and %d", created, h.count(`SELECT count(*) FROM segments WHERE workspace_id = $1::uuid`, ws), quota.MaxSegments)
	}
}

// Segment edits, flag saves that reference the segment, and deletes, all at
// once and with the database slowed down so the windows are wide: nothing
// deadlocks (a deadlock would surface as a 500) and nothing is left dangling.
func TestSegmentFlagAndDeleteRaceWithoutDeadlock(t *testing.T) {
	x := newTenants(t)
	h := x.h
	ws := x.wsA.ID
	h.expect(201, x.a, "POST", segPath(ws), segReq("racy", []string{"a"}))
	h.expect(201, x.a, "POST", segPath(ws), segReq("racy2", []string{"a"}))
	for i := 0; i < 3; i++ {
		x.ruleFlag(fmt.Sprintf("f%d", i), inSeg("racy"))
	}
	h.slowDown("flag_configs", "UPDATE", 15*time.Millisecond)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var bad []string
	note := func(label string, st int, body []byte, ok ...int) {
		for _, o := range ok {
			if st == o {
				return
			}
		}
		mu.Lock()
		bad = append(bad, fmt.Sprintf("%s: %d %s", label, st, body))
		mu.Unlock()
	}
	for i := 0; i < 6; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			st, b := h.call(x.a, "PATCH", segPath(ws, "/racy"), map[string]any{"members": []string{"a", fmt.Sprintf("m%d", i)}})
			note("segment edit", st, b, 200, 404)
		}()
		go func() {
			defer wg.Done()
			rules := []map[string]any{{"clauses": []map[string]any{inSeg("racy"), inSeg("racy2")}, "variationId": "on"}}
			st, b := h.call(x.a, "PATCH", "/environments/"+x.devA+"/flags/"+fmt.Sprintf("f%d", i%3), map[string]any{"targetingRules": rules})
			note("flag save", st, b, 200, 400)
		}()
		go func() {
			defer wg.Done()
			if i == 3 {
				st, b := h.call(x.a, "DELETE", segPath(ws, "/racy2?confirm=true"), nil)
				note("delete", st, b, 204, 404)
			}
		}()
	}
	wg.Wait()
	if len(bad) > 0 {
		t.Fatalf("unexpected results:\n%s", strings.Join(bad, "\n"))
	}
}

// ---- real Redis -------------------------------------------------------------------

func TestRealRedisCarriesSegmentEvents(t *testing.T) {
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
	h := newHarnessWith(t, func(d *server.Deps) {
		d.Publisher, d.Subscriber = redisx.Publisher{Client: client}, redisx.Subscriber{Client: client}
		slowFallbacks(d)
	})
	x := &tenants{h: h, a: h.newUser("alice"), b: h.newUser("bob")}
	x.wsA, x.wsB = h.me(x.a).Workspaces[0], h.me(x.b).Workspaces[0]
	x.devA, x.devB = envID(t, x.wsA, "dev"), envID(t, x.wsB, "dev")
	x.sdkKeyA = h.newSDKKey(x.a, x.devA)
	keyB := h.newSDKKey(x.b, x.devB)
	h.expect(201, x.a, "POST", segPath(x.wsA.ID), segReq("beta", nil))
	x.ruleFlag("gated", inSeg("beta"))
	sA, sB := h.openStream(x.sdkKeyA), h.openStream(keyB)

	start := time.Now()
	h.expect(200, x.a, "PATCH", segPath(x.wsA.ID, "/beta"), map[string]any{"members": []string{"newcomer"}})
	msg := sA.next(3 * time.Second)
	if !strings.Contains(msg, `"action":"segment_updated"`) || !strings.Contains(msg, `"gated"`) {
		t.Fatalf("A's stream through real Redis: %q", msg)
	}
	t.Logf("segment event crossed a real Redis in %v", time.Since(start))
	x.wantServed(x.sdkKeyA, "newcomer", nil, "gated", "on")
	if other := sB.next(400 * time.Millisecond); other != "" {
		t.Errorf("B's stream heard A's segment change: %q", other)
	}
}
