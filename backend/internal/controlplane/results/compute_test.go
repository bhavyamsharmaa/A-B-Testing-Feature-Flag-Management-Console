package results

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var t1 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// input builds a two-variation experiment with the given exposures, converters and rollout.
func input(exp map[string]int64, conv map[string]int64, rollout map[string]uint32) Inputs {
	start, now := t1, t1.Add(72*time.Hour)
	x := experimentRow{
		Key: "exp", FlagKey: "flag", Status: "running", StartedAt: &start,
		Variations:  []string{"control", "variant"},
		StartConfig: &startConfig{Rollout: rollout, TargetingRules: json.RawMessage("[]")},
	}
	sums := map[string]armSums{}
	for v, n := range exp {
		sums[v] = armSums{Subjects: n, Converters: conv[v], Events: conv[v], SumN2: float64(conv[v]), SumV: float64(conv[v]), SumV2: float64(conv[v])}
	}
	return Inputs{
		Experiment: x, Now: now, Exposures: exp,
		Metrics: []MetricInput{{Spec: MetricSpec{EventName: "buy", Kind: KindConversion, IsPrimary: true}, Sums: sums}},
	}
}

func even() map[string]uint32 { return map[string]uint32{"control": 50000, "variant": 50000} }

func TestComputeSignificantConversionDifference(t *testing.T) {
	r := Compute(input(map[string]int64{"control": 10000, "variant": 10000}, map[string]int64{"control": 1000, "variant": 1200}, even()))
	c := r.Metrics[0].Comparisons[0]
	if c.Status != StatusSignificant || c.PValue == nil || *c.PValue > 1e-4 || c.AdjustedPValue == nil || *c.AdjustedPValue != *c.PValue {
		t.Errorf("%+v", c)
	}
	if r.Control != "control" || r.Alpha != 0.05 || c.AlphaPerComparison != 0.05 || c.ConfidenceLevel != 0.95 {
		t.Errorf("header %+v / %+v", r, c)
	}
	if !r.Trustworthy || r.SRM.Detected || r.SRM.Confidence != "high" || r.SRM.ExpectedSource != "rollout_at_start" {
		t.Errorf("srm %+v", r.SRM)
	}
	if r.SampleSize != 20000 || r.PeekingNote == "" || len(r.Method) == 0 {
		t.Errorf("sample size / notes missing: %+v", r)
	}
}

func TestComputeBonferroniWithThreeVariations(t *testing.T) {
	in := input(map[string]int64{"control": 5000, "a": 5000, "b": 5000}, map[string]int64{"control": 500, "a": 560, "b": 520}, nil)
	in.Experiment.Variations = []string{"control", "a", "b"}
	in.Experiment.StartConfig.Rollout = map[string]uint32{"control": 33334, "a": 33333, "b": 33333}
	r := Compute(in)
	if len(r.Metrics[0].Comparisons) != 2 {
		t.Fatal("two comparisons expected")
	}
	for _, c := range r.Metrics[0].Comparisons {
		if c.AlphaPerComparison != 0.025 || c.ConfidenceLevel != 0.975 {
			t.Errorf("alpha per comparison: %+v", c)
		}
		if c.PValue == nil || c.AdjustedPValue == nil || *c.AdjustedPValue != min1(*c.PValue*2) {
			t.Errorf("adjusted p: %v vs %v", c.AdjustedPValue, c.PValue)
		}
	}
}

func min1(v float64) float64 {
	if v > 1 {
		return 1
	}
	return v
}

