package integration

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/controlplane/scheduling"
	"helios/backend/internal/platform/events"
	"helios/backend/internal/platform/graceful"
	"helios/backend/internal/platform/lifecycle"
	"helios/backend/internal/server"
)

// ---- the whole server, on a real listener, shut down the way the platform does it ----------------------

type liveServer struct {
	base   string
	signal context.CancelFunc // the SIGTERM
	done   chan error         // graceful.Serve's result
	life   *lifecycle.State
}

func startGraceful(t *testing.T, h *harness, opt graceful.Options) *liveServer {
	t.Helper()
	life := lifecycle.New()
	d := h.deps
	d.Lifecycle = life
	d.Limiter = ccLimiter()
	d.Context = t.Context()
	router := server.New(d)
	srv := &http.Server{Handler: router.Mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, signal := context.WithCancel(context.Background())
	opt.Life = life
	done := make(chan error, 1)
	go func() { done <- graceful.Serve(ctx, srv, ln, opt) }()
	t.Cleanup(func() {
		signal()
		select { // (a test that already read the result has nothing left to wait for)
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	return &liveServer{base: "http://" + ln.Addr().String(), signal: signal, done: done, life: life}
}

// sseReader reads a stream to its end and keeps what it received.
type sseReader struct {
	mu     sync.Mutex
	text   strings.Builder
	closed chan struct{}
	status int
}

func readStream(t *testing.T, base, sdkKey string) *sseReader {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/sdk/stream", nil)
	req.Header.Set("X-Helios-SDK-Key", sdkKey)
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	r := &sseReader{closed: make(chan struct{}), status: res.StatusCode}
	if res.StatusCode != 200 {
		res.Body.Close()
		close(r.closed)
		return r
	}
	go func() {
		defer close(r.closed)
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			r.mu.Lock()
			r.text.WriteString(sc.Text() + "\n")
			r.mu.Unlock()
		}
	}()
	return r
}

func (r *sseReader) got() string { r.mu.Lock(); defer r.mu.Unlock(); return r.text.String() }

func probe(t *testing.T, base, path string) (int, string) {
	t.Helper()
	res, err := (&http.Client{Timeout: 2 * time.Second}).Get(base + path)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestSIGTERMFlipsReadinessFirstTellsStreamsToReconnectAndDrainsRequests(t *testing.T) {
	x := trackWorld(t, 4, 2*time.Second)
	h := x.h
	live := startGraceful(t, h, graceful.Options{Timeout: 5 * time.Second, ReadyDelay: 500 * time.Millisecond})
	// before the signal: alive and ready
	if code, body := probe(t, live.base, "/readyz"); code != 200 || !strings.Contains(body, `"ok"`) {
		t.Fatalf("readyz before: %d %s", code, body)
	}
	streams := []*sseReader{readStream(t, live.base, x.sdkKeyA), readStream(t, live.base, x.sdkKeyA), readStream(t, live.base, x.sdkKeyA)}
	for _, s := range streams {
		if s.status != 200 {
			t.Fatalf("stream status %d", s.status)
		}
	}
	time.Sleep(100 * time.Millisecond)

	// a request in flight when the signal arrives: its insert takes 900 ms
	h.slowDown("events", "INSERT", 900*time.Millisecond)
	var slow trackResp
	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		slow = trackRaw(t, live.base, x.sdkKeyA, []byte(`{"events":[{"id":"inflight-1","name":"purchase","subjectKey":"s"}]}`))
	}()
	time.Sleep(200 * time.Millisecond)

	signalAt := time.Now()
	live.signal()

	// 1. readiness flips at once, while the listener is still up (ReadyDelay is 500 ms)
	var flippedAfter time.Duration
	for {
		code, body := probe(t, live.base, "/readyz")
		if code == 503 && strings.Contains(body, "shutting_down") {
			flippedAfter = time.Since(signalAt)
			break
		}
		if time.Since(signalAt) > time.Second {
			t.Fatalf("readyz still %d %s a second after the signal", code, body)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if flippedAfter > 250*time.Millisecond {
		t.Errorf("readiness flipped after %v", flippedAfter)
	}
	if code, body := probe(t, live.base, "/healthz"); code != 200 || body != "ok" {
		t.Errorf("healthz while shutting down: %d %s (the process is still alive)", code, body)
	}
	// 2. a new stream is refused, with a Retry-After
	req, _ := http.NewRequest("GET", live.base+"/sdk/stream", nil)
	req.Header.Set("X-Helios-SDK-Key", x.sdkKeyA)
	if res, err := http.DefaultClient.Do(req); err != nil {
		t.Errorf("new stream during the ready delay: %v", err)
	} else {
		res.Body.Close()
		if res.StatusCode != 503 || res.Header.Get("Retry-After") == "" {
			t.Errorf("new stream: %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
		}
	}
	// 3. the open streams were told to reconnect and closed
	for i, s := range streams {
		select {
		case <-s.closed:
		case <-time.After(2 * time.Second):
			t.Fatalf("stream %d is still open 2 s after the signal", i)
		}
		if !strings.Contains(s.got(), "event: reconnect") || !strings.Contains(s.got(), "server_shutdown") {
			t.Errorf("stream %d was not told to reconnect: %q", i, s.got())
		}
	}
	// 4. the request that was in flight completes, and its row is stored
	select {
	case <-slowDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight request never finished")
	}
	if slow.Status != 200 || slow.statuses()[0] != "accepted" {
		t.Errorf("in-flight request: %d %s", slow.Status, slow.Raw)
	}
	if h.count(`SELECT count(*) FROM events WHERE event_id = 'inflight-1'`) != 1 {
		t.Error("the acknowledged event is not stored")
	}
	// 5. the server exits cleanly, inside its budget, and stops listening
	select {
	case err := <-live.done:
		if err != nil {
			t.Errorf("Serve returned %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the server never exited")
	}
	if took := time.Since(signalAt); took > 5*time.Second {
		t.Errorf("shutdown took %v with a budget of 5s", took)
	}
	t.Logf("readiness flipped %v after the signal; whole shutdown %v (budget 5s, in-flight insert 900ms)", flippedAfter.Round(time.Millisecond), time.Since(signalAt).Round(time.Millisecond))
	if _, err := net.DialTimeout("tcp", strings.TrimPrefix(live.base, "http://"), 300*time.Millisecond); err == nil {
		t.Error("still accepting connections after shutdown")
	}
}

func TestARequestThatOutlivesTheBudgetIsCutOffAndTheServerStillExitsOnTime(t *testing.T) {
	x := trackWorld(t, 4, 5*time.Second)
	h := x.h
	live := startGraceful(t, h, graceful.Options{Timeout: 600 * time.Millisecond, ReadyDelay: 0})
	h.slowDown("events", "INSERT", 4*time.Second)
	errc := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest("POST", live.base+"/track", strings.NewReader(`{"events":[{"id":"slow-1","name":"purchase","subjectKey":"s"}]}`))
		req.Header.Set("X-Helios-SDK-Key", x.sdkKeyA)
		req.Header.Set("Content-Type", "application/json")
		res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err == nil {
			res.Body.Close()
		}
		errc <- err
	}()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	live.signal()
	select {
	case err := <-live.done:
		if !errors.Is(err, graceful.ErrBudget) {
			t.Errorf("Serve returned %v, want ErrBudget", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the server did not exit near its budget")
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Errorf("exited after %v with a budget of 600ms", took)
	}
	if err := <-errc; err == nil {
		t.Error("the cut-off request got an answer")
	}
}

// ---- jobs: a claimed row is never stranded -----------------------------------------------------------

type cancelOnPublish struct{ cancel context.CancelFunc }

func (c cancelOnPublish) Publish(context.Context, string, []byte) error { c.cancel(); return nil }

var _ events.Publisher = cancelOnPublish{}

func TestScheduledChangesClaimedButNotRunWhenAskedToStopAreReleasedAtOnce(t *testing.T) {
	w := newSchedWorld(t)
	h := w.h
	const n = 6
	for i := 0; i < n; i++ {
		key := "rel" + string(rune('a'+i))
		h.expect(201, w.a, "POST", "/environments/"+w.devA+"/flags", boolFlag(key))
		w.scheduleOK(t, w.a, w.devA, key, "k"+key, "enable", 2*time.Minute, nil)
	}
	w.clock.advance(3 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// the first change that applies announces itself; that is when the "SIGTERM" arrives
	runner := scheduling.NewRunner(h.pool, cancelOnPublish{cancel}, w.clock.now, scheduling.Config{WorkerID: "term-1", BatchSize: n})
	st, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Succeeded < 1 || st.Succeeded >= n || st.Released != n-st.Succeeded {
		t.Fatalf("stats after the stop: %+v (6 claimed, some applied, the rest released)", st)
	}
	if got := h.count(`SELECT count(*) FROM scheduled_changes WHERE status = 'running' OR lease_owner IS NOT NULL`); got != 0 {
		t.Fatalf("%d change(s) are still leased to the stopped worker: stranded until the lease expires", got)
	}
	if got := h.count(`SELECT count(*) FROM scheduled_changes WHERE status = 'pending' AND attempts = 0`); got != n-st.Succeeded {
		t.Errorf("%d pending with no attempts, want %d (the attempt that never happened is not counted)", got, n-st.Succeeded)
	}
	// the next worker takes them at once, long before any lease would have expired
	st2, err := scheduling.NewRunner(h.pool, h.bus, w.clock.now, scheduling.Config{WorkerID: "next"}).Run(context.Background())
	if err != nil || st2.Succeeded != n-st.Succeeded || st2.Reclaimed != 0 {
		t.Errorf("the next run: %+v %v (nothing needed reclaiming)", st2, err)
	}
	if got := h.count(`SELECT count(*) FROM scheduled_changes WHERE status = 'succeeded'`); got != n {
		t.Errorf("%d of %d succeeded", got, n)
	}
}

func TestOutboxRowsClaimedButNotSentWhenAskedToStopAreReleasedAndASendUnderWayIsRecorded(t *testing.T) {
	w := newOutboxWorld(t)
	const n = 6
	var ids []string
	for i := 0; i < n; i++ {
		ids = append(ids, w.enqueue(t, w.wsA.ID, w.endpoint(t, w.wsA.ID)))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// the first send is under way when the stop arrives: it still finishes and is recorded
	s := &fakeSender{during: func(outbox.Delivery) { cancel(); time.Sleep(50 * time.Millisecond) }}
	st, err := w.worker("term-1", s, outbox.Config{Concurrency: 1, BatchSize: n}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Delivered != 1 || st.Released != n-1 {
		t.Fatalf("stats: %+v, want 1 delivered and %d released", st, n-1)
	}
	if got := w.h.count(`SELECT count(*) FROM outbox WHERE status = 'delivering' OR lease_owner IS NOT NULL`); got != 0 {
		t.Fatalf("%d row(s) still leased to the stopped worker", got)
	}
	delivered, pending := 0, 0
	for _, id := range ids {
		status, attempts, _, _ := w.state(id)
		switch status {
		case "delivered":
			delivered++
			if attempts != 1 {
				t.Errorf("the delivered row has %d attempts", attempts)
			}
		case "pending":
			pending++
			if attempts != 0 {
				t.Errorf("a released row kept %d attempt(s) it never made", attempts)
			}
		default:
			t.Errorf("row in state %q", status)
		}
	}
	if delivered != 1 || pending != n-1 {
		t.Errorf("%d delivered, %d pending", delivered, pending)
	}
	// and the next worker sends them straight away, each exactly once overall
	s2 := &fakeSender{}
	st2, err := w.worker("next", s2, outbox.Config{}).Run(context.Background())
	if err != nil || st2.Delivered != n-1 || st2.Reclaimed != 0 {
		t.Errorf("next run: %+v %v", st2, err)
	}
	if len(s.deliveries())+len(s2.deliveries()) != n {
		t.Errorf("%d sends in total for %d rows (a row must be sent once)", len(s.deliveries())+len(s2.deliveries()), n)
	}
}

// A worker that is killed outright (SIGKILL, no chance to release) leaves rows that the lease recovers.
func TestARowLeftBehindByAKilledWorkerIsRecoveredWhenItsLeaseExpires(t *testing.T) {
	w := newOutboxWorld(t)
	id := w.enqueue(t, w.wsA.ID, w.endpoint(t, w.wsA.ID))
	killed := w.worker("killed", &fakeSender{}, outbox.Config{Lease: time.Minute})
	claimed, err := killed.Claim(context.Background(), 5) // claimed, then the process is gone
	if err != nil || len(claimed) != 1 {
		t.Fatal(err, claimed)
	}
	if status, _, _, _ := w.state(id); status != "delivering" {
		t.Fatalf("setup: %s", status)
	}
	next := &fakeSender{}
	if st, _ := w.worker("next", next, outbox.Config{Lease: time.Minute}).Run(context.Background()); st.Claimed != 0 {
		t.Fatalf("taken over before the lease expired: %+v", st)
	}
	w.clock.advance(2 * time.Minute)
	st, err := w.worker("next", next, outbox.Config{Lease: time.Minute}).Run(context.Background())
	if err != nil || st.Delivered != 1 || st.Reclaimed != 1 {
		t.Errorf("after the lease expired: %+v %v", st, err)
	}
}
