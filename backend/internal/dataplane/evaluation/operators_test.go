package evaluation

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

type opCase struct {
	name   string
	clause Clause
	attrs  map[string]any
	want   bool
}

func check(t *testing.T, cases []opCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.clause.Matches(tc.attrs); got != tc.want {
				t.Errorf("%s %s %v (negate=%v) on %v = %v, want %v",
					tc.clause.Attribute, tc.clause.Operator, tc.clause.Value, tc.clause.Negate, tc.attrs, got, tc.want)
			}
		})
	}
}

func cl(op string, value any) Clause { return Clause{Attribute: "a", Operator: op, Value: value} }
func neg(c Clause) Clause            { c.Negate = true; return c }
func at(v any) map[string]any        { return map[string]any{"a": v} }

func TestStringOperators(t *testing.T) {
	check(t, []opCase{
		{"startsWith yes", cl(OpStartsWith, "hel"), at("helios"), true},
		{"startsWith no", cl(OpStartsWith, "eli"), at("helios"), false},
		{"startsWith is case sensitive", cl(OpStartsWith, "HEL"), at("helios"), false},
		{"startsWith whole string", cl(OpStartsWith, "helios"), at("helios"), true},
		{"startsWith longer than input", cl(OpStartsWith, "helios!"), at("helios"), false},
		{"startsWith empty input", cl(OpStartsWith, "a"), at(""), false},
		{"startsWith unicode", cl(OpStartsWith, "café"), at("café au lait"), true},
		{"startsWith emoji", cl(OpStartsWith, "🚀"), at("🚀 launch"), true},
		{"startsWith not a prefix of a rune", cl(OpStartsWith, "\xc3"), at("é"), true}, // bytes, like contains: documented
		{"endsWith yes", cl(OpEndsWith, "@helios.dev"), at("a@helios.dev"), true},
		{"endsWith no", cl(OpEndsWith, "@helios.com"), at("a@helios.dev"), false},
		{"endsWith unicode", cl(OpEndsWith, "ñ"), at("niño"), false},
		{"endsWith unicode yes", cl(OpEndsWith, "ño"), at("niño"), true},
		{"startsWith number attribute", cl(OpStartsWith, "1"), at(float64(123)), false},
		{"startsWith number value", cl(OpStartsWith, float64(1)), at("123"), false},
		{"endsWith bool attribute", cl(OpEndsWith, "e"), at(true), false},
		{"startsWith array attribute", cl(OpStartsWith, "a"), at([]any{"a"}), false},
		{"startsWith object attribute", cl(OpStartsWith, "a"), at(map[string]any{"a": 1}), false},
		{"startsWith nil value", cl(OpStartsWith, nil), at("a"), false},
		{"negated startsWith", neg(cl(OpStartsWith, "hel")), at("helios"), false},
		{"negated startsWith other", neg(cl(OpStartsWith, "xyz")), at("helios"), true},
		{"negated startsWith on a number stays false", neg(cl(OpStartsWith, "1")), at(float64(1)), false},
		{"negated startsWith on a missing attribute stays false", neg(cl(OpStartsWith, "1")), map[string]any{}, false},
	})
}

func TestNumericOperators(t *testing.T) {
	check(t, []opCase{
		{"lt yes", cl(OpLessThan, float64(10)), at(float64(9)), true},
		{"lt equal", cl(OpLessThan, float64(10)), at(float64(10)), false},
		{"lte equal", cl(OpLessThanOrEqual, float64(10)), at(float64(10)), true},
		{"lte above", cl(OpLessThanOrEqual, float64(10)), at(float64(10.0001)), false},
		{"gte equal", cl(OpGreaterThanOrEqual, float64(10)), at(float64(10)), true},
		{"gte below", cl(OpGreaterThanOrEqual, float64(10)), at(float64(9.999)), false},
		{"gt unchanged", cl(OpGreaterThan, float64(10)), at(float64(10)), false},
		{"negative numbers", cl(OpLessThan, float64(-1)), at(float64(-2)), true},
		{"zero", cl(OpLessThanOrEqual, float64(0)), at(float64(0)), true},
		{"huge", cl(OpLessThan, 1e308), at(float64(1e307)), true},
		{"string attribute is not coerced", cl(OpLessThan, float64(10)), at("5"), false},
		{"string value is not coerced", cl(OpLessThan, "10"), at(float64(5)), false},
		{"bool attribute", cl(OpGreaterThanOrEqual, float64(0)), at(true), false},
		{"array attribute", cl(OpLessThan, float64(1)), at([]any{float64(0)}), false},
		{"negated lt is gte", neg(cl(OpLessThan, float64(10))), at(float64(10)), true},
		{"negated lt below", neg(cl(OpLessThan, float64(10))), at(float64(9)), false},
		{"negated lt on a string stays false", neg(cl(OpLessThan, float64(10))), at("5"), false},
		{"negated gt on a number", neg(cl(OpGreaterThan, float64(10))), at(float64(3)), true},
	})
}