func TestComputeEdgeCases(t *testing.T) {
	t.Run("no exposures at all", func(t *testing.T) {
		r := Compute(input(map[string]int64{}, nil, even()))
		if r.SampleSize != 0 || r.SRM.PValue != nil || r.SRM.Reason == "" {
			t.Errorf("%+v", r.SRM)
		}
		for _, c := range r.Metrics[0].Comparisons {
			if c.Status != StatusInvalid || c.PValue != nil {
				t.Errorf("%+v", c)
			}
		}
		if !hasWarning(r, "NO_EXPOSURES") {
			t.Error("NO_EXPOSURES expected")
		}
	})
	t.Run("one arm empty", func(t *testing.T) {
		r := Compute(input(map[string]int64{"control": 500}, nil, even()))
		c := r.Metrics[0].Comparisons[0]
		if c.Status != StatusInvalid || c.StatusReasons[0] != "VARIANT_HAS_NO_EXPOSURES" {
			t.Errorf("%+v", c)
		}
		if !r.SRM.Detected { // 500 vs 0 against 50/50
			t.Errorf("an empty arm is a sample ratio mismatch: %+v", r.SRM)
		}
		r = Compute(input(map[string]int64{"variant": 500}, nil, even()))
		if c := r.Metrics[0].Comparisons[0]; c.StatusReasons[0] != "CONTROL_HAS_NO_EXPOSURES" {
			t.Errorf("%+v", c)
		}
	})
	t.Run("single variation", func(t *testing.T) {
		in := input(map[string]int64{"control": 500}, nil, nil)
		in.Experiment.Variations = []string{"control"}
		r := Compute(in)
		if len(r.Metrics[0].Comparisons) != 0 || !hasWarning(r, "SINGLE_VARIATION") {
			t.Errorf("%+v", r.Metrics[0])
		}
	})
	t.Run("insufficient data keeps descriptive numbers only", func(t *testing.T) {
		r := Compute(input(map[string]int64{"control": 99, "variant": 5000}, map[string]int64{"control": 30, "variant": 30}, even()))
		c := r.Metrics[0].Comparisons[0]
		if c.Status != StatusInsufficientData || c.PValue != nil || c.AdjustedPValue != nil || c.Difference == nil {
			t.Errorf("%+v", c)
		}
		// 100 exposures but only 20 conversions overall
		r = Compute(input(map[string]int64{"control": 100, "variant": 100}, map[string]int64{"control": 10, "variant": 10}, even()))
		if c := r.Metrics[0].Comparisons[0]; c.Status != StatusInsufficientData || c.StatusReasons[0] != "CONVERSIONS_BELOW_25" {
			t.Errorf("%+v", c)
		}
		// exactly at the minimums: a verdict
		r = Compute(input(map[string]int64{"control": 100, "variant": 100}, map[string]int64{"control": 10, "variant": 15}, even()))
		if c := r.Metrics[0].Comparisons[0]; c.Status == StatusInsufficientData {
			t.Errorf("100 exposures and 25 conversions are enough: %+v", c)
		}
	})
	t.Run("zero variance count metric", func(t *testing.T) {
		in := input(map[string]int64{"control": 200, "variant": 200}, nil, even())
		in.Metrics[0].Spec.Kind = KindCount
		in.Metrics[0].Sums = map[string]armSums{"control": {Subjects: 200}, "variant": {Subjects: 200}}
		c := Compute(in).Metrics[0].Comparisons[0]
		if c.Status != StatusInvalid || c.StatusReasons[0] != ReasonZeroVariance || c.PValue != nil {
			t.Errorf("%+v", c)
		}
	})
	t.Run("sample ratio mismatch makes everything untrustworthy", func(t *testing.T) {
		r := Compute(input(map[string]int64{"control": 10000, "variant": 12000}, map[string]int64{"control": 1000, "variant": 1500}, even()))
		if !r.SRM.Detected || r.Trustworthy || !hasWarning(r, "SRM_FAILED") {
			t.Fatalf("%+v", r.SRM)
		}
		for _, v := range r.Metrics[0].Variations {
			if v.Trustworthy {
				t.Errorf("variation %s still trusted", v.VariationID)
			}
		}
		for _, c := range r.Metrics[0].Comparisons {
			if c.Trustworthy {
				t.Error("comparison still trusted")
			}
		}
	})
	t.Run("no recorded rollout: equal split, low confidence", func(t *testing.T) {
		r := Compute(input(map[string]int64{"control": 500, "variant": 500}, nil, nil))
		if r.SRM.ExpectedSource != "equal_assumed" || r.SRM.Confidence != "low" || !hasWarning(r, "SRM_LOW_CONFIDENCE") {
			t.Errorf("%+v", r.SRM)
		}
	})
	t.Run("targeting rules at start lower the confidence", func(t *testing.T) {
		in := input(map[string]int64{"control": 500, "variant": 500}, nil, even())
		in.Experiment.StartConfig.TargetingRules = json.RawMessage(`[{"clauses":[],"variationId":"variant"}]`)
		if r := Compute(in); r.SRM.Confidence != "low" || r.SRM.ConfidenceReason == "" {
			t.Errorf("%+v", r.SRM)
		}
	})
	t.Run("unequal rollout is respected", func(t *testing.T) {
		r := Compute(input(map[string]int64{"control": 7500, "variant": 2500}, nil, map[string]uint32{"control": 75000, "variant": 25000}))
		if r.SRM.Detected {
			t.Errorf("75/25 observed 75/25: %+v", r.SRM)
		}
	})
}

