// Package evaluation implements the data-plane hot path described in PRD §4:
// evaluation is a map lookup against a ruleset held in process memory,
// swapped atomically when a new version arrives over Redis pub/sub or the
// 30s poll fallback. No datastore call sits on this path, by design — a
// Redis GET alone (~0.5-2ms) would already threaten the p99 < 50ms budget
// under load; Postgres more so. See Goals G1/G2 and the "why no DB call on
// the hot path" note in the PRD.
package evaluation

import (
	"sync/atomic"
)

// Ruleset is the full, per-environment snapshot of flags an Evaluation
// Service instance needs to answer /evaluate with no further I/O.
type Ruleset struct {
	Version uint64
	Flags   map[string]*FlagConfig // keyed by flag key
}

type FlagConfig struct {
	FlagKey                 string
	Enabled                 bool
	VariationType           string
	Variations              map[string]any // variationID -> typed value
	TargetingRules          []TargetingRule
	Rollout                 []VariationWeight
	Salt                    string
	FallthroughVariationID  string
}

type TargetingRule struct {
	SegmentID   string // resolved segment, evaluated top-down, first match wins (US-02 AC-1)
	VariationID string
}

// Reason mirrors the OpenAPI contract's EvaluationResult.reason enum, so the
// SDK and the remote /evaluate endpoint can be tested against one fixture
// suite for identical output (PRD §5 Testing).
type Reason string

const (
	ReasonTargetingMatch Reason = "TARGETING_MATCH"
	ReasonRollout        Reason = "ROLLOUT"
	ReasonFallthrough    Reason = "FALLTHROUGH"
	ReasonFlagDisabled   Reason = "FLAG_DISABLED"
	ReasonFlagNotFound   Reason = "FLAG_NOT_FOUND"
	ReasonTypeMismatch   Reason = "TYPE_MISMATCH"
)

type EvalResult struct {
	VariationID string
	Value       any
	Reason      Reason
}

type Context struct {
	SubjectKey string
	Attributes map[string]any
}

// Engine holds the current ruleset behind an atomic pointer so readers never
// block a writer's swap and vice versa (US-09 AC-1: p99 < 1ms, zero network
// I/O, lock-free atomic-pointer swap).
type Engine struct {
	current atomic.Pointer[Ruleset]
}

func NewEngine() *Engine {
	e := &Engine{}
	e.current.Store(&Ruleset{Flags: map[string]*FlagConfig{}})
	return e
}

// Swap atomically replaces the ruleset. Called by the SSE/poll subscriber
// when a new version arrives — never by the evaluation path itself.
func (e *Engine) Swap(rs *Ruleset) {
	e.current.Store(rs)
}

// Evaluate resolves a single flag against context. fallback is REQUIRED:
// there is no overload that omits it, so undefined behaviour on a missing
// or type-mismatched flag is unrepresentable in the API (US-09 AC-2). Every
// error path here returns (fallback, reason) and never panics or errors
// upward — an unknown flag or type mismatch must degrade silently and log a
// metric, not throw (US-01 AC-3).
func (e *Engine) Evaluate(flagKey string, ctx Context, fallback any) EvalResult {
	rs := e.current.Load()
	fc, found := rs.Flags[flagKey]
	if !found {
		return EvalResult{Value: fallback, Reason: ReasonFlagNotFound}
	}
	if !fc.Enabled {
		return EvalResult{Value: fallback, Reason: ReasonFlagDisabled}
	}

	// 1. Targeting rules, top-down, first match wins (US-02 AC-1).
	//    A missing context attribute makes the clause false, never a match
	//    or an error (US-02 AC-2) — that check lives in segment evaluation,
	//    intentionally kept out of this file to isolate the hot-path
	//    bucketing logic from targeting-clause logic.
	for _, rule := range fc.TargetingRules {
		if segmentMatches(rule.SegmentID, ctx) {
			val, ok := fc.Variations[rule.VariationID]
			if !ok {
				return EvalResult{Value: fallback, Reason: ReasonTypeMismatch}
			}
			return EvalResult{VariationID: rule.VariationID, Value: val, Reason: ReasonTargetingMatch}
		}
	}

	// 2. Percentage rollout via deterministic bucketing (US-03).
	if len(fc.Rollout) > 0 {
		b := Bucket(flagKey, fc.Salt, ctx.SubjectKey)
		if variationID, ok := AssignVariation(b, fc.Rollout); ok {
			val, valOK := fc.Variations[variationID]
			if !valOK {
				return EvalResult{Value: fallback, Reason: ReasonTypeMismatch}
			}
			return EvalResult{VariationID: variationID, Value: val, Reason: ReasonRollout}
		}
	}

	// 3. Fallthrough.
	val, ok := fc.Variations[fc.FallthroughVariationID]
	if !ok {
		return EvalResult{Value: fallback, Reason: ReasonTypeMismatch}
	}
	return EvalResult{VariationID: fc.FallthroughVariationID, Value: val, Reason: ReasonFallthrough}
}

// segmentMatches is a placeholder wired up once segment rule evaluation
// (attribute operators: equals/in/contains/greaterThan/exists) lands.
// Left as an explicit stub rather than inlined so the missing-attribute
// safety rule (US-02 AC-2) gets its own unit tests independent of bucketing.
func segmentMatches(segmentID string, ctx Context) bool {
	_ = segmentID
	_ = ctx
	return false
}
