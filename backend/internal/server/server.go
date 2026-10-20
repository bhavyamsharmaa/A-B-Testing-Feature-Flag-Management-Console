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
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/apitokens"
	"helios/backend/internal/controlplane/auditlog"
	"helios/backend/internal/controlplane/experiments"
	"helios/backend/internal/controlplane/flags"
	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/controlplane/results"
	"helios/backend/internal/controlplane/scheduling"
	"helios/backend/internal/controlplane/sdkkeys"
	"helios/backend/internal/controlplane/segments"
	"helios/backend/internal/controlplane/webhooks"
	"helios/backend/internal/controlplane/workspaces"
	"helios/backend/internal/controlplane/wspurge"
	"helios/backend/internal/dataplane/configcache"
	"helios/backend/internal/dataplane/evaluation"
	"helios/backend/internal/dataplane/stream"
	"helios/backend/internal/dataplane/track"
	"helios/backend/internal/delivery"
	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/apitoken"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/exposuretoken"
	"helios/backend/internal/platform/health"
	"helios/backend/internal/platform/mail"
	"helios/backend/internal/platform/netguard"
	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/platform/secretbox"
)

type Deps struct {
	Pool *pgxpool.Pool
	// Authn wraps a handler so it only runs for an authenticated user (and
	// puts that user in the request context).
	Authn          func(http.Handler) http.Handler
	Publisher      events.Publisher
	Subscriber     events.Subscriber
	SDKKeyCacheTTL time.Duration
	// InvitesByID allows accepting an invite by id and lists open invites in
	// /me. false leaves only the one-time link.
	InvitesByID bool
	// Email decides whether users with an unconfirmed email may bootstrap a
	// workspace or accept an invite. Nil means no check.
	Email *auth.EmailPolicy
	// Context bounds background work (the revocation watcher); nil means
	// context.Background().
	Context context.Context
	// Limiter holds the rate limits; nil means the defaults.
	Limiter *ratelimit.Limiter
	// ExposureTokens signs the exposure tokens /evaluate returns and verifies
	// the ones /track receives; nil means no tokens are issued and none checked.
	ExposureTokens *exposuretoken.Keyring
	// ExposureMode is what /track does with them (default off).
	ExposureMode exposuretoken.Mode
	// SchedulerSecret enables POST /internal/scheduler/run when it is at least
	// scheduling.MinSharedLen long; empty (or shorter) leaves the endpoint
	// answering 404. It is a secret: never log it.
	SchedulerSecret string
	// Clock is the time source of scheduled changes; nil means the real clock.
	// Tests inject their own.
	Clock scheduling.Clock
	// Scheduler tunes the scheduled-change runner (grace window, lease, ...).
	Scheduler scheduling.Config
	// APITokenCacheTTL bounds how long an instance trusts a verified API token it was not
	// told was revoked (zero: apitoken.DefaultCacheTTL, 15 seconds).
	APITokenCacheTTL time.Duration
	// Delivery configures webhooks and invite email (the signing-secret box, the SSRF guard, the
	// mail sender). The zero value is safe: webhooks report themselves unavailable, mail goes to the
	// sink, and endpoints must be public https hosts.
	Delivery delivery.Options
	// OutboxSecret enables POST /internal/outbox/run when at least outbox.MinSharedLen long;
	// empty (or shorter) leaves the endpoint answering 404. It is a secret: never log it.
	OutboxSecret string
	// PurgeSecret enables POST /internal/workspaces/purge when at least wspurge.MinSharedLen long; empty (or
	// shorter) leaves the endpoint answering 404. It is a secret: never log it.
	PurgeSecret string
	// StreamRecheck is how often an open /sdk/stream asks whether its key was
	// revoked. Zero means 10 seconds.
	StreamRecheck time.Duration
	// ConfigCacheTTL is how long /evaluate may serve an environment's flag configuration from memory; zero turns
	// the cache off (every request reads the database, which the tests rely on). The binary defaults it to 10 s.
	// ConfigCacheMaxEntries and ConfigCacheMaxBytes bound its memory (zero: 1000 environments, 32 MiB).
	ConfigCacheTTL        time.Duration
	ConfigCacheMaxEntries int
	ConfigCacheMaxBytes   int64
	// TrackMaxWriters bounds how many /track batches use the database at once (zero: the pool size minus 2, at least 2);
	// TrackMaxWait is how long a batch waits for a turn before a fast 503 (zero: no bound, used by tests that
	// want the pre-gate behaviour; the binary sets 500 ms).
	TrackMaxWriters int
	TrackMaxWait    time.Duration
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
	// ScopeInternal: not part of the public API. Called by a scheduler job with
	// a shared secret; carries no tenant id and returns counts only.
	ScopeInternal Scope = "internal"
)

