package auth

import (
	"context"
	"slices"
)

// APIToken is the API token a request was authenticated with (see package
// apitoken). The request's User is then the token's CREATOR, so everything that
// looks a person up (the workspace guard reads their membership and role afresh on
// every request) sees the creator as they are now: a creator who was removed makes
// the token fail like a stranger, a demoted creator caps the token at once.
// The guard then caps the role at Role and keeps the token inside WorkspaceID and
// EnvironmentIDs.
type APIToken struct {
	ID          string
	Name        string
	Prefix      string // "hlt_" and 8 hex characters: for lists and audit entries; not secret
	WorkspaceID string
	// Role is the highest workspace role the token may use: viewer, editor or admin.
	Role string
	// EnvironmentIDs restricts the token to these environments; empty means all of its workspace's.
	EnvironmentIDs []string
	CreatorID      string
	CreatorEmail   string
}

// Restricted reports whether the token is limited to some environments.
func (t APIToken) Restricted() bool { return len(t.EnvironmentIDs) > 0 }

// AllowsEnvironment reports whether the token may reach the environment.
func (t APIToken) AllowsEnvironment(id string) bool {
	return !t.Restricted() || slices.Contains(t.EnvironmentIDs, id)
}

type apiTokenKey struct{}

// WithAPIToken stores the token principal in ctx.
func WithAPIToken(ctx context.Context, t APIToken) context.Context {
	return context.WithValue(ctx, apiTokenKey{}, t)
}

// APITokenFrom returns the token the request was authenticated with, if any.
func APITokenFrom(ctx context.Context) (APIToken, bool) {
	t, ok := ctx.Value(apiTokenKey{}).(APIToken)
	return t, ok
}
