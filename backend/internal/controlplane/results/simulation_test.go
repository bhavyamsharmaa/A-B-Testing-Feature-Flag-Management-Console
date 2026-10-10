package results

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"gonum.org/v1/gonum/stat/distuv"
)

// The simulations below are statistical checks of the whole pipeline from sums
// to verdicts (Compute). They are deterministic (fixed seeds) and quick, so
// they run with the normal tests; `go test -v -run Simulation` prints the
// observed rates.
//
// Synthetic data with a known truth:
//   * A/A: no true difference. The share of experiments called significant is
//     the false-positive rate; at alpha 0.05 it should be near 5%.
//   * 10% vs 12% with 20,000 subjects per arm: a real effect; the share
//     detected is the power.
//   * a deliberate sample ratio mismatch.

func conversionExperiment(rng *rand.Rand, nC, nV int64, pC, pV float64) Inputs {
	src := rand.NewPCG(rng.Uint64(), rng.Uint64())
	xC := int64(distuv.Binomial{N: float64(nC), P: pC, Src: src}.Rand())
	xV := int64(distuv.Binomial{N: float64(nV), P: pV, Src: src}.Rand())
	return input(map[string]int64{"control": nC, "variant": nV}, map[string]int64{"control": xC, "variant": xV}, even())
}

func rate(hits, total int) float64 { return float64(hits) / float64(total) }

func TestSimulationAAFalsePositiveRate(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261009, 1))
	const runs = 200
	fp := 0
	for i := 0; i < runs; i++ {
		r := Compute(conversionExperiment(rng, 20000, 20000, 0.10, 0.10))
		if r.Metrics[0].Comparisons[0].Status == StatusSignificant {
			fp++
		}
	}
	got := rate(fp, runs)
	t.Logf("A/A conversion, %d experiments of 20,000 + 20,000 subjects at 10%%: %d false positives = %.1f%% (alpha 5%%)", runs, fp, 100*got)
	if got < 0.015 || got > 0.095 { // 5% +/- ~3 standard errors for 200 runs
		t.Errorf("false-positive rate %.3f is not near 0.05", got)
	}
}

func TestSimulationAAFalsePositiveRateThreeArmsFamilyWise(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261009, 2))
	const runs = 400
	fp := 0
	for i := 0; i < runs; i++ {
		src := rand.NewPCG(rng.Uint64(), rng.Uint64())
		exp, conv := map[string]int64{}, map[string]int64{}
		for _, v := range []string{"control", "a", "b"} {
			exp[v] = 6000
			conv[v] = int64(distuv.Binomial{N: 6000, P: 0.1, Src: src}.Rand())
		}
		in := input(exp, conv, nil)
		in.Experiment.Variations = []string{"control", "a", "b"}
		in.Experiment.StartConfig.Rollout = map[string]uint32{"control": 33334, "a": 33333, "b": 33333}
		any := false
		for _, c := range Compute(in).Metrics[0].Comparisons {
			any = any || c.Status == StatusSignificant
		}
		if any {
			fp++
		}
	}
	got := rate(fp, runs)
	t.Logf("A/A/A with Bonferroni, %d experiments: family-wise false positives = %.1f%% (target <= 5%%)", runs, 100*got)
	if got > 0.08 { // Bonferroni is conservative: at or below 5%
		t.Errorf("family-wise error %.3f", got)
	}
}

