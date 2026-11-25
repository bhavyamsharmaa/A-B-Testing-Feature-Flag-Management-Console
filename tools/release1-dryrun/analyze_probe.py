#!/usr/bin/env python3
"""analyze_probe.py PROBE_FILE [MARKERS_FILE]: totals, failures, slowest requests, and what ran at the time."""
import sys, collections
rows = [l.strip().split(",") for l in open(sys.argv[1]) if l.count(",") == 3]
rows = [(int(t), k, int(s), int(ms)) for t, k, s, ms in rows]
marks = []
if len(sys.argv) > 2:
    marks = [(int(l.split(",")[0]), l.strip().split(",")[1]) for l in open(sys.argv[2]) if "," in l]
print(f"{len(rows)} requests; failures (status not 2xx): {sum(1 for r in rows if not 200 <= r[2] < 300)}")
by = collections.defaultdict(list)
for t, k, s, ms in rows: by[k].append(ms)
for k, v in sorted(by.items()):
    v.sort(); print(f"  {k:15s} n={len(v):6d} p50={v[len(v)//2]:4d} ms p99={v[int(len(v)*.99)]:5d} ms max={v[-1]:5d} ms")
slow = sorted(rows, key=lambda r: -r[3])[:5]
def at(t):
    cur = "-"
    for mt, name in marks:
        if mt <= t: cur = name
    return cur
for t, k, s, ms in slow: print(f"  slowest: {k} {ms} ms status {s} during [{at(t)}]")
bad = collections.Counter((k, s) for t, k, s, ms in rows if not 200 <= s < 300)
for (k, s), n in bad.items(): print(f"  non-2xx: {k} status {s} x{n}")
