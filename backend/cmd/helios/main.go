// Command helios is the container image's entrypoint: one image carries the API and the job commands, and this picks
// which one runs.
//
//	docker run IMAGE                      # the API (the default command)
//	docker run IMAGE scheduler            # cmd/scheduler: due scheduled changes, once, then exit
//	docker run IMAGE outbox               # cmd/outbox: webhook and invite-email deliveries, once
//	docker run IMAGE purge-workspaces     # cmd/purgeworkspaces: workspaces past their deletion grace
//	docker run IMAGE purge-events         # cmd/purgeevents: events past retention
//	docker run IMAGE segment-usage        # cmd/segmentusage: the check before rolling the backend back
//	docker run IMAGE healthcheck          # GET /healthz on this container's own listener (HEALTHCHECK uses it)
//	docker run IMAGE commands             # this list
//
// It replaces itself with the chosen command (exec), so the command is PID 1, receives SIGTERM directly, and nothing of
// this program stays in memory. Arguments after the name go to the command unchanged. There is no shell in the image.
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"helios/backend/internal/platform/config"
)

// Commands maps a name on the command line to the binary in the image.
var Commands = map[string]string{
	"api":              "api",
	"scheduler":        "scheduler",
	"outbox":           "outbox",
	"purge-workspaces": "purgeworkspaces",
	"purge-events":     "purgeevents",
	"segment-usage":    "segmentusage",
}

func binDir() string {
	if d := os.Getenv("HELIOS_BIN_DIR"); d != "" {
		return d
	}
	return "/app/bin"
}

func names() []string {
	var n []string
	for k := range Commands {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func main() {
	args := os.Args[1:]
	name := "api"
	if len(args) > 0 {
		name, args = args[0], args[1:]
	}
	switch name {
	case "commands", "help", "-h", "--help":
		fmt.Println("commands: " + strings.Join(append(names(), "healthcheck", "commands"), ", "))
		return
	case "healthcheck":
		os.Exit(healthcheck())
	}
	bin, ok := Commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "helios: unknown command %q; commands: %s\n", name, strings.Join(names(), ", "))
		os.Exit(2)
	}
	path := filepath.Join(binDir(), bin)
	if err := syscall.Exec(path, append([]string{bin}, args...), os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "helios: cannot start %s: %v\n", name, err)
		os.Exit(127)
	}
}

// healthcheck asks this container's own API whether the process is alive (/healthz: no dependency checks, so a database
// outage never gets a healthy container restarted). Exit 0 for alive.
func healthcheck() int {
	cfg := config.Load()
	addr, err := cfg.ListenAddress()
	if err != nil {
		fmt.Fprintln(os.Stderr, "helios healthcheck:", err)
		return 1
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "helios healthcheck: not answering")
		return 1
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "helios healthcheck: status %d\n", res.StatusCode)
		return 1
	}
	return 0
}
