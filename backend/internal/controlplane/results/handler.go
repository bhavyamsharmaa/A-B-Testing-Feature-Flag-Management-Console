package results

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/httpx"
)

// cacheTTL: a dashboard polling every few seconds shares one computation.
const cacheTTL = 5 * time.Second

var eventNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type cacheEntry struct {
	at   time.Time
	resp Response
}

type Handlers struct {
	pool *pgxpool.Pool
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

func NewHandlers(pool *pgxpool.Pool) *Handlers {
	return &Handlers{pool: pool, now: time.Now, cache: map[string]cacheEntry{}}
}

// Get handles GET /environments/{envId}/experiments/{key}/results.
//
// Query: metrics=<eventName>:<kind>[,...] (kind: conversion, count, value; at
// most MaxMetrics). Without it the experiment's primary metric is used. The
// workspace and environment come from the route guard, never from the query.
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	access := rbac.AccessFrom(r.Context())
	ws, envID, key := access.Env.WorkspaceID, access.Env.ID, r.PathValue("key")

	specs, err := parseMetrics(r.URL.Query()["metrics"])
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '`+queryTimeout+`'`); err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}

	x, err := loadExperiment(ctx, tx, ws, envID, key)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "EXPERIMENT_NOT_FOUND", "experiment "+key+" not found")
		return
	}
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	if x.StartedAt == nil {
		httpx.WriteError(w, http.StatusConflict, "EXPERIMENT_NOT_STARTED", "a draft experiment has no run window and no data yet")
		return
	}

	chosen, err := h.resolveMetrics(ctx, tx, ws, x, specs)
	if err != nil {
		var bad badRequest
		if errors.As(err, &bad) {
			httpx.WriteError(w, http.StatusBadRequest, "INVALID_REQUEST", bad.Error())
			return
		}
		h.fail(w, r, err)
		return
	}

	// The cache is consulted only after the guard and the lookup above have
	// tied the experiment to the caller's own workspace and environment.
	cacheKey := x.ID + "|" + metricsKey(chosen)
	if resp, ok := h.cached(cacheKey); ok {
		httpx.WriteJSON(w, http.StatusOK, resp)
		return
	}

	now := h.now()
	end := now
	if x.StoppedAt != nil {
		end = *x.StoppedAt
	}
	in := Inputs{Experiment: x, Now: now}
	if in.Exposures, err = exposureCounts(ctx, tx, ws, x.ID); err != nil {
		h.fail(w, r, err)
		return
	}
	for _, spec := range chosen {
		sums, err := metricSums(ctx, tx, ws, x.ID, spec.EventName, *x.StartedAt, end)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		dropped, err := droppedEvents(ctx, tx, ws, x.ID, spec.EventName, *x.StartedAt, end)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		in.Metrics = append(in.Metrics, MetricInput{Spec: spec, Sums: sums, Dropped: dropped})
	}
	if in.Changes, err = flagChanges(ctx, tx, ws, envID, x.FlagKey, *x.StartedAt, end); err != nil {
		h.fail(w, r, err)
		return
	}
	if in.OtherRuns, err = otherRuns(ctx, tx, ws, envID, x.FlagID, x.ID); err != nil {
		h.fail(w, r, err)
		return
	}

	resp := Compute(in)
	h.store(cacheKey, resp)
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "57014" { // query_canceled: the statement timeout
		httpx.WriteError(w, http.StatusServiceUnavailable, "RESULTS_TIMEOUT", "computing results took too long; try again shortly")
		return
	}
	httpx.WriteInternal(w, r, err)
}

type badRequest string

func (b badRequest) Error() string { return string(b) }

// parseMetrics reads metrics=eventName:kind pairs (repeated or comma-separated).
func parseMetrics(values []string) ([]MetricSpec, error) {
	var out []MetricSpec
	seen := map[string]bool{}
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			i := strings.LastIndex(part, ":") // event names may contain ":"; the kind follows the last one
			if i < 0 {
				return nil, fmt.Errorf("metrics must be <eventName>:<kind>, e.g. purchase:conversion")
			}
			name, kind := part[:i], part[i+1:]
			if !eventNameRE.MatchString(name) || strings.HasPrefix(name, "$") {
				return nil, fmt.Errorf("metrics must be <eventName>:<kind>, e.g. purchase:conversion")
			}
			if kind != KindConversion && kind != KindCount && kind != KindValue {
				return nil, fmt.Errorf("metric kind must be %s, %s or %s", KindConversion, KindCount, KindValue)
			}
			if seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, MetricSpec{EventName: name, Kind: kind})
			if len(out) > MaxMetrics {
				return nil, fmt.Errorf("at most %d metrics per request", MaxMetrics)
			}
		}
	}
	return out, nil
}

func metricsKey(specs []MetricSpec) string {
	parts := make([]string, len(specs))
	for i, s := range specs {
		parts[i] = s.EventName + ":" + s.Kind
	}
	return strings.Join(parts, ",")
}

// resolveMetrics fills in names and primary flags from the experiment's own
// metric definitions. With no request the primary metric is used. An event
// name that the experiment does not define must exist among its events.
func (h *Handlers) resolveMetrics(ctx context.Context, q querier, ws string, x experimentRow, asked []MetricSpec) ([]MetricSpec, error) {
	defined := map[string]MetricSpec{}
	var primary *MetricSpec
	for i, m := range x.Metrics {
		defined[m.EventName] = m
		if m.IsPrimary {
			primary = &x.Metrics[i]
		}
	}
	if len(asked) == 0 {
		if primary == nil {
			return nil, badRequest("the experiment has no primary metric; pass metrics=<eventName>:<kind>")
		}
		asked = []MetricSpec{{EventName: primary.EventName, Kind: primary.Kind}}
	}
	out := make([]MetricSpec, 0, len(asked))
	for _, a := range asked {
		if d, ok := defined[a.EventName]; ok {
			a.Name, a.IsPrimary = d.Name, d.IsPrimary
		} else {
			exists, err := eventExists(ctx, q, ws, x.ID, a.EventName)
			if err != nil {
				return nil, err
			}
			if !exists {
				return nil, badRequest("the experiment has no event named " + a.EventName)
			}
		}
		out = append(out, a)
	}
	return out, nil
}

func (h *Handlers) cached(key string) (Response, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.cache[key]
	if !ok || h.now().Sub(e.at) > cacheTTL {
		return Response{}, false
	}
	return e.resp, true
}

func (h *Handlers) store(key string, resp Response) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.cache) >= 500 {
		for k, e := range h.cache {
			if h.now().Sub(e.at) > cacheTTL {
				delete(h.cache, k)
			}
		}
		if len(h.cache) >= 500 {
			h.cache = map[string]cacheEntry{}
		}
	}
	h.cache[key] = cacheEntry{at: h.now(), resp: resp}
}
