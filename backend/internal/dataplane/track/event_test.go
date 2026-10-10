package track

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func TestParseAcceptsAWellFormedEvent(t *testing.T) {
	raw := `{"id":"e-1","name":"purchase","subjectKey":"user 42","value":19.5,
		"experimentId":"3F2504E0-4F89-41D3-9A0C-0305E82C3301","timestamp":"2026-10-09T11:00:00+02:00","properties":{ "plan": "pro" }}`
	ev, err := parse(json.RawMessage(raw), t0)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Value == nil || *ev.Value != 19.5 || ev.ExperimentID != "3f2504e0-4f89-41d3-9a0c-0305e82c3301" ||
		ev.Properties != `{"plan":"pro"}` || !ev.OccurredAt.Equal(time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("%+v", ev)
	}
	if ev, err := parse(json.RawMessage(`{"id":"e","name":"x","subjectKey":"s"}`), t0); err != nil || !ev.OccurredAt.Equal(t0) {
		t.Errorf("a missing timestamp defaults to now: %+v %v", ev, err)
	}
}

func TestParseRejects(t *testing.T) {
	long := strings.Repeat("x", MaxSubjectKey+1)
	for name, raw := range map[string]string{
		"not an object":              `[]`,
		"unknown field":              `{"id":"e","name":"x","subjectKey":"s","workspaceId":"w"}`,
		"no id":                      `{"name":"x","subjectKey":"s"}`,
		"id too long":                `{"id":"` + strings.Repeat("a", 129) + `","name":"x","subjectKey":"s"}`,
		"reserved name":              `{"id":"e","name":"$custom","subjectKey":"s"}`,
		"empty name":                 `{"id":"e","name":"","subjectKey":"s"}`,
		"no subject":                 `{"id":"e","name":"x"}`,
		"subject too long":           `{"id":"e","name":"x","subjectKey":"` + long + `"}`,
		"string value":               `{"id":"e","name":"x","subjectKey":"s","value":"5"}`,
		"NaN string":                 `{"id":"e","name":"x","subjectKey":"s","value":"NaN"}`,
		"overflow":                   `{"id":"e","name":"x","subjectKey":"s","value":1e999}`,
		"future":                     `{"id":"e","name":"x","subjectKey":"s","timestamp":"2026-10-09T13:00:00Z"}`,
		"too old":                    `{"id":"e","name":"x","subjectKey":"s","timestamp":"2026-01-01T00:00:00Z"}`,
		"not a date":                 `{"id":"e","name":"x","subjectKey":"s","timestamp":"soon"}`,
		"exposure no variation":      `{"id":"e","name":"$exposure","subjectKey":"s","experimentId":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}`,
		"exposure with value":        `{"id":"e","name":"$exposure","subjectKey":"s","value":1,"variationId":"on","experimentId":"3f2504e0-4f89-41d3-9a0c-0305e82c3301"}`,
		"variation on a plain event": `{"id":"e","name":"x","subjectKey":"s","variationId":"on"}`,
		"experiment not a uuid":      `{"id":"e","name":"x","subjectKey":"s","experimentId":"nope"}`,
		"properties scalar":          `{"id":"e","name":"x","subjectKey":"s","properties":"a"}`,
		"properties too big":         `{"id":"e","name":"x","subjectKey":"s","properties":{"k":"` + strings.Repeat("v", MaxPropertyBytes) + `"}}`,
		"properties NUL":             `{"id":"e","name":"x","subjectKey":"s","properties":{"k":"\u0000"}}`,
	} {
		if _, err := parse(json.RawMessage(raw), t0); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A message that comes back to the client never quotes what it was sent.
func TestParseErrorsDoNotEchoTheEvent(t *testing.T) {
	const secret = "SECRETSUBJECT-123"
	for _, raw := range []string{
		`{"id":"e","name":"x","subjectKey":"s","` + secret + `":1}`,
		`{"id":"e","name":"x","subjectKey":"s","timestamp":"` + secret + `"}`,
		`{"id":"e","name":"x","subjectKey":"s","value":"` + secret + `"}`,
		`{"id":"` + secret + `!","name":"x","subjectKey":"s"}`,
		`{"id":"e","name":"` + secret + `!","subjectKey":"s"}`,
		`{"id":"e","name":"x","subjectKey":"s","properties":"` + secret + `"}`,
		`{"id":"e","name":"x","subjectKey":"s","experimentId":"` + secret + `"}`,
	} {
		_, err := parse(json.RawMessage(raw), t0)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("%s -> %v", raw, err)
		}
	}
}
