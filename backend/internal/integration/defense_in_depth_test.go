package integration

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

// The composite foreign keys make "a flag_config joins a flag of another
// workspace" impossible, so the workspace_id filters in the flag queries are
// redundant on consistent data. Redundant protection nobody tests is not
// protection, so this test makes the data inconsistent ON PURPOSE (it switches
// foreign-key triggers off for one local, superuser transaction) and checks
// that the filters still refuse. Remove a filter and the matching assertion
// below fails.
func TestWorkspaceFiltersHoldEvenIfTheDataIsInconsistent(t *testing.T) {
	x := newTenants(t)
	h := x.h
	const foreign = "bobs-planted-flag"
	h.expect(201, x.b, "POST", "/environments/"+x.devB+"/flags", boolFlag(foreign))

	// Plant a flag_config that ties B's flag to A's dev environment.
	var superuser bool
	if err := h.pool.QueryRow(context.Background(), `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&superuser); err != nil || !superuser {
		t.Skip("needs a superuser connection to switch foreign keys off (the throwaway local database is one)")
	}
	_, err := h.pool.Exec(context.Background(), fmt.Sprintf(`
		BEGIN;
		SET LOCAL session_replication_role = replica;
		INSERT INTO flag_configs (workspace_id, flag_id, environment_id, salt, fallthrough_variation_id, enabled)
		SELECT '%s'::uuid, f.id, '%s'::uuid, 'planted', 'on', true
		FROM flags f WHERE f.workspace_id = '%s'::uuid AND f.key = '%s';
		COMMIT;`, x.wsA.ID, x.devA, x.wsB.ID, foreign))
	if err != nil {
		t.Fatalf("plant inconsistent row: %v", err)
	}
	if n := h.count(`SELECT count(*) FROM flag_configs fc JOIN flags f ON f.id = fc.flag_id WHERE fc.environment_id = $1::uuid AND f.key = $2`, x.devA, foreign); n != 1 {
		t.Fatalf("the planted row is not there (%d); the test is not testing anything", n)
	}

	envA := "/environments/" + x.devA
	// Without the filters, A would read, change, kill and evaluate B's flag.
	if b := h.expect(404, x.a, "GET", envA+"/flags/"+foreign, nil); bytes.Contains(b, []byte(`"enabled"`)) {
		t.Errorf("GET returned B's flag: %s", b)
	}
	if b := h.expect(200, x.a, "GET", envA+"/flags", nil); bytes.Contains(b, []byte(foreign)) {
		t.Errorf("the list shows B's flag: %s", b)
	}
	h.expect(404, x.a, "PATCH", envA+"/flags/"+foreign, map[string]any{"enabled": false})
	h.expect(404, x.a, "POST", envA+"/flags/"+foreign+"/kill", nil)
	h.expect(404, x.a, "DELETE", envA+"/flags/"+foreign+"?force=true", nil)
	if st, b := h.call(x.a, "POST", envA+"/experiments", map[string]any{
		"flagKey": foreign, "key": "planted-exp", "name": "n",
		"metrics": []map[string]any{{"name": "M", "eventName": "e", "type": "conversion", "isPrimary": true}},
	}); st != 404 || !bytes.Contains(b, []byte("FLAG_NOT_FOUND")) {
		t.Errorf("experiment on B's flag: %d %s", st, b)
	}
	// The data plane: A's SDK key must not read it either.
	b := h.sdkPost(x.sdkKeyA, "/evaluate", map[string]any{"context": map[string]any{"subjectKey": "u"}, "flagKeys": []string{foreign}})
	if !bytes.Contains(b, []byte("FLAG_NOT_FOUND")) || strings.Contains(string(b), `"value":true`) {
		t.Errorf("A's SDK key evaluated B's flag: %s", b)
	}

	// And B's flag is untouched by all of it.
	got := decode[struct{ Config struct{ Enabled bool } }](t, h.expect(200, x.b, "GET", "/environments/"+x.devB+"/flags/"+foreign, nil))
	_ = got
	if n := h.count(`SELECT count(*) FROM flags WHERE workspace_id = $1::uuid AND key = $2`, x.wsB.ID, foreign); n != 1 {
		t.Error("B's flag was deleted")
	}
	if n := h.count(`SELECT count(*) FROM audit_logs WHERE workspace_id = $1::uuid AND resource_id = $2 AND action <> 'flag.create'`, x.wsB.ID, foreign); n != 0 {
		t.Errorf("%d audit rows record changes to B's flag", n)
	}
}