// Route is one registered route and how it is protected.
type Route struct {
	Method  string
	Path    string // e.g. /environments/{envId}/flags/{key}
	Scope   Scope
	MinRole rbac.WorkspaceRole // for ScopeEnv and ScopeWorkspace: the lowest role let through
	// TokenAllowed: an API token may reach this route (see tokenaccess.go). False for
	// every route not explicitly classified.
	TokenAllowed bool
	// Limits names the rate-limit classes in front of the route (none for the
	// health probe, which must never be limited).
	Limits []string
}

// Router is the mux plus the registry of everything mounted on it.
type Router struct {
	Mux    *http.ServeMux
	Routes []Route
	// ConfigCache is this instance's flag configuration cache (disabled when Deps.ConfigCacheTTL is zero); exposed so
	// tests can look at what it holds.
	ConfigCache *configcache.Cache
	// TrackGate bounds /track's database work (nil when off); exposed for tests.
	TrackGate *track.Gate
}

func (r *Router) add(pattern string, scope Scope, min rbac.WorkspaceRole, limits []string, h http.Handler) {
	method, path, _ := strings.Cut(pattern, " ")
	r.Mux.Handle(pattern, h)
	r.Routes = append(r.Routes, Route{Method: method, Path: path, Scope: scope, MinRole: min, Limits: limits, TokenAllowed: tokenAllowed(pattern)})
}

// limit is one extra rate limit on a route, on top of the general ones.
type limit struct {
	class string
	byIP  bool
}

func perUser(class string) limit { return limit{class: class} }
func perIP(class string) limit   { return limit{class: class, byIP: true} }

// sdkThrottle adapts the limiter to the SDK-key verifier: failed attempts are
// counted per client IP, and an IP that has used up its allowance is refused
// before any (expensive) verification of an uncached key.
type sdkThrottle struct{ l *ratelimit.Limiter }

func (t sdkThrottle) Allowed(r *http.Request) (bool, time.Duration) {
	return t.l.Peek(ratelimit.ClassSDKBad, t.l.ClientIPOf(r))
}
func (t sdkThrottle) Failed(r *http.Request) { t.l.Take(ratelimit.ClassSDKBad, t.l.ClientIPOf(r)) }

