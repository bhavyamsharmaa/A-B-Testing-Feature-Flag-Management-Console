package integration

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"helios/backend/internal/controlplane/results"
	"helios/backend/internal/platform/db"
)

type expOut struct {
	Key                string
	ControlVariationID string
	Variations         []struct {
		ID        string
		IsControl bool
	}
}

func (x *tenants) newExperiment(t *testing.T, flag, key, control string) expOut {
	t.Helper()
	body := map[string]any{"flagKey": flag, "key": key, "name": key,
		"metrics": []map[string]any{{"name": "M", "eventName": "e", "type": "conversion", "isPrimary": true}}}
	if control != "" {
		body["controlVariationId"] = control
	}
	return decode[expOut](t, x.h.expect(201, x.a, "POST", "/environments/"+x.devA+"/experiments", body))
}

func (x *tenants) threeVariationFlag(t *testing.T, key string) {
	t.Helper()
	x.h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", map[string]any{
		"key": key, "name": key, "variationType": "string",
		"variations": []map[string]any{{"id": "red", "value": "red"}, {"id": "green", "value": "green"}, {"id": "blue", "value": "blue"}},
	})
}

func TestTheFirstVariationIsTheControlUnlessYouChooseAnother(t *testing.T) {
	x := newTenants(t)
	h := x.h
	x.threeVariationFlag(t, "colour")
	def := x.newExperiment(t, "colour", "by-default", "")
	if def.ControlVariationID != "red" || len(def.Variations) != 3 || !def.Variations[0].IsControl {
		t.Errorf("default: %+v", def)
	}
	chosen := x.newExperiment(t, "colour", "chosen", "blue")
	if chosen.ControlVariationID != "blue" {
		t.Fatalf("chosen: %+v", chosen)
	}
	var controls []string
	for _, v := range chosen.Variations {
		if v.IsControl {
			controls = append(controls, v.ID)
		}
	}
	if strings.Join(controls, ",") != "blue" || chosen.Variations[0].ID != "red" { // listed in the flag's order, one marked
		t.Errorf("variations: %+v", chosen.Variations)
	}
	// the list and the single read agree
	list := decode[struct{ Experiments []expOut }](t, h.expect(200, x.a, "GET", "/environments/"+x.devA+"/experiments?flagKey=colour", nil))
	got := map[string]string{}
	for _, e := range list.Experiments {
		got[e.Key] = e.ControlVariationID
	}
	if got["by-default"] != "red" || got["chosen"] != "blue" {
		t.Errorf("list: %v", got)
	}
	if one := decode[expOut](t, h.expect(200, x.a, "GET", "/environments/"+x.devA+"/experiments/chosen", nil)); one.ControlVariationID != "blue" {
		t.Errorf("get: %+v", one)
	}
	// a control that is not one of the flag's variations is refused, and nothing is created
	for _, bad := range []string{"purple", "RED", " red"} {
		st, b := h.call(x.a, "POST", "/environments/"+x.devA+"/experiments", map[string]any{"flagKey": "colour", "key": "bad-" + strings.TrimSpace(strings.ToLower(bad)), "name": "x", "controlVariationId": bad,
			"metrics": []map[string]any{{"name": "M", "eventName": "e", "type": "conversion", "isPrimary": true}}})
		if st != 400 {
			t.Errorf("control %q: %d %s", bad, st, b)
		}
	}
	if n := h.count(`SELECT count(*) FROM experiments WHERE key LIKE 'bad-%'`); n != 0 {
		t.Errorf("%d experiments created by refused requests", n)
	}
}

