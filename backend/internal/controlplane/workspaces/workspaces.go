// Package workspaces is the tenant layer: a user's workspaces (a personal one
// is created on their first authenticated GET /me), workspace creation,
// renaming and switching, members and their roles, and invites.
//
// A workspace is the tenant boundary: it owns environments, flags,
// experiments, SDK keys and the audit log. Users belong to any number of
// workspaces with a role in each (owner, admin, editor, viewer). Provisioning
// is explicit, transactional code, not a database trigger.
package workspaces

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/httpx"
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

type Environment struct {
	ID           string
	Key          string
	Name         string
	IsProduction bool
}

// Workspace is a workspace as one user sees it: with their role in it.
type Workspace struct {
	ID           string
	Name         string
	Slug         string
	Role         rbac.WorkspaceRole
	Environments []Environment
}

// PendingInvite is an open invite addressed to the caller's email.
type PendingInvite struct {
	ID             string
	WorkspaceID    string
	WorkspaceName  string
	Role           rbac.WorkspaceRole
	InvitedByEmail string
	ExpiresAt      time.Time
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

// apiError is a failure with a specific HTTP answer.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e apiError) Error() string { return e.Message }

// writeError maps a service error to its HTTP response.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ae apiError
	var re roleError
	switch {
	case errors.As(err, &ae):
		httpx.WriteError(w, ae.Status, ae.Code, ae.Message)
	case errors.As(err, &re):
		httpx.WriteError(w, http.StatusForbidden, "FORBIDDEN", re.msg)
	default:
		if quota.WriteError(w, err) {
			return
		}
		httpx.WriteInternal(w, r, err)
	}
}

// load returns the user's workspaces, most recently used first (the first is
// the active one), each with its environments. A user in none gets an empty
// list.
func load(ctx context.Context, q querier, userID string) ([]Workspace, error) {
	rows, err := q.Query(ctx, `
		SELECT w.id::text, w.name, w.slug, m.role::text
		FROM workspace_members m JOIN workspaces w ON w.id = m.workspace_id
		WHERE m.user_id = $1::uuid
		ORDER BY m.last_active_at DESC NULLS LAST, w.created_at, w.id`, userID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Workspace, error) {
		var w Workspace
		err := row.Scan(&w.ID, &w.Name, &w.Slug, &w.Role)
		return w, err
	})
	if err != nil || len(list) == 0 {
		return []Workspace{}, err
	}
	envRows, err := q.Query(ctx, `
		SELECT e.workspace_id::text, e.id::text, e.key, e.name, e.is_production
		FROM environments e
		WHERE e.workspace_id IN (SELECT workspace_id FROM workspace_members WHERE user_id = $1::uuid)
		ORDER BY e.is_production, e.key`, userID)
	if err != nil {
		return nil, err
	}
	defer envRows.Close()
	byWorkspace := map[string][]Environment{}
	for envRows.Next() {
		var wsID string
		var e Environment
		if err := envRows.Scan(&wsID, &e.ID, &e.Key, &e.Name, &e.IsProduction); err != nil {
			return nil, err
		}
		byWorkspace[wsID] = append(byWorkspace[wsID], e)
	}
	if err := envRows.Err(); err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Environments = byWorkspace[list[i].ID]
		if list[i].Environments == nil {
			list[i].Environments = []Environment{}
		}
	}
	return list, nil
}

// pendingInvites lists the open invites addressed to email.
func pendingInvites(ctx context.Context, q querier, email string) ([]PendingInvite, error) {
	e, ok := normalizeEmail(email)
	if !ok {
		return []PendingInvite{}, nil
	}
	rows, err := q.Query(ctx, `
		SELECT i.id::text, i.workspace_id::text, w.name, i.role::text, COALESCE(u.email, ''), i.expires_at
		FROM workspace_invites i
		JOIN workspaces w ON w.id = i.workspace_id
		LEFT JOIN auth.users u ON u.id = i.invited_by
		WHERE i.email = $1 AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now()
		ORDER BY i.created_at DESC`, e)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PendingInvite, error) {
		var p PendingInvite
		err := row.Scan(&p.ID, &p.WorkspaceID, &p.WorkspaceName, &p.Role, &p.InvitedByEmail, &p.ExpiresAt)
		return p, err
	})
	if list == nil {
		list = []PendingInvite{}
	}
	return list, err
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

// withSlugRetry re-runs fn when the random slug suffix collided.
func withSlugRetry(fn func() error) error {
	var err error
	for range 4 {
		if err = fn(); !isUniqueViolation(err, "workspaces_slug_key") {
			return err
		}
	}
	return err
}

// lockUser serialises workspace creation for one user (two tabs, or a create
// racing the first-/me provisioning) for the rest of the transaction.
func lockUser(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, "helios:workspaces:"+userID)
	return err
}

// createWorkspaceTx inserts a workspace owned by user, with the default
// environments, the owner membership (marked as the active workspace) and an
// audit row, in the caller's transaction.
func createWorkspaceTx(ctx context.Context, tx pgx.Tx, user auth.User, name string) (string, error) {
	plan, err := provisionPlan(defaultEnvironments)
	if err != nil {
		return "", err
	}
	slug, err := newSlug(name)
	if err != nil {
		return "", err
	}
	var workspaceID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspaces (name, slug, owner_id) VALUES ($1, $2, $3::uuid) RETURNING id::text`,
		name, slug, user.ID,
	).Scan(&workspaceID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO workspace_members (user_id, workspace_id, role, last_active_at)
		VALUES ($1::uuid, $2::uuid, 'owner', now())`,
		user.ID, workspaceID,
	); err != nil {
		return "", err
	}
	created := make([]string, 0, len(plan))
	for _, spec := range plan {
		if _, err := tx.Exec(ctx, `
			INSERT INTO environments (workspace_id, key, name, is_production)
			VALUES ($1::uuid, $2, $3, $4)`,
			workspaceID, spec.Key, spec.Name, spec.IsProduction,
		); err != nil {
			return "", err
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
		After:        map[string]any{"name": name, "slug": slug, "environments": created},
	}); err != nil {
		return "", err
	}
	return workspaceID, nil
}

// Ensure returns the user's workspaces, creating their personal workspace
// the first time they have none.
//
// Concurrent first requests (two tabs) produce ONE workspace: the creating
// transaction first takes a per-user advisory lock and re-checks under it.
func (s *Service) Ensure(ctx context.Context, user auth.User) ([]Workspace, error) {
	if list, err := load(ctx, s.pool, user.ID); err != nil || len(list) > 0 {
		return list, err
	}
	if _, err := provisionPlan(defaultEnvironments); err != nil {
		return nil, err
	}
	err := withSlugRetry(func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if err := lockUser(ctx, tx, user.ID); err != nil {
				return err
			}
			existing, err := load(ctx, tx, user.ID)
			if err != nil || len(existing) > 0 {
				return err
			}
			_, err = createWorkspaceTx(ctx, tx, user, workspaceName(user.Email))
			return err
		})
	})
	if err != nil {
		return nil, err
	}
	return load(ctx, s.pool, user.ID)
}
