package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var loadClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns: 4096, MaxIdleConnsPerHost: 4096, MaxConnsPerHost: 0,
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, IdleConnTimeout: 30 * time.Second,
	},
}

func apiPid(o options) int {
	if o.pid > 0 {
		return o.pid
	}
	u, _ := url.Parse(o.api)
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+u.Port(), "-sTCP:LISTEN", "-t").Output()
	if err != nil {
		return 0
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(f[0])
	return n
}

func runAll(o options) error {
	var all []Result
	for _, sc := range []string{"evaluate", "track", "stream", "mixed"} {
		rs, err := scenario(o, sc)
		if err != nil {
			return err
		}
		all = append(all, rs...)
	}
	printTable(all)
	return writeJSON(o, all)
}

func runOne(o options, sc string) error {
	rs, err := scenario(o, sc)
	if err != nil {
		return err
	}
	printTable(rs)
	return writeJSON(o, rs)
}

func writeJSON(o options, rs []Result) error {
	if o.out == "" {
		return nil
	}
	b, _ := json.MarshalIndent(rs, "", "  ")
	return os.WriteFile(o.out, b, 0o644)
}

func printTable(rs []Result) {
	fmt.Println("\n| scenario | c | req/s | events/s | p50 ms | p95 ms | p99 ms | max ms | errors | RSS MB (start→peak) | API CPU % | DB conns | goroutines |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, r := range rs {
		ev := ""
		if r.EventsPS > 0 {
			ev = fmt.Sprintf("%.0f", r.EventsPS)
		}
		fmt.Printf("| %s | %d | %.0f | %s | %.1f | %.1f | %.1f | %.0f | %.2f%% (%d) | %.0f→%.0f | %.0f | %d | %d |\n",
			r.Scenario, r.Concurrency, r.RPS, ev, r.P50ms, r.P95ms, r.P99ms, r.MaxMs, r.ErrorRate*100, r.Errors, r.RSSStartMB, r.RSSPeakMB, r.CPUAvgPct, r.DBConnMax, r.GoroutMax)
		if len(r.Extra) > 0 {
			keys := make([]string, 0, len(r.Extra))
			for k := range r.Extra {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var parts []string
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s=%v", k, r.Extra[k]))
			}
			fmt.Printf("|   ↳ %s | | | | | | | | | | | | |\n", strings.Join(parts, ", "))
		}
	}
}

// scenario runs one named scenario (or the mixed one) and returns its results.
func scenario(o options, name string) ([]Result, error) {
	st, err := loadState(o)
	if err != nil {
		return nil, err
	}
	if st.API != o.api {
		fmt.Fprintf(os.Stderr, "note: seeded against %s, running against %s\n", st.API, o.api)
	}
	if res, err := httpc.Get(o.api + "/healthz"); err != nil || res.StatusCode != 200 {
		return nil, fmt.Errorf("the API at %s does not answer /healthz (tools/loadtest/run.sh up): %v", o.api, err)
	} else {
		res.Body.Close()
	}
	pid := apiPid(o)
	fmt.Printf("== %s: %s for %s (api pid %d)\n", name, o.api, o.duration, pid)
	// A short warm-up so connections, caches and the JIT-less runtime are in their steady state.
	if name != "stream" {
		warm := o
		warm.duration = 2 * time.Second
		_ = oneKind(warm, st, "evaluate", warm.concurrency, nil)
	}
	samp := startSampler(o, pid)
	stopProfiles := startProfiles(o, name)
	var results []Result
	switch name {
	case "evaluate", "track":
		r := oneKind(o, st, name, o.concurrency, nil)
		results = append(results, r)
	case "stream":
		results = append(results, streamKind(o, st, o.streams, o.duration, true))
	case "mixed":
		var wg sync.WaitGroup
		var re, rt, rs Result
		wg.Add(3)
		go func() { defer wg.Done(); re = oneKind(o, st, "evaluate", o.concurrency, nil) }()
		go func() { defer wg.Done(); rt = oneKind(o, st, "track", max(2, o.concurrency/4), nil) }()
		go func() { defer wg.Done(); rs = streamKind(o, st, max(50, o.streams/2), o.duration, true) }()
		wg.Wait()
		re.Scenario, rt.Scenario, rs.Scenario = "mixed: evaluate", "mixed: track", "mixed: streams+edits"
		results = append(results, re, rt, rs)
	default:
		return nil, fmt.Errorf("unknown scenario %q", name)
	}
	stopProfiles()
	var res Result
	samp.finish(&res)
	for i := range results {
		results[i].Label = o.label
		results[i].RSSStartMB, results[i].RSSPeakMB, results[i].CPUAvgPct = res.RSSStartMB, res.RSSPeakMB, res.CPUAvgPct
		results[i].DBConnMax, results[i].GoroutMax = res.DBConnMax, res.GoroutMax
	}
	return results, nil
}

