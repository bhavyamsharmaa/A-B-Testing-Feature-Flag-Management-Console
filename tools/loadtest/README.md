# Load test (local only)

A small load generator for the data plane: `POST /evaluate` (the hot path), `POST /track` (batches), many concurrent
`GET /sdk/stream` connections with flag edits flowing to them, and all of it at once. Standard library only; no paid
account; **it refuses any target that is not loopback**.

```bash
backend/scripts/local_stack.sh start         # Postgres and Redis (and the normal dev stack)
tools/loadtest/run.sh up                      # a SEPARATE API on :8081: own database (helios_load, rebuilt empty), own devauth
                                              # on :54322, rate limits off, pprof on :6061, built from the tree you have checked out
tools/loadtest/run.sh seed                    # the workspace under test (50 flags: the per-workspace limit), 2 segments, 5 running
                                              # experiments, an SDK key, plus 20 filler workspaces of 50 flags for database volume
tools/loadtest/run.sh all -duration 20s       # evaluate, track, stream, mixed -> one table
tools/loadtest/run.sh run -scenario evaluate -c 64 -duration 30s -profile ~/helios-local/loadtest/prof -out /abs/path/out.json
tools/loadtest/run.sh down
```

Flags: `-c` workers (default 32), `-streams` (500), `-batch` events per `/track` request (25), `-keys` flag keys per
`/evaluate` (8), `-duration`, `-label`, `-out` (JSON, give an absolute path), `-profile DIR` (CPU, heap and goroutine
profiles from the API's pprof listener, read with `go tool pprof -top FILE`).

What it records per scenario: requests/s (and events/s for `/track`), p50/p95/p99/max latency, error rate by status, the API
process's RSS and CPU (`ps`), the database's connection count (`psql` on `pg_stat_activity`) and the API's goroutine
count (pprof). For streams it also times each flag edit's arrival at the open streams. The seed state (an SDK key and a
session token) is written to `~/helios-local/loadtest-state.json`, mode 600, and the tool refuses a state path inside the repository.

The generator runs on the same machine as the API and Postgres, so **the numbers are relative**: compare runs on
the same machine with the same seed, not against other machines. `docs/PERFORMANCE.md` has the baseline and what changed.
`results/baseline.json` holds the raw baseline run.
