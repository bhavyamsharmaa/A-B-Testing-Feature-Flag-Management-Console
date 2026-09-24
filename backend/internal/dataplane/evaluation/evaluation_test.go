package evaluation

import (
	"fmt"
	"testing"
)

func boolFlag() *FlagConfig {
	return &FlagConfig{
		Key:                    "new-checkout",
		Enabled:                true,
		Variations:             []Variation{{ID: "on", Value: true}, {ID: "off", Value: false}},
		Salt:                   "s1",
		FallthroughVariationID: "off",
	}
}

// US-03 AC-1: a 10% rollout over 100,000 users includes 9,900-10,100.
func TestRolloutAccuracy(t *testing.T) {
	fc := boolFlag()
	fc.Rollout = map[string]uint32{"on": 10_000, "off": 90_000}
	on := 0
	for i := range 100_000 {
		if Evaluate(fc, Context{SubjectKey: fmt.Sprintf("user-%d", i)}).VariationID == "on" {
			on++
		}
	}
	if on < 9_900 || on > 10_100 {
		t.Fatalf("10%% rollout included %d of 100000 users, want 9900-10100", on)
	}
}

// US-03 AC-2: same user, same variation, on every call.
func TestAssignmentIsStable(t *testing.T) {
	fc := boolFlag()
	fc.Rollout = map[string]uint32{"on": 50_000, "off": 50_000}
	for i := range 200 {
		ctx := Context{SubjectKey: fmt.Sprintf("user-%d", i)}
		first := Evaluate(fc, ctx).VariationID
		for range 1_000 {
			if got := Evaluate(fc, ctx).VariationID; got != first {
				t.Fatalf("user-%d flipped from %s to %s", i, first, got)
			}
		}
	}
}

// US-03 AC-3: raising the rollout never removes anyone already included.
func TestRaisingRolloutIsMonotonic(t *testing.T) {
	low, high := boolFlag(), boolFlag()
	low.Rollout = map[string]uint32{"on": 20_000, "off": 80_000}
	high.Rollout = map[string]uint32{"on": 60_000, "off": 40_000}
	for i := range 20_000 {
		ctx := Context{SubjectKey: fmt.Sprintf("user-%d", i)}
		if Evaluate(low, ctx).VariationID == "on" && Evaluate(high, ctx).VariationID != "on" {
			t.Fatalf("user-%d lost access when rollout was raised", i)
		}
	}
}

// US-03 AC-4: different salts give independent assignments.
func TestSaltsAreIndependent(t *testing.T) {
	a, b := boolFlag(), boolFlag()
	a.Rollout = map[string]uint32{"on": 50_000, "off": 50_000}
	b.Rollout = a.Rollout
	b.Salt = "s2"
	both := 0
	const n = 100_000
	for i := range n {
		ctx := Context{SubjectKey: fmt.Sprintf("user-%d", i)}
		if Evaluate(a, ctx).VariationID == "on" && Evaluate(b, ctx).VariationID == "on" {
			both++
		}
	}
	// Independent 50/50 flags overlap on ~25%.
	if both < 24_000 || both > 26_000 {
		t.Fatalf("%d users in both 50%% rollouts, want ~25000", both)
	}
}

func TestEvaluateOrderAndReasons(t *testing.T) {
	fc := boolFlag()
	fc.TargetingRules = []TargetingRule{{
		Clauses:     []Clause{{Attribute: "country", Operator: OpIn, Value: []any{"IN", "US"}}},
		VariationID: "on",
	}}

	if r := Evaluate(nil, Context{SubjectKey: "u"}); r.Reason != ReasonFlagNotFound || r.Value != nil {
		t.Errorf("missing flag: got %+v", r)
	}
	disabled := boolFlag()
	disabled.Enabled = false
	if r := Evaluate(disabled, Context{SubjectKey: "u"}); r.Reason != ReasonFlagDisabled || r.Value != nil {
		t.Errorf("disabled flag: got %+v", r)
	}
	if r := Evaluate(fc, Context{SubjectKey: "u", Attributes: map[string]any{"country": "IN"}}); r.Reason != ReasonTargetingMatch || r.Value != true {
		t.Errorf("targeting match: got %+v", r)
	}
	if r := Evaluate(fc, Context{SubjectKey: "u", Attributes: map[string]any{"country": "FR"}}); r.Reason != ReasonFallthrough || r.VariationID != "off" {
		t.Errorf("fallthrough: got %+v", r)
	}
	fc.FallthroughVariationID = "deleted"
	if r := Evaluate(fc, Context{SubjectKey: "u"}); r.Reason != ReasonTypeMismatch || r.Value != nil {
		t.Errorf("dangling variation: got %+v", r)
	}
}

