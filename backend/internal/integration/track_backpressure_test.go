package integration

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"helios/backend/internal/platform/ratelimit"
	"helios/backend/internal/server"
)

// /track never acknowledges an event before it is stored, and when the database cannot take more writers it says so
// at once instead of queueing without limit. These tests slow the events INSERT down inside this test's database so
// that the windows are wide enough to see.

func trackWorld(t *testing.T, writers int, wait time.Duration) *tenants {
	t.Helper()
	return newTenantsWith(t, func(d *server.Deps) {
		d.TrackMaxWriters, d.TrackMaxWait = writers, wait
		d.Limiter = ratelimit.New(ratelimit.Config{Rules: map[string]ratelimit.Rule{
			ratelimit.ClassSDKKey:         {PerSecond: 100000, Burst: 100000},
			ratelimit.ClassSDKTrack:       {PerSecond: 100000, Burst: 100000},
			ratelimit.ClassSDKTrackEvents: {PerSecond: 100, Burst: 1000},
		}, StreamsPerKey: 100})
	})
}

func TestAnEventIsNotAcknowledgedBeforeItIsStored(t *testing.T) {
	x := trackWorld(t, 4, 2*time.Second)
	h := x.h
	h.slowDown("events", "INSERT", 600*time.Millisecond)
	type answer struct {
		r  trackResp
		at time.Duration
	}
	got := make(chan answer, 1)
	start := time.Now()
	go func() {
		r := h.track(x.sdkKeyA, evt("ack-1", "purchase", "s1"))
		got <- answer{r, time.Since(start)}
	}()
	time.Sleep(250 * time.Millisecond)
	select {
	case a := <-got:
		t.Fatalf("the batch was answered after %v, before its row could have been written (the insert takes 600 ms): %s", a.at, a.r.Raw)
	default:
	}
	if n := h.count(`SELECT count(*) FROM events WHERE event_id = 'ack-1'`); n != 0 {
		t.Fatalf("the row is visible before the insert committed: %d", n)
	}
	a := <-got
	if a.r.Status != 200 || a.r.statuses()[0] != "accepted" {
		t.Fatalf("answer: %d %s", a.r.Status, a.r.Raw)
	}
	if n := h.count(`SELECT count(*) FROM events WHERE event_id = 'ack-1'`); n != 1 {
		t.Errorf("acknowledged as accepted, but %d rows are stored", n)
	}
	if a.at < 550*time.Millisecond {
		t.Errorf("answered after %v: faster than the insert", a.at)
	}
}

