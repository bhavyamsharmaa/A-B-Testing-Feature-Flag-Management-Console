package results

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Inputs is everything Compute needs; it is plain data so the arithmetic is
// testable without a database.
type Inputs struct {
	Experiment experimentRow
	Now        time.Time
	Exposures  map[string]int64 // variation id -> exposed subjects
	Metrics    []MetricInput
	Changes    []flagChange
	OtherRuns  []otherRun
	// UnverifiedExposures accepted without a verifying exposure token.
	UnverifiedExposures int64
}

// MetricInput is one requested metric with its aggregated sums.
type MetricInput struct {
	Spec    MetricSpec
	Sums    map[string]armSums
	Dropped attribution
}

const peekingNote = "These p-values and intervals are valid only for a sample size fixed in advance. " +
	"Looking at results repeatedly while the experiment runs and stopping when a difference appears " +
	"inflates the false-positive rate well above 5%. The sample size at this reading is given in sampleSize. " +
	"No sequential correction is applied."

// Compute turns counts into the response. It never returns NaN or Infinity.
func Compute(in Inputs) Response {
	x := in.Experiment
	resp := Response{
		ExperimentKey: x.Key, FlagKey: x.FlagKey, Status: x.Status, ReadAt: in.Now.UTC(),
		Alpha: Alpha, PeekingNote: peekingNote,
		Thresholds: Thresholds{MinExposuresPerVariation, MinConversionsTotal, SRMThreshold},
		Method:     methodNotes(),
		Warnings:   []Warning{},
	}

	// Run window: started_at to stopped_at, or now while running.
	start := in.Now
	if x.StartedAt != nil {
		start = *x.StartedAt
	}
	end := in.Now
	if x.StoppedAt != nil {
		end = *x.StoppedAt
	}
	resp.Window = Window{Start: start.UTC(), End: end.UTC(), Stopped: x.StoppedAt, Running: x.Status == "running", Hours: round2(end.Sub(start).Hours())}

	// Variations in the flag's order; anything observed but unknown goes last.
	variations := append([]string(nil), x.Variations...)
	known := map[string]bool{}
	for _, v := range variations {
		known[v] = true
	}
	var extra []string
	for v := range in.Exposures {
		if !known[v] {
			extra = append(extra, v)
		}
	}
	sort.Strings(extra)
	variations = append(variations, extra...)
	if len(variations) > 0 {
		resp.Control = variations[0]
	}
	for _, v := range variations {
		resp.SampleSize += in.Exposures[v]
	}

	resp.SRM = srmBlock(x, variations, in.Exposures)
	resp.Trustworthy = !resp.SRM.Detected

	for _, mi := range in.Metrics {
		resp.Metrics = append(resp.Metrics, computeMetric(mi, variations, in.Exposures, resp.Trustworthy))
	}
	resp.Warnings = warnings(in, resp, variations)
	return resp
}

func computeMetric(mi MetricInput, variations []string, exposures map[string]int64, trustworthy bool) MetricResult {
	res := MetricResult{Metric: mi.Spec, Variations: []VariationStats{}, Comparisons: []Comparison{}}
	moments := map[string]Moments{}
	var converters int64
	for i, v := range variations {
		a := mi.Sums[v]
		n := exposures[v] // the denominator is the exposure count itself
		vs := VariationStats{VariationID: v, IsControl: i == 0, Exposures: n, ConvertingSubjects: a.Converters, Events: a.Events, Trustworthy: trustworthy}
		var m Moments
		switch mi.Spec.Kind {
		case KindConversion:
			m = Moments{N: n, Sum: float64(a.Converters), SumSq: float64(a.Converters)}
		case KindCount:
			m = Moments{N: n, Sum: float64(a.Events), SumSq: a.SumN2}
		default:
			m = Moments{N: n, Sum: a.SumV, SumSq: a.SumV2}
		}
		moments[v] = m
		converters += a.Converters
		if n > 0 {
			if mi.Spec.Kind == KindConversion {
				vs.Rate = finite(float64(a.Converters) / float64(n))
			} else {
				vs.Mean = finite(m.Mean())
				if n > 1 {
					vs.StdDev = finite(math.Sqrt(m.Variance()))
				}
			}
		}
		res.Variations = append(res.Variations, vs)
	}
	if len(variations) < 2 {
		return res // a single variation has nothing to compare against
	}

	control := variations[0]
	m := len(variations) - 1
	alphaPer := Alpha / float64(m)
	level := 1 - alphaPer
	for _, v := range variations[1:] {
		c := Comparison{VariationID: v, Against: control, AlphaPerComparison: alphaPer, ConfidenceLevel: level, Trustworthy: trustworthy}
		nC, nV := exposures[control], exposures[v]
		switch {
		case nC == 0:
			c.Status, c.StatusReasons = StatusInvalid, []string{"CONTROL_HAS_NO_EXPOSURES"}
		case nV == 0:
			c.Status, c.StatusReasons = StatusInvalid, []string{"VARIANT_HAS_NO_EXPOSURES"}
		default:
			var t Test
			if mi.Spec.Kind == KindConversion {
				t = TwoProportion(int64(moments[control].Sum), nC, int64(moments[v].Sum), nV, level)
			} else {
				t = Welch(moments[control], moments[v], level)
			}
			c.Test = t
			var short []string
			if nC < MinExposuresPerVariation || nV < MinExposuresPerVariation {
				short = append(short, fmt.Sprintf("EXPOSURES_BELOW_%d", MinExposuresPerVariation))
			}
			if mi.Spec.Kind == KindConversion && converters < MinConversionsTotal {
				short = append(short, fmt.Sprintf("CONVERSIONS_BELOW_%d", MinConversionsTotal))
			}
			switch {
			case len(short) > 0:
				// Descriptive numbers stay; nothing inferential is shown.
				c.Status, c.StatusReasons = StatusInsufficientData, short
				c.Test.Statistic, c.Test.PValue, c.Test.DegreesOfFreedom = nil, nil, nil
				c.Test.DifferenceCI, c.Test.RelativeLiftCI = Interval{}, Interval{}
				c.Test.Reasons = map[string]string{"inference": "INSUFFICIENT_DATA"}
			case t.PValue == nil:
				c.Status = StatusInvalid
				c.StatusReasons = []string{t.Reasons["pValue"]}
			default:
				c.AdjustedPValue = Bonferroni(t.PValue, m)
				if c.AdjustedPValue != nil && *c.AdjustedPValue < Alpha {
					c.Status = StatusSignificant
				} else {
					c.Status = StatusNotSignificant
				}
			}
		}
		res.Comparisons = append(res.Comparisons, c)
	}
	return res
}

