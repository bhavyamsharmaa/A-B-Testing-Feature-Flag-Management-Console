package flags

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"helios/backend/internal/dataplane/evaluation"
)

func boolRequest() createRequest {
	return createRequest{
		Key:           "new-checkout",
		Name:          "New checkout",
		VariationType: "boolean",
		Variations:    []evaluation.Variation{{ID: "on", Value: true}, {ID: "off", Value: false}},
	}
}

func TestCreateValidation(t *testing.T) {
	if err := boolRequest().validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	mutations := map[string]func(*createRequest){
		"bad key":       func(r *createRequest) { r.Key = "has space" },
		"no name":       func(r *createRequest) { r.Name = "" },
		"unknown type":  func(r *createRequest) { r.VariationType = "date" },
		"one variation": func(r *createRequest) { r.Variations = r.Variations[:1] },
		"duplicate id":  func(r *createRequest) { r.Variations[1].ID = "on" },
		"empty id":      func(r *createRequest) { r.Variations[0].ID = "" },
		"oversized payload": func(r *createRequest) {
			r.VariationType = "string"
			r.Variations[0].Value = strings.Repeat("x", 5000)
			r.Variations[1].Value = "y"
		},
	}
	for name, mutate := range mutations {
		r := boolRequest()
		mutate(&r)
		if err := r.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	r := boolRequest()
	r.Variations[1].Value = "false"
	var mismatch errTypeMismatch
	if err := r.validate(); !errors.As(err, &mismatch) {
		t.Errorf("string value on boolean flag: got %v, want TYPE_MISMATCH", err)
	}
}

func TestParseRollout(t *testing.T) {
	ids := map[string]bool{"on": true, "off": true}
	if got, err := parseRollout(json.RawMessage(`{"on":10000,"off":90000}`), ids); err != nil || got["on"] != 10000 {
		t.Fatalf("valid rollout: %v, %v", got, err)
	}
	if got, err := parseRollout(json.RawMessage(`null`), ids); err != nil || got != nil {
		t.Fatalf("null rollout should clear: %v, %v", got, err)
	}
	for _, bad := range []string{`{"on":10000}`, `{"on":50000,"ghost":50000}`, `{"on":-1,"off":100001}`, `{"on":"half"}`, `[1,2]`} {
		if _, err := parseRollout(json.RawMessage(bad), ids); err == nil {
			t.Errorf("rollout %s accepted", bad)
		}
	}
}

func TestParseRules(t *testing.T) {
	ids := map[string]bool{"on": true, "off": true}
	if rules, err := parseRules(json.RawMessage(`null`), ids); err != nil || len(rules) != 0 || rules == nil {
		t.Fatalf("null rules should clear to an empty list: %v, %v", rules, err)
	}
	good := `[{"clauses":[{"attribute":"country","operator":"in","value":["IN"]}],"variationId":"on"}]`
	if _, err := parseRules(json.RawMessage(good), ids); err != nil {
		t.Fatalf("valid rules rejected: %v", err)
	}
	bad := `[{"clauses":[{"attribute":"country","operator":"in","value":["IN"]}],"variationId":"ghost"}]`
	if _, err := parseRules(json.RawMessage(bad), ids); err == nil {
		t.Fatal("rule serving unknown variation accepted")
	}
}