// byKey limits per SDK key (its prefix); it must run after the verifier.
func byKey(l *ratelimit.Limiter, class string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sc, ok := apikey.ScopeFrom(r.Context()); ok {
				if ok, wait := l.Take(class, sc.Prefix); !ok {
					ratelimit.Reject(w, wait)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
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
	del := d.Delivery
	if del.Box == nil {
		del.Box, _ = secretbox.New("")
	}
	if del.Guard == nil {
		del.Guard = netguard.New(netguard.Policy{})
	}
	if del.Mail == nil {
		del.Mail = &mail.Sink{}
	}
	if del.Outbox.Now == nil && d.Clock != nil {
		del.Outbox.Now = d.Clock
	}
	outboxWorker := delivery.NewWorker(d.Pool, del)
	wh := webhooks.NewHandlers(d.Pool, del.Box, del.Guard, outboxWorker, del.Outbox.Now)
	sdkKeys := apikey.NewVerifier(d.Pool, d.SDKKeyCacheTTL)
	// The per-instance flag configuration cache. Everything that publishes goes through the invalidating publisher,
	// so a change made on this instance is visible to its next request whether or not the broker is up.
	cfgCache := configcache.New(func(ctx context.Context, ws, env string) (*evaluation.Snapshot, error) {
		return evaluation.LoadSnapshot(ctx, d.Pool, ws, env)
	}, configcache.Options{TTL: d.ConfigCacheTTL, MaxEntries: d.ConfigCacheMaxEntries, MaxBytes: d.ConfigCacheMaxBytes})
	pub := events.Publisher(configcache.InvalidatingPublisher{Inner: d.Publisher, Cache: cfgCache})
	drops := &dropFan{sdk: sdkKeys, configs: cfgCache} // the API token verifier joins it below, once it exists
	ws := workspaces.NewHandlers(workspaces.NewService(d.Pool).WithMail(del.Mail.Mode(), del.Outbox.Now).WithRuntime(drops, pub), d.Email, d.InvitesByID)
	fl := flags.NewHandlers(d.Pool, pub)
	al := auditlog.NewHandlers(d.Pool)
	sg := segments.NewHandlers(d.Pool, pub)
	var clock func() time.Time
	if d.Clock != nil {
		clock = d.Clock
	}
	sc := scheduling.NewHandlers(d.Pool, d.Clock)
	runner := scheduling.NewRunner(d.Pool, pub, d.Clock, d.Scheduler)
	ex := experiments.NewHandlers(d.Pool, pub)
	res := results.NewHandlers(d.Pool)
	keys := sdkkeys.NewHandlers(d.Pool, sdkKeys, pub)
	ctx := d.Context
	if ctx == nil {
		ctx = context.Background()
	}
	// Hear about revocations made on other instances (no-op without a broker).
	go sdkKeys.Watch(ctx, d.Subscriber)
	if cfgCache.Enabled() {
		go cfgCache.Run(ctx, d.Subscriber) // flag changes and workspace drops made on other instances
	}
	recheck := d.StreamRecheck
	if recheck == 0 {
		recheck = 10 * time.Second
	}

	lim := d.Limiter
	if lim == nil {
		lim = ratelimit.New(ratelimit.Config{})
	}
	sdkKeys.SetThrottle(sdkThrottle{lim})

	rt := &Router{Mux: http.NewServeMux()}
	// controlPlane puts every authenticated route behind: a per-IP limit
	// (before authentication, so it also covers token guessing), the
	// authenticator, a per-user limit, then the route's own extra limits.
	tokens := apitoken.NewVerifier(d.Pool, lim, d.APITokenCacheTTL, clock)
	tokens.OnRejected = apitokens.AuditRejection(d.Pool)
	drops.tokens = tokens
	tk := apitokens.NewHandlers(d.Pool, tokens, pub)
	go tokens.Watch(ctx, d.Subscriber) // revocations announced by other instances
	// authn is the authentication of a control-plane route: the user JWT, or an API
	// token when the bearer starts with "hlt_" and the route is classified token-allowed.
	authn := func(pattern string) func(http.Handler) http.Handler {
		return tokens.Wrap(d.Authn, tokenAllowed(pattern))
	}
	controlPlane := func(pattern string, inner http.Handler, extras []limit) (http.Handler, []string) {
		names := []string{ratelimit.ClassIP, ratelimit.ClassUser}
		h := inner
		for i := len(extras) - 1; i >= 0; i-- {
			if !extras[i].byIP {
				h = lim.ByUser(extras[i].class)(h)
			}
		}
		h = authn(pattern)(lim.ByUser(ratelimit.ClassUser)(h))
		for i := len(extras) - 1; i >= 0; i-- {
			if extras[i].byIP {
				h = lim.ByIP(extras[i].class)(h)
			}
		}
		h = lim.ByIP(ratelimit.ClassIP)(h)
		for _, e := range extras {
			names = append(names, e.class)
		}
		return h, names
	}
	// The health probe is never rate-limited: orchestrators poll it.
	public := func(pattern string, h http.HandlerFunc) { rt.add(pattern, ScopePublic, "", nil, h) }
	self := func(pattern string, h http.HandlerFunc, extras ...limit) {
		wrapped, names := controlPlane(pattern, h, extras)
		rt.add(pattern, ScopeSelf, "", names, wrapped)
	}
	// env: authenticate, resolve {envId} through the caller's workspace
	// membership (404 when it isn't theirs), then check their role against req.
	env := func(pattern string, req rbac.Requirement, h http.HandlerFunc, extras ...limit) {
		wrapped, names := controlPlane(pattern, guard.Require(req, h), extras)
		rt.add(pattern, ScopeEnv, workspaceRoleFor(req), names, wrapped)
	}
	workspace := func(pattern string, min rbac.WorkspaceRole, h http.HandlerFunc, extras ...limit) {
		wrapped, names := controlPlane(pattern, guard.RequireWorkspace(min, h), extras)
		rt.add(pattern, ScopeWorkspace, min, names, wrapped)
	}
	// internal: a route for machines, not people or browsers: no user, no tenant,
	// a shared secret checked inside the handler. Its rate limit is per client IP
	// and runs first, so guessing the secret is limited like using it.
	internal := func(pattern string, h http.Handler, class string) {
		rt.add(pattern, ScopeInternal, "", []string{class}, lim.ByIP(class)(h))
	}
	// sdkAlso: per-IP throttling of uncached key verifications (in the
	// verifier), then a per-key limit once the key is known. inHandler names
	// limits the handler applies itself, declared so the registry lists them.
	sdkAlso := func(pattern string, h http.Handler, inHandler []string, classes ...string) {
		inner := h
		for i := len(classes) - 1; i >= 0; i-- {
			inner = byKey(lim, classes[i])(inner)
		}
		names := append([]string{ratelimit.ClassSDKBad}, classes...)
		rt.add(pattern, ScopeSDK, "", append(names, inHandler...), sdkKeys.Middleware(inner))
	}

	// sdk is sdkAlso for routes whose limits are all per-key classes.
	sdk := func(pattern string, h http.Handler, classes ...string) {
		sdkAlso(pattern, h, nil, classes...)
	}

	public("GET /healthz", health.Handler(d.Pool))

	// Account and workspaces. The first GET /me creates the personal workspace.
	self("GET /me", ws.Me, perUser(ratelimit.ClassMe))
	self("GET /workspaces", ws.List, perUser(ratelimit.ClassMe))
	self("POST /workspaces", ws.Create, perUser(ratelimit.ClassWorkspace))
	// accept is limited per user AND per IP: the token is a secret one could guess at.
	self("POST /invites/accept", ws.AcceptInvite, perUser(ratelimit.ClassAccept), perIP(ratelimit.ClassAccept))
	workspace("PATCH /workspaces/{wsId}", rbac.WorkspaceAdmin, ws.Rename)
	workspace("POST /workspaces/{wsId}/switch", rbac.WorkspaceViewer, ws.Switch)
	workspace("GET /workspaces/{wsId}/members", rbac.WorkspaceViewer, ws.Members)
	workspace("PATCH /workspaces/{wsId}/members/{userId}", rbac.WorkspaceAdmin, ws.ChangeRole)
	// Any member may call this to leave; removing someone else needs admin
	// (checked in the handler against the locked membership rows).
	workspace("DELETE /workspaces/{wsId}/members/{userId}", rbac.WorkspaceViewer, ws.RemoveMember)
	workspace("GET /workspaces/{wsId}/invites", rbac.WorkspaceAdmin, ws.Invites)
	workspace("POST /workspaces/{wsId}/invites", rbac.WorkspaceAdmin, ws.CreateInvite, perUser(ratelimit.ClassInvite))
	workspace("DELETE /workspaces/{wsId}/invites/{inviteId}", rbac.WorkspaceAdmin, ws.RevokeInvite, perUser(ratelimit.ClassInvite))
	// Ownership transfer: the owner offers to an admin, who accepts (session only: tokens are denied).
	workspace("POST /workspaces/{wsId}/ownership-offers", rbac.WorkspaceOwner, ws.OfferOwnership, perUser(ratelimit.ClassInvite))
	workspace("GET /workspaces/{wsId}/ownership-offers", rbac.WorkspaceOwner, ws.OpenOffer)
	workspace("DELETE /workspaces/{wsId}/ownership-offers/{offerId}", rbac.WorkspaceOwner, ws.CancelOffer, perUser(ratelimit.ClassInvite))
	self("POST /ownership-offers/{offerId}/accept", ws.AcceptOffer, perUser(ratelimit.ClassAccept))
	self("POST /ownership-offers/{offerId}/decline", ws.DeclineOffer, perUser(ratelimit.ClassAccept))
	// Deleting a workspace: schedule (owner, after typing its name), restore (during the grace period; a self
	// route because a workspace scheduled for deletion is invisible to the workspace routes).
	workspace("POST /workspaces/{wsId}/schedule-deletion", rbac.WorkspaceOwner, ws.ScheduleDeletion, perUser(ratelimit.ClassWorkspace))
	self("POST /workspaces/restore", ws.Restore, perUser(ratelimit.ClassWorkspace))
	workspace("POST /workspaces/{wsId}/invites/{inviteId}/resend", rbac.WorkspaceAdmin, ws.ResendInvite, perUser(ratelimit.ClassInvite))

	// Webhooks (see docs/WEBHOOKS.md): admins and owners, session only (token-denied).
	workspace("GET /workspaces/{wsId}/webhooks", rbac.WorkspaceAdmin, wh.List)
	workspace("POST /workspaces/{wsId}/webhooks", rbac.WorkspaceAdmin, wh.Create, perUser(ratelimit.ClassWebhookWrite))
	workspace("PATCH /workspaces/{wsId}/webhooks/{endpointId}", rbac.WorkspaceAdmin, wh.Update, perUser(ratelimit.ClassWebhookWrite))
	workspace("DELETE /workspaces/{wsId}/webhooks/{endpointId}", rbac.WorkspaceAdmin, wh.Delete, perUser(ratelimit.ClassWebhookWrite))
	workspace("POST /workspaces/{wsId}/webhooks/{endpointId}/rotate-secret", rbac.WorkspaceAdmin, wh.RotateSecret, perUser(ratelimit.ClassWebhookWrite))
	workspace("POST /workspaces/{wsId}/webhooks/{endpointId}/test", rbac.WorkspaceAdmin, wh.Test, perUser(ratelimit.ClassWebhookTest))
	workspace("GET /workspaces/{wsId}/webhooks/{endpointId}/deliveries", rbac.WorkspaceAdmin, wh.Deliveries)
	workspace("POST /workspaces/{wsId}/webhooks/{endpointId}/deliveries/{deliveryId}/resend", rbac.WorkspaceAdmin, wh.Resend, perUser(ratelimit.ClassWebhookTest))
	purger := wspurge.New(d.Pool, wspurge.Config{Pub: pub})
	internal("POST /internal/workspaces/purge", wspurge.InternalHandler(d.PurgeSecret, purger.Run), ratelimit.ClassPurgeRun)
	internal("POST /internal/outbox/run", outbox.InternalHandler(d.OutboxSecret, outboxWorker.Run), ratelimit.ClassOutboxRun)

	// API tokens (see docs/API_TOKENS.md). Managing them is for admins and owners and is
	// session-only: these routes are classified token-denied. A token's creator is the
	// authority behind it, so these are the only places tokens are minted or revoked.
	workspace("POST /workspaces/{wsId}/api-tokens", rbac.WorkspaceAdmin, tk.Create, perUser(ratelimit.ClassTokenWrite))
	workspace("GET /workspaces/{wsId}/api-tokens", rbac.WorkspaceAdmin, tk.List)
	workspace("DELETE /workspaces/{wsId}/api-tokens/{tokenId}", rbac.WorkspaceAdmin, tk.Revoke, perUser(ratelimit.ClassTokenWrite))
	// The environments the caller can see: the first thing a pipeline asks for.
	workspace("GET /workspaces/{wsId}/environments", rbac.WorkspaceViewer, tk.Environments)

	env("GET /environments/{envId}/flags", rbac.Min(rbac.Viewer), fl.List)
	env("GET /environments/{envId}/flags/{key}", rbac.Min(rbac.Viewer), fl.Get)
	env("POST /environments/{envId}/flags", rbac.Min(rbac.Editor), fl.Create)
	env("PATCH /environments/{envId}/flags/{key}", rbac.FlagWrite, fl.Update)
	env("DELETE /environments/{envId}/flags/{key}", rbac.Min(rbac.Admin), fl.Delete)
	// Promotion: copy a flag's configuration from another environment of the
	// workspace into {envId} (the target). Preview is a read, any role. Apply is
	// an edit of the target, so it needs what an edit there needs: an approver
	// for production. {key} is the flag.
	env("POST /environments/{envId}/flags/{key}/promotions/preview", rbac.Min(rbac.Viewer), fl.PromotePreview)
	env("POST /environments/{envId}/flags/{key}/promotions", rbac.FlagWrite, fl.PromoteApply)
	// Editor+ in every environment, production included: an unnecessary
	// kill costs a disabled feature, a blocked one during an incident costs
	// prolonged user harm (PRD US-06).
	env("POST /environments/{envId}/flags/{key}/kill", rbac.Min(rbac.Editor), fl.Kill)

	// Segments are workspace-wide (every environment uses the same ones):
	// any member reads (default limits), editors and above write, behind a
	// tighter per-user limit of their own (the writes are the expensive part).
	workspace("GET /workspaces/{wsId}/segments", rbac.WorkspaceViewer, sg.List)
	workspace("GET /workspaces/{wsId}/segments/{segKey}", rbac.WorkspaceViewer, sg.Get)
	workspace("GET /workspaces/{wsId}/segments/{segKey}/usage", rbac.WorkspaceViewer, sg.Usage)
	workspace("POST /workspaces/{wsId}/segments", rbac.WorkspaceEditor, sg.Create, perUser(ratelimit.ClassSegmentWrite))
	workspace("PATCH /workspaces/{wsId}/segments/{segKey}", rbac.WorkspaceEditor, sg.Update, perUser(ratelimit.ClassSegmentWrite))
	workspace("DELETE /workspaces/{wsId}/segments/{segKey}", rbac.WorkspaceEditor, sg.Delete, perUser(ratelimit.ClassSegmentWrite))

	// Scheduled changes. Any role reads. Writing needs an editor here and then the
	// requirement of the change's own kind in the handler (an approver for a
	// production change that is not a kill), the same as making it now; the
	// runner checks it again when it executes. {key} is the flag.
	env("GET /environments/{envId}/scheduled-changes", rbac.Min(rbac.Viewer), sc.List)
	env("GET /environments/{envId}/scheduled-changes/{changeId}", rbac.Min(rbac.Viewer), sc.Get)
	env("POST /environments/{envId}/flags/{key}/scheduled-changes", rbac.Min(rbac.Editor), sc.Create, perUser(ratelimit.ClassScheduleWrite))
	env("POST /environments/{envId}/scheduled-changes/{changeId}/cancel", rbac.Min(rbac.Editor), sc.Cancel, perUser(ratelimit.ClassScheduleWrite))
	internal("POST /internal/scheduler/run", scheduling.InternalHandler(d.SchedulerSecret, runner.Run), ratelimit.ClassSchedulerRun)

	// Same requirement as listing flags: any role in {envId}.
	env("GET /environments/{envId}/audit-logs", rbac.Min(rbac.Viewer), al.List)
	// Exporting is for admins and owners, session only, and rate limited; the export is itself audited.
	env("GET /environments/{envId}/audit-logs/export", rbac.Min(rbac.Admin), al.Export, perUser(ratelimit.ClassAuditExport))
	// One flag's history (the flag drawer's History tab): any role that can see the flag.
	env("GET /environments/{envId}/flags/{key}/history", rbac.Min(rbac.Viewer), al.History)

	// Editors create and start; stopping needs an approver; any role reads.
	env("GET /environments/{envId}/experiments", rbac.Min(rbac.Viewer), ex.List)
	env("GET /environments/{envId}/experiments/{key}", rbac.Min(rbac.Viewer), ex.Get)
	env("POST /environments/{envId}/experiments", rbac.Min(rbac.Editor), ex.Create)
	env("PATCH /environments/{envId}/experiments/{key}", rbac.Min(rbac.Editor), ex.SetControl)
	env("POST /environments/{envId}/experiments/{key}/start", rbac.Min(rbac.Editor), ex.Start)
	env("POST /environments/{envId}/experiments/{key}/stop", rbac.Min(rbac.Approver), ex.Stop)
	// Live counts of the data arriving (not results): any role reads.
	env("GET /environments/{envId}/experiments/{key}/event-counts", rbac.Min(rbac.Viewer), ex.EventCounts)
	// Results: any role reads; per-user limit because it is the costliest read.
	env("GET /environments/{envId}/experiments/{key}/results", rbac.Min(rbac.Viewer), res.Get, perUser(ratelimit.ClassResults))

	// SDK keys are admin-only: they grant read access to the environment.
	env("GET /environments/{envId}/sdk-keys", rbac.Min(rbac.Admin), keys.List)
	env("POST /environments/{envId}/sdk-keys", rbac.Min(rbac.Admin), keys.Create, perUser(ratelimit.ClassKeyCreate))
	env("DELETE /environments/{envId}/sdk-keys/{keyId}", rbac.Min(rbac.Admin), keys.Revoke)

	var cfgSource evaluation.Source // nil: every request reads the database
	if cfgCache.Enabled() {
		cfgSource = cfgCache
	}
	rt.ConfigCache = cfgCache
	sdk("POST /evaluate", evaluation.HandlerWith(d.Pool, d.ExposureTokens, cfgSource), ratelimit.ClassSDKKey)
	// Event ingestion: the same SDK-key authentication (and revocation) as
	// /evaluate, with buckets of its own: batches per key here, events per key
	// inside the handler (it needs the batch size).
	store := track.NewPgStore(d.Pool)
	var trackGate *track.Gate
	if d.TrackMaxWait > 0 {
		writers := d.TrackMaxWriters
		if writers <= 0 {
			// Leave two connections of the pool for /evaluate (segment reads), the console and the jobs' endpoints.
			// Measured on the load test: half the pool cost 11% of /track throughput, pool-2 about 5% (docs/PERFORMANCE.md).
			writers = int(d.Pool.Config().MaxConns) - 2
		}
		trackGate = track.NewGate(max(writers, 2), d.TrackMaxWait)
	}
	rt.TrackGate = trackGate
	sdkAlso("POST /track", track.Handler(track.Deps{Writer: store, Experiments: store, Limiter: lim, Tokens: d.ExposureTokens, Mode: d.ExposureMode, Gate: trackGate}),
		[]string{ratelimit.ClassSDKTrackEvents}, ratelimit.ClassSDKTrack)
	sdk("GET /sdk/stream", stream.Handler(d.Subscriber, sdkKeys, recheck, lim), ratelimit.ClassSDKKey, ratelimit.ClassStream)

	return rt
}

// dropFan forgets a workspace in every verifier of this instance: its SDK keys (and their streams) and its API tokens.
type dropFan struct {
	sdk     *apikey.Verifier
	tokens  *apitoken.Verifier
	configs *configcache.Cache
}

func (f *dropFan) InvalidateWorkspace(workspaceID string) {
	f.sdk.InvalidateWorkspace(workspaceID)
	if f.configs != nil {
		f.configs.InvalidateWorkspace(workspaceID)
	}
	if f.tokens != nil {
		f.tokens.InvalidateWorkspace(workspaceID)
	}
}
