package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/server"
)

var placeholderRE = regexp.MustCompile(`\{([A-Za-z]+)\}`)

// Every route registered on the real router is exercised here, taken from the
// router's own registry rather than a hand-written list. A route added later
// is picked up automatically, and fails this test unless it is scoped:
//   - a path with {envId} must be registered as an environment route, and a
//     path with {wsId} as a workspace route (both answer 404 to non-members);
//   - any other path parameter is unknown, so the test fails until someone
//     decides how that id is tenant-checked and teaches this test;
//   - routes without ids must be declared public, self-only or SDK-key routes.
func TestEveryRouteIsTenantScoped(t *testing.T) {
	x := newTenants(t)
	h := x.h
	if len(h.routes) < 20 {
		t.Fatalf("only %d routes registered; the registry is not being filled", len(h.routes))
	}

	// A's resources that routes take as ids.
	var keyID string
	if err := h.pool.QueryRow(context.Background(), `SELECT id::text FROM api_keys WHERE workspace_id = $1::uuid`, x.wsA.ID).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	inviteID := decode[struct{ Invite struct{ ID string } }](t, h.expect(201, x.a, "POST", "/workspaces/"+x.wsA.ID+"/invites",
		map[string]any{"email": "someone@example.com", "role": "viewer"})).Invite.ID
	viewer := h.newUser("victor")
	h.me(viewer)
	tok := decode[struct{ Token string }](t, h.expect(201, x.a, "POST", "/workspaces/"+x.wsA.ID+"/invites",
		map[string]any{"email": viewer.email, "role": "viewer"})).Token
	h.expect(200, viewer, "POST", "/invites/accept", map[string]any{"token": tok})

	forbidden := []string{secretFlag, "acme-secret-exp", x.a.email, x.a.id, x.wsA.ID, x.devA, x.wsA.Name, "alice key", keyID, inviteID, "someone@example.com", x.sdkKeyA[:12]}
	fill := func(path string) string {
		key := secretFlag
		if strings.Contains(path, "/experiments/") {
			key = "acme-secret-exp"
		}
		return strings.NewReplacer("{envId}", x.devA, "{wsId}", x.wsA.ID, "{userId}", x.a.id,
			"{inviteId}", inviteID, "{keyId}", keyID, "{key}", key).Replace(path)
	}
	assertNoLeak := func(label string, body []byte) {
		t.Helper()
		for _, f := range forbidden {
			if bytes.Contains(body, []byte(f)) {
				t.Errorf("%s leaked %q: %s", label, f, body)
			}
		}
	}
	keyB := h.newSDKKey(x.b, x.devB)

	seen := map[server.Scope]int{}
	for _, rt := range h.routes {
		label := rt.Method + " " + rt.Path
		seen[rt.Scope]++

		// 1. The registration must match what the path says.
		for _, m := range placeholderRE.FindAllStringSubmatch(rt.Path, -1) {
			switch m[1] {
			case "envId":
				if rt.Scope != server.ScopeEnv {
					t.Errorf("%s: has {envId} but is registered as %q", label, rt.Scope)
				}
			case "wsId":
				if rt.Scope != server.ScopeWorkspace {
					t.Errorf("%s: has {wsId} but is registered as %q", label, rt.Scope)
				}
			case "key", "userId", "inviteId", "keyId":
				if rt.Scope != server.ScopeEnv && rt.Scope != server.ScopeWorkspace {
					t.Errorf("%s: {%s} on a route that is not environment- or workspace-scoped", label, m[1])
				}
			default:
				t.Errorf("%s: unknown path parameter {%s}: decide how it is tenant-checked, then teach this test", label, m[1])
			}
		}
		if (rt.Scope == server.ScopeEnv && !strings.Contains(rt.Path, "{envId}")) ||
			(rt.Scope == server.ScopeWorkspace && !strings.Contains(rt.Path, "{wsId}")) {
			t.Errorf("%s: registered as %q but has no such parameter, so nothing resolves a tenant", label, rt.Scope)
		}

		switch rt.Scope {
		case server.ScopeEnv, server.ScopeWorkspace:
			path := fill(rt.Path)
			wantCode := "ENVIRONMENT_NOT_FOUND"
			if rt.Scope == server.ScopeWorkspace {
				wantCode = "WORKSPACE_NOT_FOUND"
			}

			// 2. Signed out: 401.
			if st, _ := h.call(user{}, rt.Method, path, map[string]any{}); st != 401 {
				t.Errorf("%s signed out: status %d, want 401", label, st)
			}
			// 3. Another tenant (B, in a workspace of their own): 404, nothing of A's.
			st, body := h.call(x.b, rt.Method, path, map[string]any{})
			if st != 404 || !bytes.Contains(body, []byte(wantCode)) {
				t.Errorf("%s as B: status %d, want 404 %s; body %s", label, st, wantCode, body)
			}
			assertNoLeak(label+" as B", body)
			// 4. A member below the route's role: 403, not 404 (they can see it exists).
			vst, vbody := h.call(viewer, rt.Method, path, map[string]any{})
			switch {
			case rt.MinRole != rbac.WorkspaceViewer:
				if vst != 403 {
					t.Errorf("%s as a viewer: status %d, want 403 (needs %s); body %s", label, vst, rt.MinRole, vbody)
				}
			case rt.Method == "DELETE" && strings.HasSuffix(rt.Path, "/members/{userId}"):
				// open to any member, but removing someone else is refused by the handler
				if vst != 403 {
					t.Errorf("%s as a viewer removing the owner: status %d, want 403", label, vst)
				}
			default:
				if vst == 403 || vst == 404 || vst == 401 {
					t.Errorf("%s as a viewer: status %d, but viewers are allowed here", label, vst)
				}
			}

		case server.ScopePublic:
			if st, _ := h.call(user{}, rt.Method, rt.Path, nil); st != 200 {
				t.Errorf("%s: status %d", label, st)
			}

		case server.ScopeSelf:
			// Signed out: 401. As B: only B's own data, and never A's.
			if st, _ := h.call(user{}, rt.Method, rt.Path, map[string]any{}); st != 401 {
				t.Errorf("%s signed out: status %d, want 401", label, st)
			}
			switch label {
			case "GET /me", "GET /workspaces":
				assertNoLeak(label+" as B", h.expect(200, x.b, rt.Method, rt.Path, nil))
			case "POST /workspaces":
				// creates a workspace for the caller; nothing of A's comes back
				assertNoLeak(label+" as B", h.expect(201, x.b, rt.Method, rt.Path, map[string]any{"name": "B's second"}))
			case "POST /invites/accept":
				// A's open invite, by id, from someone it isn't addressed to; and a made-up token
				for _, body := range []map[string]any{{"inviteId": inviteID}, {"token": "hinv_" + strings.Repeat("A", 43)}} {
					st, b := h.call(x.b, rt.Method, rt.Path, body)
					if st != 404 {
						t.Errorf("%s as B with %v: status %d, want 404", label, body, st)
					}
					assertNoLeak(label+" as B", b)
				}
			default:
				t.Errorf("%s: a self-scoped route this test does not know; add a case that proves it only touches the caller's data", label)
			}

		case server.ScopeSDK:
			switch label {
			case "POST /evaluate":
				// B's key asking for A's flag by name finds nothing.
				b := h.sdkPost(keyB, "/evaluate", map[string]any{"context": map[string]any{"subjectKey": "u"}, "flagKeys": []string{secretFlag}})
				if !bytes.Contains(b, []byte("FLAG_NOT_FOUND")) || !bytes.Contains(b, []byte(`"value":null`)) {
					t.Errorf("%s with B's key: %s", label, b)
				}
				// The response repeats the flag key B itself asked for; anything else of A's would be a leak.
				assertNoLeak(label+" with B's key", bytes.ReplaceAll(b, []byte(secretFlag), nil))
			case "GET /sdk/stream":
				s := h.openStream(keyB)
				for _, name := range h.bus.Subscribers() {
					if !strings.Contains(name, x.wsB.ID) || strings.Contains(name, x.wsA.ID) {
						t.Errorf("%s with B's key subscribed to %q", label, name)
					}
				}
				s.cancel()
			default:
				t.Errorf("%s: an SDK route this test does not know", label)
			}

		default:
			t.Errorf("%s: route registered with unknown scope %q", label, rt.Scope)
		}
	}
	for _, sc := range []server.Scope{server.ScopeEnv, server.ScopeWorkspace, server.ScopeSelf, server.ScopeSDK, server.ScopePublic} {
		if seen[sc] == 0 {
			t.Errorf("no route registered with scope %q; the test is not exercising it", sc)
		}
	}
	t.Logf("checked %d routes: %v", len(h.routes), seen)

	// A is untouched by all of it.
	got := decode[struct{ Config struct{ Enabled bool } }](t, h.expect(200, x.a, "GET", "/environments/"+x.devA+"/flags/"+secretFlag, nil))
	if !got.Config.Enabled {
		t.Error("A's flag was changed by a request that was supposed to be refused")
	}
}

func (h *harness) sdkPost(sdkKey, path string, body any) []byte {
	h.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", h.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Helios-SDK-Key", sdkKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	return buf.Bytes()
}
