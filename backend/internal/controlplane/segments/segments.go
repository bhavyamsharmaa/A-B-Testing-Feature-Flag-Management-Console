// Package segments implements control-plane CRUD for reusable audience
// segments: a named set of subjects (explicit member keys, plus optional
// attribute rules) that flag targeting rules reference with inSegment and
// notInSegment.
//
// A subject is in a segment if it is an explicit member OR it matches the
// segment's rules; the rules are ANDed, like the clauses of a flag rule
// (docs/SEGMENTS.md). A segment cannot reference another segment, so no cycle
// can exist.
//
// Every mutation writes its audit row in the same transaction, and a change
// that alters who is in a segment bumps the version of every flag
// configuration that uses it and tells the SDK streams.
//
// Lock order (see also quota and flags): create takes the workspace row (the
// segments quota), then inserts. update and delete take the segment row FOR
// UPDATE, then the flag_configs rows that use it, by id. A flag update that
// references segments takes those segment rows FOR SHARE, by key, before its
// own flag_configs row. Segments first, flag configurations second, everywhere,
// so there is no cycle.
package segments

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/audit"
	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/dataplane/evaluation"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/httpx"
)

type Handlers struct {
	pool   *pgxpool.Pool
	events events.Publisher
}

func NewHandlers(pool *pgxpool.Pool, publisher events.Publisher) *Handlers {
	return &Handlers{pool: pool, events: publisher}
}

// segmentEvent is published to the flag channel of every environment whose
// flags use the segment, after the change committed. Like the flag event it is
// a cue to re-pull /evaluate, not the state, and its shape is not part of the
// public API contract.
type segmentEvent struct {
	Action     string   `json:"action"` // "segment_updated" or "segment_deleted"
	SegmentKey string   `json:"segmentKey"`
	Version    int64    `json:"version"`
	FlagKeys   []string `json:"flagKeys"`
}

// segmentView is a segment as the API returns it. Members is present on a
// single-segment read and absent from the list.
type segmentView struct {
	Key         string              `json:"key"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Rules       []evaluation.Clause `json:"rules"`
	MemberCount int                 `json:"memberCount"`
	Members     *[]string           `json:"members,omitempty"`
	Version     int64               `json:"version"`
	CreatedAt   time.Time           `json:"createdAt"`
	UpdatedAt   time.Time           `json:"updatedAt"`
}

type segmentRow struct {
	ID        string
	View      segmentView
	rulesText string
}

const selectSegment = `
	SELECT s.id::text, s.key, s.name, s.description, s.rules::text,
	       (SELECT count(*) FROM segment_members m WHERE m.workspace_id = s.workspace_id AND m.segment_id = s.id),
	       s.version, s.created_at, s.updated_at
	FROM segments s`

type scanner interface{ Scan(dest ...any) error }

func scanSegment(row scanner) (segmentRow, error) {
	var r segmentRow
	v := &r.View
	if err := row.Scan(&r.ID, &v.Key, &v.Name, &v.Description, &r.rulesText, &v.MemberCount, &v.Version, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return r, err
	}
	v.Rules = []evaluation.Clause{}
	if err := json.Unmarshal([]byte(r.rulesText), &v.Rules); err != nil {
		return r, err
	}
	return r, nil
}

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func loadMembers(ctx context.Context, q queryer, workspaceID, segmentID string) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT subject_key FROM segment_members
		WHERE workspace_id = $1::uuid AND segment_id = $2::uuid ORDER BY subject_key`, workspaceID, segmentID)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if keys == nil {
		keys = []string{}
	}
	return keys, err
}

// summary is what the audit log records of a segment: everything except the
// member list, which can hold thousands of keys. Changes to the members are
// recorded as counts and a short sample (see memberDiff).
type summary struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Rules       []evaluation.Clause `json:"rules"`
	MemberCount int                 `json:"memberCount"`
	Version     int64               `json:"version"`
	MemberDiff  *memberDiff         `json:"memberChanges,omitempty"`
	FlagsUsing  *int                `json:"flagConfigsUsing,omitempty"`
}

const auditSampleSize = 10

