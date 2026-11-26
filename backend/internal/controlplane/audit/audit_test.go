package audit

import (
	"context"
	"strings"
	"testing"
)

// An entry without a workspace is refused before any SQL runs (tx is nil: a
// call that reached the database would panic).
func TestWriteRequiresWorkspace(t *testing.T) {
	err := Write(context.Background(), nil, Entry{Action: "flag.create", ActorEmail: "a@example.com"})
	if err == nil || !strings.Contains(err.Error(), "WorkspaceID is required") {
		t.Fatalf("err = %v", err)
	}
}
