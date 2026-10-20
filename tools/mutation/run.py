#!/usr/bin/env python3
"""Mutation checks: prove each protection is tested by breaking it and watching a test fail.

For every entry in mutations.py the script edits one source file, runs the tests that should
notice, records CAUGHT (a test failed) or SURVIVED (all passed: a gap), and always puts the file
back. A mutation that merely stops the code compiling is reported as BROKEN and is not counted as
caught: it proves nothing. Exit status is 1 if any mutation survived or was broken.

    python3 tools/mutation/run.py backend            # Go mutations (needs local Postgres + Redis, see below)
    python3 tools/mutation/run.py frontend           # logic mutations, Playwright unit spec (no stack needed)
    python3 tools/mutation/run.py ui                 # browser mutations (needs the local stack, see README)
    python3 tools/mutation/run.py all --results tools/mutation/RESULTS.md
    python3 tools/mutation/run.py backend --only quota --only regex     # entries whose label contains a word
    python3 tools/mutation/run.py backend --parallel 4               # the backend entries in 4 worktrees at once (see below)
    python3 tools/mutation/run.py backend --shard 2/4                # one slice of the backend entries (what --parallel runs)

Environment (local throwaway services only; both are refused if they are not on this machine):
    HELIOS_TEST_DATABASE_URL   e.g. postgres://helios@127.0.0.1:55432/postgres?sslmode=disable
    HELIOS_TEST_REDIS_URL      e.g. redis://127.0.0.1:56379/0   (optional; the Redis test is skipped without it)

PARALLEL RUNS. A mutation edits a file in place, so two cannot run in one working tree. `--parallel K` makes K git
worktrees of the committed HEAD in ${HELIOS_LOCAL_DIR:-~/helios-local}/mutation-worktrees (outside the repository), runs one slice of
the backend entries in each (`--shard i/K`), and merges the answers; each test run makes its own database, so they do not collide.
The browser (`ui`, `frontend`) entries share one running stack and one set of screenshots and stay serial.

It refuses to start if a file it would edit has uncommitted changes, so a crash can never lose work
(and `git checkout -- <file>` always restores).
"""
import argparse, datetime, os, subprocess, sys
from urllib.parse import urlparse

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, '..', '..'))
sys.path.insert(0, HERE)
import mutations  # noqa: E402


def local_only(name, url, required):
    if not url:
        if required:
            sys.exit(f'{name} is not set (see the docstring of {os.path.relpath(__file__, ROOT)})')
        return
    host = urlparse(url).hostname
    if host not in ('127.0.0.1', 'localhost', '::1'):
        sys.exit(f'{name} must point at this machine, got host {host!r}; refusing')


def dirty(files):
    out = subprocess.run(['git', 'status', '--porcelain', '--'] + sorted(files), cwd=ROOT, capture_output=True, text=True).stdout
    return [l for l in out.splitlines() if l.strip()]


def apply(path, old, new, check):
    """old/new may be lists of equal length: several edits, all of which must match. path may be a list
    too (one entry per edit, the same file may repeat): an edit that spans files."""
    olds, news = (old, new) if isinstance(old, (list, tuple)) else ([old], [new])
    paths = path if isinstance(path, (list, tuple)) else [path] * len(olds)
    originals, mutated = {}, {}
    for p in set(paths):
        full = os.path.join(ROOT, p)
        with open(full) as f:
            originals[p] = mutated[p] = f.read()
    for p, o, n in zip(paths, olds, news):
        if o not in mutated[p]:
            return None, 'PATTERN NOT FOUND (the code changed; update mutations.py)'
        mutated[p] = mutated[p].replace(o, n, 1)
    try:
        for p, text in mutated.items():
            with open(os.path.join(ROOT, p), 'w') as f:
                f.write(text)
        return originals, check()
    finally:
        for p, text in originals.items():
            with open(os.path.join(ROOT, p), 'w') as f:
                f.write(text)


