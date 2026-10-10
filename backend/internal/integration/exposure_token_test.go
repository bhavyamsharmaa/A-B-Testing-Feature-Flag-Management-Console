package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"helios/backend/internal/dataplane/track"
	"helios/backend/internal/platform/exposuretoken"
	"helios/backend/internal/server"
)

const (
	tokSecretCurrent  = "CURRENT-signing-secret-value-for-tests-0123456789"
	tokSecretPrevious = "PREVIOUS-signing-secret-value-for-tests-012345678"
)

func tokenRing(t *testing.T, current, previous string) *exposuretoken.Keyring {
	t.Helper()
	k, err := exposuretoken.NewKeyring(current, previous, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func withTokens(k *exposuretoken.Keyring, mode exposuretoken.Mode) func(*server.Deps) {
	return func(d *server.Deps) { d.ExposureTokens, d.ExposureMode = k, mode }
}

type evalOut struct {
	Evaluations []struct {
		FlagKey       string  `json:"flagKey"`
		VariationID   *string `json:"variationId"`
		ExperimentID  *string `json:"experimentId"`
		ExposureToken *string `json:"exposureToken"`
	} `json:"evaluations"`
}

// evaluate asks /evaluate as an SDK would and returns the served variation and its token.
func (x *tenants) evaluate(key, subject, flag string) (variation, experimentID, token string, raw []byte) {
	x.h.t.Helper()
	raw = x.h.sdkPost(key, "/evaluate", map[string]any{"context": map[string]any{"subjectKey": subject}, "flagKeys": []string{flag}})
	var out evalOut
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Evaluations) != 1 {
		x.h.t.Fatalf("evaluate: %v %s", err, raw)
	}
	e := out.Evaluations[0]
	if e.VariationID != nil {
		variation = *e.VariationID
	}
	if e.ExperimentID != nil {
		experimentID = *e.ExperimentID
	}
	if e.ExposureToken != nil {
		token = *e.ExposureToken
	}
	return
}

func exposureWith(id, exp, variation, subject, token string) map[string]any {
	e := exposure(id, exp, variation, subject)
	if token != "" {
		e["exposureToken"] = token
	}
	return e
}

func other(variation string) string {
	if variation == "on" {
		return "off"
	}
	return "on"
}

func (h *harness) verifiedValue(eventID string) *bool {
	h.t.Helper()
	var v *bool
	if err := h.pool.QueryRow(context.Background(), `SELECT exposure_verified FROM events WHERE event_id = $1`, eventID).Scan(&v); err != nil {
		h.t.Fatalf("event %s: %v", eventID, err)
	}
	return v
}

func code(r trackResp, i int) string {
	if len(r.Results) <= i {
		return "NO RESULT"
	}
	if r.Results[i].Code != "" {
		return r.Results[i].Status + ":" + r.Results[i].Code
	}
	return r.Results[i].Status
}

// Tokens are issued only for a flag in a running experiment; every other
// evaluation is byte for byte what it was without tokens.
func TestEvaluateIssuesTokensOnlyForRunningExperiments(t *testing.T) {
	ring := tokenRing(t, "k1:"+tokSecretCurrent, "")
	with := newTenantsWith(t, withTokens(ring, exposuretoken.ModeRequired))
	h := with.h
	h.expect(201, with.a, "POST", "/environments/"+with.devA+"/flags", boolFlag("plain"))
	h.expect(200, with.a, "PATCH", "/environments/"+with.devA+"/flags/plain", map[string]any{"enabled": true})

	_, _, tok, plainWith := with.evaluate(with.sdkKeyA, "u1", "plain")
	if tok != "" || bytes.Contains(plainWith, []byte("exposureToken")) {
		t.Fatalf("a flag outside any experiment got a token: %s", plainWith)
	}
	// the same flag with no token machinery at all
	without := newTenants(t)
	without.h.expect(201, without.a, "POST", "/environments/"+without.devA+"/flags", boolFlag("plain"))
	without.h.expect(200, without.a, "PATCH", "/environments/"+without.devA+"/flags/plain", map[string]any{"enabled": true})
	_, _, _, plainWithout := without.evaluate(without.sdkKeyA, "u1", "plain")
	if !bytes.Equal(plainWith, plainWithout) {
		t.Errorf("tokens changed an unrelated flag's answer:\n with    %s\n without %s", plainWith, plainWithout)
	}

	_, _, draftTok, _ := with.evaluate(with.sdkKeyA, "u1", secretFlag)
	if draftTok != "" {
		t.Error("a draft experiment issued a token")
	}
	exp := with.runningExperiment()
	variation, expID, token, raw := with.evaluate(with.sdkKeyA, "u1", secretFlag)
	if expID != exp || variation == "" || !strings.HasPrefix(token, "v1.k1.") {
		t.Fatalf("running experiment: %s", raw)
	}
	for _, secret := range []string{tokSecretCurrent, "u1"} {
		if strings.Contains(token, secret) {
			t.Errorf("the token contains %q", secret)
		}
	}
	if bytes.Contains(raw, []byte(tokSecretCurrent)) {
		t.Error("the secret is in an evaluate response")
	}
}

func TestRequiredModeRejectsEveryKindOfBadToken(t *testing.T) {
	logs := captureLogs(t)
	ring := tokenRing(t, "k2:"+tokSecretCurrent, "k1:"+tokSecretPrevious)
	x := newTenantsWith(t, withTokens(ring, exposuretoken.ModeRequired))
	h := x.h
	exp := x.runningExperiment()

	// a second experiment (another flag) and another tenant with a running experiment of its own
	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", boolFlag("second"))
	h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/second", map[string]any{"enabled": true})
	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/experiments", map[string]any{
		"flagKey": "second", "key": "second-exp", "name": "second",
		"metrics": []map[string]any{{"name": "M", "eventName": "e", "type": "conversion", "isPrimary": true}}})
	h.expect(200, x.a, "POST", "/environments/"+x.devA+"/experiments/second-exp/start", nil)
	_, exp2, tok2, _ := x.evaluate(x.sdkKeyA, "alice", "second")

	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/flags", boolFlag(secretFlag))
	h.expect(200, x.b, "PATCH", "/environments/"+x.devB+"/flags/"+secretFlag, map[string]any{"enabled": true})
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/experiments", map[string]any{
		"flagKey": secretFlag, "key": "bobs", "name": "bobs",
		"metrics": []map[string]any{{"name": "M", "eventName": "e", "type": "conversion", "isPrimary": true}}})
	h.expect(200, x.b, "POST", "/environments/"+x.devB+"/experiments/bobs/start", nil)
	keyB := h.newSDKKey(x.b, x.devB)
	_, _, tokB, _ := x.evaluate(keyB, "alice", secretFlag) // a genuine token, from another workspace

	variation, _, good, _ := x.evaluate(x.sdkKeyA, "alice", secretFlag)
	bind := exposuretoken.Binding{Workspace: x.wsA.ID, Environment: x.devA, Experiment: exp, Variation: variation, Subject: "alice"}
	expired := ring.Issue(bind, time.Now().Add(-25*time.Hour))
	unknownKid := tokenRing(t, "zz:"+tokSecretCurrent, "").Issue(bind, time.Now()) // right secret, id the server does not know
	forged := tokenRing(t, "k2:"+"another-secret-that-is-long-enough-123456", "").Issue(bind, time.Now())
	previousKey := tokenRing(t, "k1:"+tokSecretPrevious, "").Issue(bind, time.Now()) // rotated: still accepted

	cases := []struct {
		name string
		ev   map[string]any
		want string
	}{
		{"a genuine token", exposureWith("g1", exp, variation, "alice", good), "accepted"},
		{"a token signed with the previous key (rotation overlap)", exposureWith("g2", exp, variation, "alice2", tokenRing(t, "k1:"+tokSecretPrevious, "").Issue(
			exposuretoken.Binding{Workspace: x.wsA.ID, Environment: x.devA, Experiment: exp, Variation: variation, Subject: "alice2"}, time.Now())), "accepted"},
		{"no token", exposureWith("b1", exp, variation, "bob", ""), "rejected:" + track.CodeExposureTokenMissing},
		{"a forged token", exposureWith("b2", exp, variation, "alice", forged), "rejected:" + track.CodeExposureTokenInvalid},
		{"garbage", exposureWith("b3", exp, variation, "alice", "v1.k2.1.AAAA"), "rejected:" + track.CodeExposureTokenInvalid},
		{"a token for another subject", exposureWith("b4", exp, variation, "mallory", good), "rejected:" + track.CodeExposureTokenInvalid},
		{"a token for another variation", exposureWith("b5", exp, other(variation), "alice", good), "rejected:" + track.CodeExposureTokenInvalid},
		{"a token for another experiment", exposureWith("b6", exp, variation, "alice", tok2), "rejected:" + track.CodeExposureTokenInvalid},
		{"a token from another workspace", exposureWith("b7", exp, variation, "alice", tokB), "rejected:" + track.CodeExposureTokenInvalid},
		{"an expired token", exposureWith("b8", exp, variation, "alice", expired), "rejected:" + track.CodeExposureTokenExpired},
		{"a token signed with an unknown key id", exposureWith("b9", exp, variation, "alice", unknownKid), "rejected:" + track.CodeExposureTokenInvalid},
	}
	_ = exp2
	_ = previousKey
	var all []map[string]any
	for _, c := range cases {
		all = append(all, c.ev)
	}
	resp := h.track(x.sdkKeyA, all...)
	if resp.Status != 200 {
		t.Fatalf("%d %s", resp.Status, resp.Raw)
	}
	for i, c := range cases {
		if got := code(resp, i); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if rows := h.eventRows(`event_name = '$exposure'`); rows != 2 {
		t.Errorf("%d exposures stored, want only the 2 that verified", rows)
	}
	if v := h.verifiedValue("g1"); v == nil || !*v {
		t.Errorf("a verified exposure is stored as %v", v)
	}
	// a non-exposure event never needs a token, and carrying one is refused
	if r := h.track(x.sdkKeyA, evt("n1", "purchase", "alice", map[string]any{"experimentId": exp})); r.Counts.Accepted != 1 {
		t.Errorf("a plain event needs no token: %s", r.Raw)
	}
	if r := h.track(x.sdkKeyA, evt("n2", "purchase", "alice", map[string]any{"exposureToken": good})); r.Counts.Rejected != 1 {
		t.Errorf("a token on a non-exposure: %s", r.Raw)
	}

	// Nothing in the responses or the logs quotes a token or a secret; the messages are fixed.
	for i := range cases {
		msg := resp.Results[i].Message
		for _, tok := range []string{good, forged, expired, unknownKid, tokB, tok2} {
			if strings.Contains(msg, tok) {
				t.Errorf("a message quotes a token: %s", msg)
			}
		}
	}
	for _, secret := range []string{tokSecretCurrent, tokSecretPrevious} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("a signing secret is in the logs")
		}
		if bytes.Contains(resp.Raw, []byte(secret)) {
			t.Errorf("a signing secret is in a response")
		}
		h.mu.Lock()
		for _, b := range h.bodies {
			if bytes.Contains(b, []byte(secret)) {
				t.Errorf("a signing secret is in a response body")
			}
		}
		h.mu.Unlock()
	}
	if strings.Contains(logs.String(), good) || strings.Contains(logs.String(), forged) {
		t.Error("a token is in the logs")
	}
	_ = fmt.Sprint
}

