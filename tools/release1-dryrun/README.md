# Release 1 dry run (local only)

Everything the release 1 rehearsal used, so it can be repeated on a real dump or on the synthetic one. **Nothing here touches production,
Supabase, Render, Vercel or Upstash**; the rehearsal database is on the local throwaway server (127.0.0.1:55432) and every restore script refuses anything else.
Keep every dump outside the repository. Do not use `backend/scripts/backup_db.sh` for this: it writes to the git-ignored `backend/backups/` inside the checkout.

| file | what |
|---|---|
| `synth_old_prod.sql`, `build_synthetic.sh` | a SYNTHETIC "old production" at the schema of `main` (6c90844), when no real dump is available |
| `restore_copy.sh <DUMP> <DB>` | the runbook's Step 3 as a script |
| `synthetic_logins.sql`, `prep_migrated.sh <DUMP> <DB> <CHECKOUT>` | restore, give the synthetic people logins, apply 0004, 0006, 0007 |
| `lockprobe.py` | the locks each migration holds at commit, then applies it |
| `probe.py`, `analyze_probe.py` | keep a backend busy while migrations run; slowest and failed requests |
| `oldflows.py` | the OLD backend's console and SDK flows (baseline, and "old backend on the migrated database") |
| `streamoverlap.py` | does a flag change cross between an old and a new instance on `/sdk/stream`? |
| `e2e/runbook-clickthrough.spec.ts`, `e2e/lockstep.spec.ts` | the runbook's Step 9 as Playwright, and the mismatched-release checks |

To redo it, from the root of a checkout of `release/1` (paths are examples; build the old binaries from `6c90844`, the new ones from this branch):

```bash
D=~/helios-local/dryrun     # binaries: old-api, old-mkkey (from 6c90844), new-api, new-devauth (this branch); plus a checkout of 6c90844 in $D/old-main
tools/release1-dryrun/build_synthetic.sh $D/old-main/backend/db/migrations dryrun_src      # or use your own dump instead
# mint SDK keys with the old code into dryrun_src, put them in $D/work/keys.env as KEY_PRODUCTION= / KEY_DEV= / KEY_STAGING= (staging revoked)
# apply synthetic_logins.sql to dryrun_src, and describe it as a service named helios_prod in a pg_service.conf of its own
mkdir -p "$D/work" && chmod 700 "$D/work"
umask 077
PGSERVICEFILE=$D/pg_service.conf env -u DATABASE_URL -u PGHOST -u PGPORT -u PGUSER -u PGPASSWORD -u PGDATABASE pg_dump \
  --dbname "service=helios_prod" --schema=public --format=custom --no-owner --no-privileges \
  --file "$D/work/rehearsal.dump"
tools/release1-dryrun/prep_migrated.sh "$D/work/rehearsal.dump" helios_new "$PWD"
# stack: new-devauth on :54321, new-api on :8080 (with REDIS_URL and the runbook's variables, dummy values), vite from frontend/ on :5173
cp tools/release1-dryrun/e2e/*.spec.ts frontend/e2e/ && (cd frontend && HELIOS_KEYS=$D/work/keys.env npx playwright test e2e/runbook-clickthrough.spec.ts)
# then remove the two copied specs from frontend/e2e/
```

The click-through signs people up and so makes a second workspace: run it on a fresh copy each time.
