package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func main() {
	ctx := context.Background()

	dbURL := mustEnv("DATABASE_URL")
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("postgres connect: %v", err)
	}
	defer pool.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mustEnv("REDIS_ADDR")})
	defer rdb.Close()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// TODO (Week 1): mount route groups per api/openapi.yaml
	//   r.Route("/api/v1", func(r chi.Router) {
	//       r.Post("/evaluate", handlers.Evaluate(engine))
	//       r.Get("/sdk/stream", handlers.SDKStream(pubsub))
	//       r.Post("/events", handlers.IngestEvents(pool))
	//       r.Route("/environments/{env}", func(r chi.Router) {
	//           r.Route("/flags", handlers.FlagRoutes(pool))
	//           r.Route("/segments", handlers.SegmentRoutes(pool))
	//           r.Route("/experiments", handlers.ExperimentRoutes(pool))
	//           r.Post("/flags/{key}/kill", handlers.Kill(pool, rdb))
	//       })
	//   })

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("helios api listening on :%s", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatal(err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("missing required env var %s", key)
	}
	return v
}