// startProfiles takes a CPU profile for most of the run and a heap and goroutine profile at the end, when the API
// exposes pprof (PPROF_ADDR) and -profile names a directory.
func startProfiles(o options, name string) func() {
	if o.profileDir == "" || o.pprof == "" {
		return func() {}
	}
	_ = os.MkdirAll(o.profileDir, 0o755)
	secs := int(o.duration.Seconds()) - 2
	if secs < 2 {
		secs = 2
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fetch(o.pprof+"/debug/pprof/profile?seconds="+strconv.Itoa(secs), filepath.Join(o.profileDir, name+"-cpu.pb.gz"), time.Duration(secs+30)*time.Second)
	}()
	return func() {
		<-done
		fetch(o.pprof+"/debug/pprof/heap", filepath.Join(o.profileDir, name+"-heap.pb.gz"), 30*time.Second)
		fetch(o.pprof+"/debug/pprof/goroutine", filepath.Join(o.profileDir, name+"-goroutine.pb.gz"), 30*time.Second)
	}
}

func fetch(u, path string, timeout time.Duration) {
	c := &http.Client{Timeout: timeout}
	res, err := c.Get(u)
	if err != nil {
		fmt.Fprintln(os.Stderr, "profile:", err)
		return
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	_ = os.WriteFile(path, b, 0o644)
}

// ---- evaluate / track ---------------------------------------------------------------------------------

var (
	countries = []string{"DE", "FR", "NL", "US", "GB", "BR", "JP"}
	plans     = []string{"free", "pro", "team"}
	names     = []string{"page_view", "click", "purchase", "signup", "search"}
)

func oneKind(o options, st State, kind string, workers int, _ *struct{}) Result {
	rec := newRecorder()
	var events atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	deadline := start.Add(o.duration)
	var seq atomic.Int64
	runID := time.Now().UnixNano()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w) + runID))
			l := newLocal()
			for time.Now().Before(deadline) {
				var body []byte
				var path string
				if kind == "evaluate" {
					path, body = "/evaluate", evalBody(rng, st, o.keysPerEval)
				} else {
					path, body = "/track", trackBody(rng, st, o.batch, runID, &seq)
				}
				t0 := time.Now()
				status, err := post(o.api+path, st.SDKKey, body)
				l.add(time.Since(t0), status, err)
				if kind == "track" && err == nil && status == 200 {
					events.Add(int64(o.batch))
				}
			}
			rec.merge(l)
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)
	res := Result{Scenario: kind, Concurrency: workers}
	rec.fill(&res, elapsed)
	if kind == "track" {
		res.EventsPS = float64(events.Load()) / elapsed.Seconds()
	}
	return res
}

func post(url, key string, body []byte) (int, error) {
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Helios-SDK-Key", key)
	res, err := loadClient.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode, nil
}

func evalBody(rng *rand.Rand, st State, n int) []byte {
	off := rng.Intn(len(st.FlagKeys))
	keys := make([]string, 0, n)
	for i := 0; i < n && i < len(st.FlagKeys); i++ {
		keys = append(keys, st.FlagKeys[(off+i)%len(st.FlagKeys)])
	}
	b, _ := json.Marshal(map[string]any{
		"context": map[string]any{
			"subjectKey": "user-" + strconv.Itoa(rng.Intn(20000)),
			"attributes": map[string]any{"country": countries[rng.Intn(len(countries))], "plan": plans[rng.Intn(len(plans))], "age": 12 + rng.Intn(60)},
		},
		"flagKeys": keys,
	})
	return b
}