func TestNegationOfEveryLegacyOperator(t *testing.T) {
	attrs := map[string]any{"s": "pro", "n": float64(5), "b": true, "tags": []any{"x", "y"}}
	check(t, []opCase{
		{"not equals", neg(Clause{Attribute: "s", Operator: OpEquals, Value: "free"}), attrs, true},
		{"not equals, equal", neg(Clause{Attribute: "s", Operator: OpEquals, Value: "pro"}), attrs, false},
		{"not equals, other type stays false", neg(Clause{Attribute: "s", Operator: OpEquals, Value: float64(5)}), attrs, false},
		{"not equals bool", neg(Clause{Attribute: "b", Operator: OpEquals, Value: false}), attrs, true},
		{"not in", neg(Clause{Attribute: "s", Operator: OpIn, Value: []any{"a", "b"}}), attrs, true},
		{"not in, member", neg(Clause{Attribute: "s", Operator: OpIn, Value: []any{"a", "pro"}}), attrs, false},
		{"not in, list of another type stays false", neg(Clause{Attribute: "s", Operator: OpIn, Value: []any{float64(1)}}), attrs, false},
		{"not in on an array attribute stays false", neg(Clause{Attribute: "tags", Operator: OpIn, Value: []any{"x"}}), attrs, false},
		{"not contains", neg(Clause{Attribute: "s", Operator: OpContains, Value: "zzz"}), attrs, true},
		{"not contains, contained", neg(Clause{Attribute: "s", Operator: OpContains, Value: "ro"}), attrs, false},
		{"not contains in array", neg(Clause{Attribute: "tags", Operator: OpContains, Value: "z"}), attrs, true},
		{"not contains, number in string stays false", neg(Clause{Attribute: "s", Operator: OpContains, Value: float64(5)}), attrs, false},
		{"not contains on a number stays false", neg(Clause{Attribute: "n", Operator: OpContains, Value: "5"}), attrs, false},
		{"missing attribute is false even negated", neg(Clause{Attribute: "nope", Operator: OpEquals, Value: "x"}), attrs, false},
	})
}