func TestTheDatabaseAllowsAtMostOneControlPerExperiment(t *testing.T) {
	x := newTenants(t)
	x.threeVariationFlag(t, "colour")
	x.newExperiment(t, "colour", "one-control", "green")
	var id string
	_ = x.h.pool.QueryRow(context.Background(), `SELECT id::text FROM experiments WHERE key = 'one-control'`).Scan(&id)
	if err := x.h.rawExec(`UPDATE experiment_variations SET is_control = true WHERE experiment_id = $1::uuid`, id); err == nil || !strings.Contains(err.Error(), "one_control_per_experiment") {
		t.Errorf("two controls were accepted: %v", err)
	}
	if n := x.h.count(`SELECT count(*) FROM experiment_variations WHERE experiment_id = $1::uuid AND is_control`, id); n != 1 {
		t.Errorf("%d controls", n)
	}
	// a row cannot belong to another workspace's experiment
	if err := x.h.rawExec(`INSERT INTO experiment_variations (experiment_id, workspace_id, variation_id, position) VALUES ($1::uuid, $2::uuid, 'x', 9)`, id, x.wsB.ID); err == nil {
		t.Error("a variation row crossed workspaces")
	}
}

func (x *tenants) exposeOne(t *testing.T, key, variation string) {
	t.Helper()
	if err := x.h.rawExec(`INSERT INTO events (workspace_id, environment_id, experiment_id, event_id, event_name, subject_key, variation_id, occurred_at)
		SELECT x.workspace_id, x.environment_id, x.id, 'expose-' || $2, '$exposure', $2, $3, now() FROM experiments x WHERE x.key = $1`, key, "subject-"+variation, variation); err != nil {
		t.Fatal(err)
	}
}

