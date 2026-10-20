package track

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"helios/backend/internal/platform/apikey"
	"helios/backend/internal/platform/exposuretoken"
	"helios/backend/internal/platform/httpx"
	"helios/backend/internal/platform/ratelimit"
)

type request struct {
	Events []json.RawMessage `json:"events"`
}

// Per-event statuses.
const (
	StatusAccepted  = "accepted"
	StatusDuplicate = "duplicate"
	StatusRejected  = "rejected"
)

// Result is the outcome of one event, in request order.
type Result struct {
	Index   int    `json:"index"`
	ID      string `json:"id,omitempty"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// Deps are the handler's collaborators.
type Deps struct {
	Writer      Writer
	Experiments ExperimentLookup
	// Limiter caps events per minute per key; nil means no cap.
	Limiter *ratelimit.Limiter
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Tokens verifies exposure tokens; with Mode off (or nil) none are checked.
	Tokens *exposuretoken.Keyring
	// Mode is what to do with an exposure whose token does not verify.
	Mode exposuretoken.Mode
	// Gate bounds concurrent database work and sheds load with a fast 503 when it is saturated; nil means no bound.
	Gate *Gate
}

// Handler serves POST /track. It must run behind apikey's middleware: the
// workspace and environment are taken from the key's scope and from nothing
// else.
//
// A body that cannot be parsed, is too large, or has too many events fails as
// a whole. Anything wrong with a single event is reported for that event only
// and the rest of the batch is stored.
func Handler(d Deps) http.HandlerFunc {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sc, ok := apikey.ScopeFrom(r.Context())
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "SDK key required")
			return
		}
		var req request
		if err := httpx.DecodeJSONLimit(w, r, &req, MaxBodyBytes); err != nil {
			if httpx.TooLarge(err) {
				httpx.WriteError(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE",
					fmt.Sprintf("the request body is at most %d KB", MaxBodyBytes>>10))
				return
			}
			// The decoder's message can quote the body; send a fixed text.
			httpx.BadRequest(w, `the body must be {"events":[...]} with known fields only`)
			return
		}
		if n := len(req.Events); n < 1 || n > MaxBatch {
			httpx.WriteError(w, http.StatusBadRequest, "BATCH_SIZE", fmt.Sprintf("send 1-%d events per request", MaxBatch))
			return
		}
		if d.Limiter != nil {
			if ok, wait := d.Limiter.TakeN(ratelimit.ClassSDKTrackEvents, sc.Prefix, len(req.Events)); !ok {
				ratelimit.Reject(w, wait)
				return
			}
		}

		results := make([]Result, len(req.Events))
		valid := make([]Event, 0, len(req.Events))
		at := make([]int, 0, len(req.Events)) // valid[i] was request event at[i]
		seen := map[string]bool{}
		clock := now()
		for i, raw := range req.Events {
			results[i] = Result{Index: i}
			ev, err := parse(raw, clock)
			if err != nil {
				var inv invalid
				msg := "invalid event"
				if errors.As(err, &inv) {
					msg = inv.msg
				}
				results[i].Status, results[i].Code, results[i].Message = StatusRejected, CodeInvalidEvent, msg
				continue
			}
			results[i].ID = ev.ID
			if seen[ev.ID] { // the same id twice in one batch: the first one counts
				results[i].Status = StatusDuplicate
				continue
			}
			seen[ev.ID] = true
			valid = append(valid, ev)
			at = append(at, i)
		}

		// Experiments are looked up inside the key's own workspace and
		// environment, so another tenant's experiment is indistinguishable
		// from one that does not exist.
		var expIDs []string
		for _, ev := range valid {
			if ev.ExperimentID != "" {
				expIDs = append(expIDs, ev.ExperimentID)
			}
		}
		// From here on the batch uses the database. Take a turn (bounded concurrency); if none comes within the wait,
		// say so at once: nothing was written, and the client retries the same ids. The turn is held until the INSERT
		// has returned (an event is never acknowledged before it is stored) and given back right after, not when a
		// slow client has finished reading the answer.
		releaseTurn := func() {}
		if d.Gate != nil && len(valid) > 0 {
			rel, err := d.Gate.Acquire(r.Context())
			if err != nil {
				if errors.Is(err, ErrBusy) {
					if d.Limiter != nil { // the retry must not be charged twice
						d.Limiter.Refund(ratelimit.ClassSDKTrackEvents, sc.Prefix, len(req.Events))
					}
					writeBusy(w)
				}
				return // otherwise the client left while it waited
			}
			var once sync.Once
			releaseTurn = func() { once.Do(rel) }
			defer releaseTurn()
		}
		known := map[string]Experiment{}
		if len(expIDs) > 0 {
			var err error
			if known, err = d.Experiments.Lookup(r.Context(), sc, expIDs); err != nil {
				unavailable(w, "lookup", err)
				return
			}
		}
		store := make([]Event, 0, len(valid))
		storeAt := make([]int, 0, len(valid))
		for j, ev := range valid {
			i := at[j]
			if ev.ExperimentID != "" {
				exp, found := known[ev.ExperimentID]
				switch {
				case !found:
					reject(&results[i], CodeExperimentNotFound, "no such experiment in this environment")
					continue
				case !exp.Running:
					reject(&results[i], CodeExperimentNotRun, "the experiment is not running")
					continue
				case ev.Name == ExposureEvent && !exp.Variations[ev.VariationID]:
					reject(&results[i], CodeInvalidVariation, "variationId is not a variation of the experiment's flag")
					continue
				}
				if ev.Name == ExposureEvent && d.Tokens != nil && d.Mode != exposuretoken.ModeOff {
					code := checkToken(d.Tokens, sc, ev, clock)
					ok := code == ""
					valid[j].Verified = &ok
					if !ok && d.Mode == exposuretoken.ModeRequired {
						reject(&results[i], code, exposureMessages[code])
						continue
					}
					ev = valid[j]
				}
			}
			store = append(store, ev)
			storeAt = append(storeAt, i)
		}

		if len(store) > 0 {
			inserted, err := d.Writer.Insert(r.Context(), sc, store)
			releaseTurn()
			if err != nil {
				unavailable(w, "insert", err)
				return
			}
			for k, ev := range store {
				if inserted[ev.ID] {
					results[storeAt[k]].Status = StatusAccepted
				} else {
					results[storeAt[k]].Status = StatusDuplicate
				}
			}
		}

		releaseTurn() // (a batch with nothing to store reached here without an insert)
		var accepted, duplicates, rejected int
		for _, res := range results {
			switch res.Status {
			case StatusAccepted:
				accepted++
			case StatusDuplicate:
				duplicates++
			default:
				rejected++
			}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"results": results, "accepted": accepted, "duplicates": duplicates, "rejected": rejected,
		})
	}
}

func reject(r *Result, code, msg string) { r.Status, r.Code, r.Message = StatusRejected, code, msg }

// unavailable answers 503 and logs only where it failed and the Postgres error
// code: never the error text, which could carry a value from the batch.
func unavailable(w http.ResponseWriter, step string, err error) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		log.Printf("track: %s failed: sqlstate %s constraint %q", step, pg.Code, pg.ConstraintName)
	} else {
		log.Printf("track: %s failed: %T", step, err)
	}
	w.Header().Set("Retry-After", retryAfterSeconds())
	httpx.WriteError(w, http.StatusServiceUnavailable, "TRACK_UNAVAILABLE", "events could not be stored; retry with the same ids")
}

// exposureMessages are fixed: they never quote the token or anything else sent.
var exposureMessages = map[string]string{
	CodeExposureTokenMissing: "this exposure needs the exposureToken that /evaluate returned for it",
	CodeExposureTokenInvalid: "the exposureToken does not match this exposure",
	CodeExposureTokenExpired: "the exposureToken has expired; evaluate again",
}

// checkToken returns "" when the exposure's token verifies for the key's own
// workspace and environment and the event's experiment, variation and subject,
// else the code to report.
func checkToken(k *exposuretoken.Keyring, sc apikey.Scope, ev Event, now time.Time) string {
	if ev.ExposureToken == "" {
		return CodeExposureTokenMissing
	}
	err := k.Verify(ev.ExposureToken, exposuretoken.Binding{
		Workspace: sc.WorkspaceID, Environment: sc.EnvironmentID, Experiment: ev.ExperimentID,
		Variation: ev.VariationID, Subject: ev.SubjectKey,
	}, now)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, exposuretoken.ErrExpired):
		return CodeExposureTokenExpired
	default:
		return CodeExposureTokenInvalid
	}
}
