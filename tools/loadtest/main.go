// Command loadtest is a small, repeatable load generator for the Helios data plane. It runs ONLY against a local
// stack: any target that is not loopback is refused. Standard library only, no paid account, no network.
//
//	go run . seed                       # a workspace with many flags, segments, experiments and an SDK key
//	go run . run -scenario evaluate     # POST /evaluate
//	go run . run -scenario track        # POST /track, batches of events
//	go run . run -scenario stream       # many concurrent SSE streams, with flag edits flowing to them
//	go run . run -scenario mixed        # all of it at once
//	go run . all                        # the four scenarios in turn, one table
//
// See README.md for the stack it expects (an API instance with rate limits off, on its own database) and
// docs/PERFORMANCE.md for what the numbers mean.
package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type options struct {
	api, auth, pprof string
	state            string
	pid              int
	db               string
	duration         time.Duration
	concurrency      int
	streams          int
	batch            int
	keysPerEval      int
	flags            int
	fillers          int
	out              string
	profileDir       string
	label            string
	scenario         string
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var o options
	home, _ := os.UserHomeDir()
	fs.StringVar(&o.api, "api", "http://127.0.0.1:8081", "the API under test (loopback only)")
	fs.StringVar(&o.auth, "auth", "http://127.0.0.1:54321", "the devauth stand-in (loopback only)")
	fs.StringVar(&o.pprof, "pprof", "", "the API's pprof listener, e.g. http://127.0.0.1:6061 (PPROF_ADDR); enables goroutine counts and profiles")
	fs.StringVar(&o.state, "state", filepath.Join(home, "helios-local", "loadtest-state.json"), "where seed writes the SDK key and ids (outside the repo)")
	fs.IntVar(&o.pid, "pid", 0, "the API's process id, for memory and CPU (default: whoever listens on -api's port)")
	fs.StringVar(&o.db, "db", os.Getenv("LOADTEST_DATABASE_URL"), "postgres URL of the API's database, for connection counts via psql (optional)")
	fs.DurationVar(&o.duration, "duration", 20*time.Second, "how long each scenario runs")
	fs.IntVar(&o.concurrency, "c", 32, "concurrent workers for evaluate and track")
	fs.IntVar(&o.streams, "streams", 500, "concurrent SSE streams for the stream scenario")
	fs.IntVar(&o.batch, "batch", 25, "events per /track request")
	fs.IntVar(&o.keysPerEval, "keys", 8, "flag keys per /evaluate request")
	fs.IntVar(&o.flags, "flags", 50, "flags in the workspace under test (seed; a workspace is limited to 50)")
	fs.IntVar(&o.fillers, "fillers", 20, "other workspaces of 50 flags each, for realistic database volume (seed)")
	fs.StringVar(&o.out, "out", "", "also write the results as JSON to this file")
	fs.StringVar(&o.profileDir, "profile", "", "directory for CPU/heap/goroutine profiles taken during the run (needs -pprof)")
	fs.StringVar(&o.label, "label", "", "a name for this run, recorded in the JSON")
	fs.StringVar(&o.scenario, "scenario", "evaluate", "evaluate | track | stream | mixed (run)")
	_ = fs.Parse(os.Args[2:])

	for _, u := range []string{o.api, o.auth, o.pprof} {
		if err := mustBeLocal(u); err != nil {
			fatal(err)
		}
	}
	if abs, err := filepath.Abs(o.state); err == nil {
		if wd, _ := os.Getwd(); strings.Contains(abs, "A-B-Testing-Feature-Flag-Management-Console") && !strings.Contains(abs, "helios-local") {
			_ = wd
			fatal(fmt.Errorf("state file %s is inside the repository; it holds an SDK key and must live outside it", abs))
		}
	}
	switch cmd {
	case "seed":
		fatalIf(seed(o))
	case "run":
		fatalIf(runOne(o, o.scenario))
	case "all":
		fatalIf(runAll(o))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: loadtest {seed|run|all} [flags]   (-h for flags)")
	os.Exit(2)
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "loadtest:", err); os.Exit(1) }
func fatalIf(err error) {
	if err != nil {
		fatal(err)
	}
}

// mustBeLocal refuses a target that is not this machine: a load test must never reach a shared or hosted system.
func mustBeLocal(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("bad URL %q", raw)
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return nil
	}
	return fmt.Errorf("%s is not a loopback address; the load test only runs against the local stack", raw)
}
