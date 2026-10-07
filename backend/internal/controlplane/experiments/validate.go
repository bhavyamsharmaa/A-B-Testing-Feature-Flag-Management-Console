package experiments

import (
	"fmt"
	"regexp"
	"strings"

	"helios/backend/internal/controlplane/flags"
)

const (
	// ReservedEventPrefix marks system event names. User metrics may not use
	// it, so they can never collide with ExposureEvent.
	ReservedEventPrefix = "$"
	// ExposureEvent is recorded once per subject when they are first served
	// a variation of a running experiment; it is the results denominator.
	ExposureEvent = "$exposure"

	MetricConversion = "conversion"
	MetricValue      = "value"

	maxMetrics     = 10
	maxNameLen     = 200
	maxHypothesis  = 2000
	maxMetricName  = 128
	maxListResults = 200
)

var eventNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type metricRequest struct {
	Name      string `json:"name"`
	EventName string `json:"eventName"`
	Type      string `json:"type"`
	IsPrimary bool   `json:"isPrimary"`
}

type createRequest struct {
	FlagKey    string          `json:"flagKey"`
	Key        string          `json:"key"`
	Name       string          `json:"name"`
	Hypothesis string          `json:"hypothesis"`
	Metrics    []metricRequest `json:"metrics"`
}

func (req createRequest) validate() error {
	if req.FlagKey == "" {
		return fmt.Errorf("flagKey is required")
	}
	if !flags.ValidKey(req.Key) {
		return fmt.Errorf("key must be 1-128 characters of letters, digits, '_', '.', '-', starting with a letter or digit")
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > maxNameLen {
		return fmt.Errorf("name is required and at most %d characters", maxNameLen)
	}
	if len(req.Hypothesis) > maxHypothesis {
		return fmt.Errorf("hypothesis is at most %d characters", maxHypothesis)
	}
	if n := len(req.Metrics); n < 1 || n > maxMetrics {
		return fmt.Errorf("an experiment needs 1-%d metrics, got %d", maxMetrics, n)
	}
	names, events, primaries := map[string]bool{}, map[string]bool{}, 0
	for i, m := range req.Metrics {
		if strings.TrimSpace(m.Name) == "" || len(m.Name) > maxMetricName {
			return fmt.Errorf("metric %d: name is required and at most %d characters", i, maxMetricName)
		}
		if names[m.Name] {
			return fmt.Errorf("metric name %q appears twice", m.Name)
		}
		names[m.Name] = true
		if strings.HasPrefix(m.EventName, ReservedEventPrefix) {
			return fmt.Errorf("metric %q: eventName %q is reserved; names starting with %q are used by Helios itself", m.Name, m.EventName, ReservedEventPrefix)
		}
		if !eventNameRE.MatchString(m.EventName) {
			return fmt.Errorf("metric %q: eventName must be 1-128 characters of letters, digits, '_', '.', ':', '-', starting with a letter or digit", m.Name)
		}
		if events[m.EventName] {
			return fmt.Errorf("eventName %q is used by more than one metric", m.EventName)
		}
		events[m.EventName] = true
		if m.Type != MetricConversion && m.Type != MetricValue {
			return fmt.Errorf("metric %q: type must be %q or %q", m.Name, MetricConversion, MetricValue)
		}
		if m.IsPrimary {
			primaries++
		}
	}
	if primaries != 1 {
		return fmt.Errorf("exactly one metric must be primary, got %d", primaries)
	}
	return nil
}

// checkVariationCount rejects flags that can't be split between arms. The
// flags table's own CHECK already guarantees 2-20; this keeps the rule (and
// its message) explicit here.
func checkVariationCount(flagKey string, n int) error {
	if n < 2 {
		return fmt.Errorf("flag %s has %d variation(s); an experiment needs a flag with at least 2", flagKey, n)
	}
	return nil
}
