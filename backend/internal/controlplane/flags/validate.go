package flags

import (
	"encoding/json"
	"fmt"
	"regexp"

	"helios/backend/internal/dataplane/evaluation"
)

var flagKeyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// maxVariationBytes is the PRD's 4 KB payload cap: Helios is not a general
// config store (§1 Non-goals).
const maxVariationBytes = 4096

type createRequest struct {
	Key           string                 `json:"key"`
	Name          string                 `json:"name"`
	Description   string                 `json:"description"`
	VariationType string                 `json:"variationType"`
	Variations    []evaluation.Variation `json:"variations"`
}

// errTypeMismatch marks a variation whose value doesn't match variationType,
// which the contract reports as its own 400 code (US-01 AC-2).
type errTypeMismatch struct{ msg string }

func (e errTypeMismatch) Error() string { return e.msg }

func (req createRequest) validate() error {
	if !flagKeyRE.MatchString(req.Key) {
		return fmt.Errorf("key must be 1-128 characters of letters, digits, '_', '.', '-', starting with a letter or digit")
	}
	if req.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !validVariationType(req.VariationType) {
		return fmt.Errorf("variationType must be one of boolean, string, number, json")
	}
	if n := len(req.Variations); n < 2 || n > 20 {
		return fmt.Errorf("a flag needs 2-20 variations, got %d", n)
	}
	seen := map[string]bool{}
	for i, v := range req.Variations {
		if v.ID == "" {
			return fmt.Errorf("variation %d: id is required", i)
		}
		if seen[v.ID] {
			return fmt.Errorf("variation id %q appears twice", v.ID)
		}
		seen[v.ID] = true
		if b, _ := json.Marshal(v.Value); len(b) > maxVariationBytes {
			return fmt.Errorf("variation %q is larger than %d bytes", v.ID, maxVariationBytes)
		}
		if !valueMatchesType(v.Value, req.VariationType) {
			return errTypeMismatch{fmt.Sprintf("variation %q is not a %s", v.ID, req.VariationType)}
		}
	}
	return nil
}

func validVariationType(t string) bool {
	switch t {
	case "boolean", "string", "number", "json":
		return true
	}
	return false
}

func valueMatchesType(v any, variationType string) bool {
	switch variationType {
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "json":
		switch v.(type) {
		case map[string]any, []any:
			return true
		}
	}
	return false
}

// updateRequest uses RawMessage so an absent field ("leave unchanged") can
// be told apart from an explicit null ("clear it").
type updateRequest struct {
	Enabled                *bool           `json:"enabled"`
	TargetingRules         json.RawMessage `json:"targetingRules"`
	Rollout                json.RawMessage `json:"rollout"`
	FallthroughVariationID *string         `json:"fallthroughVariationId"`
}

func (req updateRequest) empty() bool {
	return req.Enabled == nil && req.TargetingRules == nil && req.Rollout == nil && req.FallthroughVariationID == nil
}

func isNull(raw json.RawMessage) bool {
	return string(raw) == "null"
}

// parseRules decodes and validates targetingRules. null clears them.
func parseRules(raw json.RawMessage, variationIDs map[string]bool) ([]evaluation.TargetingRule, error) {
	rules := []evaluation.TargetingRule{}
	if isNull(raw) {
		return rules, nil
	}
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, fmt.Errorf("targetingRules: %w", err)
	}
	for i, rule := range rules {
		if err := rule.Validate(variationIDs); err != nil {
			return nil, fmt.Errorf("targetingRules[%d]: %w", i, err)
		}
	}
	return rules, nil
}

// parseRollout decodes and validates a {variationId: basisPoints} rollout.
// null clears it; otherwise the weights must sum to exactly 100000.
func parseRollout(raw json.RawMessage, variationIDs map[string]bool) (map[string]uint32, error) {
	if isNull(raw) {
		return nil, nil
	}
	var rollout map[string]uint32
	if err := json.Unmarshal(raw, &rollout); err != nil {
		return nil, fmt.Errorf("rollout must map variation ids to non-negative integer basis points: %w", err)
	}
	var total uint64
	for id, bp := range rollout {
		if !variationIDs[id] {
			return nil, fmt.Errorf("rollout: %q is not a variation of this flag", id)
		}
		total += uint64(bp)
	}
	if total != evaluation.TotalBuckets {
		return nil, fmt.Errorf("rollout basis points must sum to %d, got %d", evaluation.TotalBuckets, total)
	}
	return rollout, nil
}
