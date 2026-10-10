package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"helios/backend/internal/dataplane/track"
	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/server"
)

const expKey = "acme-secret-exp"

// trackResp is what POST /track answered.
type trackResp struct {
	Status  int
	Header  http.Header
	Raw     []byte
	Results []track.Result
	Counts  struct{ Accepted, Duplicates, Rejected int }
}

func (r trackResp) statuses() []string {
	out := make([]string, len(r.Results))
	for i, x := range r.Results {
		out[i] = x.Status
		if x.Code != "" {
			out[i] += ":" + x.Code
		}
	}
	return out
}

func trackRaw(t *testing.T, base, sdkKey string, body []byte) trackResp {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/track", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Helios-SDK-Key", sdkKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := trackResp{Status: res.StatusCode, Header: res.Header, Raw: raw}
	var parsed struct {
		Results                        []track.Result
		Accepted, Duplicates, Rejected int
	}
	if json.Unmarshal(raw, &parsed) == nil {
		out.Results = parsed.Results
		out.Counts.Accepted, out.Counts.Duplicates, out.Counts.Rejected = parsed.Accepted, parsed.Duplicates, parsed.Rejected
	}
	return out
}

func (h *harness) track(sdkKey string, events ...map[string]any) trackResp {
	h.t.Helper()
	b, _ := json.Marshal(map[string]any{"events": events})
	return trackRaw(h.t, h.srv.URL, sdkKey, b)
}

func evt(id, name, subject string, extra ...map[string]any) map[string]any {
	e := map[string]any{"id": id, "name": name, "subjectKey": subject}
	for _, m := range extra {
		for k, v := range m {
			e[k] = v
		}
	}
	return e
}

func exposure(id, exp, variation, subject string) map[string]any {
	return evt(id, track.ExposureEvent, subject, map[string]any{"experimentId": exp, "variationId": variation})
}

// runningExperiment starts A's experiment (the flag is already enabled) and returns its id.
func (x *tenants) runningExperiment() string {
	x.h.t.Helper()
	x.h.expect(200, x.a, "POST", "/environments/"+x.devA+"/experiments/"+expKey+"/start", nil)
	return x.expID()
}

func (x *tenants) expID() string {
	var id string
	if err := x.h.pool.QueryRow(context.Background(), `SELECT id::text FROM experiments WHERE key = $1`, expKey).Scan(&id); err != nil {
		x.h.t.Fatal(err)
	}
	return id
}

func (h *harness) eventRows(where string, args ...any) int {
	h.t.Helper()
	return h.count(`SELECT count(*) FROM events WHERE `+where, args...)
}

// ---------------------------------------------------------------- /evaluate

func TestEvaluateReportsTheExperimentOnlyWhileItRuns(t *testing.T) {
	x := newTenants(t)
	h := x.h
	h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", boolFlag("plain"))
	h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/plain", map[string]any{"enabled": true})
	eval := func(flags ...string) []byte {
		b, _ := json.Marshal(map[string]any{"context": map[string]any{"subjectKey": "u1"}, "flagKeys": flags})
		req, _ := http.NewRequest("POST", h.srv.URL+"/evaluate", bytes.NewReader(b))
		req.Header.Set("X-Helios-SDK-Key", x.sdkKeyA)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return out
	}
	type evals struct {
		Evaluations []struct {
			FlagKey      string
			VariationID  *string
			ExperimentID *string
		}
	}
	expOf := func(body []byte) (*string, *string) {
		e := decode[evals](t, body).Evaluations[0]
		return e.ExperimentID, e.VariationID
	}

	plainBefore := eval("plain")
	draft := eval(secretFlag)
	if id, _ := expOf(draft); id != nil {
		t.Errorf("a DRAFT experiment is reported: %s", draft)
	}

	id := x.runningExperiment()
	running := eval(secretFlag)
	gotID, variation := expOf(running)
	if gotID == nil || *gotID != id || variation == nil {
		t.Fatalf("a running experiment is not reported with the served variation: %s", running)
	}
	// Flags outside the experiment: the bytes of the answer do not change.
	if after := eval("plain"); !bytes.Equal(plainBefore, after) {
		t.Errorf("an unrelated flag's evaluation changed:\n before %s\n after  %s", plainBefore, after)
	}
	// The served variation is the flag's own: starting the experiment re-buckets nobody.
	if d1, _ := expOf(draft); d1 != nil {
		t.Fatal("unreachable")
	}
	var draftVar, runVar *string
	{
		e1 := decode[evals](t, draft).Evaluations[0]
		draftVar = e1.VariationID
		runVar = variation
	}
	if draftVar == nil || runVar == nil || *draftVar != *runVar {
		t.Errorf("starting the experiment changed the served variation: %v -> %v", draftVar, runVar)
	}

	// A flag that serves nothing (killed) has no exposure to report.
	h.expect(200, x.a, "POST", "/environments/"+x.devA+"/flags/"+secretFlag+"/kill", nil)
	if id, v := expOf(eval(secretFlag)); id != nil || v != nil {
		t.Error("a killed flag still reports an experiment")
	}
	h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/"+secretFlag, map[string]any{"enabled": true})

	h.expect(200, x.a, "POST", "/environments/"+x.devA+"/experiments/"+expKey+"/stop", nil)
	if id, _ := expOf(eval(secretFlag)); id != nil {
		t.Error("a STOPPED experiment is still reported")
	}
}