func trackBody(rng *rand.Rand, st State, n int, runID int64, seq *atomic.Int64) []byte {
	evs := make([]map[string]any, n)
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range evs {
		e := map[string]any{
			"id":         fmt.Sprintf("lt-%d-%d", runID, seq.Add(1)),
			"subjectKey": "user-" + strconv.Itoa(rng.Intn(5000)),
			"timestamp":  now,
		}
		if len(st.Experiments) > 0 && rng.Intn(10) == 0 {
			e["name"], e["experimentId"], e["variationId"] = "$exposure", st.Experiments[rng.Intn(len(st.Experiments))], "a"
		} else {
			e["name"] = names[rng.Intn(len(names))]
			if e["name"] == "purchase" {
				e["value"] = float64(rng.Intn(20000)) / 100
			}
			e["properties"] = map[string]any{"page": "/p/" + strconv.Itoa(rng.Intn(50)), "plan": plans[rng.Intn(len(plans))]}
			if len(st.Experiments) > 0 && rng.Intn(5) == 0 {
				e["experimentId"] = st.Experiments[rng.Intn(len(st.Experiments))]
			}
		}
		evs[i] = e
	}
	b, _ := json.Marshal(map[string]any{"events": evs})
	return b
}

// ---- streams ------------------------------------------------------------------------------------------

// streamKind opens `n` SSE streams, holds them for d while flag edits flow at one per second, and measures the time
// to open each one and how long an edit takes to reach the streams.
func streamKind(o options, st State, n int, d time.Duration, edits bool) Result {
	var (
		wg       sync.WaitGroup
		opened   atomic.Int64
		failed   atomic.Int64
		received atomic.Int64
		lastEdit atomic.Int64 // unix nanos of the latest edit's start
		latMu    sync.Mutex
		lats     []time.Duration
		connect  = newRecorder()
	)
	start := time.Now()
	deadline := start.Add(d)
	sem := make(chan struct{}, 50) // open in waves, as a fleet of SDKs would not all connect in the same millisecond
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			l := newLocal()
			req, _ := http.NewRequest("GET", o.api+"/sdk/stream", nil)
			req.Header.Set("X-Helios-SDK-Key", st.SDKKey)
			c := &http.Client{Transport: loadClient.Transport} // no overall timeout: the stream is long-lived
			t0 := time.Now()
			res, err := c.Do(req)
			<-sem
			if err != nil {
				l.add(time.Since(t0), 0, err)
				failed.Add(1)
				connect.merge(l)
				return
			}
			l.add(time.Since(t0), res.StatusCode, nil)
			connect.merge(l)
			defer res.Body.Close()
			if res.StatusCode != 200 {
				failed.Add(1)
				return
			}
			opened.Add(1)
			timer := time.AfterFunc(time.Until(deadline.Add(time.Second)), func() { res.Body.Close() })
			defer timer.Stop()
			sc := bufio.NewScanner(res.Body)
			for sc.Scan() {
				if strings.HasPrefix(sc.Text(), "data:") {
					received.Add(1)
					if e := lastEdit.Load(); e > 0 {
						latMu.Lock()
						lats = append(lats, time.Since(time.Unix(0, e)))
						latMu.Unlock()
					}
				}
			}
		}()
	}
	editsDone := 0
	if edits {
		tick := time.NewTicker(time.Second)
		for time.Now().Before(deadline) {
			<-tick.C
			lastEdit.Store(time.Now().UnixNano())
			on := 10000 + (editsDone%2)*40000
			_, _ = call("PATCH", o.api+"/environments/"+st.EnvID+"/flags/"+st.EditFlag, st.Token,
				map[string]any{"enabled": true, "rollout": map[string]any{"on": on, "off": 100000 - on}}, nil)
			editsDone++
		}
		tick.Stop()
	} else {
		time.Sleep(time.Until(deadline))
	}
	wg.Wait()
	res := Result{Scenario: "stream", Concurrency: n}
	connect.fill(&res, time.Since(start))
	res.RPS = float64(opened.Load()) / d.Seconds() // connections opened per second over the window
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	q := func(f float64) string {
		if len(lats) == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f", float64(lats[int(f*float64(len(lats)-1))].Microseconds())/1000)
	}
	res.Extra = map[string]any{
		"streamsOpened": opened.Load(), "streamsFailed": failed.Load(), "edits": editsDone, "eventsReceived": received.Load(),
		"editToStreamP50ms": q(0.5), "editToStreamP95ms": q(0.95), "editToStreamP99ms": q(0.99),
	}
	return res
}
