// Package evaluation is the data-plane hot path: it resolves a feature-flag
// check for a given context. Evaluate is a pure function of a flag config
// and a context, so the remote /evaluate endpoint and a future in-process
// SDK can share it and produce identical results (PRD §5 Testing).
package evaluation

// Reason mirrors EvaluationResult.reason in api/openapi.yaml.
type Reason string

const (
	ReasonTargetingMatch Reason = "TARGETING_MATCH"
	ReasonRollout        Reason = "ROLLOUT"
	ReasonFallthrough    Reason = "FALLTHROUGH"
	ReasonFlagDisabled   Reason = "FLAG_DISABLED"
	ReasonFlagNotFound   Reason = "FLAG_NOT_FOUND"
	ReasonTypeMismatch   Reason = "TYPE_MISMATCH"
)

type Variation struct {
	ID    string `json:"id"`
	Value any    `json:"value"`
}

// FlagConfig is one flag's behaviour in one environment.
type FlagConfig struct {
	Key                    string
	Enabled                bool
	Variations             []Variation // order defines rollout bucket ranges
	TargetingRules         []TargetingRule
	Rollout                map[string]uint32 // variationID -> basis points; empty means no rollout
	Salt                   string
	FallthroughVariationID string
}

type Context struct {
	SubjectKey string
	Attributes map[string]any
}

// Result carries no value when no variation applies (not found, disabled,
// broken config): the caller then serves its own fallback.
type Result struct {
	VariationID string
	Value       any
	Reason      Reason
}

// Evaluate resolves fc for ctx: targeting rules top-down, then percentage
// rollout by deterministic bucketing, then fallthrough. It never errors;
// every failure path is expressed as a Reason.
func Evaluate(fc *FlagConfig, ctx Context) Result {
	if fc == nil {
		return Result{Reason: ReasonFlagNotFound}
	}
	if !fc.Enabled {
		return Result{Reason: ReasonFlagDisabled}
	}

	for _, rule := range fc.TargetingRules {
		if rule.Matches(ctx.Attributes) {
			return fc.serve(rule.VariationID, ReasonTargetingMatch)
		}
	}

	if len(fc.Rollout) > 0 {
		weights := make([]VariationWeight, 0, len(fc.Variations))
		for _, v := range fc.Variations {
			weights = append(weights, VariationWeight{VariationID: v.ID, BasisPoints: fc.Rollout[v.ID]})
		}
		b := Bucket(fc.Key, fc.Salt, ctx.SubjectKey)
		if id, ok := AssignVariation(b, weights); ok {
			return fc.serve(id, ReasonRollout)
		}
	}

	return fc.serve(fc.FallthroughVariationID, ReasonFallthrough)
}

func (fc *FlagConfig) serve(variationID string, reason Reason) Result {
	for _, v := range fc.Variations {
		if v.ID == variationID {
			return Result{VariationID: v.ID, Value: v.Value, Reason: reason}
		}
	}
	// A rule or rollout pointing at a variation that no longer exists.
	// Validation prevents this on write; this is the belt to that brace.
	return Result{Reason: ReasonTypeMismatch}
}