// ---------------------------------------------------------------- exposures

func TestExposureIsRecordedOncePerSubjectEvenInParallel(t *testing.T) {
	x := newTenants(t)
	h := x.h
	exp := x.runningExperiment()

	const n = 50
	var wg sync.WaitGroup
	statuses := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := h.track(x.sdkKeyA, exposure(fmt.Sprintf("e-%d", i), exp, "on", "subject-1"))
			if r.Status != 200 || len(r.Results) != 1 {
				statuses[i] = fmt.Sprintf("HTTP %d %s", r.Status, r.Raw)
				return
			}
			statuses[i] = r.Results[0].Status
		}(i)
	}
	wg.Wait()
	accepted := 0
	for _, s := range statuses {
		switch s {
		case track.StatusAccepted:
			accepted++
		case track.StatusDuplicate:
		default:
			t.Errorf("unexpected outcome %q", s)
		}
	}
	if accepted != 1 {
		t.Errorf("%d of %d parallel exposures were accepted, want exactly 1", accepted, n)
	}
	if rows := h.eventRows(`event_name = '$exposure' AND experiment_id = $1::uuid`, exp); rows != 1 {
		t.Errorf("%d exposure rows, want 1", rows)
	}

	// The first exposure wins: a later one for another variation changes nothing.
	r := h.track(x.sdkKeyA, exposure("late", exp, "off", "subject-1"))
	if r.Results[0].Status != track.StatusDuplicate {
		t.Errorf("a second exposure: %v", r.statuses())
	}
	var variation string
	if err := h.pool.QueryRow(context.Background(), `SELECT variation_id FROM events WHERE event_name = '$exposure'`).Scan(&variation); err != nil || variation != "on" {
		t.Errorf("the stored exposure is %q (%v), want the first, \"on\"", variation, err)
	}
	// Another subject is a new exposure.
	if r := h.track(x.sdkKeyA, exposure("other", exp, "off", "subject-2")); r.Results[0].Status != track.StatusAccepted {
		t.Errorf("a new subject: %v", r.statuses())
	}
}

func TestRetryingABatchCreatesNoDuplicates(t *testing.T) {
	x := newTenants(t)
	h := x.h
	exp := x.runningExperiment()
	batch := []map[string]any{
		exposure("a1", exp, "on", "s1"),
		evt("a2", "purchase", "s1", map[string]any{"experimentId": exp, "value": 19.99}),
		evt("a3", "page_view", "s2", map[string]any{"properties": map[string]any{"path": "/x"}}),
	}
	first := h.track(x.sdkKeyA, batch...)
	if first.Status != 200 || first.Counts.Accepted != 3 {
		t.Fatalf("first send: %d %s", first.Status, first.Raw)
	}
	again := h.track(x.sdkKeyA, batch...)
	if again.Counts.Accepted != 0 || again.Counts.Duplicates != 3 {
		t.Errorf("retry: %s", again.Raw)
	}
	if rows := h.eventRows(`true`); rows != 3 {
		t.Errorf("%d rows after a retry, want 3", rows)
	}
	// The same id twice inside one batch counts once, too.
	dup := h.track(x.sdkKeyA, evt("same", "x", "s"), evt("same", "x", "s"))
	if dup.Counts.Accepted != 1 || dup.Counts.Duplicates != 1 {
		t.Errorf("duplicate inside a batch: %s", dup.Raw)
	}
}

// ------------------------------------------------------------- cross-tenant

