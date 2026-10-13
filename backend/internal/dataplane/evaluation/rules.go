package evaluation

import (
	"fmt"
	"strings"
	"time"
)

// Operators supported in targeting clauses (US-02 AC-1).
const (
	OpEquals      = "equals"
	OpIn          = "in"
	OpContains    = "contains"
	OpGreaterThan = "greaterThan"
	OpExists      = "exists"

	OpStartsWith           = "startsWith"
	OpEndsWith             = "endsWith"
	OpLessThan             = "lessThan"
	OpLessThanOrEqual      = "lessThanOrEqual"
	OpGreaterThanOrEqual   = "greaterThanOrEqual"
	OpSemverEquals         = "semverEquals"
	OpSemverLessThan       = "semverLessThan"
	OpSemverLessOrEqual    = "semverLessThanOrEqual"
	OpSemverGreaterThan    = "semverGreaterThan"
	OpSemverGreaterOrEqual = "semverGreaterThanOrEqual"
	OpDateBefore           = "dateBefore"
	OpDateAfter            = "dateAfter"
	OpDateEquals           = "dateEquals"
	OpMatchesRegex         = "matchesRegex"

	// Segment operators ignore Attribute: they test the subject, and Value is
	// the segment's key. Only flag rules may use them (see ClauseScope).
	OpInSegment    = "inSegment"
	OpNotInSegment = "notInSegment"
)

// LegacyOperators are the operators that existed before segments. An evaluator
// of that age treats every other operator as "no match" (see
// segments.UsesNewFeaturesSQL, which lists exactly these).
var LegacyOperators = []string{OpEquals, OpIn, OpContains, OpGreaterThan, OpExists}

// AllOperators is every operator a clause may use today.
var AllOperators = append(append([]string{}, LegacyOperators...),
	OpStartsWith, OpEndsWith, OpLessThan, OpLessThanOrEqual, OpGreaterThanOrEqual,
	OpSemverEquals, OpSemverLessThan, OpSemverLessOrEqual, OpSemverGreaterThan, OpSemverGreaterOrEqual,
	OpDateBefore, OpDateAfter, OpDateEquals, OpMatchesRegex, OpInSegment, OpNotInSegment)

// maxDateBytes bounds what is parsed as a date; no RFC 3339 timestamp is longer.
const maxDateBytes = 64

// Clause tests one context attribute. Value is ignored for "exists".
//
// Negate inverts the operator's answer, but only when the operator could be
// evaluated: a missing attribute, a value of the wrong type, an unparseable
// version or date, or a too-long regex input stays a non-match whether or not
// the clause is negated. A negated clause therefore never matches by accident.
type Clause struct {
	Attribute string `json:"attribute"`
	Operator  string `json:"operator"`
	Value     any    `json:"value,omitempty"`
	Negate    bool   `json:"negate,omitempty"`
}

// TargetingRule serves VariationID when every clause matches. Rules are
// evaluated top-down and the first match wins.
type TargetingRule struct {
	Clauses     []Clause `json:"clauses"`
	VariationID string   `json:"variationId"`
}

// matchEnv is what a clause is evaluated against.
type matchEnv struct {
	subject  string
	attrs    map[string]any
	segments map[string]Segment // nil for a segment's own rules
}

// Matches reports whether all clauses hold for attrs. Segment clauses never
// match here, because no segments are known; Evaluate uses MatchesIn.
func (r TargetingRule) Matches(attrs map[string]any) bool {
	return r.matches(matchEnv{attrs: attrs})
}

// MatchesIn reports whether all clauses hold in ctx, resolving segment clauses
// against ctx.Segments.
func (r TargetingRule) MatchesIn(ctx Context) bool {
	return r.matches(matchEnv{subject: ctx.SubjectKey, attrs: ctx.Attributes, segments: ctx.Segments})
}