func TestTheControlIsChosenBeforeExposuresAndFrozenAfter(t *testing.T) {
	x := newTenants(t)
	h := x.h
	x.threeVariationFlag(t, "colour")
	x.newExperiment(t, "colour", "pick", "")
	path := "/environments/" + x.devA + "/experiments/pick"
	set := func(u user, v string) (int, []byte) {
		return h.call(u, "PATCH", path, map[string]any{"controlVariationId": v})
	}
	// authority
	ow := &ownWorld{tenants: x, clock: &liveClock{}}
	vw := ow.join(t, x.a, x.wsA.ID, "vw", "viewer")
	if st, _ := set(vw, "green"); st != 403 {
		t.Errorf("a viewer: %d", st)
	}
	if st, _ := set(x.b, "green"); st != 404 {
		t.Errorf("another tenant: %d", st)
	}
	tok := decode[struct{ Plaintext string }](t, h.expect(201, x.a, "POST", "/workspaces/"+x.wsA.ID+"/api-tokens", map[string]any{"name": "ci", "role": "admin"})).Plaintext
	if st, _, b := h.tokenCall(tok, "PATCH", path, map[string]any{"controlVariationId": "green"}); st != 403 || !bytes.Contains(b, []byte("TOKEN_NOT_ALLOWED")) {
		t.Errorf("a token: %d %s", st, b)
	}
	// validation
	if st, _ := set(x.a, "purple"); st != 400 {
		t.Errorf("unknown variation: %d", st)
	}
	if st, _ := h.call(x.a, "PATCH", path, map[string]any{}); st != 400 {
		t.Errorf("no variation: %d", st)
	}
	if st, _ := h.call(x.a, "PATCH", "/environments/"+x.devA+"/experiments/nope", map[string]any{"controlVariationId": "red"}); st != 404 {
		t.Errorf("unknown experiment: %d", st)
	}
	// before any exposure it can change, any number of times, and the same value is a no-op
	var view expOut
	view = decode[expOut](t, h.expect(200, x.a, "PATCH", path, map[string]any{"controlVariationId": "green"}))
	if view.ControlVariationID != "green" {
		t.Fatalf("%+v", view)
	}
	h.expect(200, x.a, "PATCH", path, map[string]any{"controlVariationId": "green"})
	view = decode[expOut](t, h.expect(200, x.a, "PATCH", path, map[string]any{"controlVariationId": "blue"}))
	if n := h.count(`SELECT count(*) FROM experiment_variations v JOIN experiments e ON e.id = v.experiment_id WHERE e.key = 'pick' AND v.is_control`); n != 1 || view.ControlVariationID != "blue" {
		t.Errorf("controls: %d, view %+v", n, view)
	}
	if n := h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND action = 'experiment.control_change'`, x.wsA.ID); n != 2 {
		t.Errorf("control-change audit entries: %d (the no-op is not a change)", n)
	}
	// an event that is not an exposure does not freeze it
	h.rawExec(`INSERT INTO events (workspace_id, environment_id, event_id, event_name, subject_key, occurred_at) SELECT workspace_id, environment_id, 'other-1', 'page', 's', now() FROM experiments WHERE key = 'pick'`)
	h.expect(200, x.a, "PATCH", path, map[string]any{"controlVariationId": "red"})
	// the first exposure freezes it
	x.exposeOne(t, "pick", "green")
	st, b := set(x.a, "blue")
	if st != 409 || !bytes.Contains(b, []byte("CONTROL_FROZEN")) {
		t.Fatalf("after an exposure: %d %s", st, b)
	}
	if got := decode[expOut](t, h.expect(200, x.a, "GET", path, nil)).ControlVariationID; got != "red" {
		t.Errorf("control is %s after a refused change", got)
	}
	// choosing the control it already has is not a change, so it is not refused
	h.expect(200, x.a, "PATCH", path, map[string]any{"controlVariationId": "red"})
	// another experiment's exposures do not freeze this one
	x.newExperiment(t, "colour", "other", "")
	h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/experiments/other", map[string]any{"controlVariationId": "green"})
}

// Results compare against the designated control, and swapping the control gives the mirror image: the same p-value,
// the difference and both interval ends negated and swapped.
func TestResultsUseTheDesignatedControlAndASwapIsTheMirrorImage(t *testing.T) {
	x := newTenants(t)
	h := x.h
	for _, f := range []string{"swap-a", "swap-b"} {
		h.expect(201, x.a, "POST", "/environments/"+x.devA+"/flags", boolFlag(f))
		h.expect(200, x.a, "PATCH", "/environments/"+x.devA+"/flags/"+f, map[string]any{"enabled": true})
	}
	x.newExperiment(t, "swap-a", "ctl-on", "on")
	x.newExperiment(t, "swap-b", "ctl-off", "off")
	ids := map[string]string{}
	for _, k := range []string{"ctl-on", "ctl-off"} {
		h.expect(200, x.a, "POST", "/environments/"+x.devA+"/experiments/"+k+"/start", nil)
		var id string
		_ = h.pool.QueryRow(context.Background(), `SELECT id::text FROM experiments WHERE key = $1`, k).Scan(&id)
		ids[k] = id
		h.rawExec(`UPDATE experiments SET started_at = started_at - interval '3 days' WHERE id = $1::uuid`, id)
		// identical data: on 400 exposed / 120 converted, off 400 exposed / 80 converted
		for i := 0; i < 400; i++ {
			x.seed(id, fmt.Sprintf("on-%d", i), "$exposure", "on", "1 second", nil)
			x.seed(id, fmt.Sprintf("off-%d", i), "$exposure", "off", "1 second", nil)
		}
		for i := 0; i < 120; i++ {
			x.seed(id, fmt.Sprintf("on-%d", i), "e", "", "5 seconds", nil)
		}
		for i := 0; i < 80; i++ {
			x.seed(id, fmt.Sprintf("off-%d", i), "e", "", "5 seconds", nil)
		}
	}
	a := x.results(x.a, x.devA, "ctl-on", "")
	b := x.results(x.a, x.devA, "ctl-off", "")
	if a.Control != "on" || b.Control != "off" {
		t.Fatalf("controls: %q %q", a.Control, b.Control)
	}
	ca, cb := a.Metrics[0].Comparisons, b.Metrics[0].Comparisons
	if len(ca) != 1 || len(cb) != 1 || ca[0].VariationID != "off" || ca[0].Against != "on" || cb[0].VariationID != "on" || cb[0].Against != "off" {
		t.Fatalf("comparisons: %+v / %+v", ca, cb)
	}
	near := func(what string, x, y float64) {
		if math.Abs(x-y) > 1e-9 {
			t.Errorf("%s: %v vs %v", what, x, y)
		}
	}
	near("p-value", *ca[0].PValue, *cb[0].PValue)
	near("statistic (z) mirrored", *ca[0].Statistic, -*cb[0].Statistic)
	near("difference mirrored", *ca[0].Difference, -*cb[0].Difference)
	near("interval low = -high", *ca[0].DifferenceCI.Low, -*cb[0].DifferenceCI.High)
	near("interval high = -low", *ca[0].DifferenceCI.High, -*cb[0].DifferenceCI.Low)
	near("difference is variant minus control", *ca[0].Difference, -0.1)
	// each variation's own numbers do not depend on which one is the control
	for _, v := range []string{"on", "off"} {
		va, vb := variation(a, 0, v), variation(b, 0, v)
		if va.Exposures != vb.Exposures || va.ConvertingSubjects != vb.ConvertingSubjects {
			t.Errorf("%s: %+v vs %+v", v, va, vb)
		}
	}
	// the results page lists the control first
	if a.Metrics[0].Variations[0].VariationID != "on" || b.Metrics[0].Variations[0].VariationID != "off" {
		t.Errorf("control is not listed first: %s / %s", a.Metrics[0].Variations[0].VariationID, b.Metrics[0].Variations[0].VariationID)
	}
	_ = results.Response{}
}

// The migration: an experiment that existed before 0014 gets every variation of its flag, the first one as control, so
// no existing result changes.
func TestMigration0014BackfillsTheFirstVariationAsControl(t *testing.T) {
	adminURL := os.Getenv("HELIOS_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("HELIOS_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	u, _ := url.Parse(adminURL)
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("helios_mig_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := db.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	files, _ := filepath.Glob(filepath.Join("..", "..", "db", "migrations", "0*.sql"))
	sort.Strings(files)
	apply := func(f string) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}
	for _, f := range files[:13] { // 0001-0013: the schema before this release
		apply(f)
	}
	mustExec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var owner, ws, env, flag string
	if err := pool.QueryRow(ctx, `INSERT INTO auth.users (email) VALUES ('old@example.com') RETURNING id::text`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug, owner_id) VALUES ('Old', 'old-ws', $1::uuid) RETURNING id::text`, owner).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO environments (workspace_id, key, name) VALUES ($1::uuid, 'dev', 'Dev') RETURNING id::text`, ws).Scan(&env); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO flags (workspace_id, key, name, variation_type, variations) VALUES ($1::uuid, 'old', 'Old', 'string',
		'[{"id":"alpha","value":"a"},{"id":"beta","value":"b"},{"id":"gamma","value":"c"}]'::jsonb) RETURNING id::text`, ws).Scan(&flag); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"exp-one", "exp-two"} {
		mustExec(`INSERT INTO experiments (workspace_id, environment_id, flag_id, key, name) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $4)`, ws, env, flag, k)
	}
	apply(files[13]) // 0014
	rows, err := pool.Query(ctx, `SELECT x.key, v.variation_id, v.position, v.is_control FROM experiment_variations v JOIN experiments x ON x.id = v.experiment_id ORDER BY x.key, v.position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var k, v string
		var pos int
		var c bool
		if err := rows.Scan(&k, &v, &pos, &c); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s/%s/%d/%v", k, v, pos, c))
	}
	want := "exp-one/alpha/0/true exp-one/beta/1/false exp-one/gamma/2/false exp-two/alpha/0/true exp-two/beta/1/false exp-two/gamma/2/false"
	if strings.Join(got, " ") != want {
		t.Errorf("backfill:\n got  %s\n want %s", strings.Join(got, " "), want)
	}
}