func TestSemverOperators(t *testing.T) {
	// Precedence from semver.org §11: each is lower than the next.
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0", "10.0.0"}
	for i := range ordered {
		for j := range ordered {
			a, b := ordered[i], ordered[j]
			lt := cl(OpSemverLessThan, b)
			if got := lt.Matches(at(a)); got != (i < j) {
				t.Errorf("%s < %s = %v, want %v", a, b, got, i < j)
			}
			gte := cl(OpSemverGreaterOrEqual, b)
			if got := gte.Matches(at(a)); got != (i >= j) {
				t.Errorf("%s >= %s = %v, want %v", a, b, got, i >= j)
			}
			eq := cl(OpSemverEquals, b)
			if got := eq.Matches(at(a)); got != (i == j) {
				t.Errorf("%s == %s = %v, want %v", a, b, got, i == j)
			}
		}
	}
	check(t, []opCase{
		{"build metadata is ignored", cl(OpSemverEquals, "1.2.3"), at("1.2.3+build.7"), true},
		{"build metadata on both", cl(OpSemverEquals, "1.2.3+a"), at("1.2.3+b"), true},
		{"numeric parts compare as numbers, not text", cl(OpSemverGreaterThan, "1.9.0"), at("1.10.0"), true},
		{"gt", cl(OpSemverGreaterThan, "1.2.3"), at("1.2.4"), true},
		{"lte equal", cl(OpSemverLessOrEqual, "1.2.3"), at("1.2.3"), true},
		{"negated lt is gte for valid versions", neg(cl(OpSemverLessThan, "1.2.3")), at("1.2.3"), true},
		{"very long numeric prerelease still orders", cl(OpSemverLessThan, "1.0.0-"+strings.Repeat("9", 30)), at("1.0.0-" + strings.Repeat("9", 29)), true},
	})
	// Invalid semver is a non-match, plain or negated, never an error.
	for _, bad := range []string{"", "1", "1.2", "1.2.3.4", "v1.2.3", "V1.2.3", "01.2.3", "1.02.3", "1.2.03", "1.2.3-", "1.2.3-01", "1.2.3-a..b", "1.2.3+", "1.2.3+a..b",
		"a.b.c", "1.2.x", " 1.2.3", "1.2.3 ", "1.2.-3", "99999999999999999999.0.0", "1.2.3-é", strings.Repeat("1", 500) + ".0.0", "1.2.3-" + strings.Repeat("a", 200)} {
		for _, op := range []string{OpSemverEquals, OpSemverLessThan, OpSemverLessOrEqual, OpSemverGreaterThan, OpSemverGreaterOrEqual} {
			for _, negate := range []bool{false, true} {
				c := Clause{Attribute: "a", Operator: op, Value: "1.2.3", Negate: negate}
				if c.Matches(at(bad)) {
					t.Errorf("%s on invalid attribute %q (negate=%v) matched", op, bad, negate)
				}
				c.Value = bad
				if c.Matches(at("1.2.3")) {
					t.Errorf("%s against invalid clause value %q (negate=%v) matched", op, bad, negate)
				}
			}
		}
	}
	for _, wrong := range []any{float64(1.2), true, []any{"1.2.3"}, map[string]any{}} {
		if cl(OpSemverEquals, "1.2.3").Matches(at(wrong)) || neg(cl(OpSemverEquals, "1.2.3")).Matches(at(wrong)) {
			t.Errorf("semverEquals matched a %T attribute", wrong)
		}
	}
}

func TestDateOperators(t *testing.T) {
	check(t, []opCase{
		{"before", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at("2024-05-31T23:59:59Z"), true},
		{"before, equal", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at("2024-06-01T00:00:00Z"), false},
		{"after", cl(OpDateAfter, "2024-06-01T00:00:00Z"), at("2024-06-01T00:00:01Z"), true},
		{"equal across offsets", cl(OpDateEquals, "2024-06-01T02:00:00+02:00"), at("2024-06-01T00:00:00Z"), true},
		{"before across offsets", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at("2024-06-01T00:30:00+01:00"), true},
		{"fractional seconds", cl(OpDateAfter, "2024-06-01T00:00:00Z"), at("2024-06-01T00:00:00.5Z"), true},
		{"leap day", cl(OpDateEquals, "2024-02-29T12:00:00Z"), at("2024-02-29T12:00:00Z"), true},
		{"negated before is on or after", neg(cl(OpDateBefore, "2024-06-01T00:00:00Z")), at("2024-06-01T00:00:00Z"), true},
		{"negated before, earlier", neg(cl(OpDateBefore, "2024-06-01T00:00:00Z")), at("2024-01-01T00:00:00Z"), false},
		{"date only is not RFC 3339", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at("2024-05-01"), false},
		{"no zone is not RFC 3339", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at("2024-05-01T00:00:00"), false},
		{"space instead of T", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at("2024-05-01 00:00:00Z"), false},
		{"month 13", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at("2024-13-01T00:00:00Z"), false},
		{"feb 30", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at("2024-02-30T00:00:00Z"), false},
		{"not a leap year", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at("2023-02-29T00:00:00Z"), false},
		{"offset without colon", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at("2024-05-01T00:00:00+0200"), false},
		{"prose", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at("May 1st 2024"), false},
		{"empty", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at(""), false},
		{"epoch number is not a date", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at(float64(1714521600)), false},
		{"invalid clause value", cl(OpDateBefore, "yesterday"), at("2024-05-01T00:00:00Z"), false},
		{"negated, invalid attribute stays false", neg(cl(OpDateBefore, "2024-06-01T00:00:00Z")), at("nope"), false},
		{"huge string", cl(OpDateBefore, "2024-06-01T00:00:00Z"), at(strings.Repeat("2", 1<<20)), false},
	})
}

