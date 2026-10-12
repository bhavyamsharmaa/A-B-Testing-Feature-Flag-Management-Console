package evaluation

import (
	"fmt"
	"testing"
	"time"
)

func segFlag(op, seg string) *FlagConfig {
	fc := boolFlag()
	fc.TargetingRules = []TargetingRule{{Clauses: []Clause{{Operator: op, Value: seg}}, VariationID: "on"}}
	return fc
}

func betaSegment() Segment {
	return Segment{
		Key:     "beta",
		Members: MapMembers{"vip-1": {}, "vip-2": {}},
		Rules:   []Clause{{Attribute: "plan", Operator: OpEquals, Value: "pro"}, {Attribute: "country", Operator: OpEquals, Value: "IN"}},
	}
}

func TestSegmentMembershipIsMemberOrRules(t *testing.T) {
	segs := map[string]Segment{"beta": betaSegment()}
	cases := []struct {
		name    string
		subject string
		attrs   map[string]any
		in      bool
	}{
		{"explicit member, no attributes", "vip-1", nil, true},
		{"explicit member who fails the rules", "vip-2", map[string]any{"plan": "free"}, true},
		{"rules match, not a member", "u9", map[string]any{"plan": "pro", "country": "IN"}, true},
		{"rules are ANDed: one clause is not enough", "u9", map[string]any{"plan": "pro", "country": "US"}, false},
		{"neither", "u9", map[string]any{"plan": "free"}, false},
		{"neither, no attributes", "u9", nil, false},
		{"empty subject is never a member", "", nil, false},
		{"member keys are exact (case)", "VIP-1", nil, false},
	}
	for _, tc := range cases {
		ctx := Context{SubjectKey: tc.subject, Attributes: tc.attrs, Segments: segs}
		if got := Evaluate(segFlag(OpInSegment, "beta"), ctx); (got.Reason == ReasonTargetingMatch) != tc.in {
			t.Errorf("inSegment %s: got %+v, want in=%v", tc.name, got, tc.in)
		}
		out := Evaluate(segFlag(OpNotInSegment, "beta"), ctx)
		if (out.Reason == ReasonTargetingMatch) != !tc.in {
			t.Errorf("notInSegment %s: got %+v, want match=%v", tc.name, out, !tc.in)
		}
	}
}

func TestSegmentWithOnlyMembersOrOnlyRules(t *testing.T) {
	onlyMembers := Segment{Key: "m", Members: MapMembers{"a": {}}}
	if onlyMembers.Contains("b", map[string]any{"x": 1}) || !onlyMembers.Contains("a", nil) {
		t.Error("a segment with no rules must contain exactly its members")
	}
	onlyRules := Segment{Key: "r", Rules: []Clause{{Attribute: "x", Operator: OpExists}}}
	if !onlyRules.Contains("b", map[string]any{"x": 1}) || onlyRules.Contains("b", nil) {
		t.Error("a segment with no members must contain exactly what its rules match")
	}
	if (Segment{Key: "empty"}).Contains("a", map[string]any{"x": 1}) {
		t.Error("an empty segment contains nobody")
	}
}

// A flag whose config still names a segment that was deleted: nobody is in it,
// and nobody is "not in" it either. A stale reference must never widen a rule.
func TestDeletedSegmentReferencedByStaleConfigMatchesNobody(t *testing.T) {
	for _, op := range []string{OpInSegment, OpNotInSegment} {
		for _, segs := range []map[string]Segment{nil, {}, {"other": betaSegment()}} {
			got := Evaluate(segFlag(op, "beta"), Context{SubjectKey: "vip-1", Attributes: map[string]any{"plan": "pro", "country": "IN"}, Segments: segs})
			if got.Reason != ReasonFallthrough {
				t.Errorf("%s with missing segment: got %+v, want the fallthrough", op, got)
			}
		}
	}
}

func TestSegmentRulesCannotRecurse(t *testing.T) {
	// Validation forbids this; if bad data got in anyway, it is a non-match
	// rather than recursion.
	loop := Segment{Key: "loop", Rules: []Clause{{Operator: OpInSegment, Value: "loop"}}}
	segs := map[string]Segment{"loop": loop}
	got := Evaluate(segFlag(OpInSegment, "loop"), Context{SubjectKey: "u", Segments: segs})
	if got.Reason != ReasonFallthrough {
		t.Errorf("self-referencing segment: %+v", got)
	}
}

func TestSubjectMemberSet(t *testing.T) {
	seg := Segment{Key: "s", Members: SubjectMember{Subject: "u1", In: true}}
	if !seg.Contains("u1", nil) || seg.Contains("u2", nil) {
		t.Error("SubjectMember answers for its one subject only")
	}
	if (Segment{Key: "s", Members: SubjectMember{Subject: "u1", In: false}}).Contains("u1", nil) {
		t.Error("SubjectMember with In=false")
	}
}

// Performance sanity, not a benchmark: membership is a hash lookup, so a huge
// segment costs the same per evaluation as a tiny one. A linear scan of 200k
// members would take minutes over 200k evaluations.
func TestLargeSegmentLookupDoesNotScan(t *testing.T) {
	const n = 200_000
	members := make(MapMembers, n)
	for i := 0; i < n; i++ {
		members[fmt.Sprintf("user-%d", i)] = struct{}{}
	}
	seg := Segment{Key: "big", Members: members}
	fc := segFlag(OpInSegment, "big")
	segs := map[string]Segment{"big": seg}
	start := time.Now()
	hits := 0
	for i := 0; i < n; i++ {
		// half members, half strangers
		subject := fmt.Sprintf("user-%d", i)
		if i%2 == 1 {
			subject = fmt.Sprintf("stranger-%d", i)
		}
		if Evaluate(fc, Context{SubjectKey: subject, Segments: segs}).Reason == ReasonTargetingMatch {
			hits++
		}
	}
	if hits != n/2 {
		t.Fatalf("hits = %d, want %d", hits, n/2)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("%d evaluations against a %d-member segment took %v", n, n, d)
	}
}

func TestSegmentClauseWithoutSegmentsDoesNotMatch(t *testing.T) {
	c := Clause{Operator: OpInSegment, Value: "beta"}
	if c.Matches(map[string]any{"plan": "pro"}) {
		t.Error("Clause.Matches knows no segments")
	}
	if (TargetingRule{Clauses: []Clause{c}}).Matches(nil) {
		t.Error("TargetingRule.Matches knows no segments")
	}
}
