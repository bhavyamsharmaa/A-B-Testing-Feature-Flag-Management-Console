# Experiment results API

`GET /environments/{envId}/experiments/{key}/results[?metrics=<event>:<kind>,...]`
(viewer and above; see `api/openapi.yaml`, schema `ExperimentResults`). It computes
everything from the data `POST /track` collects (see `docs/EVENTS.md`). It declares
no winner and gives no recommendation.

## Which metric

No migration was needed. An experiment already stores metrics
(`experiment_metrics`: a name, an event name, a type `conversion` or `value`, one
primary). The request therefore either takes the **primary metric** (default) or
names metrics as `<eventName>:<kind>` with kind `conversion`, `count` or `value`.
`count` has no stored definition (the enum has two values) and an ad hoc metric is
cheaper than a schema change; an event name the experiment does not define must
exist among that experiment's events (400 otherwise), and at most 3 metrics are
allowed per request.

| kind | per-subject number |
|---|---|
| conversion | 1 if the subject has at least one matching event, else 0 |
| count | number of matching events |
| value | sum of the matching events' `value` |

Subjects with no matching event count as 0 (they are in the denominator).

## Attribution

1. **Denominator:** subjects with an `$exposure` event for this experiment, grouped
   by the variation in that exposure. The unique index guarantees one exposure per
   subject, so that variation is the subject's assignment.
2. **Numerator:** only events (a) with this experiment's id, (b) from a subject that
   has an exposure, (c) with `occurred_at` at or after that subject's own exposure
   `occurred_at`, and (d) inside the run window: `started_at` to `stopped_at`, or
   now while running.
3. Excluded events are counted, not hidden: `EVENTS_WITHOUT_EXPOSURE`,
   `EVENTS_BEFORE_EXPOSURE` and `EVENTS_OUTSIDE_WINDOW` warnings carry the numbers.
4. Events that were sent without an `experimentId` are **not used**: they are not
   attached to any experiment (see "Decisions" below).
5. Two clocks are stored per event: the client's (`occurred_at`, validated at
   ingestion to at most 5 minutes ahead and 90 days back) and the server's receipt
   time (`created_at`). How they are used is the clock rule below.

Nothing is read into Go row by row: one SQL statement per metric returns counts,
sums and sums of squares per variation (`results.MetricSumsSQL`).

## Clock skew: the rule