// US-02 AC-2: a missing attribute is false for every operator, never a match.
func TestMissingAttributeNeverMatches(t *testing.T) {
	clauses := []Clause{
		{Attribute: "plan", Operator: OpEquals, Value: "pro"},
		{Attribute: "plan", Operator: OpIn, Value: []any{"pro"}},
		{Attribute: "plan", Operator: OpContains, Value: "pro"},
		{Attribute: "age", Operator: OpGreaterThan, Value: float64(-1)},
		{Attribute: "plan", Operator: OpExists},
	}
	for _, attrs := range []map[string]any{nil, {}, {"plan": nil, "age": nil}} {
		for _, c := range clauses {
			if c.Matches(attrs) {
				t.Errorf("%s on missing attribute matched (attrs=%v)", c.Operator, attrs)
			}
		}
	}
}

func TestClauseOperators(t *testing.T) {
	attrs := map[string]any{
		"country": "IN",
		"email":   "a@helios.dev",
		"age":     float64(30),
		"tags":    []any{"beta", "staff"},
		"meta":    map[string]any{"x": 1},
	}
	cases := []struct {
		c    Clause
		want bool
	}{
		{Clause{"country", OpEquals, "IN"}, true},
		{Clause{"country", OpEquals, "US"}, false},
		{Clause{"age", OpEquals, "30"}, false}, // type mismatch is false, not coerced
		{Clause{"country", OpIn, []any{"US", "IN"}}, true},
		{Clause{"country", OpIn, "IN"}, false},
		{Clause{"email", OpContains, "@helios.dev"}, true},
		{Clause{"tags", OpContains, "beta"}, true},
		{Clause{"tags", OpContains, "admin"}, false},
		{Clause{"age", OpGreaterThan, float64(18)}, true},
		{Clause{"age", OpGreaterThan, float64(30)}, false},
		{Clause{"meta", OpEquals, map[string]any{"x": 1}}, false}, // must not panic
		{Clause{"email", OpExists, nil}, true},
		{Clause{"email", "startsWith", "a"}, false},
	}
	for _, tc := range cases {
		if got := tc.c.Matches(attrs); got != tc.want {
			t.Errorf("%s %s %v = %v, want %v", tc.c.Attribute, tc.c.Operator, tc.c.Value, got, tc.want)
		}
	}
}

func TestRuleValidate(t *testing.T) {
	ids := map[string]bool{"on": true, "off": true}
	valid := TargetingRule{Clauses: []Clause{{Attribute: "country", Operator: OpIn, Value: []any{"IN"}}}, VariationID: "on"}
	if err := valid.Validate(ids); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	invalid := []TargetingRule{
		{Clauses: valid.Clauses, VariationID: "missing"},
		{VariationID: "on"},
		{Clauses: []Clause{{Operator: OpExists}}, VariationID: "on"},
		{Clauses: []Clause{{Attribute: "a", Operator: "regex", Value: "x"}}, VariationID: "on"},
		{Clauses: []Clause{{Attribute: "a", Operator: OpIn, Value: []any{}}}, VariationID: "on"},
		{Clauses: []Clause{{Attribute: "a", Operator: OpGreaterThan, Value: "5"}}, VariationID: "on"},
	}
	for i, rule := range invalid {
		if err := rule.Validate(ids); err == nil {
			t.Errorf("invalid rule %d accepted", i)
		}
	}
}
