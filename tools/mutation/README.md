# Mutation checks

Evidence that the protections are tested: for each one, `run.py` breaks the code, runs the
tests that should notice, and expects a failure (then puts the file back). Results of the last
full run are in [RESULTS.md](RESULTS.md).

**Run it:** `python3 tools/mutation/run.py all --results tools/mutation/RESULTS.md`
(Python 3, no packages). The full run of 55 mutations took about 2.5 minutes on a laptop with warm
Go and Playwright caches (a cold first run takes longer, since each backend mutation recompiles).
`--only <word>` runs just matching entries.

Needs, all on this machine (anything else is refused):

* `backend`: `HELIOS_TEST_DATABASE_URL` (a local Postgres it may create databases on) and, for the
  Redis entries, `HELIOS_TEST_REDIS_URL`; see `docs/LOCAL_MULTITENANCY.md`.
* `frontend`: nothing but `npm install` in `frontend/`.
* `ui`: the local stack (backend on :8080, devauth on :54321, the console on :5173, a database with
  every migration), as for `npm run e2e`.

It refuses to run if a file it edits has uncommitted changes, and restores each file even if a
test run crashes. A mutation that only stops the code compiling is reported as `BROKEN`, not counted.
Add an entry to `mutations.py` whenever you add a protection that deserves proof. The exit status is
non-zero if anything survived.

**Equivalent mutants.** One idea was dropped because it cannot be observed: removing the
`f.workspace_id = fc.workspace_id` condition from the rollback-check query changes nothing, since the
composite foreign keys already make a flag configuration and its flag belong to one workspace. The
first full run reported it as SURVIVED; it is defence in depth, not a gap in the tests.
