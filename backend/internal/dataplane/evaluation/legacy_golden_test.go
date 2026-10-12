package evaluation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The golden file pins what flags that use none of the segment/operator
// features evaluate to, byte for byte: every result of a fixed corpus of
// configs and contexts, as the evaluator returned it BEFORE segments and the
// new operators existed (testdata/legacy_golden.txt was recorded from that
// code). Any change to how an old flag evaluates fails here.
//
// To re-record deliberately: UPDATE_GOLDEN=1 go test -run TestLegacyEvaluationIsByteForByteUnchanged
// (and say why in the commit message).
func legacyCorpus() []string {
	attrSets := []map[string]any{
		nil,
		{},
		{"plan": nil, "age": nil},
		{"plan": "pro", "age": float64(30), "country": "IN", "beta": true},
		{"plan": "free", "age": float64(17), "country": "US", "beta": false},
		{"plan": "", "age": float64(0), "country": ""},
		{"plan": float64(5), "age": "30", "country": []any{"IN", "US"}},
		{"tags": []any{"beta", "staff"}, "email": "a@helios.dev", "meta": map[string]any{"x": float64(1)}},
		{"tags": []any{}, "email": "ünï@çødé.dev", "age": float64(-4.5)},
		{"plan": "PRO", "country": "in", "age": float64(1e12)},
		{"beta": "true", "plan": true, "age": false},
	}
	values := []any{"pro", "", "IN", "@helios.dev", "beta", float64(5), float64(0), float64(18), float64(30), true, false, nil,
		[]any{"IN", "US"}, []any{"pro", float64(5)}, []any{}, map[string]any{"x": float64(1)}}
	attrNames := []string{"plan", "age", "country", "beta", "tags", "email", "meta", "missing"}
	ops := []string{OpEquals, OpIn, OpContains, OpGreaterThan, OpExists, "unknownOp", ""}

	var clauses []Clause
	for _, a := range attrNames {
		for _, op := range ops {
			for _, v := range values {
				clauses = append(clauses, Clause{Attribute: a, Operator: op, Value: v})
			}
		}
	}

	subjects := []string{"u1", "u2", "ünï", "", "user-12345"}
	variations := []Variation{{ID: "on", Value: true}, {ID: "off", Value: false}, {ID: "dim", Value: "x"}}
	var lines []string
	emit := func(label string, fc *FlagConfig) {
		for ai, attrs := range attrSets {
			for _, s := range subjects {
				r := Evaluate(fc, Context{SubjectKey: s, Attributes: attrs})
				b, _ := json.Marshal(r)
				lines = append(lines, fmt.Sprintf("%s a%d %q %s", label, ai, s, b))
			}
		}
	}
	for i, c := range clauses {
		if i%7 != 0 { // keep the file a reviewable size; the stride still covers every operator/attribute/value
			continue
		}
		emit(fmt.Sprintf("single%d", i), &FlagConfig{
			Key: "legacy", Enabled: true, Variations: variations, Salt: "s",
			TargetingRules:         []TargetingRule{{Clauses: []Clause{c}, VariationID: "on"}},
			FallthroughVariationID: "off",
		})
	}
	// Two-clause AND rules, multiple rules (first match wins), rollouts, disabled, dangling.
	for i := 0; i+1 < len(clauses) && i < 4000; i += 97 {
		emit(fmt.Sprintf("and%d", i), &FlagConfig{
			Key: "legacy", Enabled: true, Variations: variations, Salt: "s",
			TargetingRules: []TargetingRule{
				{Clauses: []Clause{clauses[i], clauses[i+1]}, VariationID: "dim"},
				{Clauses: []Clause{clauses[(i*3)%len(clauses)]}, VariationID: "on"},
			},
			Rollout:                map[string]uint32{"on": 30000, "off": 70000},
			FallthroughVariationID: "off",
		})
	}
	emit("disabled", &FlagConfig{Key: "legacy", Enabled: false, Variations: variations, FallthroughVariationID: "off"})
	emit("rollout3", &FlagConfig{Key: "legacy", Enabled: true, Variations: variations, Salt: "z",
		Rollout: map[string]uint32{"on": 33333, "off": 33333, "dim": 33334}, FallthroughVariationID: "off"})
	emit("dangling", &FlagConfig{Key: "legacy", Enabled: true, Variations: variations,
		TargetingRules:         []TargetingRule{{Clauses: []Clause{{Attribute: "plan", Operator: OpExists}}, VariationID: "gone"}},
		FallthroughVariationID: "gone"})
	lines = append(lines, "nil-config "+fmt.Sprint(Evaluate(nil, Context{SubjectKey: "u"})))
	return lines
}

func TestLegacyEvaluationIsByteForByteUnchanged(t *testing.T) {
	got := []byte(strings.Join(legacyCorpus(), "\n") + "\n")
	const path = "testdata/legacy_golden.txt"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d lines", bytes.Count(got, []byte("\n")))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		gl, wl := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) && i < len(wl); i++ {
			if gl[i] != wl[i] {
				t.Fatalf("legacy evaluation changed at line %d:\n got  %s\n want %s", i+1, gl[i], wl[i])
			}
		}
		t.Fatalf("legacy evaluation changed: %d lines now, %d recorded", len(gl), len(wl))
	}
}