def classify(proc, output):
    broken = ('build failed' in output) or ('[setup failed]' in output) or ('SyntaxError' in output) or ('error TS' in output)
    if broken:
        return 'BROKEN', 'the mutation does not compile; not counted'
    if proc.returncode != 0:
        fails = [l.strip() for l in output.splitlines() if l.startswith('--- FAIL') or 'Error:' in l]
        return 'CAUGHT', (fails[0] if fails else 'tests failed')[:110]
    return 'SURVIVED', 'all tests passed: this protection is not tested'


def run_backend(only, shard=None):
    env = dict(os.environ)
    local_only('HELIOS_TEST_DATABASE_URL', env.get('HELIOS_TEST_DATABASE_URL'), True)
    local_only('HELIOS_TEST_REDIS_URL', env.get('HELIOS_TEST_REDIS_URL'), False)
    rows = []
    for index, (label, path, old, new, pkgs, pattern) in enumerate(mutations.BACKEND):
        if only and not any(o in label for o in only):
            continue
        if shard and index % shard[1] != shard[0] - 1:
            continue
        def check():
            # packages of the backend module run from backend/; '../tools/<name>' is a separate module (its own go.mod),
            # run from its own directory. The first run that fails the tests (or does not compile) decides.
            names = pkgs.split()
            runs = [(os.path.join(ROOT, 'backend'), [x for x in names if not x.startswith('../tools/')])]
            runs += [(os.path.join(ROOT, 'tools', x.split('/')[-1]), ['./...']) for x in names if x.startswith('../tools/')]
            res = ('SURVIVED', 'all tests passed: this protection is not tested')
            for cwd, args in runs:
                if not args:
                    continue
                p = subprocess.run(['go', 'test', '-count=1', '-run', pattern] + args, cwd=cwd, env=env,
                                   capture_output=True, text=True, timeout=900)
                res = classify(p, p.stdout + p.stderr)
                if res[0] != 'SURVIVED':
                    return res
            return res
        _, res = apply(path, old, new, check)
        rows.append(('backend', label) + (res if isinstance(res, tuple) else ('NOT RUN', res)))
        print(' | '.join(rows[-1]), flush=True)
    return rows


def run_playwright(kind, entries, only, extra):
    rows = []
    for e in entries:
        label, path, old, new = e[:4]
        if only and not any(o in label for o in only):
            continue
        def check():
            args = ['npx', 'playwright', 'test'] + extra(e) + ['--reporter=line']
            p = subprocess.run(args, cwd=os.path.join(ROOT, 'frontend'), capture_output=True, text=True, timeout=900)
            return classify(p, p.stdout + p.stderr)
        _, res = apply(path, old, new, check)
        rows.append((kind, label) + (res if isinstance(res, tuple) else ('NOT RUN', res)))
        print(' | '.join(rows[-1]), flush=True)
    return rows


