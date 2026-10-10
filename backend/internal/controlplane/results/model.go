// Package results computes experiment results from the data /track collects:
// per-variation numbers, comparisons against the control, a sample-ratio check
// and data-quality warnings. Aggregation happens in SQL (counts, sums and sums
// of squares per variation); no event row is ever read into Go.
package results

import "time"

// Tunable constants. Results show "insufficient data" until both are met.
const (
	MinExposuresPerVariation = 100
	MinConversionsTotal      = 25
	// Alpha is the family-wise significance level; with m comparisons each one
	// is tested at Alpha/m (Bonferroni).
	Alpha = 0.05
	// SRMThreshold: a sample-ratio p-value below it marks the data untrustworthy.
	SRMThreshold = 0.001
	// ShortRun: a run window shorter than this is flagged.
	ShortRun = 24 * time.Hour
	// MaxMetrics is the hard cap on metrics per request.
	MaxMetrics = 3
	// ExposureEvent mirrors the ingestion constant.
	ExposureEvent = "$exposure"
)

// Metric kinds.
const (
	KindConversion = "conversion" // 1 if the subject has at least one matching event
	KindCount      = "count"      // number of matching events per subject
	KindValue      = "value"      // sum of the events' values per subject
)

// Statuses of a comparison.
const (
	StatusSignificant      = "significant"
	StatusNotSignificant   = "not_significant"
	StatusInsufficientData = "insufficient_data"
	StatusInvalid          = "invalid"
)

// MetricSpec identifies one metric: an event name and how it is aggregated.
type MetricSpec struct {
	Name      string `json:"name,omitempty"` // the experiment's own name for it, when it has one
	EventName string `json:"eventName"`
	Kind      string `json:"kind"`
	IsPrimary bool   `json:"isPrimary"`
}

// VariationStats are the per-variation numbers of one metric.
type VariationStats struct {
	VariationID string `json:"variationId"`
	IsControl   bool   `json:"isControl"`
	// Exposures is the number of subjects exposed to this variation.
	Exposures int64 `json:"exposures"`
	// Subjects with at least one matching event (all kinds report it).
	ConvertingSubjects int64 `json:"convertingSubjects"`
	// Events is the number of matching events from exposed subjects, after the exposure, in the run window.
	Events int64 `json:"events"`
	// Rate is convertingSubjects/exposures for a conversion metric; Mean is the
	// per-subject mean (count or value metric). The other is null.
	Rate *float64 `json:"rate"`
	Mean *float64 `json:"mean"`
	// StdDev is the per-subject sample standard deviation (count and value metrics).
	StdDev *float64 `json:"stdDev"`
	// Trustworthy is false for every variation when the sample-ratio check fails.
	Trustworthy bool `json:"trustworthy"`
}

// Comparison is one variation against the control.
type Comparison struct {
	VariationID string `json:"variationId"`
	Against     string `json:"against"`
	Status      string `json:"status"`
	// Reasons for a status other than significant/not_significant.
	StatusReasons []string `json:"statusReasons,omitempty"`
	Test
	// AdjustedPValue is PValue * (number of comparisons), capped at 1 (Bonferroni).
	AdjustedPValue *float64 `json:"adjustedPValue"`
	// Alpha is the per-comparison level used: Alpha / comparisons.
	AlphaPerComparison float64 `json:"alphaPerComparison"`
	// ConfidenceLevel of the intervals: 1 - AlphaPerComparison.
	ConfidenceLevel float64 `json:"confidenceLevel"`
	Trustworthy     bool    `json:"trustworthy"`
}

// MetricResult is everything for one metric.
type MetricResult struct {
	Metric      MetricSpec       `json:"metric"`
	Variations  []VariationStats `json:"variations"`
	Comparisons []Comparison     `json:"comparisons"`
}

// Warning is one data-quality finding.
type Warning struct {
	Code     string         `json:"code"`
	Severity string         `json:"severity"` // "info" | "warning" | "critical"
	Message  string         `json:"message"`
	Detail   map[string]any `json:"detail,omitempty"`
}

// SRMBlock is the sample-ratio check.
type SRMBlock struct {
	SRMResult
	Observed []VariationCount `json:"observed"`
	// Expected is the split the check compared against (shares summing to 1).
	Expected []VariationShare `json:"expected"`
	// ExpectedSource: "rollout_at_start" or "equal_assumed".
	ExpectedSource string `json:"expectedSource"`
	// Confidence is "high" only when the split came from the recorded rollout
	// and no targeting rule can override it.
	Confidence       string  `json:"confidence"`
	ConfidenceReason string  `json:"confidenceReason,omitempty"`
	Threshold        float64 `json:"threshold"`
}

type VariationCount struct {
	VariationID string `json:"variationId"`
	Count       int64  `json:"count"`
}

type VariationShare struct {
	VariationID string  `json:"variationId"`
	Share       float64 `json:"share"`
}

// Window is the period events are counted in.
type Window struct {
	Start   time.Time  `json:"start"`
	End     time.Time  `json:"end"`
	Stopped *time.Time `json:"stoppedAt"`
	Running bool       `json:"running"`
	Hours   float64    `json:"hours"`
}

// Response is the body of GET .../experiments/{key}/results.
type Response struct {
	ExperimentKey string    `json:"experimentKey"`
	FlagKey       string    `json:"flagKey"`
	Status        string    `json:"status"`
	ReadAt        time.Time `json:"readAt"`
	Window        Window    `json:"window"`
	// Control is the variation everything is compared with: the first variation
	// of the flag (variation order is the flag's own).
	Control string `json:"control"`
	// SampleSize is the total number of exposures at this reading.
	SampleSize int64 `json:"sampleSize"`
	// Trustworthy is false when the sample-ratio check failed.
	Trustworthy bool           `json:"trustworthy"`
	SRM         SRMBlock       `json:"srm"`
	Metrics     []MetricResult `json:"metrics"`
	Warnings    []Warning      `json:"warnings"`
	// Alpha is the family-wise level; comparisons are tested at Alpha/m.
	Alpha      float64    `json:"alpha"`
	Thresholds Thresholds `json:"thresholds"`
	// PeekingNote and Method are plain language; no winner is declared.
	PeekingNote string   `json:"peekingNote"`
	Method      []string `json:"method"`
}

type Thresholds struct {
	MinExposuresPerVariation int     `json:"minExposuresPerVariation"`
	MinConversionsTotal      int     `json:"minConversionsTotal"`
	SRMPValue                float64 `json:"srmPValue"`
}