func TestRegexOperator(t *testing.T) {
	check(t, []opCase{
		{"matches anywhere (unanchored)", cl(OpMatchesRegex, "ell"), at("hello"), true},
		{"anchored", cl(OpMatchesRegex, "^ell$"), at("hello"), false},
		{"anchored yes", cl(OpMatchesRegex, "^h.*o$"), at("hello"), true},
		{"character class", cl(OpMatchesRegex, `^[a-z]+@helios\.dev$`), at("alice@helios.dev"), true},
		{"case-insensitive flag", cl(OpMatchesRegex, `(?i)^PRO$`), at("pro"), true},
		{"unicode class", cl(OpMatchesRegex, `^\p{L}+$`), at("héllo"), true},
		{"unicode no", cl(OpMatchesRegex, `^\p{L}+$`), at("h3llo"), false},
		{"empty input, empty-matching pattern", cl(OpMatchesRegex, `^$`), at(""), true},
		{"empty input", cl(OpMatchesRegex, `a`), at(""), false},
		{"negated", neg(cl(OpMatchesRegex, `^pro`)), at("free"), true},
		{"negated, matches", neg(cl(OpMatchesRegex, `^pro`)), at("pro-plus"), false},
		{"number attribute", cl(OpMatchesRegex, `\d+`), at(float64(5)), false},
		{"negated number attribute stays false", neg(cl(OpMatchesRegex, `\d+`)), at(float64(5)), false},
		{"number pattern", cl(OpMatchesRegex, float64(5)), at("5"), false},
		{"invalid UTF-8 input does not panic", cl(OpMatchesRegex, `^.+$`), at("\xff\xfe"), true},
		{"pattern that does not compile is a non-match", cl(OpMatchesRegex, `(`), at("("), false},
		{"negated pattern that does not compile is a non-match", neg(cl(OpMatchesRegex, `(`)), at("x"), false},
		{"input at the cap", cl(OpMatchesRegex, `^a+$`), at(strings.Repeat("a", MaxRegexInputBytes)), true},
		{"input over the cap is a non-match", cl(OpMatchesRegex, `^a+$`), at(strings.Repeat("a", MaxRegexInputBytes+1)), false},
		{"input over the cap is a non-match, negated too", neg(cl(OpMatchesRegex, `^a+$`)), at(strings.Repeat("a", MaxRegexInputBytes+1)), false},
		{"huge input", cl(OpMatchesRegex, `a`), at(strings.Repeat("a", 50<<20)), false},
	})
}

// (a+)+$ and friends take exponential time in a backtracking engine. Go's
// regexp is RE2, so each of these must finish in milliseconds.
func TestCatastrophicRegexFinishesQuickly(t *testing.T) {
	evil := []string{`(a+)+$`, `^(a+)+$`, `(a|aa)+$`, `^(a|a?)+$`, `(.*a){20}`, `^(([a-z])+.)+[A-Z]([a-z])+$`, `(x+x+)+y`, `^(a*)*$`, `(a?){30}a{30}`}
	input := strings.Repeat("a", MaxRegexInputBytes-1) + "!"
	start := time.Now()
	for _, p := range evil {
		if err := ValidateRegex(p); err != nil {
			t.Errorf("%q should be a valid pattern: %v", p, err)
			continue
		}
		c := cl(OpMatchesRegex, p)
		c.Matches(at(input))
		neg(c).Matches(at(input))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("catastrophic patterns took %v: matching is not linear time", d)
	}
}