// srmBlock compares observed exposures with the split recorded at start.
func srmBlock(x experimentRow, variations []string, exposures map[string]int64) SRMBlock {
	b := SRMBlock{Threshold: SRMThreshold, Observed: []VariationCount{}, Expected: []VariationShare{}}
	weights := make([]float64, len(variations))
	var sum float64
	if x.StartConfig != nil && len(x.StartConfig.Rollout) > 0 {
		for i, v := range variations {
			weights[i] = float64(x.StartConfig.Rollout[v])
			sum += weights[i]
		}
		b.ExpectedSource, b.Confidence = "rollout_at_start", "high"
		if x.StartConfig.hasTargeting() {
			b.Confidence = "low"
			b.ConfidenceReason = "targeting rules were set at start; they can send subjects to a variation regardless of the rollout, so the expected split is approximate"
		}
	}
	if sum <= 0 {
		for i := range weights {
			weights[i] = 1
		}
		sum = float64(len(weights))
		b.ExpectedSource, b.Confidence = "equal_assumed", "low"
		b.ConfidenceReason = "no rollout split was recorded when the experiment started; an equal split is assumed"
	}
	obs := make([]int64, len(variations))
	for i, v := range variations {
		obs[i] = exposures[v]
		b.Observed = append(b.Observed, VariationCount{v, obs[i]})
		if sum > 0 {
			b.Expected = append(b.Expected, VariationShare{v, round6(weights[i] / sum)})
		}
	}
	b.SRMResult = SRM(obs, weights, SRMThreshold)
	return b
}

