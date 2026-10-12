package flags

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/dataplane/evaluation"
)

// ruleSegmentRefs returns the distinct segment keys the raw targetingRules
// reference, sorted (the order the rows are locked in). Anything that does not
// decode yields none: parseRules reports it with a proper message afterwards.
func ruleSegmentRefs(raw json.RawMessage) []string {
	if raw == nil || isNull(raw) {
		return nil
	}
	var rules []evaluation.TargetingRule
	if json.Unmarshal(raw, &rules) != nil {
		return nil
	}
	refs := evaluation.SegmentRefs(rules)
	sort.Strings(refs)
	return refs
}

// lockSegments takes the named segments of the workspace FOR SHARE, in key
// order, and returns the keys that exist. With no keys it does nothing, so
// flags that reference no segment run exactly the SQL they always did.
func lockSegments(ctx context.Context, tx pgx.Tx, workspaceID string, keys []string) (map[string]bool, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT key FROM segments
		WHERE workspace_id = $1::uuid AND key = ANY($2::text[])
		ORDER BY key FOR SHARE`, workspaceID, keys)
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(found))
	for _, k := range found {
		known[k] = true
	}
	return known, nil
}

// checkSegmentsExist rejects a rule that names a segment this workspace does
// not have (a typo, a deleted segment, or another workspace's segment: those
// are indistinguishable on purpose).
func checkSegmentsExist(rules []evaluation.TargetingRule, known map[string]bool) error {
	for i, r := range rules {
		for j, c := range r.Clauses {
			if c.Operator != evaluation.OpInSegment && c.Operator != evaluation.OpNotInSegment {
				continue
			}
			if key, _ := c.Value.(string); !known[key] {
				return fmt.Errorf("targetingRules[%d]: clause %d: segment %q does not exist in this workspace", i, j, key)
			}
		}
	}
	return nil
}
