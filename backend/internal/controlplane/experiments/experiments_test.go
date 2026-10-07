package experiments

import (
	"encoding/json"
	"strings"
	"testing"
)

func validRequest() createRequest {
	return createRequest{
		FlagKey: "new-checkout",
		Key:     "checkout-test",
		Name:    "Checkout test",
		Metrics: []metricRequest{
			{Name: "Purchases", EventName: "purchase", Type: MetricConversion, IsPrimary: true},
			{Name: "Revenue", EventName: "revenue", Type: MetricValue},
		},
	}
}

func TestCreateValidation(t *testing.T) {
	if err := validRequest().validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	cases := map[string]struct {
		mutate func(*createRequest)
		want   string // substring of the error
	}{
		"no flag key":        {func(r *createRequest) { r.FlagKey = "" }, "flagKey"},
		"bad key":            {func(r *createRequest) { r.Key = "has space" }, "key must be"},
		"key too long":       {func(r *createRequest) { r.Key = strings.Repeat("a", 129) }, "key must be"},
		"no name":            {func(r *createRequest) { r.Name = "  " }, "name"},
		"long hypothesis":    {func(r *createRequest) { r.Hypothesis = strings.Repeat("x", 2001) }, "hypothesis"},
		"no metrics":         {func(r *createRequest) { r.Metrics = nil }, "1-10 metrics"},
		"too many metrics":   {func(r *createRequest) { r.Metrics = manyMetrics(11) }, "1-10 metrics"},
		"no primary":         {func(r *createRequest) { r.Metrics[0].IsPrimary = false }, "exactly one"},
		"two primaries":      {func(r *createRequest) { r.Metrics[1].IsPrimary = true }, "exactly one"},
		"duplicate name":     {func(r *createRequest) { r.Metrics[1].Name = "Purchases" }, "appears twice"},
		"duplicate event":    {func(r *createRequest) { r.Metrics[1].EventName = "purchase" }, "more than one metric"},
		"empty event":        {func(r *createRequest) { r.Metrics[1].EventName = "" }, "eventName"},
		"event with space":   {func(r *createRequest) { r.Metrics[1].EventName = "a b" }, "eventName"},
		"unknown type":       {func(r *createRequest) { r.Metrics[1].Type = "count" }, "type must be"},
		"reserved $exposure": {func(r *createRequest) { r.Metrics[1].EventName = ExposureEvent }, "reserved"},
		"reserved $ prefix":  {func(r *createRequest) { r.Metrics[1].EventName = "$custom" }, "reserved"},
	}
	for name, c := range cases {
		r := validRequest()
		c.mutate(&r)
		err := r.validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", name, err, c.want)
		}
	}
}

func TestEventNameAllowsDotsColonsDashes(t *testing.T) {
	r := validRequest()
	r.Metrics[1].EventName = "checkout.step:2-done_ok"
	if err := r.validate(); err != nil {
		t.Errorf("rejected: %v", err)
	}
}

func manyMetrics(n int) []metricRequest {
	ms := make([]metricRequest, n)
	for i := range ms {
		ms[i] = metricRequest{Name: string(rune('A' + i)), EventName: "e" + string(rune('a'+i)), Type: MetricValue, IsPrimary: i == 0}
	}
	return ms
}

func TestCanTransition(t *testing.T) {
	all := []Status{StatusDraft, StatusRunning, StatusStopped}
	allowed := map[[2]Status]bool{
		{StatusDraft, StatusRunning}:   true,
		{StatusRunning, StatusStopped}: true,
	}
	for _, from := range all {
		for _, to := range all {
			if got := canTransition(from, to); got != allowed[[2]Status{from, to}] {
				t.Errorf("%s -> %s: %v", from, to, got)
			}
		}
	}
}

func snap(rules, rollout, fallthroughID string, version int64) configSnapshot {
	return configSnapshot{
		TargetingRules:         json.RawMessage(rules),
		Rollout:                json.RawMessage(rollout),
		FallthroughVariationID: fallthroughID,
		Version:                version,
	}
}

func TestAssignmentChanged(t *testing.T) {
	base := snap(`[]`, `{"on":50000,"off":50000}`, "off", 3)

	same := []configSnapshot{
		snap(`[]`, `{"off":50000,"on":50000}`, "off", 9),       // key order and version don't matter
		snap(`[ ]`, ` {"on": 50000, "off": 50000} `, "off", 3), // whitespace doesn't matter
	}
	for i, s := range same {
		if assignmentChanged(base, s) {
			t.Errorf("same[%d] reported as changed", i)
		}
	}

	changed := map[string]configSnapshot{
		"weights":     snap(`[]`, `{"on":60000,"off":40000}`, "off", 4),
		"rollout off": snap(`[]`, `null`, "off", 4),
		"fallthrough": snap(`[]`, `{"on":50000,"off":50000}`, "on", 4),
		"rules":       snap(`[{"variationId":"on"}]`, `{"on":50000,"off":50000}`, "off", 4),
	}
	for name, s := range changed {
		if !assignmentChanged(base, s) {
			t.Errorf("%s not detected", name)
		}
	}

	// Empty and JSON null both mean "absent".
	if assignmentChanged(snap(``, `null`, "off", 1), snap(`null`, ``, "off", 1)) {
		t.Error("absent and null should compare equal")
	}
}

func TestConfigChangedSinceStart(t *testing.T) {
	start := snap(`[]`, `{"on":50000,"off":50000}`, "off", 1)
	moved := snap(`[]`, `{"on":80000,"off":20000}`, "off", 2)
	togglesOnly := snap(`[]`, `{"on":50000,"off":50000}`, "off", 5)

	cases := []struct {
		name   string
		status Status
		start  *configSnapshot
		stop   *configSnapshot
		live   configSnapshot
		want   bool
	}{
		{"draft", StatusDraft, nil, nil, moved, false},
		{"running, untouched", StatusRunning, &start, nil, togglesOnly, false},
		{"running, weights moved", StatusRunning, &start, nil, moved, true},
		{"stopped, was clean, edited afterwards", StatusStopped, &start, &togglesOnly, moved, false},
		{"stopped, weights moved during run", StatusStopped, &start, &moved, start, true},
	}
	for _, c := range cases {
		if got := configChangedSinceStart(c.status, c.start, c.stop, c.live); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCheckVariationCount(t *testing.T) {
	for _, n := range []int{0, 1} {
		err := checkVariationCount("f", n)
		if err == nil || !strings.Contains(err.Error(), "at least 2") {
			t.Errorf("n=%d: %v", n, err)
		}
	}
	for _, n := range []int{2, 20} {
		if err := checkVariationCount("f", n); err != nil {
			t.Errorf("n=%d rejected: %v", n, err)
		}
	}
}
