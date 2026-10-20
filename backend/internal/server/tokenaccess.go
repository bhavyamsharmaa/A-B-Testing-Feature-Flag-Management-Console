package server

import "sort"

// tokenAccess is the API-token allowlist: for EVERY registered route, whether
// an API token may reach it. It is keyed by the route's pattern ("METHOD path")
// exactly as registered, and it is an allowlist in effect:
//
//   - a route that is absent from this map is refused to tokens, whatever
//     its role requirement (tokenAllowed returns false), so a route added tomorrow
//     is unreachable by tokens until someone decides otherwise;
//   - TestEveryRouteIsClassifiedForTokens fails when a registered route is absent
//     from the map (and when the map names a route that no longer exists), so the
//     decision cannot be forgotten, only made.
//
// A token reaches what a CI pipeline legitimately needs: flags (read, edit, kill),
// experiments (read, start, stop), results, segments, promotion, scheduled changes,
// the audit log, and the environments it can see. It never reaches: managing tokens
// (a token cannot mint or revoke tokens, its own included), members, invites,
// workspaces, SDK keys, deleting or creating flags and experiments, the caller's own
// account, or anything that is not control-plane at all (health, the SDK endpoints,
// the internal scheduler route: their own credentials apply, and a token is not one).
//
// What a token may do on an allowed route is decided by the same guard as for a
// person, with the token's role capped by its creator's current role.
var tokenAccess = map[string]bool{
	// Not control-plane routes: a token has no meaning here.
	"GET /healthz":                 false,
	"POST /evaluate":               false,
	"POST /track":                  false,
	"GET /sdk/stream":              false,
	"POST /internal/scheduler/run": false,

	// The caller's own account and the workspace itself: a person's, never a pipeline's.
	"GET /me":                                      false,
	"GET /workspaces":                              false,
	"POST /workspaces":                             false,
	"POST /invites/accept":                         false,
	"PATCH /workspaces/{wsId}":                     false,
	"POST /workspaces/{wsId}/switch":               false,
	"GET /workspaces/{wsId}/members":               false,
	"PATCH /workspaces/{wsId}/members/{userId}":    false,
	"DELETE /workspaces/{wsId}/members/{userId}":   false,
	"GET /workspaces/{wsId}/invites":               false,
	"POST /workspaces/{wsId}/invites":              false,
	"DELETE /workspaces/{wsId}/invites/{inviteId}": false,

	// Token management: session only. A token can never mint, list or revoke tokens.
	"POST /workspaces/{wsId}/api-tokens":                                           false,
	"GET /workspaces/{wsId}/api-tokens":                                            false,
	"POST /workspaces/{wsId}/invites/{inviteId}/resend":                            false,
	"GET /workspaces/{wsId}/webhooks":                                              false,
	"POST /workspaces/{wsId}/webhooks":                                             false,
	"PATCH /workspaces/{wsId}/webhooks/{endpointId}":                               false,
	"DELETE /workspaces/{wsId}/webhooks/{endpointId}":                              false,
	"POST /workspaces/{wsId}/webhooks/{endpointId}/rotate-secret":                  false,
	"POST /workspaces/{wsId}/webhooks/{endpointId}/test":                           false,
	"GET /workspaces/{wsId}/webhooks/{endpointId}/deliveries":                      false,
	"POST /workspaces/{wsId}/webhooks/{endpointId}/deliveries/{deliveryId}/resend": false,
	"POST /workspaces/{wsId}/ownership-offers":                                     false,
	"GET /workspaces/{wsId}/ownership-offers":                                      false,
	"DELETE /workspaces/{wsId}/ownership-offers/{offerId}":                         false,
	"POST /workspaces/{wsId}/schedule-deletion":                                    false,
	"POST /workspaces/restore":                                                     false,
	"POST /ownership-offers/{offerId}/accept":                                      false,
	"POST /ownership-offers/{offerId}/decline":                                     false,
	"POST /internal/workspaces/purge":                                              false,
	"POST /internal/outbox/run":                                                    false,
	"DELETE /workspaces/{wsId}/api-tokens/{tokenId}":                               false,

	// SDK key management: keys grant read access to an environment.
	"GET /environments/{envId}/sdk-keys":            false,
	"POST /environments/{envId}/sdk-keys":           false,
	"DELETE /environments/{envId}/sdk-keys/{keyId}": false,

	// Creating and deleting flags and experiments are decisions for a person.
	"POST /environments/{envId}/flags":         false,
	"DELETE /environments/{envId}/flags/{key}": false,
	"POST /environments/{envId}/experiments":   false,

	// What a pipeline needs.
	"GET /workspaces/{wsId}/environments":                            true,
	"GET /environments/{envId}/flags":                                true,
	"GET /environments/{envId}/flags/{key}":                          true,
	"PATCH /environments/{envId}/flags/{key}":                        true,
	"POST /environments/{envId}/flags/{key}/kill":                    true,
	"POST /environments/{envId}/flags/{key}/promotions/preview":      true,
	"POST /environments/{envId}/flags/{key}/promotions":              true,
	"GET /environments/{envId}/scheduled-changes":                    true,
	"GET /environments/{envId}/scheduled-changes/{changeId}":         true,
	"POST /environments/{envId}/flags/{key}/scheduled-changes":       true,
	"POST /environments/{envId}/scheduled-changes/{changeId}/cancel": true,
	"GET /environments/{envId}/audit-logs":                           true,
	"GET /environments/{envId}/experiments":                          true,
	"GET /environments/{envId}/experiments/{key}":                    true,
	"POST /environments/{envId}/experiments/{key}/start":             true,
	"POST /environments/{envId}/experiments/{key}/stop":              true,
	"GET /environments/{envId}/experiments/{key}/event-counts":       true,
	"GET /environments/{envId}/experiments/{key}/results":            true,
	"GET /workspaces/{wsId}/segments":                                true,
	"GET /workspaces/{wsId}/segments/{segKey}":                       true,
	"GET /workspaces/{wsId}/segments/{segKey}/usage":                 true,
	"POST /workspaces/{wsId}/segments":                               true,
	"PATCH /workspaces/{wsId}/segments/{segKey}":                     true,
	"DELETE /workspaces/{wsId}/segments/{segKey}":                    true,
}

// tokenAllowed is the classification of a route pattern. Unknown means no.
func tokenAllowed(pattern string) bool { return tokenAccess[pattern] }

// TokenClassified reports whether the route's pattern has been classified at all.
func TokenClassified(r Route) bool {
	_, ok := tokenAccess[r.Method+" "+r.Path]
	return ok
}

// UnclassifiedRoutes lists the registered routes with no entry in the allowlist
// map, and the map entries that name no registered route, sorted. Both lists
// should be empty.
func UnclassifiedRoutes(routes []Route) (missing, stale []string) {
	registered := map[string]bool{}
	for _, r := range routes {
		p := r.Method + " " + r.Path
		registered[p] = true
		if _, ok := tokenAccess[p]; !ok {
			missing = append(missing, p)
		}
	}
	for p := range tokenAccess {
		if !registered[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	return missing, stale
}
