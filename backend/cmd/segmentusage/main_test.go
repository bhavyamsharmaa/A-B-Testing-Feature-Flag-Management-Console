package main

import (
	"strings"
	"testing"

	"helios/backend/internal/controlplane/segments"
)

func TestReport(t *testing.T) {
	if got := report(nil); !strings.Contains(got, "no flag rule uses") || strings.Contains(got, "workspace ") {
		t.Errorf("empty report: %q", got)
	}
	got := report([]segments.FlagUse{
		{WorkspaceID: "w1", FlagKey: "a", Environment: "dev", Features: []string{"inSegment", "negate"}},
		{WorkspaceID: "w1", FlagKey: "a", Environment: "staging", Features: []string{"inSegment"}},
		{WorkspaceID: "w2", FlagKey: "b", Environment: "dev", Features: []string{"matchesRegex"}},
	})
	for _, want := range []string{"3 flag configuration(s) in 2 workspace(s)", "workspace w1: 2 flag configuration(s)", "workspace w2: 1 flag configuration(s)",
		"uses: inSegment, negate", "env=staging", "BEFORE rolling back"} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
}
