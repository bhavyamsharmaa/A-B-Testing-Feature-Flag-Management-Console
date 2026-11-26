// Package audit records an append-only log of every control-plane mutation,
// written in the same transaction as the change it describes (US-07 AC-1):
// if the change commits, its audit row commits with it, and vice versa.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	SeverityInfo     = "info"
	SeverityCritical = "critical"
)

// Entry is one audit_logs row. Before/After are marshaled to JSON; pass nil
// for "didn't exist" (creates) or "no longer exists" (deletes).
type Entry struct {
	ActorID       string
	ActorEmail    string
	EnvironmentID string // empty for changes not scoped to one environment
	Action        string // e.g. "flag.create", "flag.kill", "member.remove"
	ResourceType  string
	ResourceID    string
	Severity      string
	Before        any
	After         any
}

// Write inserts e using tx. Callers must use the same tx as the mutation.
func Write(ctx context.Context, tx pgx.Tx, e Entry) error {
	before, err := toJSON(e.Before)
	if err != nil {
		return err
	}
	after, err := toJSON(e.After)
	if err != nil {
		return err
	}
	severity := e.Severity
	if severity == "" {
		severity = SeverityInfo
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_logs
			(actor_id, actor_email, environment_id, action, resource_type, resource_id, severity, diff_before, diff_after)
		VALUES
			(NULLIF($1, '')::uuid, $2, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8::jsonb, $9::jsonb)`,
		e.ActorID, e.ActorEmail, e.EnvironmentID, e.Action, e.ResourceType, e.ResourceID, severity, before, after,
	)
	if err != nil {
		return fmt.Errorf("audit: write %s: %w", e.Action, err)
	}
	return nil
}

// toJSON returns a *string so a nil value becomes SQL NULL rather than the
// JSON literal "null". Strings, not []byte: under the simple query protocol
// a []byte parameter is sent as bytea and fails the ::jsonb cast.
func toJSON(v any) (*string, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("audit: marshal: %w", err)
	}
	s := string(b)
	return &s, nil
}
