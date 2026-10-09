// Command api is the entrypoint for the Helios backend HTTP server.
//
// It reads configuration from the environment, connects to Postgres, and
// mounts the control-plane (Supabase JWT + workspace membership and roles) and
// data-plane (SDK key) routes; see internal/server.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/config"
	"helios/backend/internal/platform/cors"
	"helios/backend/internal/platform/db"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/redisx"
	"helios/backend/internal/server"
)

// sdkKeyCacheTTL bounds how long a revoked SDK key keeps working.
const sdkKeyCacheTTL = time.Minute

func main() {
	cfg := config.Load()
	if cfg.SupabaseURL == "" {
		log.Fatal("SUPABASE_URL is required, e.g. https://<project-ref>.supabase.co")
	}

	appCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()

	pool, err := db.Connect(appCtx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("could not connect to database: %v", err)
	}
	defer pool.Close()

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
	if cfg.RedisURL == "" {
		log.Printf("WARNING: REDIS_URL not set; flag-change propagation disabled (evaluation is unaffected)")
	} else if redisClient, err := redisx.Connect(appCtx, cfg.RedisURL); err != nil {
		log.Printf("WARNING: could not connect to Redis, flag-change propagation disabled: %v", err)
	} else {
		log.Printf("connected to Redis")
		publisher = redisx.Publisher{Client: redisClient}
		subscriber = redisx.Subscriber{Client: redisClient}
		defer redisClient.Close()
	}

	mux := server.New(server.Deps{
		Pool:           pool,
		Authn:          verifier.Middleware,
		Publisher:      publisher,
		Subscriber:     subscriber,
		SDKKeyCacheTTL: sdkKeyCacheTTL,
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           cors.Middleware(cfg.CORSAllowedOrigins, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("helios backend listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
