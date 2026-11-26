package quota

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckBoundaries(t *testing.T) {
	for _, c := range []struct {
		have, limit int
		wantErr     bool
	}{
		{0, 50, false}, {49, 50, false}, {50, 50, true}, {51, 50, true}, {0, 0, true},
	} {
		err := check("flags", c.have, c.limit)
		if (err != nil) != c.wantErr {
			t.Errorf("have=%d limit=%d: err=%v", c.have, c.limit, err)
		}
	}
}

func TestLimits(t *testing.T) {
	if MaxEnvironments != 3 || MaxFlags != 50 || MaxActiveSDKKeys != 10 {
		t.Errorf("limits changed: %d %d %d", MaxEnvironments, MaxFlags, MaxActiveSDKKeys)
	}
}

func TestCheckEnvironments(t *testing.T) {
	if err := CheckEnvironments(3); err != nil {
		t.Errorf("3 environments rejected: %v", err)
	}
	if err := CheckEnvironments(4); err == nil {
		t.Error("4 environments accepted")
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	if !WriteError(rec, Exceeded{Resource: "flags", Limit: 50}) {
		t.Fatal("Exceeded not handled")
	}
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"QUOTA_EXCEEDED"`) || !strings.Contains(rec.Body.String(), "at most 50 flags per workspace") {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	if WriteError(rec, errors.New("boom")) || rec.Body.Len() != 0 {
		t.Error("an unrelated error must not be handled")
	}
}

func TestExceededMessageNamesWhatIsCounted(t *testing.T) {
	if got := (Exceeded{Resource: "workspaces", Limit: 5, Per: "user"}).Error(); got != "limit reached: at most 5 workspaces per user" {
		t.Errorf("got %q", got)
	}
	if got := (Exceeded{Resource: "flags", Limit: 50}).Error(); got != "limit reached: at most 50 flags per workspace" {
		t.Errorf("got %q", got)
	}
}
