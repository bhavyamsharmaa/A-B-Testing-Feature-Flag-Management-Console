// Command mkkey mints an API key for an environment and prints it once.
// Only its Argon2id hash is stored, so the printed key can't be recovered.
//
//	DATABASE_URL=... go run ./cmd/mkkey -env production
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/config"
	"helios/backend/internal/platform/db"
)

func main() {
	envKey := flag.String("env", "", "environment key, e.g. dev, staging, production")
	kind := flag.String("kind", "sdk", "key kind: sdk or server")
	flag.Parse()
	if *envKey == "" {
		log.Fatal("-env is required")
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, config.Load().DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	env, err := rbac.LoadEnvironment(ctx, pool, *envKey)
	if err != nil {
		log.Fatalf("environment %q: %v", *envKey, err)
	}
	plaintext, prefix, hash, err := apikey.Generate(apikey.Kind(*kind))
	if err != nil {
		log.Fatal(err)
	}

	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO api_keys (environment_id, kind, key_prefix, key_hash)
			VALUES ($1::uuid, $2::api_key_kind, $3, $4)
			RETURNING id::text`,
			env.ID, *kind, prefix, hash,
		).Scan(&id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			ActorEmail:    "cli:mkkey",
			EnvironmentID: env.ID,
			Action:        "api_key.create",
			ResourceType:  "api_key",
			ResourceID:    id,
			After:         map[string]string{"kind": *kind, "prefix": prefix, "key": "[REDACTED]"},
		})
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("%s key for %s (shown once, store it now):\n%s\n", *kind, env.Key, plaintext)
}
