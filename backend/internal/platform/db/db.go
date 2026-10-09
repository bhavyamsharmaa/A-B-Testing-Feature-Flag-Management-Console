// Package db owns the process-wide Postgres connection pool.
package db

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pooled connection to Postgres and verifies it's reachable
// before returning, so a bad DATABASE_URL fails fast at startup instead of
// on the first request.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, redact(fmt.Errorf("db: parse config: %w", err), databaseURL)
	}
	// Supabase's transaction pooler (port 6543) doesn't support the named
	// prepared statements pgx uses by default; queries fail there even though
	// Ping succeeds. The simple protocol works with every Supabase connection
	// mode. An explicit default_query_exec_mode in the URL still wins.
	if !strings.Contains(databaseURL, "default_query_exec_mode") {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	}

	pool, err := pgxpool.NewWithConfig(connectCtx, cfg)
	if err != nil {
		return nil, redact(fmt.Errorf("db: create pool: %w", err), databaseURL)
	}

	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, redact(fmt.Errorf("db: ping: %w", err), databaseURL)
	}

	return pool, nil
}

// redactedError keeps the wrapped error (errors.Is/As still work) but prints
// a message with the connection string's secrets removed: the startup code
// logs this error, and a driver's parse failure can quote the URL.
type redactedError struct {
	msg string
	err error
}

func (e redactedError) Error() string { return e.msg }
func (e redactedError) Unwrap() error { return e.err }

func redact(err error, databaseURL string) error {
	msg := err.Error()
	if u, perr := url.Parse(databaseURL); perr == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			msg = strings.ReplaceAll(msg, u.User.String(), "[redacted]")
			msg = strings.ReplaceAll(msg, pw, "[redacted]")
			if enc, ok := strings.CutPrefix(u.User.String(), u.User.Username()+":"); ok {
				msg = strings.ReplaceAll(msg, enc, "[redacted]")
			}
		}
	}
	msg = strings.ReplaceAll(msg, databaseURL, "[redacted url]")
	return redactedError{msg: msg, err: err}
}