func TestComputeWarnings(t *testing.T) {
	in := input(map[string]int64{"control": 500, "variant": 500}, nil, even())
	short := t1.Add(2 * time.Hour)
	in.Now = short
	in.Changes = []flagChange{
		{At: t1.Add(time.Hour), Action: "flag.update", Changed: []string{"rollout"}},
		{At: t1.Add(90 * time.Minute), Action: "flag.kill", Changed: []string{"enabled"}},
		{At: t1.Add(100 * time.Minute), Action: "flag.update", Changed: nil},
	}
	in.OtherRuns = []otherRun{{Key: "earlier", StartedAt: t1.Add(-240 * time.Hour)}}
	in.Metrics[0].Dropped = attribution{NoExposure: 3, BeforeExposure: 2, OutsideWindow: 1}
	r := Compute(in)
	for _, code := range []string{"FLAG_CONFIG_CHANGED", "EXPERIMENT_RESTARTED", "SHORT_RUN", "EVENTS_WITHOUT_EXPOSURE", "EVENTS_BEFORE_EXPOSURE", "EVENTS_OUTSIDE_WINDOW"} {
		if !hasWarning(r, code) {
			t.Errorf("missing warning %s", code)
		}
	}
	var changes []Warning
	for _, w := range r.Warnings {
		if w.Code == "FLAG_CONFIG_CHANGED" {
			changes = append(changes, w)
		}
	}
	if len(changes) != 2 { // the entry that changed nothing is not reported
		t.Fatalf("%d config-change warnings, want 2", len(changes))
	}
	if !strings.Contains(changes[0].Message, "rollout") || !strings.Contains(changes[0].Message, "2026-10-01 01:00 UTC") || changes[0].Severity != "warning" {
		t.Errorf("%+v", changes[0])
	}
	if changes[1].Severity != "info" { // only the enabled toggle
		t.Errorf("%+v", changes[1])
	}
}

func hasWarning(r Response, code string) bool {
	for _, w := range r.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestComputeNeverSerialisesNonFiniteNumbers(t *testing.T) {
	for _, in := range []Inputs{
		input(map[string]int64{}, nil, even()),
		input(map[string]int64{"control": 1, "variant": 1}, map[string]int64{"control": 1, "variant": 1}, even()),
		input(map[string]int64{"control": 1000, "variant": 1000}, map[string]int64{"control": 1000, "variant": 1000}, even()),
		input(map[string]int64{"control": 1000, "variant": 1000}, map[string]int64{}, even()),
	} {
		b, err := json.Marshal(Compute(in))
		if err != nil {
			t.Fatal(err) // encoding/json refuses NaN and Inf
		}
		if strings.Contains(string(b), "NaN") || strings.Contains(string(b), "Inf") {
			t.Errorf("%s", b)
		}
	}
}
