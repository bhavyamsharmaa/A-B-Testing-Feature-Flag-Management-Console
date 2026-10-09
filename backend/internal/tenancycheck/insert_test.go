package tenancycheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// tenantTables carry workspace_id. Migration 0004 gives that column a
// transitional DEFAULT so the old backend keeps working while it is applied;
// until 0005 drops it, an INSERT that forgets workspace_id would silently land
// in the legacy workspace. This test fails the build if any INSERT in the Go
// code omits it, so the code never relies on the default. (api_keys and
// workspace_invites gained workspace_id in 0006.)
var tenantTables = map[string]bool{
	"environments":           true,
	"flags":                  true,
	"flag_configs":           true,
	"experiments":            true,
	"user_environment_roles": true,
	"audit_logs":             true,
	"workspace_members":      true,
	"workspace_invites":      true,
	"api_keys":               true,
}

var insertRE = regexp.MustCompile(`(?is)INSERT\s+INTO\s+([a-z_][a-z0-9_]*)\s*(\(([^)]*)\))?`)

// insertsMissingWorkspace returns "table" for every INSERT into a tenant
// table in sql whose column list lacks workspace_id (or has no column list).
func insertsMissingWorkspace(sql string) []string {
	var bad []string
	for _, m := range insertRE.FindAllStringSubmatch(sql, -1) {
		table, columns := strings.ToLower(m[1]), strings.ToLower(m[3])
		if !tenantTables[table] {
			continue
		}
		hasColumn := false
		for _, c := range strings.Split(columns, ",") {
			if strings.TrimSpace(c) == "workspace_id" {
				hasColumn = true
			}
		}
		if !hasColumn {
			bad = append(bad, table)
		}
	}
	return bad
}

func TestCheckerFlagsMissingWorkspaceID(t *testing.T) {
	for name, sql := range map[string]string{
		"no workspace":              `INSERT INTO flags (key, name) VALUES ($1, $2)`,
		"no column list":            `INSERT INTO flag_configs VALUES ($1)`,
		"mixed case":                "insert into Audit_Logs (actor_id) values ($1)",
		"newline":                   "INSERT INTO\n  environments\n  (key, name) VALUES ($1, $2)",
		"second statement":          `INSERT INTO experiment_metrics (name) VALUES ($1); INSERT INTO experiments (key) VALUES ($1)`,
		"api key without workspace": `INSERT INTO api_keys (environment_id, kind) VALUES ($1, $2)`,
	} {
		if len(insertsMissingWorkspace(sql)) == 0 {
			t.Errorf("%s: not detected", name)
		}
	}
	for name, sql := range map[string]string{
		"has workspace":      `INSERT INTO flags (workspace_id, key) VALUES ($1, $2)`,
		"workspace last":     `INSERT INTO flags (key, workspace_id) VALUES ($1, $2)`,
		"not a tenant table": `INSERT INTO experiment_metrics (experiment_id, name) VALUES ($1, $2)`,
		"workspaces itself":  `INSERT INTO workspaces (name) VALUES ($1)`,
		"not an insert":      `SELECT 1 FROM flags`,
	} {
		if bad := insertsMissingWorkspace(sql); len(bad) != 0 {
			t.Errorf("%s: wrongly flagged %v", name, bad)
		}
	}
}

// Every INSERT into a tenant table anywhere in the backend's Go code (outside
// tests) must name workspace_id.
func TestEveryTenantInsertNamesWorkspaceID(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			sql, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, m := range insertRE.FindAllStringSubmatch(sql, -1) {
				if tenantTables[strings.ToLower(m[1])] {
					checked++
				}
			}
			for _, table := range insertsMissingWorkspace(sql) {
				t.Errorf("%s: INSERT INTO %s does not set workspace_id", fset.Position(lit.Pos()), table)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Guards against the walker silently scanning nothing.
	if checked < 8 {
		t.Errorf("only %d tenant-table INSERTs found; the check is not looking at the code", checked)
	}
}

// Routes must be mounted through internal/server's registry (its Router.add),
// which is what internal/integration's every-route test iterates over. A
// handler mounted directly on a mux anywhere else would escape that test.
func TestRoutesAreOnlyMountedThroughTheRegistry(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	allowed := map[string]int{
		filepath.Join(root, "internal", "server", "server.go"): 1,  // Router.add: the one place
		filepath.Join(root, "cmd", "devauth", "main.go"):       -1, // local-only dev tool, its own mux
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		calls := 0
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Handle" || sel.Sel.Name == "HandleFunc") {
				calls++
			}
			return true
		})
		want, isAllowed := allowed[path]
		switch {
		case calls == 0:
		case !isAllowed:
			t.Errorf("%s registers %d route(s) directly; mount them through internal/server so they are tenant-checked", path, calls)
		case want >= 0 && calls != want:
			t.Errorf("%s has %d Handle calls, want %d", path, calls, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// No log or print call may be handed a value that looks like a credential. A
// name-based check cannot prove absence, but it stops the easy mistake: adding
// log.Printf("... %v", token) or printing the Authorization header.
// (cmd/mkkey prints the key it just minted on purpose: that is its output.)
func TestLogCallsAreNotGivenCredentials(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	suspicious := []string{"token", "plaintext", "secret", "password", "passwd", "authorization", "bearer", "apikey", "sdkkey", "jwt", "cookie", "keyhash", "databaseurl"}
	logFuncs := map[string]bool{"Printf": true, "Print": true, "Println": true, "Fatal": true, "Fatalf": true, "Fatalln": true, "Panic": true, "Panicf": true}
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasSuffix(path, filepath.Join("cmd", "mkkey", "main.go")) {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, _ := sel.X.(*ast.Ident)
			if pkg == nil || (pkg.Name != "log" && pkg.Name != "fmt") || !(logFuncs[sel.Sel.Name] || strings.HasPrefix(sel.Sel.Name, "Fprint")) {
				return true
			}
			checked++
			for _, arg := range call.Args {
				ast.Inspect(arg, func(m ast.Node) bool {
					var name string
					switch v := m.(type) {
					case *ast.Ident:
						name = v.Name
					case *ast.SelectorExpr:
						name = v.Sel.Name
					default:
						return true
					}
					low := strings.ToLower(name)
					for _, bad := range suspicious {
						if strings.Contains(low, bad) {
							t.Errorf("%s: %s.%s is given %q, which looks like a credential", fset.Position(call.Pos()), pkg.Name, sel.Sel.Name, name)
						}
					}
					return true
				})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 8 {
		t.Errorf("only %d log calls found; the check is not looking at the code", checked)
	}
}
