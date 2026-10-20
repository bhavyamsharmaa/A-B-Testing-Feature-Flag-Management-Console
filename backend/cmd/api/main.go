// Command api is the entrypoint for the Helios backend HTTP server.
//
// It reads configuration from the environment, connects to Postgres, and
// mounts the control-plane (Supabase JWT + workspace membership and roles) and
// data-plane (SDK key) routes; see internal/server.
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/controlplane/scheduling"
	"helios/backend/internal/delivery"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/config"
	"helios/backend/internal/platform/cors"
	"helios/backend/internal/platform/db"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/exposuretoken"
	"helios/backend/internal/platform/graceful"
	"helios/backend/internal/platform/lifecycle"
	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/platform/redisx"
	"helios/backend/internal/server"
)

// sdkKeyCacheTTL bounds how long a revoked SDK key keeps working on an
// instance that did not process the revocation itself (the one that did drops
// its cache entry at once). Argon2id costs tens of milliseconds, so a short TTL
// is cheap: one verification per key per instance every 15 seconds.
const sdkKeyCacheTTL = 15 * time.Second

func main() {
	cfg := config.Load()
	config.MustBeSafe(cfg, config.RoleAPI)
	if cfg.SupabaseURL == "" {
		log.Fatal("SUPABASE_URL is required, e.g. https://<project-ref>.supabase.co")
	}

	appCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()

	startPprof(appCtx, cfg.PprofAddr)

	poolSize, err := config.ParseDBMaxConns(cfg.DBMaxConns)
	if err != nil {
		log.Fatal(err)
	}
	if poolSize == 0 {
		poolSize = db.DefaultMaxConns(runtime.NumCPU())
	}
	pool, err := db.ConnectWith(appCtx, cfg.DatabaseURL, db.Options{MaxConns: poolSize})
	if err != nil {
		log.Fatalf("could not connect to database: %v", err)
	}
	defer pool.Close()
	// Every instance holds its own pool: (instances x this) + the job processes must stay under the pooler's limit.
	log.Printf("database pool: at most %d connections from this instance (set DB_MAX_CONNS; instances x this must stay under the pooler's limit)", poolSize)

	verifier, err := auth.NewVerifier(appCtx, cfg.SupabaseURL)
	if err != nil {
		log.Fatalf("could not load Supabase signing keys: %v", err)
	}

	// Redis is a propagation optimization, not a correctness dependency —
	// unlike the Supabase JWKS check above, a connection failure here is a
	// warning, not log.Fatal. /evaluate reads Postgres directly regardless
	// of whether this succeeds; only /sdk/stream and mutation fan-out
	// degrade (fan-out silently no-ops; /sdk/stream returns 503).
	var publisher events.Publisher = events.NoopPublisher{}
	var subscriber events.Subscriber = events.NoopSubscriber{}
	var redisPing func(context.Context) error // nil: Redis is not in use, /readyz reports "not_configured"
	if cfg.RedisURL == "" {
		log.Printf("WARNING: REDIS_URL not set; flag-change propagation disabled (evaluation is unaffected)")
	} else if redisClient, err := redisx.Connect(appCtx, cfg.RedisURL); err != nil {
		log.Printf("WARNING: could not connect to Redis, flag-change propagation disabled: %v", err)
	} else {
		log.Printf("connected to Redis")
		publisher = redisx.Publisher{Client: redisClient}
		subscriber = redisx.Subscriber{Client: redisClient}
		redisPing = func(ctx context.Context) error { return redisClient.Ping(ctx).Err() }
		defer redisClient.Close()
	}

	mode, ok := auth.ParseEmailMode(cfg.EmailConfirmation)
	if !ok {
		log.Fatalf("EMAIL_CONFIRMATION must be off, enforce or strict, got %q", cfg.EmailConfirmation)
	}
	// With the public anon key the backend can ask Supabase itself whether an
	// address is confirmed, so it can insist on a positive answer.
	if mode == auth.EmailEnforce && cfg.SupabaseAnonKey != "" && cfg.EmailConfirmation == "" {
		mode = auth.EmailStrict
	}
	emailPolicy := auth.NewEmailPolicy(mode, cfg.SupabaseURL, cfg.SupabaseAnonKey, nil)
	switch {
	case mode == auth.EmailOff:
		log.Printf("WARNING: EMAIL_CONFIRMATION=off: users with unconfirmed emails can create workspaces and accept invites")
	case !emailPolicy.HasLookup():
		log.Printf("email confirmation: %s, reading only the token's own claims (set SUPABASE_ANON_KEY, which is public, to also ask Supabase)", mode)
	default:
		log.Printf("email confirmation: %s, asking Supabase's user endpoint", mode)
	}

	limits, err := ratelimit.ParseConfig(cfg.RateLimitDisabled, cfg.RateLimits, cfg.TrustedProxyHops, cfg.StreamsPerKey)
	if err != nil {
		log.Fatalf("rate limit settings: %v", err)
	}
	switch {
	case limits.Disabled:
		log.Printf("WARNING: RATE_LIMIT_DISABLED=true: no rate limits")
	case limits.TrustedProxyHops == 0:
		log.Printf("rate limits on, keyed by the socket address (set TRUSTED_PROXY_HOPS=1 behind one reverse proxy such as Render, or every client shares one IP bucket)")
	default:
		log.Printf("rate limits on, client IP from X-Forwarded-For (%d trusted proxy hop(s))", limits.TrustedProxyHops)
	}

	exposureMode, err := exposuretoken.ParseMode(cfg.ExposureVerification)
	if err != nil {
		log.Fatal(err)
	}
	exposureMaxAge := exposuretoken.DefaultMaxAge
	if v := strings.TrimSpace(cfg.ExposureTokenMaxAge); v != "" {
		if exposureMaxAge, err = time.ParseDuration(v); err != nil || exposureMaxAge < time.Minute {
			log.Fatalf("EXPOSURE_TOKEN_MAX_AGE must be a duration of at least 1m, such as 24h")
		}
	}
	var keyring *exposuretoken.Keyring
	if cfg.ExposureTokenKey != "" {
		if keyring, err = exposuretoken.NewKeyring(cfg.ExposureTokenKey, cfg.ExposureTokenKeyPrevious, exposureMaxAge); err != nil {
			log.Fatal(err) // the message never quotes the secret
		}
	} else {
		if exposureMode == exposuretoken.ModeRequired {
			log.Fatalf("EXPOSURE_VERIFICATION=required needs EXPOSURE_TOKEN_KEY: a key generated at startup would differ on every instance and restart")
		}
		if keyring, err = exposuretoken.DevKeyring(exposureMaxAge); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("exposure verification: %s (tokens valid %s)", exposureMode, exposureMaxAge)
	if exposureMode == exposuretoken.ModeReport && keyring.Dev {
		log.Printf("WARNING: EXPOSURE_VERIFICATION=report with a development key: with more than one instance most tokens will fail to verify")
	}

	var schedulerCfg scheduling.Config
	if v := strings.TrimSpace(cfg.SchedulerGrace); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute || d > 24*time.Hour {
			log.Fatalf("SCHEDULER_GRACE must be a duration between 1m and 24h, got %q", v)
		}
		schedulerCfg.Grace = d
	}
	// The secret is never printed: only whether the endpoint is on.
	switch {
	case cfg.SchedulerSecret == "":
		log.Printf("scheduler endpoint: disabled (SCHEDULER_SECRET is not set); run changes with cmd/scheduler")
	case len(cfg.SchedulerSecret) < scheduling.MinSharedLen:
		log.Printf("scheduler endpoint: DISABLED, SCHEDULER_SECRET is shorter than %d characters", scheduling.MinSharedLen)
	default:
		log.Printf("scheduler endpoint: enabled")
	}

	delOpts, err := delivery.FromConfig(cfg)
	if err != nil {
		log.Fatalf("webhooks / email settings: %v", err) // never quotes a key or password
	}
	// Only whether things are on: never a key, password or address.
	if delOpts.Box.Available() {
		log.Printf("webhooks: available (secrets sealed with AES-256-GCM)")
	} else {
		log.Printf("webhooks: UNAVAILABLE, WEBHOOK_SECRET_KEY is not set (endpoints cannot be created; fail closed)")
	}
	if cfg.WebhookAllowInsecureLocal == "true" {
		log.Printf("WARNING: WEBHOOK_ALLOW_INSECURE_LOCAL=true: webhooks may use http and loopback addresses (development only)")
	}
	if delOpts.Mail.Mode() == "sink" {
		log.Printf("invite email: SINK, nothing is actually sent (set MAIL_MODE=smtp to send)")
	} else {
		log.Printf("invite email: smtp")
	}
	switch {
	case cfg.OutboxSecret == "":
		log.Printf("outbox endpoint: disabled (OUTBOX_SECRET is not set); deliver with cmd/outbox")
	case len(cfg.OutboxSecret) < outbox.MinSharedLen:
		log.Printf("outbox endpoint: DISABLED, OUTBOX_SECRET is shorter than %d characters", outbox.MinSharedLen)
	default:
		log.Printf("outbox endpoint: enabled")
	}

	cacheSettings, err := config.ParseConfigCache(cfg.ConfigCacheTTL, cfg.ConfigCacheMaxEntries, cfg.ConfigCacheMaxMB)
	if err != nil {
		log.Fatal(err)
	}
	if cacheSettings.TTL == 0 {
		log.Printf("flag configuration cache: OFF (every /evaluate reads the database)")
	} else {
		log.Printf("flag configuration cache: on, entries live at most %s, at most %d environments / %d MiB", cacheSettings.TTL, cacheSettings.MaxEntries, cacheSettings.MaxBytes>>20)
	}

	trackSettings, err := config.ParseTrack(cfg.TrackMaxWriters, cfg.TrackMaxWait)
	if err != nil {
		log.Fatal(err)
	}

	shutdown, err := config.ParseShutdown(cfg.ShutdownTimeout, cfg.ShutdownReadyDelay)
	if err != nil {
		log.Fatal(err)
	}
	life := lifecycle.New()

	router := server.New(server.Deps{
		Lifecycle:             life,
		RedisPing:             redisPing,
		ReadyRequireRedis:     strings.EqualFold(strings.TrimSpace(cfg.ReadyRequireRedis), "true"),
		TrackMaxWriters:       trackSettings.MaxWriters,
		TrackMaxWait:          trackSettings.MaxWait,
		ConfigCacheTTL:        cacheSettings.TTL,
		ConfigCacheMaxEntries: cacheSettings.MaxEntries,
		ConfigCacheMaxBytes:   cacheSettings.MaxBytes,
		Delivery:              delOpts,
		OutboxSecret:          cfg.OutboxSecret,
		PurgeSecret:           cfg.PurgeSecret,
		SchedulerSecret:       cfg.SchedulerSecret,
		Scheduler:             schedulerCfg,
		ExposureTokens:        keyring,
		ExposureMode:          exposureMode,
		Context:               appCtx,
		InvitesByID:           !strings.EqualFold(strings.TrimSpace(cfg.InvitesByID), "false"),
		Limiter:               ratelimit.New(limits),
		Email:                 emailPolicy,
		Pool:                  pool,
		Authn:                 verifier.Middleware,
		Publisher:             publisher,
		Subscriber:            subscriber,
		SDKKeyCacheTTL:        sdkKeyCacheTTL,
	})

	listenAddr, err := cfg.ListenAddress()
	if err != nil {
		log.Fatal(err)
	}
	// No WriteTimeout on purpose: /sdk/stream responses last as long as the connection does. Streams end on shutdown,
	// on revocation, or when the client or the platform's own request limit closes them (docs/DEPLOYMENT.md).
	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           cors.Middleware(cfg.CORSAllowedOrigins, router.Mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("cannot listen on %s: %v", listenAddr, err)
	}
	log.Printf("helios backend listening on %s (on SIGTERM: not ready at once, streams told to reconnect, requests drained, exit within %s)", listenAddr, shutdown.Timeout)

	// SIGTERM is what every platform sends first; SIGINT is a person at a terminal.
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	if err := graceful.Serve(sigCtx, srv, ln, graceful.Options{Life: life, Timeout: shutdown.Timeout, ReadyDelay: shutdown.ReadyDelay}); err != nil {
		log.Printf("shutdown: %v", err)
	}
	log.Printf("helios backend stopped")
}
