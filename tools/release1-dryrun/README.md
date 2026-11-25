# Release 1 dry run (local only)

Everything the release 1 rehearsal used, so it can be repeated on a real dump or on the synthetic one. **Nothing here touches production,
Supabase, Render, Vercel or Upstash**; every database is on the local throwaway server (127.0.0.1:55432) and every script refuses anything else.
Never put a dump in the repository: `backup_db.sh` writes it to the git-ignored `backend/backups/` of the checkout you ran it from.

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

To redo it (paths are examples; build the binaries from the commits named in the runbook):

```bash
D=~/helios-local/dryrun                                   # binaries: old-api, old-mkkey (from 6c90844), new-api, new-devauth (from 1e2f5e6)
tools/release1-dryrun/build_synthetic.sh $D/old-main/backend/db/migrations dryrun_src      # or use your own dump instead
# mint SDK keys with the old code into dryrun_src, put them in $D/work/keys.env as KEY_PRODUCTION= / KEY_DEV= / KEY_STAGING=
PGSERVICEFILE=$D/pg_service.conf PGSERVICE=helios_prod ~/helios-release1/backend/scripts/backup_db.sh   # a service named helios_prod pointing at dryrun_src
tools/release1-dryrun/prep_migrated.sh <DUMP> helios_new ~/helios-release1
# stack: new-devauth on :54321, new-api on :8080 (production-style variables, dummy values), vite from ~/helios-release1/frontend on :5173
cp tools/release1-dryrun/e2e/*.spec.ts ~/helios-release1/frontend/e2e/ && (cd ~/helios-release1/frontend && HELIOS_KEYS=$D/work/keys.env npx playwright test e2e/runbook-clickthrough.spec.ts)
```
