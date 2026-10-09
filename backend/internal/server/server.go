// Package server wires every HTTP route. cmd/api uses it with Supabase JWT
// authentication; the integration tests use it with a stub authenticator, so
// they exercise the real routes, guards and SQL.
package server

import (
	"net/http"
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
}

// New returns the router with every route mounted.
func New(d Deps) *http.ServeMux {
	guard := rbac.NewGuard(d.Pool)
	ws := workspaces.NewHandlers(workspaces.NewService(d.Pool))
	fl := flags.NewHandlers(d.Pool, d.Publisher)
	al := auditlog.NewHandlers(d.Pool)
	ex := experiments.NewHandlers(d.Pool)
	keys := sdkkeys.NewHandlers(d.Pool)
	sdkKeys := apikey.NewVerifier(d.Pool, d.SDKKeyCacheTTL)

	// protected wraps an /environments/{envId}/... handler: authenticate,
	// resolve {envId} through the caller's workspace membership (404 when it
	// isn't theirs), then check their role against req.
	protected := func(req rbac.Requirement, h http.HandlerFunc) http.Handler {
		return d.Authn(guard.Require(req, h))
	}
	// inWorkspace wraps a /workspaces/{wsId}/... handler the same way.
	inWorkspace := func(min rbac.WorkspaceRole, h http.HandlerFunc) http.Handler {
		return d.Authn(guard.RequireWorkspace(min, h))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health.Handler(d.Pool))

	// Account and workspaces. The first GET /me creates the personal workspace.
	mux.Handle("GET /me", d.Authn(http.HandlerFunc(ws.Me)))
	mux.Handle("GET /workspaces", d.Authn(http.HandlerFunc(ws.List)))
	mux.Handle("POST /workspaces", d.Authn(http.HandlerFunc(ws.Create)))
	mux.Handle("PATCH /workspaces/{wsId}", inWorkspace(rbac.WorkspaceAdmin, ws.Rename))
	mux.Handle("POST /workspaces/{wsId}/switch", inWorkspace(rbac.WorkspaceViewer, ws.Switch))
	mux.Handle("GET /workspaces/{wsId}/members", inWorkspace(rbac.WorkspaceViewer, ws.Members))
	mux.Handle("PATCH /workspaces/{wsId}/members/{userId}", inWorkspace(rbac.WorkspaceAdmin, ws.ChangeRole))
	// Any member may call this to leave; removing someone else needs admin
	// (checked in the handler against the locked membership rows).
	mux.Handle("DELETE /workspaces/{wsId}/members/{userId}", inWorkspace(rbac.WorkspaceViewer, ws.RemoveMember))
	mux.Handle("GET /workspaces/{wsId}/invites", inWorkspace(rbac.WorkspaceAdmin, ws.Invites))
	mux.Handle("POST /workspaces/{wsId}/invites", inWorkspace(rbac.WorkspaceAdmin, ws.CreateInvite))
	mux.Handle("DELETE /workspaces/{wsId}/invites/{inviteId}", inWorkspace(rbac.WorkspaceAdmin, ws.RevokeInvite))
	mux.Handle("POST /invites/accept", d.Authn(http.HandlerFunc(ws.AcceptInvite)))

	mux.Handle("GET /environments/{envId}/flags", protected(rbac.Min(rbac.Viewer), fl.List))
	mux.Handle("GET /environments/{envId}/flags/{key}", protected(rbac.Min(rbac.Viewer), fl.Get))
	mux.Handle("POST /environments/{envId}/flags", protected(rbac.Min(rbac.Editor), fl.Create))
	mux.Handle("PATCH /environments/{envId}/flags/{key}", protected(rbac.FlagWrite, fl.Update))
	mux.Handle("DELETE /environments/{envId}/flags/{key}", protected(rbac.Min(rbac.Admin), fl.Delete))
	// Editor+ in every environment, production included: an unnecessary
	// kill costs a disabled feature, a blocked one during an incident costs
	// prolonged user harm (PRD US-06).
	mux.Handle("POST /environments/{envId}/flags/{key}/kill", protected(rbac.Min(rbac.Editor), fl.Kill))

	// Same requirement as listing flags: any role in {envId}.
	mux.Handle("GET /environments/{envId}/audit-logs", protected(rbac.Min(rbac.Viewer), al.List))

	// Editors create and start; stopping needs an approver; any role reads.
	mux.Handle("GET /environments/{envId}/experiments", protected(rbac.Min(rbac.Viewer), ex.List))
	mux.Handle("GET /environments/{envId}/experiments/{key}", protected(rbac.Min(rbac.Viewer), ex.Get))
	mux.Handle("POST /environments/{envId}/experiments", protected(rbac.Min(rbac.Editor), ex.Create))
	mux.Handle("POST /environments/{envId}/experiments/{key}/start", protected(rbac.Min(rbac.Editor), ex.Start))
	mux.Handle("POST /environments/{envId}/experiments/{key}/stop", protected(rbac.Min(rbac.Approver), ex.Stop))

	// SDK keys are admin-only: they grant read access to the environment.
	mux.Handle("GET /environments/{envId}/sdk-keys", protected(rbac.Min(rbac.Admin), keys.List))
	mux.Handle("POST /environments/{envId}/sdk-keys", protected(rbac.Min(rbac.Admin), keys.Create))
	mux.Handle("DELETE /environments/{envId}/sdk-keys/{keyId}", protected(rbac.Min(rbac.Admin), keys.Revoke))

	mux.Handle("POST /evaluate", sdkKeys.Middleware(evaluation.Handler(d.Pool)))
	mux.Handle("GET /sdk/stream", sdkKeys.Middleware(stream.Handler(d.Subscriber)))

	return mux
}