// Verification off: old clients (no token) keep working and nothing is stored about tokens.
func TestOffModeAcceptsOldClientsAndStoresNothing(t *testing.T) {
	x := newTenantsWith(t, withTokens(tokenRing(t, "k1:"+tokSecretCurrent, ""), exposuretoken.ModeOff))
	exp := x.runningExperiment()
	r := x.h.track(x.sdkKeyA, exposure("old1", exp, "on", "u1"), exposureWith("old2", exp, "on", "u2", "v1.k1.1.AAAA"))
	if r.Counts.Accepted != 2 {
		t.Fatalf("%s", r.Raw)
	}
	if x.h.verifiedValue("old1") != nil || x.h.verifiedValue("old2") != nil {
		t.Error("off mode recorded a verification result")
	}
	// and with no keyring configured at all
	y := newTenants(t)
	expY := y.runningExperiment()
	if r := y.h.track(y.sdkKeyA, exposure("old3", expY, "on", "u1")); r.Counts.Accepted != 1 {
		t.Errorf("%s", r.Raw)
	}
}

// Report mode: everything is accepted, the verification result is stored, and
// the results API warns with the count of unverified exposures.
func TestReportModeAcceptsRecordsAndWarns(t *testing.T) {
	x := newTenantsWith(t, withTokens(tokenRing(t, "k1:"+tokSecretCurrent, ""), exposuretoken.ModeReport))
	h := x.h
	exp := x.backdated()
	var good []map[string]any
	for i := 0; i < 3; i++ {
		subject := fmt.Sprintf("v%d", i)
		variation, _, token, _ := x.evaluate(x.sdkKeyA, subject, secretFlag)
		good = append(good, exposureWith("good-"+subject, exp, variation, subject, token))
	}
	bad := []map[string]any{
		exposureWith("bad-none", exp, "on", "n1", ""),
		exposureWith("bad-forged", exp, "off", "n2", "v1.k1.1700000000.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
	}
	r := h.track(x.sdkKeyA, append(good, bad...)...)
	if r.Counts.Accepted != 5 || r.Counts.Rejected != 0 {
		t.Fatalf("report mode must accept everything: %s", r.Raw)
	}
	for _, id := range []string{"good-v0", "good-v1", "good-v2"} {
		if v := h.verifiedValue(id); v == nil || !*v {
			t.Errorf("%s stored as %v, want true", id, v)
		}
	}
	for _, id := range []string{"bad-none", "bad-forged"} {
		if v := h.verifiedValue(id); v == nil || *v {
			t.Errorf("%s stored as %v, want false", id, v)
		}
	}
	res := x.results(x.a, x.devA, expKey, "")
	w, ok := warningCodes(res)["EXPOSURES_UNVERIFIED"]
	if !ok || w.Detail["exposures"] != float64(2) {
		t.Fatalf("the results warning is missing or wrong: %+v", res.Warnings)
	}
	// another tenant's experiment never sees this count
	if res := x.results(x.a, x.devA, expKey, ""); res.SampleSize != 5 {
		t.Errorf("unverified exposures are still counted (report mode): %d", res.SampleSize)
	}
}

// Nothing to warn about when every exposure verified.
func TestNoWarningWhenEveryExposureVerified(t *testing.T) {
	x := newTenantsWith(t, withTokens(tokenRing(t, "k1:"+tokSecretCurrent, ""), exposuretoken.ModeReport))
	exp := x.backdated()
	variation, _, token, _ := x.evaluate(x.sdkKeyA, "only", secretFlag)
	if r := x.h.track(x.sdkKeyA, exposureWith("e1", exp, variation, "only", token)); r.Counts.Accepted != 1 {
		t.Fatal(string(r.Raw))
	}
	if _, ok := warningCodes(x.results(x.a, x.devA, expKey, ""))["EXPOSURES_UNVERIFIED"]; ok {
		t.Error("a warning about unverified exposures with none present")
	}
}
