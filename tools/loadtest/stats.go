package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Result is one scenario's outcome.
type Result struct {
	Label       string         `json:"label,omitempty"`
	Scenario    string         `json:"scenario"`
	Duration    float64        `json:"seconds"`
	Concurrency int            `json:"concurrency"`
	Requests    int            `json:"requests"`
	Errors      int            `json:"errors"`
	ErrorRate   float64        `json:"errorRate"`
	Statuses    map[string]int `json:"statuses"`
	RPS         float64        `json:"requestsPerSecond"`
	EventsPS    float64        `json:"eventsPerSecond,omitempty"`
	P50ms       float64        `json:"p50ms"`
	P95ms       float64        `json:"p95ms"`
	P99ms       float64        `json:"p99ms"`
	MaxMs       float64        `json:"maxMs"`
	RSSStartMB  float64        `json:"rssStartMB,omitempty"`
	RSSPeakMB   float64        `json:"rssPeakMB,omitempty"`
	CPUAvgPct   float64        `json:"apiCpuAvgPct,omitempty"`
	DBConnMax   int            `json:"dbConnectionsMax,omitempty"`
	GoroutMax   int            `json:"goroutinesMax,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// recorder collects latencies per worker (no lock on the hot path) and merges them at the end.
type recorder struct {
	mu       sync.Mutex
	lat      []time.Duration
	statuses map[string]int
	errors   int
	reqs     int
}

func newRecorder() *recorder { return &recorder{statuses: map[string]int{}} }

type local struct {
	lat      []time.Duration
	statuses map[string]int
	errors   int
}

func newLocal() *local { return &local{statuses: map[string]int{}} }

func (l *local) add(d time.Duration, status int, err error) {
	l.lat = append(l.lat, d)
	switch {
	case err != nil:
		l.errors++
		l.statuses["transport"]++
	case status >= 200 && status < 300:
		l.statuses[strconv.Itoa(status)]++
	default:
		l.errors++
		l.statuses[strconv.Itoa(status)]++
	}
}

func (r *recorder) merge(l *local) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lat = append(r.lat, l.lat...)
	r.errors += l.errors
	r.reqs += len(l.lat)
	for k, v := range l.statuses {
		r.statuses[k] += v
	}
}

func (r *recorder) fill(res *Result, elapsed time.Duration) {
	sort.Slice(r.lat, func(i, j int) bool { return r.lat[i] < r.lat[j] })
	pick := func(q float64) float64 {
		if len(r.lat) == 0 {
			return 0
		}
		i := int(q * float64(len(r.lat)-1))
		return float64(r.lat[i].Microseconds()) / 1000
	}
	res.Requests, res.Errors, res.Statuses = r.reqs, r.errors, r.statuses
	if r.reqs > 0 {
		res.ErrorRate = float64(r.errors) / float64(r.reqs)
	}
	res.Duration = elapsed.Seconds()
	res.RPS = float64(r.reqs) / elapsed.Seconds()
	res.P50ms, res.P95ms, res.P99ms = pick(0.50), pick(0.95), pick(0.99)
	if n := len(r.lat); n > 0 {
		res.MaxMs = float64(r.lat[n-1].Microseconds()) / 1000
	}
}

// sampler records the API process's memory and CPU, the database's connection count and the goroutine count while a
// scenario runs.
type sampler struct {
	pid      int
	db       string
	dbname   string
	pprof    string
	stop     chan struct{}
	done     chan struct{}
	rssFirst float64
	rssPeak  float64
	cpuSum   float64
	cpuN     int
	connMax  int
	gorMax   int
}

func startSampler(o options, pid int) *sampler {
	s := &sampler{pid: pid, db: o.db, pprof: o.pprof, stop: make(chan struct{}), done: make(chan struct{})}
	if o.db != "" {
		if i := strings.LastIndex(o.db, "/"); i >= 0 {
			s.dbname = strings.SplitN(o.db[i+1:], "?", 2)[0]
		}
	}
	go s.loop()
	return s
}

func (s *sampler) loop() {
	defer close(s.done)
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		s.once()
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
	}
}

func (s *sampler) once() {
	if s.pid > 0 {
		if out, err := exec.Command("ps", "-o", "rss=,%cpu=", "-p", strconv.Itoa(s.pid)).Output(); err == nil {
			f := strings.Fields(string(out))
			if len(f) == 2 {
				rss, _ := strconv.ParseFloat(f[0], 64)
				cpu, _ := strconv.ParseFloat(f[1], 64)
				rss /= 1024
				if s.rssFirst == 0 {
					s.rssFirst = rss
				}
				if rss > s.rssPeak {
					s.rssPeak = rss
				}
				s.cpuSum += cpu
				s.cpuN++
			}
		}
	}
	if s.db != "" && s.dbname != "" {
		q := fmt.Sprintf("SELECT count(*) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()", strings.ReplaceAll(s.dbname, "'", ""))
		if out, err := exec.Command("psql", s.adminURL(), "-qAt", "-c", q).Output(); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && n > s.connMax {
				s.connMax = n
			}
		}
	}
	if s.pprof != "" {
		if n := goroutines(s.pprof); n > s.gorMax {
			s.gorMax = n
		}
	}
}

// adminURL is the database URL with the database name replaced by "postgres" (so the sampler's own connection is
// not counted against the database being measured).
func (s *sampler) adminURL() string {
	i := strings.LastIndex(s.db, "/")
	rest := s.db[i+1:]
	q := ""
	if j := strings.Index(rest, "?"); j >= 0 {
		q = rest[j:]
	}
	return s.db[:i] + "/postgres" + q
}

func (s *sampler) finish(res *Result) {
	close(s.stop)
	<-s.done
	res.RSSStartMB, res.RSSPeakMB = round1(s.rssFirst), round1(s.rssPeak)
	if s.cpuN > 0 {
		res.CPUAvgPct = round1(s.cpuSum / float64(s.cpuN))
	}
	res.DBConnMax, res.GoroutMax = s.connMax, s.gorMax
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func goroutines(base string) int {
	res, err := httpc.Get(base + "/debug/pprof/goroutine?debug=1")
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	buf := make([]byte, 128)
	n, _ := res.Body.Read(buf)
	var total int
	_, _ = fmt.Sscanf(string(buf[:n]), "goroutine profile: total %d", &total)
	return total
}