func TestTrackKeysAreConfinedToTheirOwnTenant(t *testing.T) {
	logs := captureLogs(t)
	x := newTenants(t)
	h := x.h
	exp := x.runningExperiment()
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/flags", boolFlag("bobs-flag"))
	keyB := h.newSDKKey(x.b, x.devB)

	// B's key naming A's experiment: not found, exactly like an id that never existed.
	real := h.track(keyB, exposure("x1", exp, "on", "victim-subject"))
	ghost := h.track(keyB, exposure("x1", "00000000-0000-4000-8000-000000000000", "on", "victim-subject"))
	if real.Results[0].Code != track.CodeExperimentNotFound || ghost.Results[0].Code != track.CodeExperimentNotFound {
		t.Fatalf("A's experiment via B's key: %s / ghost: %s", real.Raw, ghost.Raw)
	}
	if real.Results[0].Message != ghost.Results[0].Message {
		t.Error("A's experiment is distinguishable from one that does not exist")
	}
	// The payload cannot name a tenant.
	for _, field := range []string{"workspaceId", "environmentId", "workspace_id", "environment_id"} {
		r := h.track(keyB, evt("w", "x", "s", map[string]any{field: x.wsA.ID}))
		if r.Counts.Rejected != 1 {
			t.Errorf("a %q field was accepted: %s", field, r.Raw)
		}
	}
	if rows := h.eventRows(`true`); rows != 0 {
		t.Fatalf("%d events were written by refused requests", rows)
	}

	// A good event on B's key lands in B's workspace and environment, and only there.
	if r := h.track(keyB, evt("ok", "page_view", "s")); r.Counts.Accepted != 1 {
		t.Fatal(string(r.Raw))
	}
	var ws, env string
	if err := h.pool.QueryRow(context.Background(), `SELECT workspace_id::text, environment_id::text FROM events`).Scan(&ws, &env); err != nil {
		t.Fatal(err)
	}
	if ws != x.wsB.ID || env != x.devB {
		t.Errorf("B's event is in workspace %s / environment %s", ws, env)
	}
	// And A's counts (control plane) never show it, nor does B's see A's experiment.
	h.expect(200, x.a, "POST", "/environments/"+x.devA+"/experiments/"+expKey+"/stop", nil)
	if r := h.track(x.sdkKeyA, evt("late", "x", "s", map[string]any{"experimentId": exp})); r.Results[0].Code != track.CodeExperimentNotRun {
		t.Errorf("an event for a stopped experiment: %s", r.Raw)
	}

	// Nothing of A's appears in anything B's key was told, in the logs, or in B's view.
	for _, secret := range []string{exp, secretFlag, expKey, x.wsA.ID, x.devA} {
		for _, body := range [][]byte{real.Raw, ghost.Raw} {
			if bytes.Contains(body, []byte(secret)) && secret != exp { // the id B itself sent may be echoed back
				t.Errorf("a response to B's key contains %q: %s", secret, body)
			}
		}
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the logs contain %q", secret)
		}
	}
}

// ------------------------------------------------------------- validation

