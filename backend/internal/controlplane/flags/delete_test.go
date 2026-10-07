package flags

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A running experiment blocks deletion with a 409 whose code the console and
// API clients can match on. The handler returns this before it looks at
// ?force, so force cannot bypass it.
func TestHasRunningExperimentResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	writeHasRunningExperiment(rec, []string{"production/checkout-test", "staging/checkout-test"})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body struct{ Code, Message string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "HAS_RUNNING_EXPERIMENT" {
		t.Errorf("code = %q", body.Code)
	}
	for _, want := range []string{"production/checkout-test", "staging/checkout-test"} {
		if !strings.Contains(body.Message, want) {
			t.Errorf("message %q does not name %s", body.Message, want)
		}
	}
}
