package evaluation

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/exposuretoken"
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
	// ExposureToken proves the server served this variation of the experiment
	// to this subject; the client passes it back on its $exposure event. It is
	// absent (not null) when the flag is in no running experiment, so
	// evaluations of other flags are byte for byte what they always were.
	ExposureToken *string `json:"exposureToken,omitempty"`
}

// Handler serves POST /evaluate. It must run behind apikey's middleware,
// which decides the environment from the SDK key.
//
// This reads flag configs from Postgres on every request: the naive,
// DB-backed step of M1. The PRD's in-memory ruleset with Redis-driven swaps
// replaces this load in M2; Evaluate itself doesn't change.
func Handler(pool *pgxpool.Pool, tokens *exposuretoken.Keyring) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, ok := apikey.ScopeFrom(r.Context())
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

		configs, experiments, err := loadConfigs(r, pool, scope, req.FlagKeys)
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
				// A variation was served: if the flag is in a running
				// experiment, tell the client which one so it can report the
				// exposure. Assignment is the flag's own (nothing is re-bucketed).
				if exp, ok := experiments[key]; ok {
					out.ExperimentID = &exp
					if tokens != nil {
						t := tokens.Issue(exposuretoken.Binding{
							Workspace: scope.WorkspaceID, Environment: scope.EnvironmentID, Experiment: exp,
							Variation: res.VariationID, Subject: req.Context.SubjectKey,
						}, time.Now())
						out.ExposureToken = &t
					}
				}
			}
			results = append(results, out)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"evaluations": results})
	}
}

// loadConfigs reads the flags of the key's own environment. The workspace
// filter is redundant with the composite foreign keys (a flag_config can only
// join a flag and an environment of one workspace), kept as defence in depth:
// were the data ever inconsistent, a key still could not read another
// workspace's flag.
//
// The second result maps flag key to the id of its RUNNING experiment in this
// environment (at most one: the one_running_per_flag_env index).
func loadConfigs(r *http.Request, pool *pgxpool.Pool, scope apikey.Scope, keys []string) (map[string]*FlagConfig, map[string]string, error) {
	rows, err := pool.Query(r.Context(), `
		SELECT f.key, f.variations, fc.enabled, fc.targeting_rules,
		       COALESCE(fc.rollout, '{}'::jsonb), fc.salt, fc.fallthrough_variation_id,
		       (SELECT x.id::text FROM experiments x
		         WHERE x.flag_id = f.id AND x.environment_id = fc.environment_id
		           AND x.workspace_id = f.workspace_id AND x.status = 'running')
		FROM flags f
		JOIN flag_configs fc ON fc.flag_id = f.id
		WHERE fc.environment_id = $1::uuid AND f.workspace_id = $3::uuid AND f.key = ANY($2::text[])`,
		scope.EnvironmentID, keys, scope.WorkspaceID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	configs := make(map[string]*FlagConfig, len(keys))
	experiments := map[string]string{}
	for rows.Next() {
		var fc FlagConfig
		var variations, rules, rollout string
		var experimentID *string
		if err := rows.Scan(&fc.Key, &variations, &fc.Enabled, &rules, &rollout, &fc.Salt, &fc.FallthroughVariationID, &experimentID); err != nil {
			return nil, nil, err
		}
		if experimentID != nil {
			experiments[fc.Key] = *experimentID
		}
		if err := json.Unmarshal([]byte(variations), &fc.Variations); err != nil {
			return nil, nil, fmt.Errorf("flag %s variations: %w", fc.Key, err)
		}
		if err := json.Unmarshal([]byte(rules), &fc.TargetingRules); err != nil {
			return nil, nil, fmt.Errorf("flag %s targeting rules: %w", fc.Key, err)
		}
		if err := json.Unmarshal([]byte(rollout), &fc.Rollout); err != nil {
			return nil, nil, fmt.Errorf("flag %s rollout: %w", fc.Key, err)
		}
		configs[fc.Key] = &fc
	}
	return configs, experiments, rows.Err()
}
