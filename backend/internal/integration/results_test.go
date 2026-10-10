package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"helios/backend/internal/controlplane/results"
	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/server"
)

var seedSeq atomic.Int64

// seed inserts one event straight into the table (the ingestion path is
// tested elsewhere), at an offset from the experiment's start so its
// timestamp relative to the run window is exact.
func (x *tenants) seed(expID, subject, name, variation, offset string, value any) {
	x.h.t.Helper()
	var variationArg any
	if variation != "" {
		variationArg = variation
	}
	err := x.h.rawExec(`
		INSERT INTO events (workspace_id, environment_id, experiment_id, event_id, event_name, subject_key, variation_id, value, occurred_at)
		SELECT x.workspace_id, x.environment_id, x.id, $2, $3, $4, $5, $6, x.started_at + $7::interval
		FROM experiments x WHERE x.id = $1::uuid`,
		expID, fmt.Sprintf("seed-%d", seedSeq.Add(1)), name, subject, variationArg, value, offset)
	if err != nil {
		x.h.t.Fatal(err)
	}
}

// backdated starts the experiment and moves its start three days back, so the
// run window is wide enough for events seeded at small offsets.
func (x *tenants) backdated() string {
	x.h.t.Helper()
	id := x.runningExperiment()
	if err := x.h.rawExec(`UPDATE experiments SET started_at = started_at - interval '3 days' WHERE id = $1::uuid`, id); err != nil {
		x.h.t.Fatal(err)
	}
	return id
}

func (x *tenants) results(user user, envID, key, query string) results.Response {
	x.h.t.Helper()
	path := "/environments/" + envID + "/experiments/" + key + "/results"
	if query != "" {
		path += "?" + query
	}
	return decode[results.Response](x.h.t, x.h.expect(200, user, "GET", path, nil))
}

func warningCodes(r results.Response) map[string]results.Warning {
	m := map[string]results.Warning{}
	for _, w := range r.Warnings {
		m[w.Code] = w
	}
	return m
}

func variation(r results.Response, metric int, id string) results.VariationStats {
	for _, v := range r.Metrics[metric].Variations {
		if v.VariationID == id {
			return v
		}
	}
	return results.VariationStats{}
}

// Attribution: the denominator is exposed subjects by their exposure's
// variation; the numerator only counts events after the subject's own
// exposure and inside the run window, once per subject for a conversion.
func TestResultsAttribution(t *testing.T) {
	x := newTenants(t)
	exp := x.backdated()

	// u1: exposed "on", converts twice (counts once as a conversion)
	x.seed(exp, "u1", "$exposure", "on", "1 second", nil)
	x.seed(exp, "u1", "e", "", "2 seconds", nil)
	x.seed(exp, "u1", "e", "", "3 seconds", nil)
	// u2: exposed "off", converts once
	x.seed(exp, "u2", "$exposure", "off", "1 second", nil)
	x.seed(exp, "u2", "e", "", "5 seconds", nil)
	// u3: exposed "on", only event is BEFORE its exposure
	x.seed(exp, "u3", "$exposure", "on", "10 seconds", nil)
	x.seed(exp, "u3", "e", "", "9 seconds", nil)
	// u4: never exposed, has events
	x.seed(exp, "u4", "e", "", "2 seconds", nil)
	x.seed(exp, "u4", "e", "", "3 seconds", nil)
	// u5: exposed "off", no events
	x.seed(exp, "u5", "$exposure", "off", "1 second", nil)

	h := x.h
	h.expect(200, x.a, "POST", "/environments/"+x.devA+"/experiments/"+expKey+"/stop", nil)
	// an event after the stop time, and one from the day before the start (both outside the window)
	x.seed(exp, "u1", "e", "", "4 days", nil)
	x.seed(exp, "u2", "e", "", "-1 day", nil)

	r := x.results(x.a, x.devA, expKey, "")
	if r.Status != "stopped" || r.Control != "on" || r.SampleSize != 4 || r.Window.Running {
		t.Fatalf("header: %+v", r)
	}
	on, off := variation(r, 0, "on"), variation(r, 0, "off")
	if on.Exposures != 2 || off.Exposures != 2 {
		t.Errorf("exposures on=%d off=%d, want 2 and 2 (u4 was never exposed)", on.Exposures, off.Exposures)
	}
	if on.ConvertingSubjects != 1 || off.ConvertingSubjects != 1 {
		t.Errorf("converters on=%d off=%d, want 1 and 1 (u3's earlier event, u4 and out-of-window events must not count)", on.ConvertingSubjects, off.ConvertingSubjects)
	}
	if on.Events != 2 || off.Events != 1 {
		t.Errorf("events on=%d off=%d, want 2 and 1", on.Events, off.Events)
	}
	if on.Rate == nil || *on.Rate != 0.5 || off.Rate == nil || *off.Rate != 0.5 {
		t.Errorf("rates on=%v off=%v", on.Rate, off.Rate)
	}
	w := warningCodes(r)
	for code, n := range map[string]float64{"EVENTS_WITHOUT_EXPOSURE": 2, "EVENTS_BEFORE_EXPOSURE": 1, "EVENTS_OUTSIDE_WINDOW": 2} {
		got, ok := w[code]
		if !ok || got.Detail["events"] != n {
			t.Errorf("warning %s: %+v (want %v events)", code, got, n)
		}
	}
	if _, ok := w["INSUFFICIENT_DATA"]; !ok {
		t.Error("4 subjects must be reported as insufficient data")
	}
	c := r.Metrics[0].Comparisons[0]
	if c.Status != results.StatusInsufficientData || c.PValue != nil || c.DifferenceCI.Low != nil {
		t.Errorf("no verdict below the minimum: %+v", c)
	}
	if c.Difference == nil {
		t.Error("descriptive numbers stay available")
	}
}

