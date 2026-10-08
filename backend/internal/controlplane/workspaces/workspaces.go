// Package workspaces provisions a private workspace for each user on their
// first authenticated GET /me, and serves /me.
//
// A workspace is the tenant boundary: it owns environments, flags,
// experiments and the audit log, and a user belongs to exactly one (the
// primary key on workspace_members.user_id). Provisioning is explicit,
// transactional code, not a database trigger.
package workspaces

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/auth"
)

type environmentSpec struct {
	Key          string
	Name         string
	IsProduction bool
}

// defaultEnvironments is what every new workspace starts with.
var defaultEnvironments = []environmentSpec{
	{"dev", "Development", false},
	{"staging", "Staging", false},
	{"production", "Production", true},
}

// provisionPlan validates defaultEnvironments against the quota and the
// environment-key rules. Pure, so it is tested without a database.
func provisionPlan(specs []environmentSpec) ([]environmentSpec, error) {
	if err := quota.CheckEnvironments(len(specs)); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, s := range specs {
		if !rbac.ValidEnvironmentKey(s.Key) {
			return nil, fmt.Errorf("workspaces: invalid environment key %q", s.Key)
		}
		if seen[s.Key] {
			return nil, fmt.Errorf("workspaces: duplicate environment key %q", s.Key)
		}
		seen[s.Key] = true
	}
	return specs, nil
}

// workspaceName derives a display name from the user's email.
func workspaceName(email string) string {
	local, _, _ := strings.Cut(strings.TrimSpace(email), "@")
	local = strings.TrimSpace(local)
	if local == "" {
		return "My workspace"
	}
	if r := []rune(local); len(r) > 40 {
		local = string(r[:40])
	}
	return local + "'s workspace"
}

type Workspace struct {
	ID           string
	Name         string
	Environments []Environment
}

type Environment struct {
	ID           string
	Key          string
	Name         string
	IsProduction bool
	Role         rbac.Role
}

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// load returns the user's workspace with their role in each environment, or
// found=false when they have none yet.
func load(ctx context.Context, q querier, userID string) (ws Workspace, found bool, err error) {
	err = q.QueryRow(ctx, `
		SELECT w.id::text, w.name
		FROM workspace_members m JOIN workspaces w ON w.id = m.workspace_id
		WHERE m.user_id = $1::uuid`, userID).Scan(&ws.ID, &ws.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, false, nil
	}
	if err != nil {
		return Workspace{}, false, err
	}
	rows, err := q.Query(ctx, `
		SELECT e.id::text, e.key, e.name, e.is_production, r.role::text
		FROM environments e
		JOIN user_environment_roles r ON r.environment_id = e.id AND r.workspace_id = e.workspace_id
		WHERE e.workspace_id = $2::uuid AND r.user_id = $1::uuid
		ORDER BY e.is_production, e.key`, userID, ws.ID)
	if err != nil {
		return Workspace{}, false, err
	}
	ws.Environments, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Environment, error) {
		var e Environment
		err := row.Scan(&e.ID, &e.Key, &e.Name, &e.IsProduction, &e.Role)
		return e, err
	})
	if ws.Environments == nil {
		ws.Environments = []Environment{}
	}
	return ws, true, err
}

// Ensure returns the user's workspace, creating it on first use.
//
// Concurrent first requests (two tabs) produce ONE workspace: each creating
// transaction first takes a per-user advisory lock, re-checks for a
// workspace under it, and only then inserts. The primary key on
// workspace_members.user_id is the backstop; if it still fires (an admin
// added the user to another workspace in the same instant), the loser
// reloads instead of failing.
func (s *Service) Ensure(ctx context.Context, user auth.User) (Workspace, error) {
	if ws, found, err := load(ctx, s.pool, user.ID); err != nil || found {
		return ws, err
	}
	plan, err := provisionPlan(defaultEnvironments)
	if err != nil {
		return Workspace{}, err
	}

	var ws Workspace
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, "helios:provision:"+user.ID); err != nil {
			return err
		}
		if existing, found, err := load(ctx, tx, user.ID); err != nil {
			return err
		} else if found {
			ws = existing
			return nil
		}

		var workspaceID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO workspaces (name, owner_id) VALUES ($1, $2::uuid) RETURNING id::text`,
			workspaceName(user.Email), user.ID,
		).Scan(&workspaceID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO workspace_members (user_id, workspace_id) VALUES ($1::uuid, $2::uuid)`,
			user.ID, workspaceID,
		); err != nil {
			return err
		}
		created := make([]string, 0, len(plan))
		for _, spec := range plan {
			var envID string
			if err := tx.QueryRow(ctx, `
				INSERT INTO environments (workspace_id, key, name, is_production)
				VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`,
				workspaceID, spec.Key, spec.Name, spec.IsProduction,
			).Scan(&envID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO user_environment_roles (user_id, environment_id, workspace_id, role)
				VALUES ($1::uuid, $2::uuid, $3::uuid, 'admin')`,
				user.ID, envID, workspaceID,
			); err != nil {
				return err
			}
			created = append(created, spec.Key)
		}
		if err := audit.Write(ctx, tx, audit.Entry{
			WorkspaceID:  workspaceID,
			ActorID:      user.ID,
			ActorEmail:   user.Email,
			Action:       "workspace.create",
			ResourceType: "workspace",
			ResourceID:   workspaceID,
			After:        map[string]any{"name": workspaceName(user.Email), "environments": created},
		}); err != nil {
			return err
		}
		var found bool
		ws, found, err = load(ctx, tx, user.ID)
		if err == nil && !found {
			err = errors.New("workspaces: workspace missing right after creation")
		}
		return err
	})
	if isUniqueViolation(err, "workspace_members_pkey") {
		// Lost a race with an invitation: use the workspace that won.
		existing, found, loadErr := load(ctx, s.pool, user.ID)
		if loadErr != nil || !found {
			return Workspace{}, errors.Join(err, loadErr)
		}
		return existing, nil
	}
	return ws, err
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
