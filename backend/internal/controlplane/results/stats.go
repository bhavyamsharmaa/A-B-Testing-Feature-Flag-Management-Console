package results

import (
	"math"

	"gonum.org/v1/gonum/stat/distuv"
)

// Everything in this file is a pure function of counts and sums. Distributions
// come from gonum; nothing here implements a CDF.
//
// A result that cannot be computed is reported as nil plus a Reason: a NaN or
// an Infinity is never returned.

// Reason codes for a number that is not available.
const (
	ReasonZeroVariance      = "ZERO_VARIANCE"
	ReasonTooFewSubjects    = "TOO_FEW_SUBJECTS"
	ReasonControlRateZero   = "CONTROL_RATE_ZERO"
	ReasonVariantRateZero   = "VARIANT_RATE_ZERO"
	ReasonControlMeanNotPos = "CONTROL_MEAN_NOT_POSITIVE"
	ReasonNotComputable     = "NOT_COMPUTABLE"
)

var stdNormal = distuv.Normal{Mu: 0, Sigma: 1}

// finite returns &v, or nil when v is NaN or infinite.
func finite(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// Interval is a confidence interval; both ends are nil when unavailable.
type Interval struct {
	Low  *float64 `json:"low"`
	High *float64 `json:"high"`
}

// Test is the outcome of comparing one variation with the control.
type Test struct {
	// Difference is variant - control (a rate difference, or a difference of means).
	Difference *float64 `json:"difference"`
	// DifferenceCI is the interval for Difference at the requested level.
	DifferenceCI Interval `json:"differenceInterval"`
	// RelativeLift is (variant - control) / control.
	RelativeLift   *float64 `json:"relativeLift"`
	RelativeLiftCI Interval `json:"relativeLiftInterval"`
	// Statistic is z (proportions) or t (means).
	Statistic *float64 `json:"statistic"`
	// DegreesOfFreedom is set for Welch's t-test only.
	DegreesOfFreedom *float64 `json:"degreesOfFreedom"`
	// PValue is two-sided and unadjusted.
	PValue *float64 `json:"pValue"`
	// Reasons explains every nil above, keyed by field name.
	Reasons map[string]string `json:"reasons,omitempty"`
}

func (t *Test) why(field, reason string) {
	if t.Reasons == nil {
		t.Reasons = map[string]string{}
	}
	t.Reasons[field] = reason
}

// zCritical is the two-sided critical value of the standard normal at the
// given confidence level (0.95 -> 1.959964).
func zCritical(level float64) float64 { return stdNormal.Quantile(1 - (1-level)/2) }

// TwoProportion compares a variant (x2 of n2) with the control (x1 of n1).
//
//   - p-value: the pooled two-proportion z-test (two-sided), z = (p2-p1)/SE0
//     with SE0 = sqrt(pbar(1-pbar)(1/n1+1/n2));
//   - interval on the difference: the unpooled Wald interval,
//     d +/- z*sqrt(p1(1-p1)/n1 + p2(1-p2)/n2);
//   - interval on the relative lift: the delta method on the log of the rate
//     ratio (Katz), SE(ln r) = sqrt((1-p1)/x1 + (1-p2)/x2), then exp() - 1.
//
// level is the confidence level of the intervals (the caller lowers alpha for
// multiple comparisons).
func TwoProportion(x1, n1, x2, n2 int64, level float64) Test {
	var t Test
	if n1 < 2 || n2 < 2 {
		for _, f := range []string{"difference", "differenceInterval", "relativeLift", "relativeLiftInterval", "statistic", "pValue"} {
			t.why(f, ReasonTooFewSubjects)
		}
		return t
	}
	N1, N2, X1, X2 := float64(n1), float64(n2), float64(x1), float64(x2)
	p1, p2 := X1/N1, X2/N2
	t.Difference = finite(p2 - p1)

	pooled := (X1 + X2) / (N1 + N2)
	se0 := math.Sqrt(pooled * (1 - pooled) * (1/N1 + 1/N2))
	if se0 == 0 { // nobody or everybody converted: no variance to test against
		t.why("statistic", ReasonZeroVariance)
		t.why("pValue", ReasonZeroVariance)
	} else {
		z := (p2 - p1) / se0
		t.Statistic = finite(z)
		t.PValue = finite(2 * stdNormal.Survival(math.Abs(z)))
	}

	zc := zCritical(level)
	se := math.Sqrt(p1*(1-p1)/N1 + p2*(1-p2)/N2)
	if se == 0 {
		t.why("differenceInterval", ReasonZeroVariance)
	} else {
		d := p2 - p1
		t.DifferenceCI = Interval{finite(d - zc*se), finite(d + zc*se)}
	}

	switch {
	case x1 == 0:
		t.why("relativeLift", ReasonControlRateZero)
		t.why("relativeLiftInterval", ReasonControlRateZero)
	default:
		t.RelativeLift = finite(p2/p1 - 1)
		if x2 == 0 {
			t.why("relativeLiftInterval", ReasonVariantRateZero)
		} else {
			selog := math.Sqrt((1-p1)/X1 + (1-p2)/X2)
			lr := math.Log(p2 / p1)
			t.RelativeLiftCI = Interval{finite(math.Exp(lr-zc*selog) - 1), finite(math.Exp(lr+zc*selog) - 1)}
			if selog == 0 {
				t.RelativeLiftCI = Interval{}
				t.why("relativeLiftInterval", ReasonZeroVariance)
			}
		}
	}
	return t
}

// Moments are the sufficient statistics of a per-subject metric in one arm.
type Moments struct {
	N     int64   // subjects
	Sum   float64 // sum of y
	SumSq float64 // sum of y^2
}

// Mean is Sum/N (0 for an empty arm).
func (m Moments) Mean() float64 {
	if m.N == 0 {
		return 0
	}
	return m.Sum / float64(m.N)
}

// Variance is the sample variance (n-1 denominator), from the sums. Tiny
// negative values from rounding are clamped to 0.
func (m Moments) Variance() float64 {
	if m.N < 2 {
		return 0
	}
	n := float64(m.N)
	v := (m.SumSq - m.Sum*m.Sum/n) / (n - 1)
	if v < 0 {
		return 0
	}
	return v
}

// Welch compares a variant with the control by Welch's unequal-variance t-test
// (Welch-Satterthwaite degrees of freedom, two-sided p from Student's t).
//
//   - interval on the difference of means: d +/- t_crit(df) * sqrt(v1/n1+v2/n2);
//   - relative lift = m2/m1 - 1, with its interval from the first-order delta
//     method on the ratio r = m2/m1: Var(r) ~ (v2/n2 + r^2 v1/n1) / m1^2, using
//     the same t critical value. It is only defined for a positive control mean.
func Welch(c, v Moments, level float64) Test {
	var t Test
	if c.N < 2 || v.N < 2 {
		for _, f := range []string{"difference", "differenceInterval", "relativeLift", "relativeLiftInterval", "statistic", "pValue", "degreesOfFreedom"} {
			t.why(f, ReasonTooFewSubjects)
		}
		return t
	}
	n1, n2 := float64(c.N), float64(v.N)
	m1, m2 := c.Mean(), v.Mean()
	v1, v2 := c.Variance(), v.Variance()
	t.Difference = finite(m2 - m1)

	a, b := v1/n1, v2/n2
	se2 := a + b
	if se2 == 0 {
		for _, f := range []string{"statistic", "pValue", "degreesOfFreedom", "differenceInterval", "relativeLiftInterval"} {
			t.why(f, ReasonZeroVariance)
		}
	} else {
		df := se2 * se2 / (a*a/(n1-1) + b*b/(n2-1))
		dist := distuv.StudentsT{Mu: 0, Sigma: 1, Nu: df}
		stat := (m2 - m1) / math.Sqrt(se2)
		t.Statistic, t.DegreesOfFreedom = finite(stat), finite(df)
		t.PValue = finite(2 * dist.Survival(math.Abs(stat)))
		crit := dist.Quantile(1 - (1-level)/2)
		d := m2 - m1
		t.DifferenceCI = Interval{finite(d - crit*math.Sqrt(se2)), finite(d + crit*math.Sqrt(se2))}

		if m1 > 0 {
			r := m2 / m1
			sr := math.Sqrt((b + r*r*a) / (m1 * m1))
			t.RelativeLiftCI = Interval{finite(r - crit*sr - 1), finite(r + crit*sr - 1)}
		}
	}
	if m1 > 0 {
		t.RelativeLift = finite(m2/m1 - 1)
	} else {
		t.why("relativeLift", ReasonControlMeanNotPos)
		if _, set := t.Reasons["relativeLiftInterval"]; !set {
			t.why("relativeLiftInterval", ReasonControlMeanNotPos)
		}
	}
	return t
}

// Bonferroni adjusts p-values for m comparisons: min(1, p*m).
func Bonferroni(p *float64, m int) *float64 {
	if p == nil || m < 1 {
		return nil
	}
	return finite(math.Min(1, *p*float64(m)))
}

// SRM is a chi-square goodness-of-fit test of the observed exposures per
// variation against the expected split.
type SRMResult struct {
	Chi2   *float64 `json:"chiSquare"`
	DF     int      `json:"degreesOfFreedom"`
	PValue *float64 `json:"pValue"`
	// Detected is true when p < Threshold.
	Detected bool `json:"detected"`
	// Reason is set when the test could not run.
	Reason string `json:"reason,omitempty"`
}

// SRM tests observed counts against expected weights (any positive scale; they
// are normalised). A variation with weight 0 that was nevertheless observed is a
// definite mismatch (p = 0) because its expected count is 0.
func SRM(observed []int64, weights []float64, threshold float64) SRMResult {
	var res SRMResult
	var total int64
	for _, o := range observed {
		total += o
	}
	var wsum float64
	for _, w := range weights {
		wsum += w
	}
	if len(observed) < 2 || len(observed) != len(weights) || total == 0 || wsum <= 0 {
		res.Reason = "NOT_ENOUGH_DATA"
		return res
	}
	var chi2 float64
	cats := 0
	for i, o := range observed {
		expected := float64(total) * weights[i] / wsum
		if expected == 0 {
			if o > 0 {
				zero := 0.0
				res.PValue, res.Detected = &zero, true
				res.Reason = "OBSERVED_IN_ZERO_WEIGHT_VARIATION"
				return res
			}
			continue
		}
		cats++
		d := float64(o) - expected
		chi2 += d * d / expected
	}
	if cats < 2 {
		res.Reason = "ONLY_ONE_EXPECTED_VARIATION"
		return res
	}
	res.DF = cats - 1
	res.Chi2 = finite(chi2)
	p := distuv.ChiSquared{K: float64(res.DF)}.Survival(chi2)
	res.PValue = finite(p)
	res.Detected = res.PValue != nil && *res.PValue < threshold
	return res
}