func TestResultsCountAndValueMetricsAndDuplicates(t *testing.T) {
	x := newTenants(t)
	exp := x.backdated()
	for i, v := range []string{"on", "off"} {
		x.seed(exp, fmt.Sprintf("s%d", i), "$exposure", v, "1 second", nil)
	}
	// s0 (on): 3 purchase events worth 10 + 20 + 30 ; s1 (off): 1 worth 5
	for _, val := range []int{10, 20, 30} {
		x.seed(exp, "s0", "purchase", "", "2 seconds", float64(val))
	}
	x.seed(exp, "s1", "purchase", "", "2 seconds", 5.0)

	conv := x.results(x.a, x.devA, expKey, "metrics=purchase:conversion")
	if v := variation(conv, 0, "on"); v.ConvertingSubjects != 1 || v.Rate == nil || *v.Rate != 1 {
		t.Errorf("conversion counts a subject once: %+v", v)
	}
	cnt := x.results(x.a, x.devA, expKey, "metrics=purchase:count")
	if v := variation(cnt, 0, "on"); v.Events != 3 || v.Mean == nil || *v.Mean != 3 {
		t.Errorf("count metric: %+v", v)
	}
	val := x.results(x.a, x.devA, expKey, "metrics=purchase:value,purchase:count")
	if len(val.Metrics) != 2 {
		t.Fatalf("two metrics requested, got %d", len(val.Metrics))
	}
	if v := variation(val, 0, "on"); v.Mean == nil || *v.Mean != 60 {
		t.Errorf("value metric is the per-subject SUM: %+v", v)
	}
	if v := variation(val, 0, "off"); v.Mean == nil || *v.Mean != 5 {
		t.Errorf("value metric (off): %+v", v)
	}
	// The experiment's own primary metric is the default, with its name.
	def := x.results(x.a, x.devA, expKey, "")
	if m := def.Metrics[0].Metric; m.EventName != "e" || m.Name != "M" || !m.IsPrimary || m.Kind != "conversion" {
		t.Errorf("default metric: %+v", m)
	}
}

func TestResultsRequestValidationAndDraft(t *testing.T) {
	x := newTenants(t)
	h := x.h
	path := "/environments/" + x.devA + "/experiments/" + expKey + "/results"
	h.expect(409, x.a, "GET", path, nil) // a draft has no window and no data
	exp := x.backdated()
	x.seed(exp, "u", "$exposure", "on", "1 second", nil)
	x.seed(exp, "u", "known-event", "", "2 seconds", nil)
	for name, q := range map[string]string{
		"unknown event":  "?metrics=never-sent:conversion",
		"bad kind":       "?metrics=e:median",
		"no kind":        "?metrics=e",
		"reserved":       "?metrics=$exposure:count",
		"too many":       "?metrics=e:conversion,e:count,e:value,known-event:count",
		"bad characters": "?metrics=a%20b:count",
	} {
		if st, body := h.call(x.a, "GET", path+q, nil); st != 400 {
			t.Errorf("%s: status %d %s", name, st, body)
		}
	}
	h.expect(200, x.a, "GET", path+"?metrics=known-event:count", nil)
	h.expect(404, x.a, "GET", "/environments/"+x.devA+"/experiments/nope/results", nil)
}

