package server

import "testing"

// A route nobody classified is refused to tokens: the default is no.
func TestUnclassifiedRoutesAreDeniedToTokens(t *testing.T) {
	for _, p := range []string{
		"GET /environments/{envId}/brand-new", "POST /workspaces/{wsId}/something-added-tomorrow", "DELETE /environments/{envId}/flags/{key}/nuke", "", "GET /",
	} {
		if tokenAllowed(p) {
			t.Errorf("%q is token-allowed without being classified", p)
		}
	}
	missing, stale := UnclassifiedRoutes([]Route{{Method: "GET", Path: "/x"}, {Method: "GET", Path: "/environments/{envId}/flags"}})
	if len(missing) != 1 || missing[0] != "GET /x" || len(stale) != len(tokenAccess)-1 {
		t.Errorf("missing %v, stale %d", missing, len(stale))
	}
}

// What a token must never reach is classified denied, by name: a mistake here is a mistake in the allowlist itself.
func TestTheDangerousRoutesAreDenied(t *testing.T) {
	for _, p := range []string{
		"POST /workspaces/{wsId}/api-tokens", "GET /workspaces/{wsId}/api-tokens", "DELETE /workspaces/{wsId}/api-tokens/{tokenId}",
		"POST /workspaces/{wsId}/invites", "GET /workspaces/{wsId}/members", "PATCH /workspaces/{wsId}/members/{userId}", "DELETE /workspaces/{wsId}/members/{userId}",
		"PATCH /workspaces/{wsId}", "POST /workspaces", "POST /invites/accept", "GET /me",
		"GET /environments/{envId}/sdk-keys", "POST /environments/{envId}/sdk-keys", "DELETE /environments/{envId}/sdk-keys/{keyId}",
		"DELETE /environments/{envId}/flags/{key}", "POST /environments/{envId}/flags", "POST /internal/scheduler/run",
	} {
		allowed, known := tokenAccess[p]
		if !known || allowed {
			t.Errorf("%s must be classified token-denied (known=%v allowed=%v)", p, known, allowed)
		}
	}
	for _, p := range []string{"PATCH /environments/{envId}/flags/{key}", "POST /environments/{envId}/flags/{key}/kill", "GET /environments/{envId}/audit-logs", "GET /workspaces/{wsId}/environments"} {
		if !tokenAccess[p] {
			t.Errorf("%s should be token-allowed", p)
		}
	}
}
