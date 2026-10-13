// Command segmentusage is the check to run BEFORE rolling the backend back to a
// version older than segments. Such a backend does not know the segment
// clauses, the extra operators or `negate`, and treats a rule that uses them
// as "no match": the rule stays stored but silently stops matching. This
// command lists, per workspace, the flags and environments whose rules use any
// of that, so each can be reverted first.
//
//	DATABASE_URL=... go run ./cmd/segmentusage
//	DATABASE_URL=... go run ./cmd/segmentusage -fail-if-any   # exit 1 when anything is listed
//
// It is read-only (a read-only transaction) and prints identifiers only:
// workspace ids, flag keys, environment keys and the feature names used. No
// rule values, no member keys, no emails. See docs/SEGMENTS.md, "Rolling back".
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"helios/backend/internal/controlplane/segments"
	"helios/backend/internal/platform/config"
	"helios/backend/internal/platform/db"
)

func main() {
	failIfAny := flag.Bool("fail-if-any", false, "exit with status 1 when any flag uses the new features")
	timeout := flag.Duration("timeout", time.Minute, "give up after this long")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	pool, err := db.Connect(ctx, config.Load().DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	uses, err := segments.FlagsUsingNewFeatures(ctx, pool)
	if err != nil {
		log.Fatalf("segmentusage: %v", err) // the pgx error carries no rule content
	}
	fmt.Print(report(uses))
	if *failIfAny && len(uses) > 0 {
		os.Exit(1)
	}
}

// report formats the uses per workspace: counts and keys only.
func report(uses []segments.FlagUse) string {
	if len(uses) == 0 {
		return "segmentusage: no flag rule uses segment clauses, the new operators or negate. Rolling back the backend will not change what any flag serves.\n"
	}
	var b strings.Builder
	workspaces := map[string]bool{}
	for _, u := range uses {
		workspaces[u.WorkspaceID] = true
	}
	fmt.Fprintf(&b, "segmentusage: %d flag configuration(s) in %d workspace(s) use segment clauses, the new operators or negate.\n", len(uses), len(workspaces))
	b.WriteString("An older backend would treat these rules as matching nobody. Revert each (remove those clauses) BEFORE rolling back.\n")
	current := ""
	for _, u := range uses {
		if u.WorkspaceID != current {
			current = u.WorkspaceID
			n := 0
			for _, v := range uses {
				if v.WorkspaceID == current {
					n++
				}
			}
			fmt.Fprintf(&b, "\nworkspace %s: %d flag configuration(s)\n", current, n)
		}
		fmt.Fprintf(&b, "  %-40s env=%-12s uses: %s\n", u.FlagKey, u.Environment, strings.Join(u.Features, ", "))
	}
	return b.String()
}