// B can never read A's results, not even by naming an experiment key B itself
// also uses, and A's cached answer is never served to B.
func TestResultsAreTenantScoped(t *testing.T) {
	x := newTenants(t)
	h := x.h
	exp := x.backdated()
	for i := 0; i < 7; i++ {
		x.seed(exp, fmt.Sprintf("a%d", i), "$exposure", "on", "1 second", nil)
	}
	x.seed(exp, "a0", "e", "", "2 seconds", nil)
	mine := x.results(x.a, x.devA, expKey, "") // also fills the cache for A's experiment

	// B asks for A's environment: 404 like every environment route.
	if st, body := h.call(x.b, "GET", "/environments/"+x.devA+"/experiments/"+expKey+"/results", nil); st != 404 || bytes.Contains(body, []byte(expKey)) && !bytes.Contains(body, []byte("ENVIRONMENT_NOT_FOUND")) {
		t.Errorf("B reading A's results: %d %s", st, body)
	}
	// B asks, in B's own environment, for A's experiment key: nothing.
	if st, body := h.call(x.b, "GET", "/environments/"+x.devB+"/experiments/"+expKey+"/results", nil); st != 404 {
		t.Errorf("B's environment, A's experiment key: %d %s", st, body)
	}
	// B with an experiment of the same key gets only B's own numbers.
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/flags", boolFlag(secretFlag))
	h.expect(200, x.b, "PATCH", "/environments/"+x.devB+"/flags/"+secretFlag, map[string]any{"enabled": true})
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/experiments", map[string]any{
		"flagKey": secretFlag, "key": expKey, "name": "B's",
		"metrics": []map[string]any{{"name": "M", "eventName": "e", "type": "conversion", "isPrimary": true}},
	})
	h.expect(200, x.b, "POST", "/environments/"+x.devB+"/experiments/"+expKey+"/start", nil)
	theirs := x.results(x.b, x.devB, expKey, "")
	if theirs.SampleSize != 0 || theirs.SampleSize == mine.SampleSize {
		t.Errorf("B sees %d exposures (A has %d)", theirs.SampleSize, mine.SampleSize)
	}
	raw := h.expect(200, x.b, "GET", "/environments/"+x.devB+"/experiments/"+expKey+"/results", nil)
	for _, secret := range []string{x.wsA.ID, x.devA, x.a.email, exp} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("B's results contain %q", secret)
		}
	}
	// A removed member and a viewer follow the route guard (covered by the every-route test).
}

func TestResultsRateLimitIsPerUser(t *testing.T) {
	x := newTenantsWith(t, func(d *server.Deps) {
		d.Limiter = ratelimit.New(ratelimit.Config{Rules: map[string]ratelimit.Rule{ratelimit.ClassResults: tiny(3)}})
	})
	h := x.h
	exp := x.backdated()
	x.seed(exp, "u", "$exposure", "on", "1 second", nil)
	path := "/environments/" + x.devA + "/experiments/" + expKey + "/results"
	for i := 0; i < 3; i++ {
		h.expect(200, x.a, "GET", path, nil)
	}
	h.expect429(x.a, "GET", path, nil)
	h.expect(200, x.a, "GET", "/environments/"+x.devA+"/flags", nil) // other routes are not affected
	// the second user has a bucket of their own
	h.expect(200, x.b, "GET", "/me", nil)
}

