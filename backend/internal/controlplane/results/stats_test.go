package results

import (
	"math"
	"testing"
)

// near: relative tolerance rel, with an absolute floor for values near 0.
func near(t *testing.T, what string, got *float64, want float64, rel float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s: got nil, want %v", what, want)
		return
	}
	if math.Abs(*got-want) > rel*math.Abs(want)+1e-12 {
		t.Errorf("%s: got %.12g, want %.12g", what, *got, want)
	}
}

func nearOrNil(t *testing.T, what string, got, want *float64, rel float64) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("%s: got %v, want nil", what, *got)
		}
		return
	}
	near(t, what, got, *want, rel)
}

func TestTwoProportionMatchesScipyAndStatsmodels(t *testing.T) {
	for _, v := range propVectors {
		got := TwoProportion(v.x1, v.n1, v.x2, v.n2, v.level)
		near(t, v.name+" difference", got.Difference, v.diff, 1e-9)
		nearOrNil(t, v.name+" z", got.Statistic, v.z, 1e-7)
		nearOrNil(t, v.name+" p", got.PValue, v.p, 1e-6)
		if v.name == "case 7" { // nobody converted anywhere: statsmodels' interval is the point 0; we say unavailable
			if got.DifferenceCI.Low != nil || got.Reasons["differenceInterval"] != ReasonZeroVariance {
				t.Errorf("zero variance must give no interval with a reason: %+v", got)
			}
			continue
		}
		nearOrNil(t, v.name+" diff low", got.DifferenceCI.Low, v.dlo, 1e-7)
		nearOrNil(t, v.name+" diff high", got.DifferenceCI.High, v.dhi, 1e-7)
		nearOrNil(t, v.name+" rel", got.RelativeLift, v.rel, 1e-9)
		nearOrNil(t, v.name+" rel low", got.RelativeLiftCI.Low, v.rlo, 1e-7)
		nearOrNil(t, v.name+" rel high", got.RelativeLiftCI.High, v.rhi, 1e-7)
	}
}

func TestWelchMatchesScipy(t *testing.T) {
	for _, v := range welchVectors {
		got := Welch(Moments{v.cN, v.cSum, v.cSumSq}, Moments{v.vN, v.vSum, v.vSumSq}, v.level)
		near(t, v.name+" diff", got.Difference, v.diff, 1e-9)
		near(t, v.name+" t", got.Statistic, v.t, 1e-6)
		near(t, v.name+" df", got.DegreesOfFreedom, v.df, 1e-6)
		near(t, v.name+" p", got.PValue, v.p, 1e-5)
		near(t, v.name+" dlo", got.DifferenceCI.Low, v.dlo, 1e-6)
		near(t, v.name+" dhi", got.DifferenceCI.High, v.dhi, 1e-6)
		near(t, v.name+" rel", got.RelativeLift, v.rel, 1e-9)
		near(t, v.name+" rlo", got.RelativeLiftCI.Low, v.rlo, 1e-6)
		near(t, v.name+" rhi", got.RelativeLiftCI.High, v.rhi, 1e-6)
	}
}

func TestSRMMatchesScipy(t *testing.T) {
	for _, v := range srmVectors {
		got := SRM(v.obs, v.w, SRMThreshold)
		near(t, "chi2", got.Chi2, v.chi2, 1e-9)
		near(t, "p", got.PValue, v.p, 1e-6)
		if got.DF != v.df {
			t.Errorf("df %d, want %d", got.DF, v.df)
		}
		if got.Detected != (v.p < SRMThreshold) {
			t.Errorf("obs %v detected=%v at p=%v", v.obs, got.Detected, v.p)
		}
	}
}

func TestZeroVarianceAndTinySamplesAreUnavailableNotNaN(t *testing.T) {
	check := func(name string, tt Test) {
		t.Helper()
		for field, v := range map[string]*float64{"diff": tt.Difference, "z": tt.Statistic, "p": tt.PValue, "lift": tt.RelativeLift,
			"dlo": tt.DifferenceCI.Low, "dhi": tt.DifferenceCI.High, "rlo": tt.RelativeLiftCI.Low, "rhi": tt.RelativeLiftCI.High, "df": tt.DegreesOfFreedom} {
			if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0)) {
				t.Errorf("%s %s is not finite: %v", name, field, *v)
			}
		}
	}
	tt := Welch(Moments{100, 500, 2500}, Moments{100, 500, 2500}, 0.95) // identical constant arms
	check("constant", tt)
	if tt.PValue != nil || tt.Reasons["pValue"] != ReasonZeroVariance {
		t.Errorf("zero variance: p=%v reasons=%v", tt.PValue, tt.Reasons)
	}
	tt = Welch(Moments{1, 3, 9}, Moments{50, 100, 300}, 0.95) // one subject
	check("n=1", tt)
	if tt.PValue != nil || tt.Reasons["pValue"] != ReasonTooFewSubjects {
		t.Errorf("n=1: %+v", tt)
	}
	tt = Welch(Moments{100, -50, 400}, Moments{100, 20, 300}, 0.95) // lift undefined, the rest fine
	check("negative mean", tt)
	if tt.RelativeLift != nil || tt.Reasons["relativeLift"] != ReasonControlMeanNotPos || tt.PValue == nil {
		t.Errorf("negative control mean: %+v", tt)
	}
	check("all converted", TwoProportion(100, 100, 100, 100, 0.95))
	check("empty arm", TwoProportion(0, 0, 5, 100, 0.95))
	check("huge", TwoProportion(5e11, 1e12, 5.1e11, 1e12, 0.95))
	if p := Bonferroni(nil, 3); p != nil {
		t.Error("Bonferroni(nil)")
	}
	if p := Bonferroni(f(0.4), 3); p == nil || *p != 1 {
		t.Errorf("Bonferroni caps at 1: %v", p)
	}
	if r := SRM([]int64{0, 0}, []float64{1, 1}, SRMThreshold); r.Reason == "" || r.PValue != nil {
		t.Errorf("SRM with no data: %+v", r)
	}
	if r := SRM([]int64{10, 5}, []float64{1, 0}, SRMThreshold); !r.Detected {
		t.Errorf("exposure in a zero-weight variation must be a mismatch: %+v", r)
	}
	if v := (Moments{N: 3, Sum: 3, SumSq: 3}).Variance(); v != 0 {
		t.Errorf("constant variance %v", v)
	}
}
