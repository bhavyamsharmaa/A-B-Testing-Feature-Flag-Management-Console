package integration

import (
	"context"
	"testing"
	"time"

	"helios/backend/internal/controlplane/scheduling"
)

// A change scheduled through an API token remembers the token, and does not
// outlive it: revoking cancels what is pending (with the reason recorded and
// audited), expiry is swept, and the executor refuses a change whose token is
// gone even if both of those were missed.

func (w *tokWorld) tokenSchedule(t *testing.T, token, key string, in time.Duration) string {
	t.Helper()
	b := w.tokExpect(t, 201, token, "POST", "/environments/"+w.devA+"/flags/sched/scheduled-changes",
		map[string]any{"idempotencyKey": key, "kind": "enable", "runAt": w.clock.now().Add(in).Format(time.RFC3339)})
	return decode[struct{ ID string }](t, b).ID
}

func (w *tokWorld) schedState(id string) (status, code string) {
	var c *string
	_ = w.h.pool.QueryRow(context.Background(), `SELECT status, result_code FROM scheduled_changes WHERE id = $1::uuid`, id).Scan(&status, &c)
	if c != nil {
		code = *c
	}
	return
}

func (w *tokWorld) schedRunner() *scheduling.Runner {
	return scheduling.NewRunner(w.h.pool, w.h.bus, w.clock.now, scheduling.Config{WorkerID: "tok-worker"})
}

func (w *tokWorld) sessionSched(t *testing.T, key string, in time.Duration) string {
	t.Helper()
	b := w.h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags/sched/scheduled-changes",
		map[string]any{"idempotencyKey": key, "kind": "enable", "runAt": w.clock.now().Add(in).Format(time.RFC3339)})
	return decode[struct{ ID string }](t, b).ID
}

func (w *tokWorld) flagEnabled(t *testing.T) bool {
	t.Helper()
	return decode[struct{ Config struct{ Enabled bool } }](t, w.h.expect(200, w.a, "GET", "/environments/"+w.devA+"/flags/sched", nil)).Config.Enabled
}

func schedTokWorld(t *testing.T) *tokWorld {
	w := newTokWorld(t, nil)
	w.h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags", boolFlag("sched"))
	return w
}

func TestRevokingATokenCancelsItsPendingScheduledChanges(t *testing.T) {
	w := schedTokWorld(t)
	tok := w.mint(t, w.a, w.wsA.ID, "ci-sched", "editor")
	viaToken := w.tokenSchedule(t, tok.Plaintext, "t1", time.Hour)
	viaSession := w.sessionSched(t, "s1", 2*time.Hour)
	if n := w.dbCount(`SELECT count(*) FROM scheduled_changes WHERE id = $1::uuid AND created_by_token_id = $2::uuid`, viaToken, tok.ID); n != 1 {
		t.Fatalf("the change does not record the token that created it")
	}
	if n := w.dbCount(`SELECT count(*) FROM scheduled_changes WHERE id = $1::uuid AND created_by_token_id IS NULL`, viaSession); n != 1 {
		t.Fatalf("a session-created change was attributed to a token")
	}

	w.h.expect(204, w.a, "DELETE", "/workspaces/"+w.wsA.ID+"/api-tokens/"+tok.ID, nil)

	if st, code := w.schedState(viaToken); st != "cancelled" || code != scheduling.CodeTokenInvalid {
		t.Errorf("token's change: %s %s", st, code)
	}
	if st, _ := w.schedState(viaSession); st != "pending" {
		t.Errorf("another person's change was cancelled: %s", st)
	}
	var reason string
	if err := w.h.pool.QueryRow(context.Background(), `SELECT result_message FROM scheduled_changes WHERE id = $1::uuid`, viaToken).Scan(&reason); err != nil || reason == "" {
		t.Errorf("no reason recorded: %q %v", reason, err)
	}
	if n := w.dbCount(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'scheduled_change.cancel' AND resource_id = $2`, w.wsA.ID, viaToken); n != 1 {
		t.Errorf("the cancellation is not audited (%d)", n)
	}
	w.clock.advance(2*time.Hour + time.Minute)
	if _, err := w.schedRunner().Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.flagEnabled(t) != true {
		t.Errorf("the session's change should still have run")
	}
}

func TestTheExecutorRefusesAChangeWhoseTokenIsGoneEvenIfTheCancelWasMissed(t *testing.T) {
	w := schedTokWorld(t)
	tok := w.mint(t, w.a, w.wsA.ID, "ci-missed", "editor")
	id := w.tokenSchedule(t, tok.Plaintext, "m1", 10*time.Minute)
	// revoke behind the API's back: no cancellation happens
	if _, err := w.h.pool.Exec(context.Background(), `UPDATE api_tokens SET revoked_at = now() WHERE id = $1::uuid`, tok.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := w.schedState(id); st != "pending" {
		t.Fatalf("setup: %s", st)
	}
	w.clock.advance(11 * time.Minute)
	r := w.schedRunner()
	claimed, err := r.Claim(context.Background(), 5) // skip the sweep: this is the executor's own check
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	var st scheduling.Stats
	if err := r.Execute(context.Background(), claimed[0], &st); err != nil || st.Cancelled != 1 || st.Succeeded != 0 {
		t.Fatalf("execute: %+v %v", st, err)
	}
	if s, code := w.schedState(id); s != "cancelled" || code != scheduling.CodeTokenInvalid {
		t.Errorf("state %s %s", s, code)
	}
	if w.flagEnabled(t) {
		t.Errorf("a revoked token's change was applied")
	}
}

func TestAnExpiredTokensPendingChangesAreCancelledBeforeTheyAreDue(t *testing.T) {
	w := schedTokWorld(t)
	tok := w.mint(t, w.a, w.wsA.ID, "ci-expiring", "editor")
	id := w.tokenSchedule(t, tok.Plaintext, "e1", 48*time.Hour)
	// the token expires in 1 hour (database clock); the change is due in 2 days
	if _, err := w.h.pool.Exec(context.Background(), `UPDATE api_tokens SET expires_at = created_at + interval '1 hour' WHERE id = $1::uuid`, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.schedRunner().Run(context.Background()); err != nil { // before expiry
		t.Fatal(err)
	}
	if st, _ := w.schedState(id); st != "pending" {
		t.Fatalf("cancelled while the token was valid: %s", st)
	}
	w.clock.advance(2 * time.Hour)
	st, err := w.schedRunner().Run(context.Background())
	if err != nil || st.Cancelled != 1 {
		t.Fatalf("run: %+v %v", st, err)
	}
	if s, code := w.schedState(id); s != "cancelled" || code != scheduling.CodeTokenInvalid {
		t.Errorf("state %s %s", s, code)
	}
	if n := w.dbCount(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'scheduled_change.cancel' AND resource_id = $2`, w.wsA.ID, id); n != 1 {
		t.Errorf("not audited (%d)", n)
	}
}