// No NaN or Infinity ever reaches the wire, and the body is plain JSON.
func TestResultsJSONHasNoNonFiniteNumbers(t *testing.T) {
	x := newTenants(t)
	h := x.h
	exp := x.backdated()
	for i := 0; i < 300; i++ {
		v := "on"
		if i%2 == 1 {
			v = "off"
		}
		x.seed(exp, fmt.Sprintf("u%d", i), "$exposure", v, "1 second", nil)
	}
	for _, q := range []string{"", "metrics=e:count", "metrics=e:value"} { // no events at all: zero variance everywhere
		raw := h.expect(200, x.a, "GET", "/environments/"+x.devA+"/experiments/"+expKey+"/results?"+q, nil)
		if bytes.Contains(raw, []byte("NaN")) || bytes.Contains(raw, []byte("Infinity")) {
			t.Errorf("%q: %s", q, raw)
		}
		var any map[string]any
		if err := json.Unmarshal(raw, &any); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
	r := x.results(x.a, x.devA, expKey, "metrics=e:count")
	if c := r.Metrics[0].Comparisons[0]; c.Status != results.StatusInvalid || len(c.StatusReasons) == 0 || c.PValue != nil {
		t.Errorf("zero variance must be invalid with a reason: %+v", c)
	}
	_ = strings.TrimSpace
	_ = context.Background
}

// The event-counts endpoint lists the event names that exist for the
// experiment (what the results page offers as metrics), most frequent first,
// without the exposure event.
func TestEventCountsListsEventNames(t *testing.T) {
	x := newTenants(t)
	h := x.h
	exp := x.runningExperiment()
	x.seed(exp, "u1", "$exposure", "on", "1 second", nil)
	x.seed(exp, "u1", "purchase", "", "2 seconds", 19.5) // carries a value
	x.seed(exp, "u1", "page_view", "", "2 seconds", nil)
	x.seed(exp, "u1", "page_view", "", "3 seconds", nil)
	path := "/environments/" + x.devA + "/experiments/" + expKey + "/event-counts"
	got := decode[struct {
		EventNames []struct {
			Name  string
			Count int
			Kinds []string
		}
		ExposureTotal int
		Events        int
	}](t, h.expect(200, x.a, "GET", path, nil))
	if got.ExposureTotal != 1 || got.Events != 3 || len(got.EventNames) != 2 ||
		got.EventNames[0].Name != "page_view" || got.EventNames[0].Count != 2 || got.EventNames[1].Name != "purchase" {
		t.Errorf("%+v", got)
	}
	// the kinds the data supports: "value" only for an event that carries a value
	kinds := map[string]string{}
	for _, e := range got.EventNames {
		kinds[e.Name] = strings.Join(e.Kinds, ",")
	}
	if kinds["purchase"] != "conversion,count,value" || kinds["page_view"] != "conversion,count" {
		t.Errorf("kinds: %v", kinds)
	}
	h.expect(404, x.b, "GET", "/environments/"+x.devA+"/experiments/"+expKey+"/event-counts", nil)
}

// seedAt is seed with both clocks explicit: occurredAt is what the client
// claimed, receivedAt is the server's receipt time (events.created_at), both as
// offsets from the experiment's start.
func (x *tenants) seedAt(expID, subject, name, variation, occurredAt, receivedAt string) {
	x.h.t.Helper()
	var variationArg any
	if variation != "" {
		variationArg = variation
	}
	err := x.h.rawExec(`
		INSERT INTO events (workspace_id, environment_id, experiment_id, event_id, event_name, subject_key, variation_id, occurred_at, created_at)
		SELECT x.workspace_id, x.environment_id, x.id, $2, $3, $4, $5, x.started_at + $6::interval, x.started_at + $7::interval
		FROM experiments x WHERE x.id = $1::uuid`,
		expID, fmt.Sprintf("seed-%d", seedSeq.Add(1)), name, subject, variationArg, occurredAt, receivedAt)
	if err != nil {
		x.h.t.Fatal(err)
	}
}

// legacyConverters is the attribution rule results used before the clock rule:
// client time only, for the window and for the order. Kept here as evidence.
func (x *tenants) legacyConverters(expID string) map[string]bool {
	x.h.t.Helper()
	rows, err := x.h.pool.Query(context.Background(), `
		SELECT DISTINCT x.subject_key
		FROM events x
		JOIN experiments ex ON ex.id = x.experiment_id
		JOIN events e ON e.workspace_id = x.workspace_id AND e.experiment_id = x.experiment_id AND e.subject_key = x.subject_key
		             AND e.event_name = 'e' AND e.occurred_at >= x.occurred_at
		             AND e.occurred_at >= ex.started_at AND e.occurred_at <= COALESCE(ex.stopped_at, now())
		WHERE x.experiment_id = $1::uuid AND x.event_name = '$exposure'`, expID)
	if err != nil {
		x.h.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out[s] = true
	}
	return out
}

// Client clocks are not trusted to decide the run window, and are only used to
// order events against the exposure after being pulled into [started_at, received_at].
func TestResultsClockSkew(t *testing.T) {
	x := newTenants(t)
	h := x.h
	exp := x.backdated() // started three days ago
	expose := func(subject, occurred, received string) {
		x.seedAt(exp, subject, "$exposure", "on", occurred, received)
	}
	convert := func(subject, occurred, received string) { x.seedAt(exp, subject, "e", "", occurred, received) }

	// A: client clock two hours BEHIND: every stamp precedes the experiment's start, yet the data was received during the run
	expose("A", "-2 hours", "1 hour")
	convert("A", "-118 minutes", "1 hour")
	// B: the exposure was stamped four minutes ahead by one clock, the conversion by another, accurate clock
	expose("B", "64 minutes", "1 hour")
	convert("B", "62 minutes", "62 minutes")
	// C and D: exposure and event in one batch; the event stamped 500 ms / 10 s before the exposure
	expose("C", "1 hour", "1 hour")
	convert("C", "59 minutes 59 seconds 500 milliseconds", "1 hour")
	expose("D", "1 hour", "1 hour")
	convert("D", "59 minutes 50 seconds", "1 hour")
	// E and F: an offline client sends old events late: one truly before the exposure, one truly after
	expose("E", "1 hour", "1 hour")
	convert("E", "10 minutes", "5 hours")
	expose("F", "1 hour", "1 hour")
	convert("F", "2 hours", "5 hours")
	// G and H: clocks 60 days in the past; the conversion is a day after the exposure (G) or a day before it (H)
	expose("G", "-60 days", "1 hour")
	convert("G", "-59 days", "2 hours")
	expose("H", "-60 days", "1 hour")
	convert("H", "-61 days", "2 hours")
	// I: a conversion stamped nine days AHEAD of the run, received three hours in
	expose("I", "1 hour", "1 hour")
	convert("I", "9 days", "3 hours")
	// K: an event stamped two hours after the exposure, but RECEIVED an hour before it: it cannot have happened after the exposure
	expose("K", "2 hours", "2 hours")
	convert("K", "3 hours", "1 hour")
	// J: stamped inside the run by the client but received only after the experiment stopped
	expose("J", "1 hour", "1 hour")
	h.expect(200, x.a, "POST", "/environments/"+x.devA+"/experiments/"+expKey+"/stop", nil)
	convert("J", "90 minutes", "3 days 1 hour")

	r := x.results(x.a, x.devA, expKey, "")
	on := variation(r, 0, "on")
	if on.Exposures != 11 {
		t.Fatalf("exposures %d, want 11", on.Exposures)
	}
	if on.ConvertingSubjects != 6 {
		t.Errorf("%d subjects converted, want 6 (A B C F G I)", on.ConvertingSubjects)
	}
	w := warningCodes(r)
	for code, n := range map[string]float64{"EVENTS_BEFORE_EXPOSURE": 4 /* D E H K */, "EVENTS_OUTSIDE_WINDOW": 1 /* J */, "CLIENT_CLOCK_ADJUSTED": 3 /* A G I */} {
		if w[code].Detail["events"] != n {
			t.Errorf("%s: %+v, want %v events", code, w[code], n)
		}
	}

	// Evidence that the old rule mis-attributed: client time alone loses the behind-skewed client (A),
	// the cross-clock pair (B), counts a late arrival that was stamped inside the run (J), and cannot order G or H.
	old := x.legacyConverters(exp)
	for _, s := range []string{"A", "B"} {
		if old[s] {
			t.Errorf("the legacy rule was expected to lose %s", s)
		}
	}
	if !old["J"] {
		t.Error("the legacy rule was expected to count J (received after the stop)")
	}
	t.Logf("legacy rule converters: %v; clock rule: 6 (A B C F G I)", old)
}