// memberDiff records how many members were added and removed and the first few
// of each, never the whole list.
type memberDiff struct {
	Added         int      `json:"added"`
	Removed       int      `json:"removed"`
	SampleAdded   []string `json:"sampleAdded,omitempty"`
	SampleRemoved []string `json:"sampleRemoved,omitempty"`
}

func sample(keys []string) []string {
	if len(keys) > auditSampleSize {
		return keys[:auditSampleSize]
	}
	return keys
}

func summaryOf(v segmentView) summary {
	return summary{Name: v.Name, Description: v.Description, Rules: v.Rules, MemberCount: v.MemberCount, Version: v.Version}
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func writeNotFound(w http.ResponseWriter, key string) {
	httpx.WriteError(w, http.StatusNotFound, "SEGMENT_NOT_FOUND", "no segment with key "+key)
}

// writeFailure maps the errors the handlers share; it reports whether it wrote.
func writeFailure(w http.ResponseWriter, r *http.Request, err error) {
	var bad validationError
	switch {
	case errors.As(err, &bad):
		httpx.BadRequest(w, bad.msg)
	case quota.WriteError(w, err):
	case errors.Is(err, pgx.ErrNoRows):
		writeNotFound(w, r.PathValue("segKey"))
	default:
		httpx.WriteInternal(w, r, err)
	}
}

// List handles GET /workspaces/{wsId}/segments.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	ws := rbac.WorkspaceAccessFrom(r.Context())
	rows, err := h.pool.Query(r.Context(), selectSegment+` WHERE s.workspace_id = $1::uuid ORDER BY s.key`, ws.WorkspaceID)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	defer rows.Close()
	out := []segmentView{}
	for rows.Next() {
		row, err := scanSegment(rows)
		if err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
		out = append(out, row.View)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"segments": out})
}

// Get handles GET /workspaces/{wsId}/segments/{segKey}, members included.
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	ws := rbac.WorkspaceAccessFrom(r.Context())
	key := r.PathValue("segKey")
	row, err := scanSegment(h.pool.QueryRow(r.Context(), selectSegment+` WHERE s.workspace_id = $1::uuid AND s.key = $2`, ws.WorkspaceID, key))
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	members, err := loadMembers(r.Context(), h.pool, ws.WorkspaceID, row.ID)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	row.View.Members = &members
	httpx.WriteJSON(w, http.StatusOK, row.View)
}

type createRequest struct {
	Key         string              `json:"key"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Rules       []evaluation.Clause `json:"rules"`
	Members     []string            `json:"members"`
}

// Create handles POST /workspaces/{wsId}/segments.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := rbac.WorkspaceAccessFrom(ctx)
	var req createRequest
	if err := httpx.DecodeJSONLimit(w, r, &req, maxBodyBytes); err != nil {
		if httpx.TooLarge(err) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "request body is too large")
			return
		}
		httpx.BadRequest(w, err.Error())
		return
	}
	if err := firstError(validateKey(req.Key), validateName(req.Name), validateDescription(req.Description)); err != nil {
		writeFailure(w, r, err)
		return
	}
	if req.Rules == nil {
		req.Rules = []evaluation.Clause{}
	}
	if err := validateRules(req.Rules); err != nil {
		writeFailure(w, r, err)
		return
	}
	members, err := cleanMembers(req.Members)
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	rules, _ := json.Marshal(req.Rules)

	var view segmentView
	err = pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		if err := quota.CheckSegment(ctx, tx, ws.WorkspaceID); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO segments (workspace_id, key, name, description, rules, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5::jsonb, $6::uuid)
			RETURNING id::text`,
			ws.WorkspaceID, req.Key, req.Name, req.Description, string(rules), ws.User.ID).Scan(&id); err != nil {
			return err
		}
		if err := insertMembers(ctx, tx, ws.WorkspaceID, id, members); err != nil {
			return err
		}
		row, err := scanSegment(tx.QueryRow(ctx, selectSegment+` WHERE s.id = $1::uuid AND s.workspace_id = $2::uuid`, id, ws.WorkspaceID))
		if err != nil {
			return err
		}
		view = row.View
		after := summaryOf(view)
		after.MemberDiff = &memberDiff{Added: len(members), SampleAdded: sample(members)}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: ws.WorkspaceID, ActorID: ws.User.ID, ActorEmail: ws.User.Email,
			Action: "segment.create", ResourceType: "segment", ResourceID: req.Key, After: after,
		})
	})
	if isUniqueViolation(err, "segments_key_unique") {
		httpx.WriteError(w, http.StatusConflict, "SEGMENT_KEY_EXISTS", "a segment with key "+req.Key+" already exists")
		return
	}
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, view)
}