func TestTrackValidation(t *testing.T) {
	x := newTenants(t)
	h := x.h
	exp := x.runningExperiment()
	now := time.Now().UTC()

	t.Run("body and batch limits", func(t *testing.T) {
		big := make([]byte, 0, 300<<10)
		big = append(big, `{"events":[{"id":"a","name":"n","subjectKey":"`...)
		big = append(big, bytes.Repeat([]byte("x"), 300<<10)...)
		big = append(big, `"}]}`...)
		if r := trackRaw(t, h.srv.URL, x.sdkKeyA, big); r.Status != 413 {
			t.Errorf("a 300 KB body: %d %s", r.Status, r.Raw)
		}
		many := make([]map[string]any, 101)
		for i := range many {
			many[i] = evt(fmt.Sprintf("m%d", i), "n", "s")
		}
		if r := h.track(x.sdkKeyA, many...); r.Status != 400 || !bytes.Contains(r.Raw, []byte("BATCH_SIZE")) {
			t.Errorf("101 events: %d %s", r.Status, r.Raw)
		}
		if r := h.track(x.sdkKeyA); r.Status != 400 {
			t.Errorf("0 events: %d", r.Status)
		}
		if r := trackRaw(t, h.srv.URL, x.sdkKeyA, []byte(`{"events":[{"id":"a","name":"n","subjectKey":"s","value":NaN}]}`)); r.Status != 400 {
			t.Errorf("a bare NaN token: %d %s", r.Status, r.Raw)
		}
		if r := trackRaw(t, h.srv.URL, x.sdkKeyA, []byte(`{"events":[]} trailing`)); r.Status != 400 {
			t.Errorf("trailing data: %d", r.Status)
		}
	})

	t.Run("per-event rejections do not fail the batch", func(t *testing.T) {
		long := strings.Repeat("p", track.MaxPropertyBytes)
		r := h.track(x.sdkKeyA,
			evt("good-1", "page_view", "s1"),
			evt("bad-ts-future", "n", "s", map[string]any{"timestamp": now.Add(time.Hour).Format(time.RFC3339)}),
			evt("bad-ts-old", "n", "s", map[string]any{"timestamp": now.Add(-track.Retention - time.Hour).Format(time.RFC3339)}),
			evt("bad-ts-junk", "n", "s", map[string]any{"timestamp": "yesterday"}),
			evt("bad-nan", "n", "s", map[string]any{"value": "NaN"}),
			evt("bad-inf", "n", "s", map[string]any{"value": json.RawMessage("1e999")}),
			evt("bad-exp", "n", "s", map[string]any{"experimentId": "00000000-0000-4000-8000-000000000000"}),
			evt("bad-exp-format", "n", "s", map[string]any{"experimentId": "not-a-uuid"}),
			evt("bad-props", "n", "s", map[string]any{"properties": map[string]any{"k": long}}),
			evt("bad-props-array", "n", "s", map[string]any{"properties": []int{1}}),
			evt("bad-props-nul", "n", "s", map[string]any{"properties": map[string]any{"k": "a\u0000b"}}),
			evt("bad-reserved", "$other", "s"),
			evt("bad-subject", "n", ""),
			evt("bad-subject-nul", "n", "a\u0000b"),
			evt("bad id!", "n", "s"),
			evt("bad-expo-novar", track.ExposureEvent, "s", map[string]any{"experimentId": exp}),
			evt("bad-expo-var", track.ExposureEvent, "s", map[string]any{"experimentId": exp, "variationId": "nope"}),
			evt("bad-var-on-plain", "n", "s", map[string]any{"variationId": "on"}),
			evt("good-2", "purchase", "s1", map[string]any{"value": 12.5, "experimentId": exp, "timestamp": now.Add(-time.Hour).Format(time.RFC3339)}),
		)
		if r.Status != 200 {
			t.Fatalf("a batch with bad events: %d %s", r.Status, r.Raw)
		}
		want := map[string]string{
			"good-1": "accepted", "good-2": "accepted",
			"bad-exp":      "rejected:" + track.CodeExperimentNotFound,
			"bad-expo-var": "rejected:" + track.CodeInvalidVariation,
		}
		for _, res := range r.Results {
			w, ok := want[res.ID]
			if !ok {
				w = "rejected:" + track.CodeInvalidEvent
			}
			got := res.Status
			if res.Code != "" {
				got += ":" + res.Code
			}
			// invalid ids parse to no id; they are INVALID_EVENT too
			if res.ID == "" {
				got = res.Status + ":" + res.Code
			}
			if got != w {
				t.Errorf("event #%d %q: %s, want %s (%s)", res.Index, res.ID, got, w, res.Message)
			}
		}
		if r.Counts.Accepted != 2 || r.Counts.Rejected != len(r.Results)-2 {
			t.Errorf("counts: %+v", r.Counts)
		}
		if rows := h.eventRows(`true`); rows != 2 {
			t.Errorf("%d rows stored, want only the 2 good events", rows)
		}
	})
}

// ----------------------------------------------------------- keys and limits