func (r TargetingRule) matches(env matchEnv) bool {
	for _, c := range r.Clauses {
		if !c.matches(env) {
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
	return c.matches(matchEnv{attrs: attrs})
}

func (c Clause) matches(env matchEnv) bool {
	switch c.Operator {
	case OpInSegment, OpNotInSegment:
		seg, found := env.segments[segmentKey(c.Value)]
		if !found {
			// An unknown (for example deleted) segment matches nobody, for
			// notInSegment too: a stale reference must not widen a rule.
			return false
		}
		in := seg.Contains(env.subject, env.attrs)
		return in == (c.Operator == OpInSegment)
	}
	v, present := env.attrs[c.Attribute]
	if !present || v == nil {
		return false
	}
	result, determinate := c.apply(v)
	if !determinate {
		return false
	}
	return result != c.Negate
}

func segmentKey(v any) string {
	s, _ := v.(string)
	return s
}

// apply evaluates the operator on a present attribute value. determinate is
// false when the operands do not suit the operator (wrong types, bad version,
// ...): the clause then matches neither plain nor negated.
func (c Clause) apply(v any) (result, determinate bool) {
	switch c.Operator {
	case OpExists:
		return true, true
	case OpEquals:
		if !isScalar(v) || !sameKind(v, c.Value) {
			return false, false
		}
		return scalarEqual(v, c.Value), true
	case OpIn:
		list, ok := c.Value.([]any)
		if !ok || !isScalar(v) {
			return false, false
		}
		comparable := false
		for _, candidate := range list {
			if sameKind(v, candidate) {
				comparable = true
				if scalarEqual(v, candidate) {
					return true, true
				}
			}
		}
		return false, comparable
	case OpContains:
		switch attr := v.(type) {
		case string:
			needle, ok := c.Value.(string)
			if !ok {
				return false, false
			}
			return strings.Contains(attr, needle), true
		case []any:
			if !isScalar(c.Value) {
				return false, false
			}
			for _, item := range attr {
				if scalarEqual(item, c.Value) {
					return true, true
				}
			}
			return false, true
		}
		return false, false
	case OpStartsWith, OpEndsWith:
		attr, ok1 := v.(string)
		prefix, ok2 := c.Value.(string)
		if !ok1 || !ok2 {
			return false, false
		}
		if c.Operator == OpStartsWith {
			return strings.HasPrefix(attr, prefix), true
		}
		return strings.HasSuffix(attr, prefix), true
	case OpGreaterThan, OpGreaterThanOrEqual, OpLessThan, OpLessThanOrEqual:
		a, aok := v.(float64)
		b, bok := c.Value.(float64)
		if !aok || !bok {
			return false, false
		}
		switch c.Operator {
		case OpGreaterThan:
			return a > b, true
		case OpGreaterThanOrEqual:
			return a >= b, true
		case OpLessThan:
			return a < b, true
		}
		return a <= b, true
	case OpSemverEquals, OpSemverLessThan, OpSemverLessOrEqual, OpSemverGreaterThan, OpSemverGreaterOrEqual:
		as, aok := v.(string)
		bs, bok := c.Value.(string)
		if !aok || !bok {
			return false, false
		}
		a, aok := parseSemver(as)
		b, bok := parseSemver(bs)
		if !aok || !bok {
			return false, false
		}
		cmp := a.compare(b)
		switch c.Operator {
		case OpSemverEquals:
			return cmp == 0, true
		case OpSemverLessThan:
			return cmp < 0, true
		case OpSemverLessOrEqual:
			return cmp <= 0, true
		case OpSemverGreaterThan:
			return cmp > 0, true
		}
		return cmp >= 0, true
	case OpDateBefore, OpDateAfter, OpDateEquals:
		as, aok := v.(string)
		bs, bok := c.Value.(string)
		if !aok || !bok {
			return false, false
		}
		a, aok := parseDate(as)
		b, bok := parseDate(bs)
		if !aok || !bok {
			return false, false
		}
		switch c.Operator {
		case OpDateBefore:
			return a.Before(b), true
		case OpDateAfter:
			return a.After(b), true
		}
		return a.Equal(b), true
	case OpMatchesRegex:
		input, ok1 := v.(string)
		pattern, ok2 := c.Value.(string)
		if !ok1 || !ok2 {
			return false, false
		}
		return regexMatch(pattern, input)
	}
	return false, false
}

// parseDate accepts RFC 3339 timestamps only ("2024-05-01T12:30:00Z",
// "2024-05-01T12:30:00.5+02:00"); a bare date or any other layout is not a date.
func parseDate(s string) (time.Time, bool) {
	if s == "" || len(s) > maxDateBytes {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

// kind names a scalar's JSON type; "" for anything else.
func kind(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	}
	return ""
}

func sameKind(a, b any) bool {
	k := kind(a)
	return k != "" && k == kind(b)
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

// ClauseScope says where a clause is used, because segment clauses may only
// appear in flag rules.
type ClauseScope int

const (
	// FlagRuleClause: a clause in a flag's targeting rule.
	FlagRuleClause ClauseScope = iota
	// SegmentRuleClause: a clause in a segment's attribute rules. It may not
	// reference a segment, so segments cannot nest and no cycle can exist.
	SegmentRuleClause
)

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
		if err := c.Validate(FlagRuleClause); err != nil {
			return fmt.Errorf("clause %d: %w", i, err)
		}
	}
	return nil
}

// SegmentRefs returns the segment keys the rules reference, in first-seen order.
func SegmentRefs(rules []TargetingRule) []string {
	var keys []string
	seen := map[string]bool{}
	for _, r := range rules {
		for _, c := range r.Clauses {
			if c.Operator != OpInSegment && c.Operator != OpNotInSegment {
				continue
			}
			if k, ok := c.Value.(string); ok && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	return keys
}

// Validate checks one clause's operator and value. Error text has no clause
// index; callers add it.
func (c Clause) Validate(scope ClauseScope) error {
	switch c.Operator {
	case OpInSegment, OpNotInSegment:
		if scope == SegmentRuleClause {
			return fmt.Errorf("%s cannot be used in a segment's rules: segments cannot reference other segments", c.Operator)
		}
		if c.Attribute != "" {
			return fmt.Errorf("%s takes no attribute (it tests the user, not an attribute)", c.Operator)
		}
		if c.Negate {
			return fmt.Errorf("%s cannot be negated; use the opposite operator", c.Operator)
		}
		if k, ok := c.Value.(string); !ok || k == "" {
			return fmt.Errorf("%s needs the key of a segment as its value", c.Operator)
		}
		return nil
	}
	if c.Attribute == "" {
		return fmt.Errorf("attribute is required")
	}
	if c.Negate && c.Operator == OpExists {
		return fmt.Errorf("exists cannot be negated: a missing attribute never matches any clause")
	}
	switch c.Operator {
	case OpExists:
	case OpEquals:
		if !isScalar(c.Value) {
			return fmt.Errorf("equals needs a string, number, or boolean value")
		}
	case OpIn:
		list, ok := c.Value.([]any)
		if !ok || len(list) == 0 {
			return fmt.Errorf("in needs a non-empty array value")
		}
	case OpContains:
		if !isScalar(c.Value) {
			return fmt.Errorf("contains needs a string, number, or boolean value")
		}
	case OpGreaterThan, OpGreaterThanOrEqual, OpLessThan, OpLessThanOrEqual:
		if _, ok := c.Value.(float64); !ok {
			return fmt.Errorf("%s needs a number value", c.Operator)
		}
	case OpStartsWith, OpEndsWith:
		if s, ok := c.Value.(string); !ok || s == "" {
			return fmt.Errorf("%s needs a non-empty text value", c.Operator)
		}
	case OpSemverEquals, OpSemverLessThan, OpSemverLessOrEqual, OpSemverGreaterThan, OpSemverGreaterOrEqual:
		s, ok := c.Value.(string)
		if !ok {
			return fmt.Errorf("%s needs a version such as 1.4.0", c.Operator)
		}
		if _, ok := parseSemver(s); !ok {
			return fmt.Errorf("%s: %q is not a valid semantic version (MAJOR.MINOR.PATCH, optional -prerelease, no leading \"v\")", c.Operator, s)
		}
	case OpDateBefore, OpDateAfter, OpDateEquals:
		s, ok := c.Value.(string)
		if !ok {
			return fmt.Errorf("%s needs an RFC 3339 timestamp such as 2024-05-01T00:00:00Z", c.Operator)
		}
		if _, ok := parseDate(s); !ok {
			return fmt.Errorf("%s: %q is not an RFC 3339 timestamp such as 2024-05-01T00:00:00Z", c.Operator, s)
		}
	case OpMatchesRegex:
		s, ok := c.Value.(string)
		if !ok {
			return fmt.Errorf("%s needs a regular expression as text", c.Operator)
		}
		if err := ValidateRegex(s); err != nil {
			return fmt.Errorf("%s: %w", c.Operator, err)
		}
	default:
		return fmt.Errorf("unknown operator %q", c.Operator)
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
