package evaluation

import (
	"fmt"
	"strings"
)

// Operators supported in targeting clauses (US-02 AC-1).
const (
	OpEquals      = "equals"
	OpIn          = "in"
	OpContains    = "contains"
	OpGreaterThan = "greaterThan"
	OpExists      = "exists"
)

// Clause tests one context attribute. Value is ignored for "exists".
type Clause struct {
	Attribute string `json:"attribute"`
	Operator  string `json:"operator"`
	Value     any    `json:"value,omitempty"`
}

// TargetingRule serves VariationID when every clause matches. Rules are
// evaluated top-down and the first match wins.
type TargetingRule struct {
	Clauses     []Clause `json:"clauses"`
	VariationID string   `json:"variationId"`
}

// Matches reports whether all clauses hold for attrs.
func (r TargetingRule) Matches(attrs map[string]any) bool {
	for _, c := range r.Clauses {
		if !c.Matches(attrs) {
			return false
		}
	}
	return len(r.Clauses) > 0
}

// Matches evaluates the clause. A missing or null attribute makes every
// operator false — never an error and never an accidental match (US-02
// AC-2, a normative safety rule). Likewise a type mismatch between the
// attribute and the clause value is simply false.
func (c Clause) Matches(attrs map[string]any) bool {
	v, present := attrs[c.Attribute]
	if !present || v == nil {
		return false
	}
	switch c.Operator {
	case OpExists:
		return true
	case OpEquals:
		return scalarEqual(v, c.Value)
	case OpIn:
		list, ok := c.Value.([]any)
		if !ok {
			return false
		}
		for _, candidate := range list {
			if scalarEqual(v, candidate) {
				return true
			}
		}
		return false
	case OpContains:
		switch attr := v.(type) {
		case string:
			needle, ok := c.Value.(string)
			return ok && strings.Contains(attr, needle)
		case []any:
			for _, item := range attr {
				if scalarEqual(item, c.Value) {
					return true
				}
			}
		}
		return false
	case OpGreaterThan:
		a, aok := v.(float64)
		b, bok := c.Value.(float64)
		return aok && bok && a > b
	}
	return false
}

// scalarEqual compares JSON scalars. Objects and arrays are never equal,
// because == on those dynamic types would panic.
func scalarEqual(a, b any) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	}
	return false
}

// Validate checks a rule's shape against the flag's variation IDs. The
// control plane calls it before saving, so evaluation never sees a rule it
// can't interpret.
func (r TargetingRule) Validate(variationIDs map[string]bool) error {
	if !variationIDs[r.VariationID] {
		return fmt.Errorf("variationId %q is not a variation of this flag", r.VariationID)
	}
	if len(r.Clauses) == 0 {
		return fmt.Errorf("rule serving %q has no clauses", r.VariationID)
	}
	for i, c := range r.Clauses {
		if c.Attribute == "" {
			return fmt.Errorf("clause %d: attribute is required", i)
		}
		switch c.Operator {
		case OpExists:
		case OpEquals:
			if !isScalar(c.Value) {
				return fmt.Errorf("clause %d: equals needs a string, number, or boolean value", i)
			}
		case OpIn:
			list, ok := c.Value.([]any)
			if !ok || len(list) == 0 {
				return fmt.Errorf("clause %d: in needs a non-empty array value", i)
			}
		case OpContains:
			if !isScalar(c.Value) {
				return fmt.Errorf("clause %d: contains needs a string, number, or boolean value", i)
			}
		case OpGreaterThan:
			if _, ok := c.Value.(float64); !ok {
				return fmt.Errorf("clause %d: greaterThan needs a number value", i)
			}
		default:
			return fmt.Errorf("clause %d: unknown operator %q", i, c.Operator)
		}
	}
	return nil
}

func isScalar(v any) bool {
	switch v.(type) {
	case string, float64, bool:
		return true
	}
	return false
}