func TestTrackRejectsRevokedAndMissingKeysLikeEvaluate(t *testing.T) {
	x := newTenants(t)
	h := x.h
	instance2 := h.newInstance(slowFallbacks) // another process: only the broadcast can help it in time
	var keyID string
	if err := h.pool.QueryRow(context.Background(), `SELECT id::text FROM api_keys WHERE workspace_id = $1::uuid`, x.wsA.ID).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"events": []map[string]any{evt("k1", "n", "s")}})
	for _, base := range []string{h.srv.URL, instance2.URL} {
		if r := trackRaw(t, base, x.sdkKeyA, body); r.Status != 200 {
			t.Fatalf("a live key is refused on %s: %d %s", base, r.Status, r.Raw)
		}
	}
	h.expect(204, x.a, "DELETE", "/environments/"+x.devA+"/sdk-keys/"+keyID, nil)
	for _, base := range []string{h.srv.URL, instance2.URL} {
		deadline := time.Now().Add(2 * time.Second)
		var r trackResp
		for {
			r = trackRaw(t, base, x.sdkKeyA, body)
			if r.Status == 401 || time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if r.Status != 401 {
			t.Errorf("a revoked key still writes on %s: %d", base, r.Status)
		}
		if st := evaluateAt(t, base, x.sdkKeyA, secretFlag); st != 401 {
			t.Errorf("evaluate on %s: %d", base, st)
		}
	}
	if r := trackRaw(t, h.srv.URL, "", body); r.Status != 401 {
		t.Errorf("no key: %d", r.Status)
	}
	// A control-plane user token is not an SDK key.
	req, _ := http.NewRequest("POST", h.srv.URL+"/track", bytes.NewReader(body))
	req.Header.Set("X-Test-User", x.a.header())
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 401 {
		t.Errorf("a user session on /track: %v %v", res, err)
	}
	if rows := h.eventRows(`event_id = 'k1' AND true`); rows != 1 {
		t.Errorf("%d rows; only the write before revocation should exist (the same id on instance 2 is its duplicate)", rows)
	}
}

func TestTrackIsRateLimitedPerKeyAndOrdinaryTrafficIsNot(t *testing.T) {
	// Events per key: a burst of 5, refilling slowly enough to stay spent.
	x := newTenantsWith(t, func(d *server.Deps) {
		d.Limiter = ratelimit.New(ratelimit.Config{Rules: map[string]ratelimit.Rule{
			ratelimit.ClassSDKTrackEvents: tiny(5), ratelimit.ClassSDKTrack: tiny(1000),
		}})
	})
	h := x.h
	other := h.newSDKKey(x.a, x.devA)
	three := func(p string) []map[string]any {
		return []map[string]any{evt(p+"1", "n", "s"), evt(p+"2", "n", "s"), evt(p+"3", "n", "s")}
	}
	if r := h.track(x.sdkKeyA, three("a")...); r.Counts.Accepted != 3 {
		t.Fatal(string(r.Raw))
	}
	r := h.track(x.sdkKeyA, three("b")...) // 3 more would make 6 of 5
	if r.Status != 429 || !bytes.Contains(r.Raw, []byte(`"code":"RATE_LIMITED"`)) {
		t.Fatalf("over the events cap: %d %s", r.Status, r.Raw)
	}
	if secs, err := strconv.Atoi(r.Header.Get("Retry-After")); err != nil || secs < 1 {
		t.Errorf("Retry-After %q", r.Header.Get("Retry-After"))
	}
	if rows := h.eventRows(`event_id LIKE 'b%'`); rows != 0 {
		t.Errorf("%d events of a refused request were stored", rows)
	}
	if r := h.track(other, three("c")...); r.Counts.Accepted != 3 {
		t.Errorf("another key shares the bucket: %d %s", r.Status, r.Raw)
	}
	// /evaluate has its own bucket.
	if st := h.evaluateStatus(x.sdkKeyA, secretFlag); st != 200 {
		t.Errorf("evaluate after /track was limited: %d", st)
	}

	// Default limits: a busy but normal client (40 batches of 25 in a row) is never refused.
	y := newTenants(t)
	for i := 0; i < 40; i++ {
		batch := make([]map[string]any, 25)
		for j := range batch {
			batch[j] = evt(fmt.Sprintf("n-%d-%d", i, j), "page_view", "s")
		}
		if r := y.h.track(y.sdkKeyA, batch...); r.Status != 200 {
			t.Fatalf("normal traffic refused at batch %d: %d %s", i, r.Status, r.Raw)
		}
	}
}

// ----------------------------------------------------------------- privacy

