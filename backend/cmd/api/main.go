// Command api is the entrypoint for the Helios backend HTTP server.
//
// It reads configuration from the environment, connects to Postgres, and
// mounts the control-plane (Supabase JWT + per-environment RBAC) and
// data-plane (SDK key) routes.
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

	"helios/backend/internal/controlplane/flags"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/dataplane/evaluation"
	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/config"
	"helios/backend/internal/platform/cors"
	"helios/backend/internal/platform/db"
	"helios/backend/internal/platform/health"
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

	authn := verifier.Middleware
	guard := rbac.NewGuard(pool)
	members := rbac.NewMemberHandlers(pool)
	fl := flags.NewHandlers(pool)
	sdkKeys := apikey.NewVerifier(pool, sdkKeyCacheTTL)

	// protected wraps an /environments/{env}/... handler: verify the JWT,
	// then check the caller's role in {env} against req.
	protected := func(req rbac.Requirement, h http.HandlerFunc) http.Handler {
		return authn(guard.Require(req, h))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health.Handler(pool))

	mux.Handle("GET /me", authn(http.HandlerFunc(members.Me)))
	mux.Handle("POST /environments/{env}/members", protected(rbac.Min(rbac.Admin), members.AddMember))
	mux.Handle("DELETE /environments/{env}/members/{userId}", protected(rbac.Min(rbac.Admin), members.RemoveMember))

	mux.Handle("GET /environments/{env}/flags", protected(rbac.Min(rbac.Viewer), fl.List))
	mux.Handle("GET /environments/{env}/flags/{key}", protected(rbac.Min(rbac.Viewer), fl.Get))
	mux.Handle("POST /environments/{env}/flags", protected(rbac.Min(rbac.Editor), fl.Create))
	mux.Handle("PATCH /environments/{env}/flags/{key}", protected(rbac.FlagWrite, fl.Update))
	mux.Handle("DELETE /environments/{env}/flags/{key}", protected(rbac.Min(rbac.Admin), fl.Delete))
	// Editor+ in every environment, production included: an unnecessary
	// kill costs a disabled feature, a blocked one during an incident costs
	// prolonged user harm (PRD US-06).
	mux.Handle("POST /environments/{env}/flags/{key}/kill", protected(rbac.Min(rbac.Editor), fl.Kill))

	mux.Handle("POST /evaluate", sdkKeys.Middleware(evaluation.Handler(pool)))

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