func firstError(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func insertMembers(ctx context.Context, tx pgx.Tx, workspaceID, segmentID string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO segment_members (workspace_id, segment_id, subject_key)
		SELECT $1::uuid, $2::uuid, k FROM unnest($3::text[]) AS k`, workspaceID, segmentID, keys)
	return err
}

type updateRequest struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Rules       json.RawMessage `json:"rules"` // absent: unchanged; null or []: no rules
	// Members, when present, REPLACES the whole explicit member list.
	Members *[]string `json:"members"`
	// ExpectedVersion, when present, must equal the segment's current version,
	// else the update is refused with 409 STALE_SEGMENT (someone else saved first).
	ExpectedVersion *int64 `json:"expectedVersion"`
}

func (u updateRequest) empty() bool {
	return u.Name == nil && u.Description == nil && u.Rules == nil && u.Members == nil
}

type affected struct {
	EnvironmentID string
	FlagKey       string
}

// Update handles PATCH /workspaces/{wsId}/segments/{segKey}.
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := rbac.WorkspaceAccessFrom(ctx)
	key := r.PathValue("segKey")
	var req updateRequest
	if err := httpx.DecodeJSONLimit(w, r, &req, maxBodyBytes); err != nil {
		if httpx.TooLarge(err) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "request body is too large")
			return
		}
		httpx.BadRequest(w, err.Error())
		return
	}
	if req.empty() {
		httpx.BadRequest(w, "nothing to update: send name, description, rules or members")
		return
	}
	if req.Name != nil {
		if err := validateName(*req.Name); err != nil {
			writeFailure(w, r, err)
			return
		}
	}
	if req.Description != nil {
		if err := validateDescription(*req.Description); err != nil {
			writeFailure(w, r, err)
			return
		}
	}
	var newRules []evaluation.Clause
	if req.Rules != nil {
		newRules = []evaluation.Clause{}
		if string(req.Rules) != "null" {
			dec := json.NewDecoder(strings.NewReader(string(req.Rules)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&newRules); err != nil {
				httpx.BadRequest(w, "rules: "+err.Error())
				return
			}
		}
		if newRules == nil {
			newRules = []evaluation.Clause{}
		}
		if err := validateRules(newRules); err != nil {
			writeFailure(w, r, err)
			return
		}
	}
	var newMembers []string
	if req.Members != nil {
		var err error
		if newMembers, err = cleanMembers(*req.Members); err != nil {
			writeFailure(w, r, err)
			return
		}
	}

	var view segmentView
	var changedEval bool
	var hit []affected
	var noop bool
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		before, err := scanSegment(tx.QueryRow(ctx, selectSegment+` WHERE s.workspace_id = $1::uuid AND s.key = $2 FOR UPDATE OF s`, ws.WorkspaceID, key))
		if err != nil {
			return err
		}
		if req.ExpectedVersion != nil && *req.ExpectedVersion != before.View.Version {
			return errStale{current: before.View.Version}
		}

		name, description := before.View.Name, before.View.Description
		if req.Name != nil {
			name = *req.Name
		}
		if req.Description != nil {
			description = *req.Description
		}
		rulesText := before.rulesText
		rulesChanged := false
		if newRules != nil {
			b, _ := json.Marshal(newRules)
			// Compare as the database would store it (jsonb normalises), by
			// round-tripping the candidate through the same decoder.
			rulesChanged = canonical(string(b)) != canonical(before.rulesText)
			rulesText = string(b)
		}

		var diff *memberDiff
		if newMembers != nil {
			existing, err := loadMembers(ctx, tx, ws.WorkspaceID, before.ID)
			if err != nil {
				return err
			}
			added, removed := diffKeys(existing, newMembers)
			if len(added)+len(removed) > 0 {
				if err := quota.CheckSegmentMembers(len(existing) + len(added) - len(removed)); err != nil {
					return err
				}
				if len(removed) > 0 {
					if _, err := tx.Exec(ctx, `
						DELETE FROM segment_members
						WHERE workspace_id = $1::uuid AND segment_id = $2::uuid AND subject_key = ANY($3::text[])`,
						ws.WorkspaceID, before.ID, removed); err != nil {
						return err
					}
				}
				if err := insertMembers(ctx, tx, ws.WorkspaceID, before.ID, added); err != nil {
					return err
				}
				diff = &memberDiff{Added: len(added), Removed: len(removed), SampleAdded: sample(added), SampleRemoved: sample(removed)}
			}
		}

		changedEval = rulesChanged || diff != nil
		if !changedEval && name == before.View.Name && description == before.View.Description {
			noop = true
			view = before.View
			if newMembers != nil {
				view.Members = &newMembers
			}
			return nil
		}

		if _, err := tx.Exec(ctx, `
			UPDATE segments SET name = $1, description = $2, rules = $3::jsonb, version = version + 1, updated_at = now()
			WHERE id = $4::uuid AND workspace_id = $5::uuid`,
			name, description, rulesText, before.ID, ws.WorkspaceID); err != nil {
			return err
		}
		if changedEval {
			if hit, err = bumpFlagConfigs(ctx, tx, ws.WorkspaceID, key); err != nil {
				return err
			}
		}
		after, err := scanSegment(tx.QueryRow(ctx, selectSegment+` WHERE s.id = $1::uuid AND s.workspace_id = $2::uuid`, before.ID, ws.WorkspaceID))
		if err != nil {
			return err
		}
		view = after.View
		afterSummary := summaryOf(view)
		afterSummary.MemberDiff = diff
		n := len(hit)
		if changedEval {
			afterSummary.FlagsUsing = &n
		}
		return audit.Write(ctx, tx, audit.Entry{
			WorkspaceID: ws.WorkspaceID, ActorID: ws.User.ID, ActorEmail: ws.User.Email,
			Action: "segment.update", ResourceType: "segment", ResourceID: key,
			Before: summaryOf(before.View), After: afterSummary,
		})
	})
	var stale errStale
	if errors.As(err, &stale) {
		httpx.WriteError(w, http.StatusConflict, "STALE_SEGMENT",
			"the segment was changed by someone else (now at version "+itoa(stale.current)+"); reload it and try again")
		return
	}
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	if !noop && changedEval {
		h.publish(ctx, ws.WorkspaceID, "segment_updated", key, view.Version, hit)
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

type errStale struct{ current int64 }

func (e errStale) Error() string { return "stale segment" }

// canonical re-encodes JSON text so key order and spacing do not matter.
func canonical(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// diffKeys returns what must be added to and removed from existing to become
// want; both sorted, so the audit sample is stable.
func diffKeys(existing, want []string) (added, removed []string) {
	have := make(map[string]struct{}, len(existing))
	for _, k := range existing {
		have[k] = struct{}{}
	}
	wanted := make(map[string]struct{}, len(want))
	for _, k := range want {
		wanted[k] = struct{}{}
		if _, ok := have[k]; !ok {
			added = append(added, k)
		}
	}
	for _, k := range existing {
		if _, ok := wanted[k]; !ok {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// publish tells every environment whose flags use the segment. Best-effort and
// after commit, like the flag events: a Redis hiccup never fails the change.
func (h *Handlers) publish(ctx context.Context, workspaceID, action, segKey string, version int64, hit []affected) {
	byEnv := map[string][]string{}
	for _, a := range hit {
		byEnv[a.EnvironmentID] = append(byEnv[a.EnvironmentID], a.FlagKey)
	}
	for envID, flagKeys := range byEnv {
		payload, err := json.Marshal(segmentEvent{Action: action, SegmentKey: segKey, Version: version, FlagKeys: flagKeys})
		if err != nil {
			log.Printf("segments: marshal event for %s: %v", segKey, err)
			continue
		}
		if err := h.events.Publish(ctx, events.FlagsChannel(workspaceID, envID), payload); err != nil {
			log.Printf("segments: publish event for %s: %v", segKey, err)
		}
	}
}
