package evaluation

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/httpx"
)

const maxFlagKeys = 100

type evaluateRequest struct {
	Context struct {
		SubjectKey string         `json:"subjectKey"`
		Attributes map[string]any `json:"attributes"`
	} `json:"context"`
	FlagKeys []string `json:"flagKeys"`
}

type evaluationResult struct {
	FlagKey      string  `json:"flagKey"`
	VariationID  *string `json:"variationId"`
	Value        any     `json:"value"`
	Reason       Reason  `json:"reason"`
	ExperimentID *string `json:"experimentId"`
}

// Handler serves POST /evaluate. It must run behind apikey's middleware,
// which decides the environment from the SDK key.
//
// This reads flag configs from Postgres on every request: the naive,
// DB-backed step of M1. The PRD's in-memory ruleset with Redis-driven swaps
// replaces this load in M2; Evaluate itself doesn't change.
func Handler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		envID, ok := apikey.EnvironmentID(r.Context())
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "SDK key required")
			return
		}
		var req evaluateRequest
		if err := httpx.DecodeJSON(w, r, &req); err != nil {
			httpx.BadRequest(w, err.Error())
			return
		}
		if req.Context.SubjectKey == "" {
			httpx.BadRequest(w, "context.subjectKey is required")
			return
		}
		if len(req.FlagKeys) == 0 || len(req.FlagKeys) > maxFlagKeys {
			httpx.BadRequest(w, fmt.Sprintf("flagKeys must contain 1-%d keys", maxFlagKeys))
			return
		}

		configs, err := loadConfigs(r, pool, envID, req.FlagKeys)
		if err != nil {
			// SDKs treat any non-200 as "serve fallback", so a DB outage
			// degrades to fallbacks rather than breaking the caller.
			log.Printf("evaluate: load configs: %v", err)
			httpx.WriteError(w, http.StatusServiceUnavailable, "EVALUATION_UNAVAILABLE", "flag data unavailable")
			return
		}

		ctx := Context{SubjectKey: req.Context.SubjectKey, Attributes: req.Context.Attributes}
		results := make([]evaluationResult, 0, len(req.FlagKeys))
		for _, key := range req.FlagKeys {
			res := Evaluate(configs[key], ctx)
			out := evaluationResult{FlagKey: key, Value: res.Value, Reason: res.Reason}
			if res.VariationID != "" {
				id := res.VariationID
				out.VariationID = &id
			}
			results = append(results, out)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"evaluations": results})
	}
}

func loadConfigs(r *http.Request, pool *pgxpool.Pool, envID string, keys []string) (map[string]*FlagConfig, error) {
	rows, err := pool.Query(r.Context(), `
		SELECT f.key, f.variations, fc.enabled, fc.targeting_rules,
		       COALESCE(fc.rollout, '{}'::jsonb), fc.salt, fc.fallthrough_variation_id
		FROM flags f
		JOIN flag_configs fc ON fc.flag_id = f.id
		WHERE fc.environment_id = $1::uuid AND f.key = ANY($2::text[])`,
		envID, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	configs := make(map[string]*FlagConfig, len(keys))
	for rows.Next() {
		var fc FlagConfig
		var variations, rules, rollout string
		if err := rows.Scan(&fc.Key, &variations, &fc.Enabled, &rules, &rollout, &fc.Salt, &fc.FallthroughVariationID); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(variations), &fc.Variations); err != nil {
			return nil, fmt.Errorf("flag %s variations: %w", fc.Key, err)
		}
		if err := json.Unmarshal([]byte(rules), &fc.TargetingRules); err != nil {
			return nil, fmt.Errorf("flag %s targeting rules: %w", fc.Key, err)
		}
		if err := json.Unmarshal([]byte(rollout), &fc.Rollout); err != nil {
			return nil, fmt.Errorf("flag %s rollout: %w", fc.Key, err)
		}
		configs[fc.Key] = &fc
	}
	return configs, rows.Err()
}