func TestValidateRegex(t *testing.T) {
	for _, ok := range []string{`a`, `^[a-z]+$`, `(?i)abc`, `\d{1,3}`, `(a+)+$`, strings.Repeat("a", MaxRegexPatternBytes)} {
		if err := ValidateRegex(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "(", "[a-", `a{2,1}`, `(?<n>a)\1`, `*a`, `a**`, `\8`, `(?P<n>`, `a{1001}`, `((a{1000}){1000}){1000}`, "\xff", strings.Repeat("a", MaxRegexPatternBytes+1)} {
		if err := ValidateRegex(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Whatever an attribute holds, no operator panics, and a clause on a value of
// the wrong kind is false whether negated or not.
func TestNoOperatorPanicsOnAnyAttributeKind(t *testing.T) {
	ops := []string{OpEquals, OpIn, OpContains, OpGreaterThan, OpExists, OpStartsWith, OpEndsWith, OpLessThan, OpLessThanOrEqual, OpGreaterThanOrEqual,
		OpSemverEquals, OpSemverLessThan, OpSemverLessOrEqual, OpSemverGreaterThan, OpSemverGreaterOrEqual, OpDateBefore, OpDateAfter, OpDateEquals, OpMatchesRegex,
		OpInSegment, OpNotInSegment, "", "nonsense"}
	kinds := []any{nil, "", "x", float64(0), float64(-1), true, false, []any{}, []any{"x", float64(1)}, []any{[]any{}}, map[string]any{}, map[string]any{"a": []any{}}, struct{}{}, 5, int64(5)}
	for _, op := range ops {
		for _, attr := range kinds {
			for _, val := range kinds {
				for _, negate := range []bool{false, true} {
					c := Clause{Attribute: "a", Operator: op, Value: val, Negate: negate}
					func() {
						defer func() {
							if r := recover(); r != nil {
								t.Errorf("panic: %s attr=%#v value=%#v negate=%v: %v", op, attr, val, negate, r)
							}
						}()
						_ = c.Matches(at(attr))
						_ = TargetingRule{Clauses: []Clause{c}}.MatchesIn(Context{SubjectKey: "u", Attributes: at(attr)})
					}()
				}
			}
		}
	}
}

func TestMismatchedKindsAreNeverAMatch(t *testing.T) {
	// operator -> an attribute kind it cannot compare, with a value it could
	// otherwise accept
	cases := map[string]struct{ attr, value any }{
		OpEquals:             {float64(1), "1"},
		OpStartsWith:         {float64(1), "1"},
		OpEndsWith:           {true, "e"},
		OpLessThan:           {"1", float64(2)},
		OpLessThanOrEqual:    {"1", float64(2)},
		OpGreaterThanOrEqual: {"3", float64(2)},
		OpGreaterThan:        {"3", float64(2)},
		OpSemverEquals:       {float64(1), "1.0.0"},
		OpDateAfter:          {float64(1), "2024-01-01T00:00:00Z"},
		OpMatchesRegex:       {float64(1), "1"},
	}
	for op, tc := range cases {
		for _, negate := range []bool{false, true} {
			c := Clause{Attribute: "a", Operator: op, Value: tc.value, Negate: negate}
			if c.Matches(at(tc.attr)) {
				t.Errorf("%s (negate=%v) matched attribute %#v", op, negate, tc.attr)
			}
		}
	}
}

func TestValidateNewOperators(t *testing.T) {
	good := []Clause{
		{Attribute: "a", Operator: OpStartsWith, Value: "x"},
		{Attribute: "a", Operator: OpEndsWith, Value: "x", Negate: true},
		{Attribute: "a", Operator: OpLessThan, Value: float64(1)},
		{Attribute: "a", Operator: OpLessThanOrEqual, Value: float64(1)},
		{Attribute: "a", Operator: OpGreaterThanOrEqual, Value: float64(1)},
		{Attribute: "a", Operator: OpSemverGreaterOrEqual, Value: "1.2.3-rc.1+b"},
		{Attribute: "a", Operator: OpDateAfter, Value: "2024-01-01T00:00:00Z"},
		{Attribute: "a", Operator: OpMatchesRegex, Value: `^a+$`},
		{Attribute: "a", Operator: OpEquals, Value: "x", Negate: true},
		{Attribute: "", Operator: OpInSegment, Value: "beta"},
		{Operator: OpNotInSegment, Value: "beta"},
	}
	for _, c := range good {
		if err := c.Validate(FlagRuleClause); err != nil {
			t.Errorf("%+v rejected: %v", c, err)
		}
	}
	bad := map[string]Clause{
		"startsWith empty":          {Attribute: "a", Operator: OpStartsWith, Value: ""},
		"startsWith number":         {Attribute: "a", Operator: OpStartsWith, Value: float64(1)},
		"lessThan string":           {Attribute: "a", Operator: OpLessThan, Value: "1"},
		"semver invalid":            {Attribute: "a", Operator: OpSemverEquals, Value: "1.2"},
		"semver v prefix":           {Attribute: "a", Operator: OpSemverEquals, Value: "v1.2.3"},
		"semver number":             {Attribute: "a", Operator: OpSemverEquals, Value: float64(1)},
		"date only":                 {Attribute: "a", Operator: OpDateBefore, Value: "2024-01-01"},
		"date number":               {Attribute: "a", Operator: OpDateBefore, Value: float64(1)},
		"regex invalid":             {Attribute: "a", Operator: OpMatchesRegex, Value: "("},
		"regex too long":            {Attribute: "a", Operator: OpMatchesRegex, Value: strings.Repeat("a", MaxRegexPatternBytes+1)},
		"regex number":              {Attribute: "a", Operator: OpMatchesRegex, Value: float64(1)},
		"negated exists":            {Attribute: "a", Operator: OpExists, Negate: true},
		"no attribute":              {Operator: OpStartsWith, Value: "x"},
		"segment with attribute":    {Attribute: "a", Operator: OpInSegment, Value: "s"},
		"segment without key":       {Operator: OpInSegment},
		"segment key not text":      {Operator: OpInSegment, Value: float64(1)},
		"segment empty key":         {Operator: OpNotInSegment, Value: ""},
		"negated inSegment":         {Operator: OpInSegment, Value: "s", Negate: true},
		"unknown operator":          {Attribute: "a", Operator: "regex", Value: "x"},
		"old unknown operator name": {Attribute: "a", Operator: "greaterthan", Value: float64(1)},
	}
	for name, c := range bad {
		if err := c.Validate(FlagRuleClause); err == nil {
			t.Errorf("%s: accepted", name)
		} else if err.Error() == "" {
			t.Errorf("%s: empty error", name)
		}
	}
	// A segment's own rules may not reference a segment.
	for _, op := range []string{OpInSegment, OpNotInSegment} {
		err := Clause{Operator: op, Value: "other"}.Validate(SegmentRuleClause)
		if err == nil || !strings.Contains(err.Error(), "cannot reference other segments") {
			t.Errorf("%s in a segment rule: %v", op, err)
		}
	}
	if err := (Clause{Attribute: "a", Operator: OpMatchesRegex, Value: `^a$`}).Validate(SegmentRuleClause); err != nil {
		t.Errorf("a normal clause in a segment rule: %v", err)
	}
}

func TestSegmentRefs(t *testing.T) {
	rules := []TargetingRule{
		{Clauses: []Clause{{Operator: OpInSegment, Value: "b"}, {Attribute: "x", Operator: OpExists}}},
		{Clauses: []Clause{{Operator: OpNotInSegment, Value: "a"}, {Operator: OpInSegment, Value: "b"}}},
	}
	got := fmt.Sprint(SegmentRefs(rules))
	if got != "[b a]" {
		t.Errorf("SegmentRefs = %s", got)
	}
}

// Every operator in AllOperators is accepted by Validate with a suitable value,
// and the list has no repeats: the rollback check (segments.UsesNewFeaturesSQL)
// and its test are built from it.
func TestAllOperatorsAreValidatable(t *testing.T) {
	values := map[string]any{
		OpEquals: "x", OpIn: []any{"x"}, OpContains: "x", OpGreaterThan: float64(1), OpExists: nil,
		OpStartsWith: "x", OpEndsWith: "x", OpLessThan: float64(1), OpLessThanOrEqual: float64(1), OpGreaterThanOrEqual: float64(1),
		OpSemverEquals: "1.0.0", OpSemverLessThan: "1.0.0", OpSemverLessOrEqual: "1.0.0", OpSemverGreaterThan: "1.0.0", OpSemverGreaterOrEqual: "1.0.0",
		OpDateBefore: "2024-01-01T00:00:00Z", OpDateAfter: "2024-01-01T00:00:00Z", OpDateEquals: "2024-01-01T00:00:00Z",
		OpMatchesRegex: "^a", OpInSegment: "s", OpNotInSegment: "s",
	}
	seen := map[string]bool{}
	for _, op := range AllOperators {
		if seen[op] {
			t.Errorf("%s listed twice", op)
		}
		seen[op] = true
		attr := "a"
		if op == OpInSegment || op == OpNotInSegment {
			attr = ""
		}
		if err := (Clause{Attribute: attr, Operator: op, Value: values[op]}).Validate(FlagRuleClause); err != nil {
			t.Errorf("%s: %v", op, err)
		}
	}
	if len(AllOperators) != len(values) {
		t.Errorf("%d operators listed, %d have test values", len(AllOperators), len(values))
	}
}
