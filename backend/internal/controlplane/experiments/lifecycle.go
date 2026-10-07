package experiments

import (
	"encoding/json"
	"reflect"
)

type Status string

const (
	StatusDraft   Status = "draft"
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
)

// canTransition is the whole lifecycle: draft -> running -> stopped.
// Stopped is terminal; re-running means creating a new experiment so each
// run's data stays separate.
func canTransition(from, to Status) bool {
	return (from == StatusDraft && to == StatusRunning) ||
		(from == StatusRunning && to == StatusStopped)
}

// configSnapshot is the part of a flag's environment config that decides who
// sees what. The enabled toggle is left out on purpose: switching a flag
// off and on (or hitting the kill switch) doesn't move anyone between
// variations.
type configSnapshot struct {
	TargetingRules         json.RawMessage `json:"targetingRules"`
	Rollout                json.RawMessage `json:"rollout"`
	FallthroughVariationID string          `json:"fallthroughVariationId"`
	Version                int64           `json:"version"`
}

// assignmentChanged reports whether a and b would assign subjects
// differently. Version is deliberately ignored.
func assignmentChanged(a, b configSnapshot) bool {
	return a.FallthroughVariationID != b.FallthroughVariationID ||
		!jsonEqual(a.TargetingRules, b.TargetingRules) ||
		!jsonEqual(a.Rollout, b.Rollout)
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	errA, errB := json.Unmarshal(normalize(a), &x), json.Unmarshal(normalize(b), &y)
	if errA != nil || errB != nil {
		return string(a) == string(b)
	}
	return reflect.DeepEqual(x, y)
}

func normalize(r json.RawMessage) []byte {
	if len(r) == 0 {
		return []byte("null")
	}
	return r
}

// configChangedSinceStart compares the config captured at start with the
// config at stop (stopped) or the live config (running). Drafts never
// report a change. This is informational: nothing blocks editing a flag
// while its experiment runs.
func configChangedSinceStart(status Status, start, stop *configSnapshot, live configSnapshot) bool {
	if start == nil {
		return false
	}
	switch status {
	case StatusRunning:
		return assignmentChanged(*start, live)
	case StatusStopped:
		return stop != nil && assignmentChanged(*start, *stop)
	}
	return false
}
