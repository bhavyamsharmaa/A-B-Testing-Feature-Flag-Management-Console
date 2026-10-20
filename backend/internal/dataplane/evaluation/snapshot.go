package evaluation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Snapshot is the compiled configuration of ONE environment: every flag's config (decoded once, immutable from here
// on: Evaluate never writes to it), the version of each, and which flag is in a running experiment. It is what the
// per-instance configuration cache (internal/dataplane/configcache) holds.
type Snapshot struct {
	Configs     map[string]*FlagConfig
	Versions    map[string]int64  // flag key -> flag_configs.version
	Experiments map[string]string // flag key -> id of its RUNNING experiment (at most one per flag and environment)
	Bytes       int               // rough size, for the cache's memory bound
}

// Source supplies snapshots (the cache implements it). A nil Source means "read the database for every request".
type Source interface {
	Snapshot(ctx context.Context, workspaceID, environmentID string) (*Snapshot, error)
}

// LoadSnapshot reads every flag of one environment of one workspace, in one query. It reads nothing for a workspace
// that is scheduled for deletion (the SDK key that got the request here would already have been refused; this is the
// second line), so a cache can never be refilled from a deleted workspace.
func LoadSnapshot(ctx context.Context, pool *pgxpool.Pool, workspaceID, environmentID string) (*Snapshot, error) {
	rows, err := pool.Query(ctx, `
		SELECT f.key, f.variations::text, fc.enabled, fc.targeting_rules::text,
		       COALESCE(fc.rollout::text, '{}'), fc.salt, fc.fallthrough_variation_id, fc.version,
		       (SELECT x.id::text FROM experiments x
		         WHERE x.flag_id = f.id AND x.environment_id = fc.environment_id
		           AND x.workspace_id = f.workspace_id AND x.status = 'running')
		FROM flags f
		JOIN flag_configs fc ON fc.flag_id = f.id
		JOIN workspaces w ON w.id = f.workspace_id AND w.deleted_at IS NULL
		WHERE fc.environment_id = $1::uuid AND f.workspace_id = $2::uuid`,
		environmentID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	s := &Snapshot{Configs: map[string]*FlagConfig{}, Versions: map[string]int64{}, Experiments: map[string]string{}, Bytes: 256}
	for rows.Next() {
		var fc FlagConfig
		var variations, rules, rollout string
		var version int64
		var experimentID *string
		if err := rows.Scan(&fc.Key, &variations, &fc.Enabled, &rules, &rollout, &fc.Salt, &fc.FallthroughVariationID, &version, &experimentID); err != nil {
			return nil, err
		}
		if experimentID != nil {
			s.Experiments[fc.Key] = *experimentID
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
		s.Configs[fc.Key] = &fc
		s.Versions[fc.Key] = version
		// the decoded structures are a few times the JSON they came from
		s.Bytes += 3*(len(variations)+len(rules)+len(rollout)) + 256
	}
	return s, rows.Err()
}