Client clocks are wrong more often than servers' (a device set to the wrong time, a
server SDK and a browser disagreeing, an offline device replaying old events). Before
this rule attribution used client time for both the run window and the order, so a
client whose clock was two hours slow lost every conversion (all of its stamps fell
before the experiment's start), and one stamped ahead lost the events that followed
its exposure. Now:

1. **Run window: server receipt time only.** An event counts only if `created_at`
   (when the batch was stored) is between `started_at` and `stopped_at` (or now).
   `/track` already refuses events for experiments that are not running, so this is
   almost always true; what it removes is client time as a way to push an event into
   or out of the run. An event received after the stop is out, whatever its stamp.
2. **Order against the exposure: client times, after clamping each into
   `[started_at, received_at]`.** `eff(t) = LEAST(GREATEST(t, started_at), created_at)`.
   An event cannot have happened after it was received, nor an exposure before the
   experiment existed. The event counts when `eff(event) > eff(exposure)`, or they are
   equal and the **raw** client times say event >= exposure (so a client whose
   clock is 60 days off still orders its own events correctly), or both came in the
   same request and the event is at most one second before the exposure (a batch is
   one moment, and stamps within it race).
3. Nothing is silent: events excluded as "before the exposure" and "outside the
   window" are counted in warnings, and `CLIENT_CLOCK_ADJUSTED` counts events that
   were attributed but whose client stamp was more than a minute outside
   `[started_at, received_at]` (a sign of clients with clock problems).

Why not the alternatives: using receipt time for the order too would break offline
clients (a late event would look later than a fresh exposure); trusting client time
for everything is the old behaviour; adding a tolerance band (say 5 minutes) was
rejected because it misattributes real events by the same amount while not helping
the skews that matter (hours).

Cases, each with a test in `TestResultsClockSkew` using fixed offsets:

| case | before | now |
|---|---|---|
| client two hours slow, data received during the run | lost | counted |
| exposure stamped 4 minutes ahead by one clock, event by an accurate one | lost | counted |
| exposure and event in one batch, event 0.5 s earlier / 10 s earlier | lost / lost | counted / not |
| offline: event truly before the exposure, sent late / truly after, sent late | not / counted | not / counted |
| both stamped 60 days in the past, event a day after / a day before | lost / lost | counted / not |
| event stamped nine days ahead, received three hours in | lost | counted |
| event stamped inside the run, received after the stop | counted | not |
| event stamped after the exposure but received before it | counted | not |

Known limits: when a client's clock runs ahead and an event genuinely a few seconds
before the exposure is received after it, it is counted (the ahead stamp was clamped
down to the receipt time); and if both stamps fall before the experiment started, the
order rests on the raw client times alone. Phase 2's golden vectors and simulations
test the statistics from counts and are unaffected by this rule (re-run, unchanged).

## Control

The **first variation of the flag** (the flag's own variation order). There is no
designated-control field. Every other variation is compared with it.

## Statistics

Libraries: `gonum.org/v1/gonum/stat/distuv` for the normal, Student's t and
chi-square distributions. No CDF is hand-written.

Let `alpha = 0.05` be the family-wise level and `m` the number of comparisons (the
variations other than the control). Each comparison is tested at `alpha/m`
(**Bonferroni**); `adjustedPValue = min(1, pValue * m)`; `significant` means
`adjustedPValue < 0.05`; the intervals use confidence `1 - alpha/m` (95% when
`m = 1`, 97.5% for two comparisons).

**Conversion metrics** (`p1`, `n1` control; `p2`, `n2` variant):

| output | method |
|---|---|
| p-value | pooled two-proportion **z-test**, two-sided. `z = (p2-p1)/sqrt(p(1-p)(1/n1+1/n2))`, `p` = pooled rate |
| interval on the difference | unpooled **Wald** interval: `(p2-p1) +/- z_crit * sqrt(p1(1-p1)/n1 + p2(1-p2)/n2)` |
| relative lift | `p2/p1 - 1` |
| interval on the relative lift | **delta method on the log rate ratio** (Katz): `SE(ln r) = sqrt((1-p1)/x1 + (1-p2)/x2)`, then `exp(ln r +/- z_crit * SE) - 1` |

**Count and value metrics** (per-subject mean and sample variance from `n`, sum and
sum of squares):

| output | method |
|---|---|
| statistic, p-value | **Welch's** unequal-variance t-test, two-sided, **Welch-Satterthwaite** degrees of freedom, Student's t |
| interval on the difference of means | `d +/- t_crit(df) * sqrt(v1/n1 + v2/n2)` |
| relative lift | `m2/m1 - 1`, only for a positive control mean |
| interval on the relative lift | **first-order delta method** on the ratio `r = m2/m1`: `Var(r) ~ (v2/n2 + r^2 v1/n1)/m1^2`, with the same `t_crit` |

Approximations to be aware of: the z-test and the Wald/Katz intervals are
large-sample (poor for tiny counts; the minimum-data rule below keeps them out of
that range); Welch's test assumes the sampling distribution of the mean is close to
normal, which for skewed revenue-like metrics needs a few thousand subjects per
arm; the lift intervals are approximations, not exact (Fieller) intervals.

**Sample ratio mismatch (SRM):** Pearson **chi-square goodness-of-fit** of observed
exposures per variation against the expected split. The expected split is the
`rollout` recorded in `start_config` when the experiment started. Confidence is
`high` only when a rollout was recorded and no targeting rule existed at start;
otherwise it is `low` (rules can send subjects to a variation regardless of the
rollout; with no recorded rollout an equal split is assumed). `p < 0.001` sets
`detected`, which marks the whole response, every variation and every comparison
`trustworthy: false` and raises `SRM_FAILED`. Exposure in a variation whose
expected share is 0 is a definite mismatch.

**Minimum data** (constants in `results/model.go`): at least 100 exposures in each
variation of a comparison, and for a conversion metric at least 25 conversions in
total. Below that a comparison is `insufficient_data`: the descriptive numbers
(difference, lift) stay, every p-value and interval is null.

**Unavailable numbers** are `null` with a reason (`reasons`, `statusReasons`):
`ZERO_VARIANCE`, `TOO_FEW_SUBJECTS`, `CONTROL_RATE_ZERO`, `VARIANT_RATE_ZERO`,
`CONTROL_MEAN_NOT_POSITIVE`, `CONTROL_HAS_NO_EXPOSURES`,
`VARIANT_HAS_NO_EXPOSURES`. A comparison with such a gap is `invalid`. A single
variation yields no comparisons. NaN and Infinity are never produced.

**No peeking claims:** the p-values and intervals are valid only for a sample size
fixed in advance. Reading them repeatedly while the experiment runs and stopping
when something looks significant inflates the false-positive rate. The response
carries `peekingNote` and `sampleSize` (total exposures at this reading). No
sequential testing is implemented.

## Response fields clients should use

* `confidenceLevel`, `alphaPerComparison`, `comparisonCount` at the top level: one
  level for every interval in the response (95% with one comparison, wider with more).
  The same fields on each comparison are **deprecated** and kept for compatibility.
* `metrics[].status` (`ok`, `insufficient_data`, `unavailable`) and
  `metrics[].statusReasons`: say "insufficient data" once per metric, not for every
  comparison. A comparison still carries its own `status`.
* `peekingNoteCode` (`FIXED_SAMPLE_SIZE`) is the stable identifier of the
  fixed-sample-size caveat; `peekingNote` is its default English text. The sample size
  is `sampleSize`.
* `warnings[].detail` is the contract: structured facts (counts, dates, reason codes)
  and always an object. `warnings[].message` is an English convenience built from it;
  do not parse it.
* `GET .../event-counts` lists `eventNames[].kinds`: the metric kinds the data supports
  for each event (`value` only when some event of that name carries a value).
* `EXPOSURES_UNVERIFIED` (exposure tokens in report mode) and `CLIENT_CLOCK_ADJUSTED`
  are warnings with `detail.exposures` / `detail.events`.

## Warnings

`NO_EXPOSURES`, `SRM_FAILED`, `SRM_LOW_CONFIDENCE`, `INSUFFICIENT_DATA`,
`EVENTS_WITHOUT_EXPOSURE`, `EVENTS_BEFORE_EXPOSURE`, `EVENTS_OUTSIDE_WINDOW`,
`FLAG_CONFIG_CHANGED` (from the audit log: date, and which of `targetingRules`,
`rollout`, `fallthroughVariationId`, `enabled` changed; a pure enable/disable or
kill is `info`), `EXPERIMENT_RESTARTED` (another experiment ran on the same flag
and environment; stopping is terminal, so a "restart" is a new experiment),
`SHORT_RUN` (window under 24 hours), `SINGLE_VARIATION`.

## Cost and limits

* One read-only transaction (repeatable read) per request, `statement_timeout` 5 s
  (503 `RESULTS_TIMEOUT` if exceeded).
* A 5-second in-process cache keyed by experiment id and metric list; the key is
  consulted only after the route guard and the lookup tied the experiment to the
  caller's workspace and environment.
* Per-user rate limit, class `results`: 1 request/s, burst 20 (429 + `Retry-After`).
* At most 3 metrics per request.
* Measured locally on 940,000 events in one experiment (250,000 exposed subjects):
  the aggregation takes about 240 ms, uses `idx_events_results` (bitmap index scans,
  no sequential scan), and the endpoint answers in 130 ms for one metric, about
  640 ms for three; a repeat within 5 s is served from the cache in about 1 ms.
  It scales with the experiment's own rows, not the whole table. Reproduce with
  `HELIOS_RESULTS_PERF=1 go test -v -run ResultsPerformance ./internal/integration`.

## How the numbers were checked

* **Golden vectors** (`results/golden_vectors_test.go`) from Python 3
  (scipy 1.18.1, statsmodels 0.15.0, numpy) in a temporary virtualenv outside the
  repository: 8 conversion cases (equal and unequal sizes, zero conversions in
  either arm, 1,000,000 subjects per arm, a 97.5% level, all-zero), 6 Welch cases
  (equal and unequal sizes and variances, Poisson counts, zero-inflated skewed
  values, a 98.3% level, n = 8 and 11) and 7 chi-square cases. Welch is checked
  against `scipy.stats.ttest_ind(equal_var=False)` on the raw arrays; proportions
  against `statsmodels` `proportions_ztest` and `confint_proportions_2indep`.
* **Simulation** (`results/simulation_test.go`, fixed seeds, runs with the normal
  tests; `go test -v -run Simulation ./internal/controlplane/results`):

  | scenario | result |
  |---|---|
  | A/A conversion, 10% vs 10%, 20,000 per arm, 200 experiments | 14 false positives = 7.0% |
  | the same, 5,000 experiments | 259 = **5.18%** (alpha 5%) |
  | A/A/A (two comparisons, Bonferroni), 400 experiments | family-wise 4.0% |
  | A/A value metric (zero-inflated exponential), 200 experiments | 4.5% |
  | 10% vs 12%, 20,000 per arm, 200 experiments | detected in 200 of 200 (100%); the 95% interval covered the true +2 points in 96.5% |
  | SRM: 50/50 rollout delivering 20,000 vs 21,000 | p = 7.9e-07, detected, every variation untrustworthy |
  | SRM on 1,000 correct 50/50 splits of 40,000 | 0 alarms at p < 0.001 |

  200 runs have a standard error of about 1.5 points, so 7.0% is within noise of 5%;
  the 5,000-run line is the precise one.

## Decisions to be aware of

* Events sent without an `experimentId` are not attributed (the join uses the
  experiment id and the `idx_events_results` / `idx_events_subject` indexes). SDKs
  must pass the experiment id on `Track` (see `docs/SDK_TRACKING.md`).
* The control is the first variation; there is no way to designate another.
* The `value` kind includes zeros for subjects without events, so it is a revenue
  per exposed subject, not per buyer.
* Intervals use the Bonferroni-adjusted confidence level, so with three or more
  variations they are wider than a plain 95% interval.

## The console's Results tab

`/console/experiments/{key}/results` (a Data and a Results tab per experiment; the
list at `/console/experiments` links to both). Read-only for every role.

* **Honest wording.** Statuses are "Significant at this sample size", "Not
  significant", "Insufficient data", "Not available" and "Not reliable (sample
  ratio mismatch)"; never "winner", and no success colour. While an experiment runs,
  "significant" carries "Not a verdict". Each status has its own shape (diamond,
  circle, dashed circle, triangle, dotted square) as well as text.
* **Warnings first**, most serious first (sample ratio mismatch, flag change with
  its date and what changed, insufficient data, dropped events, another experiment on
  the flag, short run, single variation), each with what it is and what to do. A
  sample ratio mismatch is a blocking banner and the numbers below are dimmed with
  an explanation.
* **The fixed-sample-size note and the sample size** sit directly above the first
  metric.
* **Missing values** are an em dash with the reason in words (tooltip, screen-reader
  text, and a visible line under the block). Nothing can render NaN, Infinity, null
  or undefined: `src/lib/resultsFormat.ts` formats everything, and
  `e2e/unit/results.spec.tsx` renders the view with every nullable field null and
  non-finite numbers injected and fails if such text appears.
* **Metrics:** up to three, chosen from the event names `event-counts` lists plus
  the experiment's defined metrics; the selection lives in the address
  (`?metrics=purchase:count`).
* **Polling:** every 7 s while the experiment runs and the tab is visible; paused when
  hidden and read again at once on return; a stopped experiment is read once; a 429
  slows it down (Retry-After, which the API now exposes to the browser through CORS)
  and keeps the last numbers on screen; any change of workspace, environment,
  experiment or metrics aborts the request and clears what was shown.
