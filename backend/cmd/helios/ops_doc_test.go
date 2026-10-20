package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runbook(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "OPERATIONS.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A new command under cmd/ that the runbook does not mention fails the build: a job nobody knows to schedule is a job
// that silently never runs.
func TestEveryCommandAndInternalRouteIsInTheRunbook(t *testing.T) {
	doc := runbook(t)
	entries, err := os.ReadDir("..")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n++
		if !strings.Contains(doc, "`cmd/"+e.Name()+"`") && !strings.Contains(doc, "`cmd/"+e.Name()+" ") {
			t.Errorf("cmd/%s exists but docs/OPERATIONS.md does not list it (as `cmd/%s`)", e.Name(), e.Name())
		}
	}
	if n < 8 {
		t.Fatalf("only %d cmd directories found: the test is not looking at the right place", n)
	}
	// and every image command is described with its name
	for name := range Commands {
		if !strings.Contains(doc, "`"+name+"`") && !strings.Contains(doc, "/app/helios "+name) {
			t.Errorf("the image command %q is not in the runbook", name)
		}
	}
}

// Each job is described with the things an operator needs: cadence, what breaks, the log line and a schedule recipe.
func TestEveryJobHasItsOperationalFacts(t *testing.T) {
	doc := runbook(t)
	for _, want := range []string{
		"If it stops", "Log lines to watch", "Safe concurrency", "Cadence", "Secret header",
		"X-Helios-Scheduler-Secret", "X-Helios-Outbox-Secret", "X-Helios-Purge-Secret",
		"/app/helios scheduler", "/app/helios outbox", "/app/helios purge-workspaces", "/app/helios purge-events",
		"Render cron job", "external scheduled call", "SIGTERM", "released",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("OPERATIONS.md lacks %q", want)
		}
	}
}
