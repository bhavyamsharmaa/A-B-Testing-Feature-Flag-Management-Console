// Package server wires every HTTP route. cmd/api uses it with Supabase JWT
// authentication; the integration tests use it with a stub authenticator, so
// they exercise the real routes, guards and SQL.
//
// Every route is registered through one of the helpers below, each of which
// states how the route is tenant-scoped. The registry is returned with the
// router so tests can iterate over EVERY route: a route added without going
// through a helper does not exist, and one that is mis-scoped fails
// internal/integration's route test.
package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/auditlog"
	"helios/backend/internal/controlplane/experiments"
	"helios/backend/internal/controlplane/flags"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/controlplane/sdkkeys"
	"helios/backend/internal/controlplane/workspaces"
	"helios/backend/internal/dataplane/evaluation"
	"helios/backend/internal/dataplane/stream"
	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/health"
)

type Deps struct {
	Pool *pgxpool.Pool
	// Authn wraps a handler so it only runs for an authenticated user (and
	// puts that user in the request context).
	Authn          func(http.Handler) http.Handler
	Publisher      events.Publisher
	Subscriber     events.Subscriber
	SDKKeyCacheTTL time.Duration
	// Email decides whether users with an unconfirmed email may bootstrap a
	// workspace or accept an invite. Nil means no check.
	Email *auth.EmailPolicy
	// StreamRecheck is how often an open /sdk/stream asks whether its key was
	// revoked. Zero means 10 seconds.
	StreamRecheck time.Duration
}

// Scope says how a route is tied to a tenant.
type Scope string

const (
	// ScopePublic: no authentication, no tenant data (the health probe).
	ScopePublic Scope = "public"
	// ScopeSelf: authenticated, and it only ever reads or writes the caller's
	// own data; there is no tenant id in the path.
	ScopeSelf Scope = "self"
	// ScopeEnv: /environments/{envId}/...; the caller must be a member of the
	// environment's workspace (else 404) with a sufficient role (else 403).
	ScopeEnv Scope = "environment"
	// ScopeWorkspace: /workspaces/{wsId}/...; same, for the workspace.
	ScopeWorkspace Scope = "workspace"
	// ScopeSDK: authenticated by an SDK key, which sees only its own
	// environment.
	ScopeSDK Scope = "sdk"
)

// Route is one registered route and how it is protected.
type Route struct {
	Method  string
	Path    string // e.g. /environments/{envId}/flags/{key}
	Scope   Scope
	MinRole rbac.WorkspaceRole // for ScopeEnv and ScopeWorkspace: the lowest role let through
}

// Router is the mux plus the registry of everything mounted on it.
type Router struct {
	Mux    *http.ServeMux
	Routes []Route
}

func (r *Router) add(pattern string, scope Scope, min rbac.WorkspaceRole, h http.Handler) {
	method, path, _ := strings.Cut(pattern, " ")
	r.Mux.Handle(pattern, h)
	r.Routes = append(r.Routes, Route{Method: method, Path: path, Scope: scope, MinRole: min})
}

// workspaceRoleFor is the lowest workspace role whose environment access
// satisfies req (checked for production, the stricter case).
func workspaceRoleFor(req rbac.Requirement) rbac.WorkspaceRole {
	need := req(rbac.Environment{IsProduction: true})
	if other := req(rbac.Environment{}); rank(other) > rank(need) {
		need = other
	}
	switch need {
	case rbac.Admin, rbac.Approver:
		return rbac.WorkspaceAdmin
	case rbac.Editor:
		return rbac.WorkspaceEditor
	default:
		return rbac.WorkspaceViewer
	}
}

func rank(r rbac.Role) int {
	switch r {
	case rbac.Admin:
		return 4
	case rbac.Approver:
		return 3
	case rbac.Editor:
		return 2
	}
	return 1
}

