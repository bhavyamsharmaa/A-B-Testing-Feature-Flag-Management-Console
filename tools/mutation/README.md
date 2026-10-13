# Mutation checks

Evidence that the protections are tested: for each one, `run.py` breaks the code, runs the
tests that should notice, and expects a failure (then puts the file back). Results of the last
full run are in [RESULTS.md](RESULTS.md).

**Run it:** `python3 tools/mutation/run.py all --results tools/mutation/RESULTS.md`
(Python 3, no packages). Takes about 15-20 minutes in all: `backend` ~6 minutes, `frontend`
~1 minute, `ui` ~2 minutes (more on a slow machine). `--only <word>` runs just matching entries.

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