func TestSimulationAAValueMetric(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261009, 3))
	const runs, n = 200, 3000
	fp := 0
	for i := 0; i < runs; i++ {
		moments := map[string]Moments{}
		for _, v := range []string{"control", "variant"} {
			var m Moments
			m.N = n
			for j := 0; j < n; j++ { // zero-inflated, heavy-tailed revenue per subject
				y := 0.0
				if rng.Float64() < 0.15 {
					y = rng.ExpFloat64() * 40
				}
				m.Sum += y
				m.SumSq += y * y
			}
			moments[v] = m
		}
		c, v := moments["control"], moments["variant"]
		in := input(map[string]int64{"control": n, "variant": n}, nil, even())
		in.Metrics[0].Spec.Kind = KindValue
		in.Metrics[0].Sums = map[string]armSums{
			"control": {Subjects: n, SumV: c.Sum, SumV2: c.SumSq},
			"variant": {Subjects: n, SumV: v.Sum, SumV2: v.SumSq},
		}
		if Compute(in).Metrics[0].Comparisons[0].Status == StatusSignificant {
			fp++
		}
	}
	got := rate(fp, runs)
	t.Logf("A/A value metric (zero-inflated exponential), %d experiments of %d + %d subjects: %.1f%% false positives", runs, n, n, 100*got)
	if got < 0.015 || got > 0.095 {
		t.Errorf("false-positive rate %.3f", got)
	}
}

func TestSimulationDetectsARealEffectAndASampleRatioMismatch(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261009, 4))
	const runs = 200
	hit, covered := 0, 0
	for i := 0; i < runs; i++ {
		r := Compute(conversionExperiment(rng, 20000, 20000, 0.10, 0.12))
		c := r.Metrics[0].Comparisons[0]
		if c.Status == StatusSignificant {
			hit++
		}
		if c.DifferenceCI.Low != nil && *c.DifferenceCI.Low <= 0.02 && 0.02 <= *c.DifferenceCI.High {
			covered++
		}
	}
	t.Logf("10%% vs 12%%, 20,000 per arm, %d experiments: detected %d = %.1f%%; the 95%% interval covered the true +2pp in %d = %.1f%%",
		runs, hit, 100*rate(hit, runs), covered, 100*rate(covered, runs))
	if rate(hit, runs) < 0.99 {
		t.Errorf("power %.3f", rate(hit, runs))
	}
	if cov := rate(covered, runs); cov < 0.90 || cov > 0.99 {
		t.Errorf("interval coverage %.3f is not near 0.95", cov)
	}

	// Deliberate SRM: a 50/50 rollout that delivers 20,000 vs 21,000 exposures (p ~ 0.0001).
	r := Compute(conversionExperiment(rng, 20000, 21000, 0.10, 0.10))
	var p float64
	if r.SRM.PValue != nil {
		p = *r.SRM.PValue
	}
	t.Logf("SRM case 20,000 vs 21,000 against 50/50: p = %.2g, detected = %v, trustworthy = %v", p, r.SRM.Detected, r.Trustworthy)
	if !r.SRM.Detected || r.Trustworthy {
		t.Errorf("SRM not detected: %+v", r.SRM)
	}
	// And the same data at a delivered 50/50 is not flagged: the false-alarm rate of the SRM check itself.
	alarms := 0
	for i := 0; i < 1000; i++ {
		src := rand.NewPCG(rng.Uint64(), rng.Uint64())
		n := int64(distuv.Binomial{N: 40000, P: 0.5, Src: src}.Rand())
		if SRM([]int64{n, 40000 - n}, []float64{1, 1}, SRMThreshold).Detected {
			alarms++
		}
	}
	t.Logf("SRM check on 1,000 correct 50/50 splits of 40,000: %d alarms at p < 0.001 (expected about 1)", alarms)
	if alarms > 5 {
		t.Errorf("%d SRM false alarms in 1000", alarms)
	}
	_ = fmt.Sprint
}

// 200 runs have a standard error of about 1.5 points, so one run of 200 can
// read 7% for a correct test. 5,000 runs pin the rate down (SE 0.3 points).
func TestSimulationAAFalsePositiveRateLargeRun(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261009, 5))
	const runs = 5000
	fp := 0
	for i := 0; i < runs; i++ {
		if Compute(conversionExperiment(rng, 20000, 20000, 0.10, 0.10)).Metrics[0].Comparisons[0].Status == StatusSignificant {
			fp++
		}
	}
	got := rate(fp, runs)
	t.Logf("A/A conversion, %d experiments: %d false positives = %.2f%% (alpha 5%%)", runs, fp, 100*got)
	if got < 0.04 || got > 0.06 {
		t.Errorf("false-positive rate %.4f is not near 0.05", got)
	}
}