// New returns the router with every route mounted.
func New(d Deps) *Router {
	guard := rbac.NewGuard(d.Pool)
	ws := workspaces.NewHandlers(workspaces.NewService(d.Pool), d.Email)
	fl := flags.NewHandlers(d.Pool, d.Publisher)
	al := auditlog.NewHandlers(d.Pool)
	ex := experiments.NewHandlers(d.Pool)
	sdkKeys := apikey.NewVerifier(d.Pool, d.SDKKeyCacheTTL)
	keys := sdkkeys.NewHandlers(d.Pool, sdkKeys)
	recheck := d.StreamRecheck
	if recheck == 0 {
		recheck = 10 * time.Second
	}

	rt := &Router{Mux: http.NewServeMux()}
	public := func(pattern string, h http.HandlerFunc) { rt.add(pattern, ScopePublic, "", h) }
	self := func(pattern string, h http.HandlerFunc) { rt.add(pattern, ScopeSelf, "", d.Authn(h)) }
	// env: authenticate, resolve {envId} through the caller's workspace
	// membership (404 when it isn't theirs), then check their role against req.
	env := func(pattern string, req rbac.Requirement, h http.HandlerFunc) {
		rt.add(pattern, ScopeEnv, workspaceRoleFor(req), d.Authn(guard.Require(req, h)))
	}
	workspace := func(pattern string, min rbac.WorkspaceRole, h http.HandlerFunc) {
		rt.add(pattern, ScopeWorkspace, min, d.Authn(guard.RequireWorkspace(min, h)))
	}
	sdk := func(pattern string, h http.Handler) { rt.add(pattern, ScopeSDK, "", sdkKeys.Middleware(h)) }

	public("GET /healthz", health.Handler(d.Pool))

	// Account and workspaces. The first GET /me creates the personal workspace.
	self("GET /me", ws.Me)
	self("GET /workspaces", ws.List)
	self("POST /workspaces", ws.Create)
	self("POST /invites/accept", ws.AcceptInvite)
	workspace("PATCH /workspaces/{wsId}", rbac.WorkspaceAdmin, ws.Rename)
	workspace("POST /workspaces/{wsId}/switch", rbac.WorkspaceViewer, ws.Switch)
	workspace("GET /workspaces/{wsId}/members", rbac.WorkspaceViewer, ws.Members)
	workspace("PATCH /workspaces/{wsId}/members/{userId}", rbac.WorkspaceAdmin, ws.ChangeRole)
	// Any member may call this to leave; removing someone else needs admin
	// (checked in the handler against the locked membership rows).
	workspace("DELETE /workspaces/{wsId}/members/{userId}", rbac.WorkspaceViewer, ws.RemoveMember)
	workspace("GET /workspaces/{wsId}/invites", rbac.WorkspaceAdmin, ws.Invites)
	workspace("POST /workspaces/{wsId}/invites", rbac.WorkspaceAdmin, ws.CreateInvite)
	workspace("DELETE /workspaces/{wsId}/invites/{inviteId}", rbac.WorkspaceAdmin, ws.RevokeInvite)

	env("GET /environments/{envId}/flags", rbac.Min(rbac.Viewer), fl.List)
	env("GET /environments/{envId}/flags/{key}", rbac.Min(rbac.Viewer), fl.Get)
	env("POST /environments/{envId}/flags", rbac.Min(rbac.Editor), fl.Create)
	env("PATCH /environments/{envId}/flags/{key}", rbac.FlagWrite, fl.Update)
	env("DELETE /environments/{envId}/flags/{key}", rbac.Min(rbac.Admin), fl.Delete)
	// Editor+ in every environment, production included: an unnecessary
	// kill costs a disabled feature, a blocked one during an incident costs
	// prolonged user harm (PRD US-06).
	env("POST /environments/{envId}/flags/{key}/kill", rbac.Min(rbac.Editor), fl.Kill)

	// Same requirement as listing flags: any role in {envId}.
	env("GET /environments/{envId}/audit-logs", rbac.Min(rbac.Viewer), al.List)

	// Editors create and start; stopping needs an approver; any role reads.
	env("GET /environments/{envId}/experiments", rbac.Min(rbac.Viewer), ex.List)
	env("GET /environments/{envId}/experiments/{key}", rbac.Min(rbac.Viewer), ex.Get)
	env("POST /environments/{envId}/experiments", rbac.Min(rbac.Editor), ex.Create)
	env("POST /environments/{envId}/experiments/{key}/start", rbac.Min(rbac.Editor), ex.Start)
	env("POST /environments/{envId}/experiments/{key}/stop", rbac.Min(rbac.Approver), ex.Stop)

	// SDK keys are admin-only: they grant read access to the environment.
	env("GET /environments/{envId}/sdk-keys", rbac.Min(rbac.Admin), keys.List)
	env("POST /environments/{envId}/sdk-keys", rbac.Min(rbac.Admin), keys.Create)
	env("DELETE /environments/{envId}/sdk-keys/{keyId}", rbac.Min(rbac.Admin), keys.Revoke)

	sdk("POST /evaluate", evaluation.Handler(d.Pool))
	sdk("GET /sdk/stream", stream.Handler(d.Subscriber, sdkKeys, recheck))

	return rt
}