func warnings(in Inputs, r Response, variations []string) []Warning {
	out := []Warning{}
	add := func(code, sev, msg string, detail map[string]any) {
		out = append(out, Warning{Code: code, Severity: sev, Message: msg, Detail: detail})
	}
	if in.UnverifiedExposures > 0 {
		add("EXPOSURES_UNVERIFIED", "warning", fmt.Sprintf(
			"%d exposure(s) were accepted without a valid exposure token, so they cannot be shown to have been served by Helios. They are included in the numbers.", in.UnverifiedExposures),
			map[string]any{"exposures": in.UnverifiedExposures})
	}
	if r.SampleSize == 0 {
		add("NO_EXPOSURES", "warning", "No exposures have been recorded yet, so there is nothing to compare.", nil)
	}
	if r.SRM.Detected {
		p := 0.0
		if r.SRM.PValue != nil {
			p = *r.SRM.PValue
		}
		add("SRM_FAILED", "critical", fmt.Sprintf(
			"Sample ratio mismatch: the exposures per variation differ from the expected split (p = %.3g < %g). Every number below is untrustworthy until the cause is found (assignment or exposure-reporting bug, bots, redirects).", p, SRMThreshold),
			map[string]any{"pValue": r.SRM.PValue, "confidence": r.SRM.Confidence})
	} else if r.SRM.Confidence == "low" && r.SampleSize > 0 {
		add("SRM_LOW_CONFIDENCE", "info", "The sample-ratio check is low confidence: "+r.SRM.ConfidenceReason+".", nil)
	}
	for _, m := range r.Metrics {
		for _, c := range m.Comparisons {
			if c.Status == StatusInsufficientData {
				add("INSUFFICIENT_DATA", "warning", fmt.Sprintf(
					"Not enough data for %q on %s: each variation needs at least %d exposures and a conversion metric at least %d conversions in total. No verdict is shown.",
					c.VariationID, m.Metric.EventName, MinExposuresPerVariation, MinConversionsTotal),
					map[string]any{"variationId": c.VariationID, "eventName": m.Metric.EventName, "reasons": c.StatusReasons})
				break // one per metric is enough
			}
		}
	}
	for _, mi := range in.Metrics {
		d := mi.Dropped
		if d.NoExposure > 0 {
			add("EVENTS_WITHOUT_EXPOSURE", "warning", fmt.Sprintf(
				"%d %q event(s) came from subjects with no exposure to this experiment and were excluded.", d.NoExposure, mi.Spec.EventName),
				map[string]any{"eventName": mi.Spec.EventName, "events": d.NoExposure})
		}
		if d.BeforeExposure > 0 {
			add("EVENTS_BEFORE_EXPOSURE", "info", fmt.Sprintf(
				"%d %q event(s) happened before the subject's exposure and were excluded.", d.BeforeExposure, mi.Spec.EventName),
				map[string]any{"eventName": mi.Spec.EventName, "events": d.BeforeExposure})
		}
		if d.ClockAdjusted > 0 {
			add("CLIENT_CLOCK_ADJUSTED", "info", fmt.Sprintf(
				"%d %q event(s) carried a client timestamp far outside the run, so the server's receipt time was used to bound them.", d.ClockAdjusted, mi.Spec.EventName),
				map[string]any{"eventName": mi.Spec.EventName, "events": d.ClockAdjusted})
		}
		if d.OutsideWindow > 0 {
			add("EVENTS_OUTSIDE_WINDOW", "info", fmt.Sprintf(
				"%d %q event(s) fall outside the run window and were excluded.", d.OutsideWindow, mi.Spec.EventName),
				map[string]any{"eventName": mi.Spec.EventName, "events": d.OutsideWindow})
		}
	}
	for _, c := range in.Changes {
		if len(c.Changed) == 0 {
			continue
		}
		sev := "warning"
		if len(c.Changed) == 1 && c.Changed[0] == "enabled" {
			sev = "info"
		}
		add("FLAG_CONFIG_CHANGED", sev, fmt.Sprintf(
			"The flag's configuration changed during the run (%s) on %s. Subjects exposed before and after the change may have been assigned differently.",
			joinFields(c.Changed), c.At.UTC().Format("2006-01-02 15:04 UTC")),
			map[string]any{"at": c.At.UTC(), "changed": c.Changed, "action": c.Action})
	}
	for _, o := range in.OtherRuns {
		stopped := "still running"
		if o.StoppedAt != nil {
			stopped = "stopped " + o.StoppedAt.UTC().Format("2006-01-02")
		}
		add("EXPERIMENT_RESTARTED", "warning", fmt.Sprintf(
			"The flag was also used by experiment %q (started %s, %s). Subjects may have been exposed to more than one run; each run's data is kept separate.",
			o.Key, o.StartedAt.UTC().Format("2006-01-02"), stopped),
			map[string]any{"experimentKey": o.Key, "startedAt": o.StartedAt.UTC(), "stoppedAt": o.StoppedAt})
	}
	if r.Window.End.Sub(r.Window.Start) < ShortRun {
		add("SHORT_RUN", "info", fmt.Sprintf(
			"The run window is only %.1f hours. Short runs miss daily and weekly patterns in user behaviour.", r.Window.Hours),
			map[string]any{"hours": r.Window.Hours})
	}
	if len(variations) < 2 {
		add("SINGLE_VARIATION", "warning", "The flag has a single variation, so nothing can be compared.", nil)
	}
	return out
}

func joinFields(f []string) string {
	s := ""
	for i, x := range f {
		if i > 0 {
			s += ", "
		}
		s += x
	}
	return s
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func methodNotes() []string {
	return []string{
		"Subjects are the unit. A subject belongs to the variation in their $exposure event (one per subject and experiment).",
		"Only events from exposed subjects, at or after their own exposure and inside the run window (started_at to stopped_at, or now), are counted.",
		"Conversion: a subject converts if they have at least one matching event. Count: matching events per subject. Value: the sum of matching events' values per subject. Subjects with no event count as 0.",
		"Control is the first variation of the flag. Every other variation is compared with it.",
		"Conversion metrics: pooled two-proportion z-test (two-sided); interval on the difference is the unpooled Wald interval; interval on the relative lift is the delta method on the log rate ratio.",
		"Count and value metrics: Welch's unequal-variance t-test with Welch-Satterthwaite degrees of freedom; interval on the difference of means from the t distribution; interval on the relative lift from a first-order delta method on the ratio of means, defined only for a positive control mean.",
		"With more than one comparison, each is tested at alpha / comparisons (Bonferroni); adjustedPValue is pValue times the number of comparisons, capped at 1, and the intervals use the matching confidence level.",
		"Sample ratio mismatch: chi-square goodness of fit of exposures per variation against the rollout recorded at start (equal split, low confidence, when none was recorded); p < 0.001 marks every variation untrustworthy.",
		"A number that cannot be computed is null with a reason; no verdict is given below the minimum exposures or conversions. No winner is declared.",
	}
}