def run_parallel(k, only):
    """The backend entries, k at a time: one git worktree of HEAD per slice, each running `run.py backend --shard i/k`."""
    base = os.path.join(os.environ.get('HELIOS_LOCAL_DIR', os.path.expanduser('~/helios-local')), 'mutation-worktrees')
    if os.path.abspath(base).startswith(ROOT + os.sep):
        sys.exit('the worktree directory is inside the repository; refusing')
    os.makedirs(base, exist_ok=True)
    procs = []
    for i in range(1, k + 1):
        wt = os.path.join(base, f'wt{i}')
        subprocess.run(['git', 'worktree', 'remove', '--force', wt], cwd=ROOT, capture_output=True)
        subprocess.run(['git', 'worktree', 'prune'], cwd=ROOT, capture_output=True)
        r = subprocess.run(['git', 'worktree', 'add', '--detach', wt, 'HEAD'], cwd=ROOT, capture_output=True, text=True)
        if r.returncode != 0:
            sys.exit('git worktree add failed: ' + r.stderr)
        cmd = [sys.executable, os.path.join(wt, 'tools', 'mutation', 'run.py'), 'backend', '--shard', f'{i}/{k}']
        for o in only:
            cmd += ['--only', o]
        procs.append((wt, subprocess.Popen(cmd, cwd=wt, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, env=dict(os.environ))))
    rows = []
    for wt, p in procs:
        out, _ = p.communicate()
        for line in out.splitlines():
            parts = [x.strip() for x in line.split(' | ')]
            if len(parts) >= 4 and parts[0] in ('backend',):
                rows.append((parts[0], parts[1], parts[2], ' | '.join(parts[3:])))
            elif line.strip() and 'mutations caught' not in line and not line.startswith('refusing'):
                print(line, flush=True)
            elif line.startswith('refusing'):
                print(wt + ': ' + line, flush=True)
        subprocess.run(['git', 'worktree', 'remove', '--force', wt], cwd=ROOT, capture_output=True)
    order = {m[0]: i for i, m in enumerate(mutations.BACKEND)}
    rows.sort(key=lambda r: order.get(r[1], 1 << 30))
    for r in rows:
        print(' | '.join(r), flush=True)
    return rows


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('which', choices=['backend', 'frontend', 'ui', 'all'])
    ap.add_argument('--only', action='append', default=[], help='run entries whose label contains this text (repeatable)')
    ap.add_argument('--results', help='write a markdown summary to this file')
    ap.add_argument('--shard', help='i/k: run only every k-th backend entry, starting at the i-th (1-based)')
    ap.add_argument('--parallel', type=int, default=0, help='run the backend entries in this many git worktrees at once')
    a = ap.parse_args()
    shard = None
    if a.shard:
        i, k = a.shard.split('/')
        shard = (int(i), int(k))

    files = set()
    if a.which in ('backend', 'all'):
        files |= {p for m in mutations.BACKEND for p in (m[1] if isinstance(m[1], (list, tuple)) else [m[1]])}
    if a.which in ('frontend', 'all'):
        files |= {m[1] for m in mutations.FRONTEND}
    if a.which in ('ui', 'all'):
        files |= {m[1] for m in mutations.UI}
    d = dirty(files)
    if d:
        sys.exit('refusing to run: files it edits have uncommitted changes:\n' + '\n'.join(d))

    rows = []
    started = datetime.datetime.now(datetime.timezone.utc)
    if a.which in ('backend', 'all'):
        rows += run_parallel(a.parallel, a.only) if a.parallel > 1 else run_backend(a.only, shard)
    if a.which in ('frontend', 'all'):
        rows += run_playwright('frontend', mutations.FRONTEND, a.only, lambda e: [e[4] if len(e) > 4 else 'e2e/unit/segments.spec.tsx'])
    if a.which in ('ui', 'all'):
        rows += run_playwright('ui', mutations.UI, a.only, lambda e: [e[4][5:] if e[4].startswith('file:') else ('e2e/scheduling.spec.ts' if e[4] == '' else 'e2e/segments.spec.ts')] + (['-g', e[4]] if e[4] and not e[4].startswith('file:') else []))

    bad = [r for r in rows if r[2] != 'CAUGHT']
    caught = len(rows) - len(bad)
    took = datetime.datetime.now(datetime.timezone.utc) - started
    print(f'\n{caught} of {len(rows)} mutations caught in {int(took.total_seconds() // 60)} min {int(took.total_seconds() % 60)} s')
    if a.results:
        write_results(a.results, rows, caught, started, took, a.parallel)
    sys.exit(1 if bad else 0)


def write_results(path, rows, caught, started, took=None, parallel=0):
    commit = subprocess.run(['git', 'rev-parse', '--short', 'HEAD'], cwd=ROOT, capture_output=True, text=True).stdout.strip()
    lines = [
        '# Mutation check results', '',
        f'Run on {started:%Y-%m-%d} at commit `{commit}` with `python3 tools/mutation/run.py all`.',
        f'**{caught} of {len(rows)} mutations caught.** Each row breaks one protection; a test must fail.',
        (f'The run took {int(took.total_seconds() // 60)} min {int(took.total_seconds() % 60)} s' + (f' with the backend entries spread over {parallel} worktrees.' if parallel > 1 else '.')) if took else '', '',
        '| Kind | Mutation | Result | First failing test |', '|---|---|---|---|',
    ]
    for kind, label, result, detail in rows:
        lines.append(f'| {kind} | {label} | {result} | {detail.replace("|", "/")} |')
    lines.append('')
    with open(os.path.join(ROOT, path) if not os.path.isabs(path) else path, 'w') as f:
        f.write('\n'.join(lines))


if __name__ == '__main__':
    main()
