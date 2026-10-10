// Package track implements POST /track: batched event ingestion from an SDK
// key. The workspace and environment of every event come from the key alone;
// nothing in the payload can name a tenant.
//
// Nothing here logs an event body, a subject key or a property: customers
// choose those strings and they may be personal data. Errors returned to the
// client are fixed texts that never echo what was sent.
package track

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ExposureEvent is the reserved event name for "this subject was shown this
// variation of this experiment".
const ExposureEvent = "$exposure"

const (
	MaxBatch         = 100
	MaxBodyBytes     = 256 << 10
	MaxPropertyBytes = 2048
	MaxSubjectKey    = 256
	// Retention is how long events are kept (docs/EVENTS.md). An event older
	// than this would be purged at once, so it is refused.
	Retention = 90 * 24 * time.Hour
	// MaxFuture tolerates client clock skew.
	MaxFuture = 5 * time.Minute
)

var (
	idRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// wireEvent is one event as sent. Every field is optional here; validate
// decides what is required.
type wireEvent struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	SubjectKey   string          `json:"subjectKey"`
	Value        json.RawMessage `json:"value"`
	ExperimentID string          `json:"experimentId"`
	VariationID  string          `json:"variationId"`
	Timestamp    string          `json:"timestamp"`
	Properties   json.RawMessage `json:"properties"`
	// ExposureToken is the proof /evaluate issued; only meaningful on an $exposure.
	ExposureToken string `json:"exposureToken"`
}

// Event is a validated event, ready to store.
type Event struct {
	ID           string
	Name         string
	SubjectKey   string
	Value        *float64
	ExperimentID string // "" when the event belongs to no experiment
	VariationID  string // only for $exposure
	OccurredAt   time.Time
	Properties   string // compact JSON object, or ""
	// ExposureToken as sent ("" when none), consumed by the handler's verification.
	ExposureToken string
	// Verified is the result of checking the token: nil when not checked
	// (verification off, or not an $exposure), else whether it verified.
	Verified *bool
}

// Reject codes returned per event.
const (
	CodeInvalidEvent       = "INVALID_EVENT"
	CodeExperimentNotFound = "EXPERIMENT_NOT_FOUND"
	CodeExperimentNotRun   = "EXPERIMENT_NOT_RUNNING"
	CodeInvalidVariation   = "INVALID_VARIATION"
	// Exposure verification (EXPOSURE_VERIFICATION=required). The codes do not say
	// WHICH check failed beyond "missing", "expired" and "not valid": a signature
	// failure, an unknown key id and a token for another subject look the same.
	CodeExposureTokenMissing = "EXPOSURE_TOKEN_MISSING"
	CodeExposureTokenInvalid = "EXPOSURE_TOKEN_INVALID"
	CodeExposureTokenExpired = "EXPOSURE_TOKEN_EXPIRED"
)

// invalid is a validation failure with a fixed, content-free message.
type invalid struct{ msg string }

func (e invalid) Error() string { return e.msg }

func bad(msg string) error { return invalid{msg} }

// parse decodes and validates one raw event. now is passed in for tests.
func parse(raw json.RawMessage, now time.Time) (Event, error) {
	var w wireEvent
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil || dec.More() {
		// The decoder's own message can quote what was sent; say nothing of it.
		return Event{}, bad("event is not a valid JSON object with known fields")
	}
	var ev Event
	if !idRE.MatchString(w.ID) {
		return ev, bad("id is required: 1-128 characters of letters, digits, '_', '.', ':', '-', starting with a letter or digit")
	}
	ev.ID = w.ID
	switch {
	case w.Name == ExposureEvent:
	case strings.HasPrefix(w.Name, "$"):
		return ev, bad("event names starting with '$' are reserved")
	case !nameRE.MatchString(w.Name):
		return ev, bad("name is required: 1-128 characters of letters, digits, '_', '.', ':', '-', starting with a letter or digit")
	}
	ev.Name = w.Name
	if n := utf8.RuneCountInString(w.SubjectKey); n < 1 || n > MaxSubjectKey || strings.ContainsRune(w.SubjectKey, 0) {
		return ev, bad("subjectKey is required, at most 256 characters and without NUL")
	}
	ev.SubjectKey = w.SubjectKey

	if len(w.Value) > 0 && !bytes.Equal(w.Value, []byte("null")) {
		v, err := parseNumber(w.Value)
		if err != nil {
			return ev, err
		}
		ev.Value = &v
	}
	if w.ExperimentID != "" {
		if !uuidRE.MatchString(w.ExperimentID) {
			return ev, bad("experimentId must be an experiment id (a UUID)")
		}
		ev.ExperimentID = strings.ToLower(w.ExperimentID)
	}
	if w.Name == ExposureEvent {
		if ev.ExperimentID == "" || w.VariationID == "" {
			return ev, bad("an $exposure needs experimentId and variationId")
		}
		if ev.Value != nil {
			return ev, bad("an $exposure carries no value")
		}
		if len(w.VariationID) > 128 || strings.ContainsRune(w.VariationID, 0) {
			return ev, bad("variationId is at most 128 characters")
		}
		ev.VariationID = w.VariationID
		if len(w.ExposureToken) > 256 || strings.ContainsRune(w.ExposureToken, 0) {
			return ev, bad("exposureToken is at most 256 characters")
		}
		ev.ExposureToken = w.ExposureToken
	} else if w.VariationID != "" {
		return ev, bad("variationId is only allowed on an $exposure")
	} else if w.ExposureToken != "" {
		return ev, bad("exposureToken is only allowed on an $exposure")
	}

	ev.OccurredAt = now
	if w.Timestamp != "" {
		ts, err := time.Parse(time.RFC3339Nano, w.Timestamp)
		if err != nil {
			return ev, bad("timestamp must be RFC 3339, e.g. 2026-01-02T15:04:05Z")
		}
		if ts.After(now.Add(MaxFuture)) {
			return ev, bad("timestamp is in the future")
		}
		if ts.Before(now.Add(-Retention)) {
			return ev, bad("timestamp is older than the retention window")
		}
		ev.OccurredAt = ts.UTC()
	}

	if len(w.Properties) > 0 && !bytes.Equal(w.Properties, []byte("null")) {
		props, err := compactObject(w.Properties)
		if err != nil {
			return ev, err
		}
		ev.Properties = props
	}
	return ev, nil
}

func parseNumber(raw json.RawMessage) (float64, error) {
	s := string(bytes.TrimSpace(raw))
	// Only a JSON number: not a string like "NaN" or "1".
	if s == "" || !(s[0] == '-' || (s[0] >= '0' && s[0] <= '9')) {
		return 0, bad("value must be a finite number")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, bad("value must be a finite number")
	}
	return v, nil
}

func compactObject(raw json.RawMessage) (string, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
		return "", bad("properties must be a JSON object")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", bad("properties must be a JSON object")
	}
	if buf.Len() > MaxPropertyBytes {
		return "", bad("properties are at most 2048 bytes")
	}
	// Postgres jsonb cannot store a NUL escape; it would fail the whole batch.
	if bytes.Contains(buf.Bytes(), []byte(`\u0000`)) {
		return "", bad("properties must not contain NUL")
	}
	return buf.String(), nil
}
