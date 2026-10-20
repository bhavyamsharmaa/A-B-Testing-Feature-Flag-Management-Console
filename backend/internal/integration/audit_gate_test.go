package integration

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/controlplane/scheduling"
	"helios/backend/internal/controlplane/wspurge"
	"helios/backend/internal/server"
)

// The audit log's delete exception has two gates (migration 0015): the workspace must be past its purge date AND the
// deleting transaction must have declared that one workspace and its own id. These tests try every way round them.

func (w *delWorld) auditIDs(t *testing.T) []string {
	t.Helper()
	rows, err := w.h.pool.Query(context.Background(), `SELECT id::text FROM audit_logs WHERE workspace_id = $1::uuid`, w.wsA.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestTheAuditGateIsNotOpenedByBeingDueByAWrongDeclarationOrByALeakedSetting(t *testing.T) {
	w := newDelWorld(t)
	h := w.h
	ws, other := w.wsA.ID, w.wsB.ID
	ctx := context.Background()
	w.schedule(t, 200)
	w.graceOver(t) // A is due; B is a live workspace
	before := len(w.auditIDs(t))
	if before == 0 {
		t.Fatal("setup: no audit rows")
	}
	try := func(label, declare string, declareArgs []any, del string, delArgs ...any) {
		t.Helper()
		tx, err := h.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if declare != "" {
			if _, err := tx.Exec(ctx, declare, declareArgs...); err != nil {
				t.Fatalf("%s: declare: %v", label, err)
			}
		}
		if _, err := tx.Exec(ctx, del, delArgs...); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: %q was not refused: %v", label, del, err)
		}
	}
	del := `DELETE FROM audit_logs WHERE workspace_id = $1::uuid`
	try("no declaration, workspace due", "", nil, del, ws)
	try("declared for a different workspace", `SELECT set_config('helios.audit_purge', $1::text || ':' || txid_current()::text, true)`, []any{other}, del, ws)
	try("declared for the live workspace, deleting its rows", `SELECT set_config('helios.audit_purge', $1::text || ':' || txid_current()::text, true)`, []any{other}, del, other)
	try("declared with a made-up transaction id", `SELECT set_config('helios.audit_purge', $1::text || ':1', true)`, []any{ws}, del, ws)
	try("declared with just the workspace id", `SELECT set_config('helios.audit_purge', $1::text, true)`, []any{ws}, del, ws)
	try("declared correctly, but updating", `SELECT set_config('helios.audit_purge', $1::text || ':' || txid_current()::text, true)`, []any{ws}, `UPDATE audit_logs SET severity = 'info' WHERE workspace_id = $1::uuid`, ws)
	try("declared correctly, but truncating", `SELECT set_config('helios.audit_purge', $1::text || ':' || txid_current()::text, true)`, []any{ws}, `TRUNCATE audit_logs`)

	// a setting left on a pooled connection (session level) never matches a later transaction
	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('helios.audit_purge', $1::text || ':' || txid_current()::text, false)`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, del, ws); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("a leaked session-level declaration opened the gate: %v", err)
	}
	conn.Release()

	// a workspace that is scheduled but not yet due, correctly declared: still refused
	if err := h.rawExec(`UPDATE workspaces SET purge_after = now() + interval '1 day' WHERE id = $1::uuid`, ws); err != nil {
		t.Fatal(err)
	}
	try("declared correctly, workspace not yet due", `SELECT set_config('helios.audit_purge', $1::text || ':' || txid_current()::text, true)`, []any{ws}, del, ws)
	if err := h.rawExec(`UPDATE workspaces SET purge_after = now() - interval '1 minute' WHERE id = $1::uuid`, ws); err != nil {
		t.Fatal(err)
	}

	if got := len(w.auditIDs(t)); got != before {
		t.Fatalf("audit rows went from %d to %d", before, got)
	}

	// not vacuous: the one right way works, and only for that workspace
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, wspurge.AuditGateSQL, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, del, ws); err != nil {
		t.Fatalf("the declared, due delete was refused: %v", err)
	}
	if _, err := tx.Exec(ctx, del, other); err == nil {
		t.Error("the same transaction could also delete another workspace's audit rows")
	}
	_ = tx.Rollback(ctx)
	if got := len(w.auditIDs(t)); got != before {
		t.Fatalf("rollback left %d rows, want %d", got, before)
	}
}

// Nothing a caller can do over HTTP, with a token, an SDK key, the scheduler or the outbox, removes audit rows of a
// workspace whose purge is due. (Every route is walked from the router's own registry.)
func TestNoRequestPathDeletesAuditRowsEvenForADuePurge(t *testing.T) {
	w := newDelWorld(t)
	h := w.h
	ws := w.wsA.ID
	tok := decodeTok(t, w)
	member := w.join(t, "mem", "viewer")
	w.schedule(t, 200)
	w.graceOver(t)
	before := w.auditIDs(t)
	fill := strings.NewReplacer("{envId}", w.devA, "{wsId}", ws, "{key}", "k", "{userId}", w.a.id, "{inviteId}", "11111111-1111-4111-8111-111111111111",
		"{keyId}", "11111111-1111-4111-8111-111111111111", "{segKey}", "s", "{changeId}", "11111111-1111-4111-8111-111111111111",
		"{tokenId}", "11111111-1111-4111-8111-111111111111", "{endpointId}", "11111111-1111-4111-8111-111111111111",
		"{deliveryId}", "11111111-1111-4111-8111-111111111111", "{offerId}", "11111111-1111-4111-8111-111111111111")
	n := 0
	for _, rt := range h.routes {
		if rt.Scope == server.ScopeInternal || strings.Contains(rt.Path, "stream") {
			continue // the secret-guarded jobs are the legitimate way, and streams never return
		}
		path := fill.Replace(rt.Path)
		for _, u := range []user{w.a, member, w.b} {
			h.call(u, rt.Method, path, map[string]any{})
		}
		if rt.TokenAllowed {
			h.tokenCall(tok, rt.Method, path, map[string]any{})
		}
		n++
	}
	if n < 40 {
		t.Fatalf("only %d routes walked", n)
	}
	// SDK key, scheduler, outbox (a due workspace is dead to all three, but the point is: they delete nothing)
	h.evaluateStatus(w.sdkKeyA, secretFlag)
	w.clock.advance(time.Hour)
	_, _ = scheduling.NewRunner(h.pool, h.bus, w.clock.now, scheduling.Config{WorkerID: "gate"}).Run(context.Background())
	_, _ = h.deliveryWorker(w.clock.now, outbox.Config{}).Run(context.Background())

	after := map[string]bool{}
	for _, id := range w.auditIDs(t) {
		after[id] = true
	}
	for _, id := range before {
		if !after[id] {
			t.Fatalf("audit row %s was deleted by a request path", id)
		}
	}
	t.Logf("walked %d routes; %d audit rows intact", n, len(before))
}

func decodeTok(t *testing.T, w *delWorld) string {
	t.Helper()
	return decode[struct{ Plaintext string }](t, w.h.expect(201, w.a, "POST", "/workspaces/"+w.wsA.ID+"/api-tokens", map[string]any{"name": "ci", "role": "admin"})).Plaintext
}

// The gate can be opened from exactly one place in the code base, and nothing else deletes from the audit log.
func TestOnlyThePurgeOpensTheAuditGate(t *testing.T) {
	root := filepath.Join("..", "..")
	deleteRe := regexp.MustCompile(`(?i)(delete\s+from\s+audit_logs|truncate\s+(table\s+)?audit_logs|drop\s+(table|trigger)\s+(\w+\s+)?audit)`)
	var gateUsers []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		if strings.Contains(src, "helios.audit_purge") || strings.Contains(src, "AuditGateSQL") {
			gateUsers = append(gateUsers, filepath.ToSlash(path))
		}
		if deleteRe.MatchString(src) {
			t.Errorf("%s deletes from the audit log", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(gateUsers) != 1 || !strings.HasSuffix(gateUsers[0], "internal/controlplane/wspurge/wspurge.go") {
		t.Errorf("the audit gate is referenced from %v, want only wspurge.go", gateUsers)
	}
}
