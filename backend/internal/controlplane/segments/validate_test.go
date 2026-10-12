package segments

import (
	"fmt"
	"strings"
	"testing"

	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/dataplane/evaluation"
)

func TestCleanMembers(t *testing.T) {
	got, err := cleanMembers([]string{"b", "a", "b", "a", "c"})
	if err != nil || strings.Join(got, ",") != "b,a,c" {
		t.Errorf("de-duplication keeps first-seen order: %v %v", got, err)
	}
	if got, err := cleanMembers(nil); err != nil || len(got) != 0 {
		t.Errorf("no members: %v %v", got, err)
	}
	if _, err := cleanMembers([]string{" padded "}); err != nil {
		t.Errorf("keys are exact, not trimmed or rejected for spaces: %v", err)
	}
	for name, keys := range map[string][]string{
		"empty":   {"a", ""},
		"newline": {"a\nb"},
		"tab":     {"a\tb"},
		"NUL":     {"a\x00b"},
		"DEL":     {"a\x7fb"},
		"long":    {strings.Repeat("é", MaxSubjectKeyRunes+1)},
		"binary":  {"\xff\xfe"},
	} {
		_, err := cleanMembers(keys)
		if _, ok := err.(validationError); !ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := cleanMembers([]string{strings.Repeat("é", MaxSubjectKeyRunes)}); err != nil {
		t.Errorf("a key of exactly the limit (in characters, not bytes): %v", err)
	}
}

func TestCleanMembersLimit(t *testing.T) {
	keys := make([]string, quota.MaxSegmentMembers)
	for i := range keys {
		keys[i] = fmt.Sprint("u", i)
	}
	if got, err := cleanMembers(append(keys, keys...)); err != nil || len(got) != quota.MaxSegmentMembers {
		t.Fatalf("exactly the limit, twice over: %d %v", len(got), err)
	}
	_, err := cleanMembers(append(keys, "one-more"))
	if _, ok := err.(quota.Exceeded); !ok {
		t.Fatalf("over the limit: %v", err)
	}
}

func TestValidateRules(t *testing.T) {
	ok := []evaluation.Clause{{Attribute: "plan", Operator: "equals", Value: "pro"}, {Attribute: "v", Operator: "matchesRegex", Value: "^a"}}
	if err := validateRules(ok); err != nil {
		t.Fatal(err)
	}
	if err := validateRules(nil); err != nil {
		t.Errorf("no rules: %v", err)
	}
	for _, op := range []string{"inSegment", "notInSegment"} {
		err := validateRules([]evaluation.Clause{{Operator: op, Value: "other"}})
		if _, isBad := err.(validationError); !isBad || !strings.Contains(err.Error(), "cannot reference other segments") {
			t.Errorf("%s: %v", op, err)
		}
	}
	if err := validateRules([]evaluation.Clause{{Attribute: "a", Operator: "matchesRegex", Value: "("}}); err == nil {
		t.Error("invalid regex accepted")
	}
	many := make([]evaluation.Clause, quota.MaxSegmentRuleClauses+1)
	for i := range many {
		many[i] = evaluation.Clause{Attribute: "a", Operator: "exists"}
	}
	if _, isQuota := validateRules(many).(quota.Exceeded); !isQuota {
		t.Error("too many clauses")
	}
	big := evaluation.Clause{Attribute: "a", Operator: "in", Value: []any{strings.Repeat("x", quota.MaxSegmentRuleBytes)}}
	if _, isQuota := validateRules([]evaluation.Clause{big}).(quota.Exceeded); !isQuota {
		t.Error("rule definition over the byte limit")
	}
}

func TestDiffKeys(t *testing.T) {
	added, removed := diffKeys([]string{"a", "b", "c"}, []string{"c", "d", "b", "e"})
	if strings.Join(added, ",") != "d,e" || strings.Join(removed, ",") != "a" {
		t.Errorf("added %v removed %v", added, removed)
	}
	if a, r := diffKeys([]string{"a"}, []string{"a"}); len(a)+len(r) != 0 {
		t.Errorf("no change: %v %v", a, r)
	}
}

func TestSampleIsBounded(t *testing.T) {
	keys := make([]string, 5000)
	for i := range keys {
		keys[i] = fmt.Sprint(i)
	}
	if n := len(sample(keys)); n != auditSampleSize {
		t.Errorf("sample of %d keys has %d", len(keys), n)
	}
}