func TestWhenTheDatabaseIsSaturatedBatchesAreRefusedFastAndNothingIsHalfWritten(t *testing.T) {
	x := trackWorld(t, 2, 80*time.Millisecond)
	h := x.h
	h.slowDown("events", "INSERT", 500*time.Millisecond)
	const batches = 12
	type outcome struct {
		id         string
		r          trackResp
		took       time.Duration
		retryAfter string
	}
	out := make(chan outcome, batches)
	var wg sync.WaitGroup
	for i := 0; i < batches; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "bp-" + strconv.Itoa(i)
			t0 := time.Now()
			r := h.track(x.sdkKeyA, evt(id, "purchase", "s"+strconv.Itoa(i)))
			out <- outcome{id, r, time.Since(t0), r.Header.Get("Retry-After")}
		}()
	}
	wg.Wait()
	close(out)
	var accepted, busy []outcome
	for o := range out {
		switch o.r.Status {
		case 200:
			accepted = append(accepted, o)
		case 503:
			busy = append(busy, o)
		default:
			t.Fatalf("%s: unexpected status %d %s", o.id, o.r.Status, o.r.Raw)
		}
	}
	if len(accepted) < 2 || len(busy) < 4 {
		t.Fatalf("with 2 writers and an 80 ms wait: %d accepted, %d refused of %d", len(accepted), len(busy), batches)
	}
	for _, o := range busy {
		if ra, err := strconv.Atoi(o.retryAfter); err != nil || ra < 1 || ra > 3 {
			t.Errorf("%s: Retry-After %q", o.id, o.retryAfter)
		}
		if o.took > 450*time.Millisecond {
			t.Errorf("%s: refused after %v, want about the 80 ms wait, not the insert time", o.id, o.took)
		}
		if !jsonHas(o.r.Raw, "TRACK_BUSY") {
			t.Errorf("%s: %s", o.id, o.r.Raw)
		}
	}
	// exactly what was acknowledged is stored; what was refused left nothing behind
	if n := h.count(`SELECT count(*) FROM events`); n != len(accepted) {
		t.Fatalf("%d rows stored, %d batches acknowledged", n, len(accepted))
	}
	for _, o := range busy {
		if h.count(`SELECT count(*) FROM events WHERE event_id = $1`, o.id) != 0 {
			t.Errorf("%s was refused yet stored", o.id)
		}
	}
	// retrying the refused batches with the same ids (one at a time, so a turn is free) stores each exactly once
	for _, o := range busy {
		r := h.track(x.sdkKeyA, evt(o.id, "purchase", "retry"))
		if r.Status != 200 || r.statuses()[0] != "accepted" {
			t.Fatalf("retry of %s: %d %s", o.id, r.Status, r.Raw)
		}
	}
	if n := h.count(`SELECT count(*) FROM events`); n != batches {
		t.Errorf("after the retries %d rows, want %d (one per batch, none twice)", n, batches)
	}
	if st := h.router.TrackGate.Stats(); st.Refused < uint64(len(busy)) || st.InUse != 0 || st.Waiting != 0 {
		t.Errorf("gate: %+v", st)
	}
}

func jsonHas(b []byte, s string) bool { return strings.Contains(string(b), s) }

// A refused batch is not charged against the events-per-second allowance, so the retry is not charged twice.
func TestARefusedBatchIsRefundedItsRateLimitTokens(t *testing.T) {
	x := trackWorld(t, 2, 20*time.Millisecond)
	h := x.h
	h.slowDown("events", "INSERT", 900*time.Millisecond)
	// two long writers occupy every turn
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); h.track(x.sdkKeyA, evt(fmt.Sprintf("hold-%d", i), "purchase", "s")) }()
	}
	time.Sleep(200 * time.Millisecond)
	// 12 batches of 100 events: 1,200 events against a burst of 1,000 and 100 per second. Every one is refused
	// (both turns are taken), and a refusal must give its tokens back, or the limiter would start answering 429.
	big := make([]map[string]any, 0, 100)
	refused := 0
	for round := 0; round < 12; round++ {
		big = big[:0]
		for i := 0; i < 100; i++ {
			big = append(big, evt(fmt.Sprintf("big-%d-%d", round, i), "purchase", "s"))
		}
		switch r := h.track(x.sdkKeyA, big...); r.Status {
		case 503:
			refused++
		case 429:
			t.Fatalf("round %d: rate limited although every earlier batch was refused for being busy: %s", round, r.Raw)
		default:
			t.Fatalf("round %d: %d %s", round, r.Status, r.Raw)
		}
	}
	wg.Wait()
	if refused != 12 {
		t.Errorf("%d of 12 batches refused", refused)
	}
}

// With the gate off (zero wait) behaviour is what it was before: no refusals, however slow the inserts.
func TestWithoutTheGateNothingIsRefused(t *testing.T) {
	x := trackWorld(t, 0, 0)
	h := x.h
	h.slowDown("events", "INSERT", 150*time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := h.track(x.sdkKeyA, evt("ng-"+strconv.Itoa(i), "purchase", "s")); r.Status != 200 {
				t.Errorf("%d %s", r.Status, r.Raw)
			}
		}()
	}
	wg.Wait()
	if h.router.TrackGate != nil {
		t.Error("a gate exists with TrackMaxWait zero")
	}
}
