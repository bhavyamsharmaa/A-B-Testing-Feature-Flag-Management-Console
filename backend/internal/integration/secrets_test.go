package integration

import (
	"bytes"
	"strings"
	"testing"
)

// No credential (SDK key, invite token) may appear in the server's logs or in
// any response other than the single one that is meant to show it, even on
// error paths, including real internal errors where the driver speaks.
func TestSecretsNeverReachLogsOrErrorResponses(t *testing.T) {
	logs := captureLogs(t)
	x := newTenants(t)
	h := x.h
	ws := "/workspaces/" + x.wsA.ID

	inv := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", ws+"/invites", map[string]any{"email": x.b.email, "role": "viewer"}))
	revealing := len(h.bodies) // the invite response (token) and key creation (plaintext) are the intended ones
	secrets := []string{inv.Token, x.sdkKeyA}

	// Failing flows that carry the secrets: wrong person accepting, bad and truncated tokens and keys, malformed bodies.
	carol := h.newUser("carol")
	h.me(carol)
	h.expect(403, carol, "POST", "/invites/accept", map[string]any{"token": inv.Token})
	h.expect(404, carol, "POST", "/invites/accept", map[string]any{"token": inv.Token[:len(inv.Token)-4]})
	h.expect(400, carol, "POST", "/invites/accept", map[string]any{"token": inv.Token, "inviteId": "x"})
	h.expect(400, x.a, "POST", ws+"/invites", map[string]any{"email": inv.Token, "role": "viewer"})
	h.expect(400, carol, "POST", "/invites/accept", map[string]any{"tokenn": inv.Token})
	h.evaluateStatus(x.sdkKeyA[:len(x.sdkKeyA)-3], secretFlag)
	h.evaluateStatus(strings.ToUpper(x.sdkKeyA), secretFlag)
	h.expect(404, x.b, "DELETE", "/environments/"+x.devA+"/sdk-keys/"+x.sdkKeyA, nil)

	// A real internal error with the secrets in flight: break the schema under the server.
	if _, err := h.pool.Exec(t.Context(), `ALTER TABLE flag_configs RENAME COLUMN salt TO salt_renamed`); err != nil {
		t.Fatal(err)
	}
	if st := h.evaluateStatus(x.sdkKeyA, secretFlag); st != 503 {
		t.Fatalf("evaluate with a broken schema: status %d", st)
	}
	if _, err := h.pool.Exec(t.Context(), `ALTER TABLE flag_configs RENAME COLUMN version TO version_renamed`); err != nil {
		t.Fatal(err)
	}
	st, body := h.call(x.a, "GET", "/environments/"+x.devA+"/flags", nil)
	if st != 500 || !bytes.Contains(body, []byte("internal error")) || bytes.Contains(body, []byte("version")) {
		t.Errorf("an internal error leaks detail to the client: %d %s", st, body)
	}

	// The logs saw the failures (so this proves something) but none of the secrets.
	out := logs.String()
	if !strings.Contains(out, "evaluate: load configs") || !strings.Contains(out, "GET /environments/") {
		t.Fatalf("the error paths did not log; the test is not exercising them:\n%s", out)
	}
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("a secret reached the logs:\n%s", out)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, b := range h.bodies {
		if i < revealing && i != 0 {
			continue
		}
		for _, s := range secrets {
			if i >= revealing && bytes.Contains(b, []byte(s)) {
				t.Errorf("response #%d echoes a secret: %s", i, b)
			}
		}
	}
}
