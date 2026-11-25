#!/usr/bin/env python3
"""For each migration, in order, on a LOCAL copy: run the file with its final COMMIT replaced by a query of the locks this
transaction holds and a ROLLBACK (so the locks are those held at the moment of commit), then apply the file for real.
    lockprobe.py MIGRATIONS_DIR LOCAL_DB_NAME 0004_workspaces 0006_workspace_roles_invites 0007_workspace_keeps_an_owner
"""
import re, subprocess, sys, tempfile, os
mig, db, *files = sys.argv[1:]
PB = "/opt/homebrew/opt/postgresql@16/bin/psql"; url = f"postgres://helios@127.0.0.1:55432/{db}"
Q = """SELECT c.relname, string_agg(DISTINCT l.mode, ', ' ORDER BY l.mode) FROM pg_locks l JOIN pg_class c ON c.oid = l.relation
WHERE l.pid = pg_backend_pid() AND l.locktype = 'relation' AND c.relkind = 'r' AND c.relnamespace IN ('public'::regnamespace, 'auth'::regnamespace)
GROUP BY c.relname ORDER BY 1;"""
for f in files:
    sql = open(os.path.join(mig, f + ".sql")).read()
    body = sql.split("-- ============================================================\n-- ROLLBACK")[0]
    idx = body.rfind("COMMIT;")
    probe = body[:idx] + Q + "\nROLLBACK;\n"
    with tempfile.NamedTemporaryFile("w", suffix=".sql", delete=False) as t: t.write(probe)
    r = subprocess.run([PB, url, "-At", "-v", "ON_ERROR_STOP=1", "-f", t.name], capture_output=True, text=True); os.unlink(t.name)
    print(f"== {f}: locks held at commit (table | modes)")
    lines = [l for l in r.stdout.splitlines() if "|" in l]
    for l in lines: print("   " + l.replace("|", " | "))
    if r.returncode: print("   ERROR", r.stderr[-300:]); sys.exit(1)
    r2 = subprocess.run([PB, url, "-q", "-v", "ON_ERROR_STOP=1", "-f", os.path.join(mig, f + ".sql")], capture_output=True, text=True)
    if r2.returncode: print("   real apply failed", r2.stderr[-300:]); sys.exit(1)