func TestEventContentNeverReachesLogsOrErrors(t *testing.T) {
	logs := captureLogs(t)
	x := newTenants(t)
	h := x.h
	exp := x.runningExperiment()
	const subject, prop, odd = "SUBJECT-0d6b1f", "PROPVALUE-77aa3c", "ODDFIELD-91e2"

	good := h.track(x.sdkKeyA, evt("p1", "page_view", subject, map[string]any{"properties": map[string]any{"email": prop}}))
	bad := h.track(x.sdkKeyA,
		evt("p2", "page_view", subject, map[string]any{"timestamp": subject}),
		evt("p3", "page_view", subject, map[string]any{"value": subject}),
		evt("p4", "page_view", subject, map[string]any{odd: prop}),
		evt("p5", "page_view", subject, map[string]any{"properties": []string{prop}}),
		exposure("p6", exp, prop, subject))
	garbage := trackRaw(t, h.srv.URL, x.sdkKeyA, []byte(`{"events":[{"id":"p7","`+odd+`":"`+prop+`"}], "`+subject+`": 1}`))

	// A real database failure while the batch carries the content.
	if _, err := h.pool.Exec(context.Background(), `ALTER TABLE events RENAME COLUMN value TO value_renamed`); err != nil {
		t.Fatal(err)
	}
	broken := h.track(x.sdkKeyA, evt("p8", "page_view", subject, map[string]any{"properties": map[string]any{"email": prop}}))
	if broken.Status != 503 {
		t.Fatalf("with a broken schema: %d %s", broken.Status, broken.Raw)
	}
	if !strings.Contains(logs.String(), "track: insert failed") {
		t.Fatalf("the failure was not logged, so the test proves nothing:\n%s", logs.String())
	}
	for _, s := range []string{subject, prop, odd} {
		if strings.Contains(logs.String(), s) {
			t.Errorf("event content %q reached the logs:\n%s", s, logs.String())
		}
		for name, r := range map[string]trackResp{"good": good, "bad": bad, "garbage": garbage, "broken": broken} {
			if bytes.Contains(r.Raw, []byte(s)) {
				t.Errorf("the %s response echoes %q: %s", name, s, r.Raw)
			}
		}
	}
}

// -------------------------------------------------------------- one statement

type countingTracer struct {
	mu      sync.Mutex
	inserts int
	all     int
}

func (c *countingTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.all++
	if strings.Contains(strings.ToLower(d.SQL), "insert into events") {
		c.inserts++
	}
	return ctx
}
func (c *countingTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestABatchOf100EventsIsOneInsert(t *testing.T) {
	x := newTenants(t)
	h := x.h
	cfg, err := pgxpool.ParseConfig(h.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	tr := &countingTracer{}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	exp := x.runningExperiment()
	batch := make([]track.Event, track.MaxBatch)
	for i := range batch {
		batch[i] = track.Event{ID: fmt.Sprintf("b-%d", i), Name: "page_view", SubjectKey: fmt.Sprintf("s-%d", i%7), OccurredAt: time.Now(), Properties: `{"i":1}`}
	}
	batch[0] = track.Event{ID: "expo", Name: track.ExposureEvent, SubjectKey: "s-0", ExperimentID: exp, VariationID: "on", OccurredAt: time.Now()}
	sc := apikey.Scope{WorkspaceID: x.wsA.ID, EnvironmentID: x.devA}
	got, err := track.NewPgStore(pool).Insert(context.Background(), sc, batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 || tr.inserts != 1 {
		t.Errorf("%d events stored with %d INSERT statements, want 100 with 1", len(got), tr.inserts)
	}
	if rows := h.eventRows(`workspace_id = $1::uuid`, x.wsA.ID); rows != 100 {
		t.Errorf("%d rows", rows)
	}
}

// ----------------------------------------------------- the schema's own guards

func TestEventsTableRefusesCrossTenantAndBadRows(t *testing.T) {
	x := newTenants(t)
	h := x.h
	exp := x.runningExperiment()
	ins := func(ws, env, expID, name string, value any) error {
		return h.rawExec(`INSERT INTO events (workspace_id, environment_id, experiment_id, event_name, subject_key, variation_id, value)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 's', 'on', $5)`, ws, env, nullIfEmpty(expID), name, value)
	}
	if err := ins(x.wsA.ID, x.devA, exp, "ok", nil); err != nil {
		t.Fatalf("a valid row: %v", err)
	}
	for name, err := range map[string]error{
		"B's workspace with A's environment":    ins(x.wsB.ID, x.devA, "", "x", nil),
		"B's workspace with A's experiment":     ins(x.wsB.ID, x.devB, exp, "x", nil),
		"A's workspace, B's environment":        ins(x.wsA.ID, x.devB, "", "x", nil),
		"A's experiment in another environment": ins(x.wsA.ID, envID(t, x.wsA, "staging"), exp, "x", nil),
		"NaN value":                             ins(x.wsA.ID, x.devA, "", "x", "NaN"),
		"Infinity value":                        ins(x.wsA.ID, x.devA, "", "x", "Infinity"),
		"exposure without experiment":           ins(x.wsA.ID, x.devA, "", track.ExposureEvent, nil),
	} {
		if err == nil {
			t.Errorf("the database accepted: %s", name)
		}
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
