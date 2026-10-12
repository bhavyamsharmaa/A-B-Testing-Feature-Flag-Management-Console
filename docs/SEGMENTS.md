# Segments and targeting operators

A **segment** is a reusable audience. A flag's targeting rule can say "serve this variation to
people in segment X" (`inSegment`) or "…not in X" (`notInSegment`). The same release adds
operators to targeting clauses. Rollout plan: `docs/ROLLOUT.md`, "Release 3". Schema: migration
`0010_segments.sql`. API: `api/openapi.yaml`.

## What a segment is

* Belongs to **one workspace** and is shared by all its environments. Key unique within the
  workspace (same format as flag keys), a name, a description.
* **Explicit members**: subject keys (the `subjectKey` an SDK sends), exact and case-sensitive.
* **Attribute rules** (optional): clauses built from the same operators as flag rules.

**A subject is in the segment if it is an explicit member OR it matches the rules.**

### Do the rules combine with AND or OR? — AND

The rules of one segment are **ANDed**, exactly like the clauses inside one flag rule. A subject
matches the rules only if it satisfies every clause. We chose AND because it is the one
combinator the product already has (a flag rule's clauses), so there is nothing new to learn, and
because it keeps a segment's meaning easy to state ("members, or people who are all of these
things"). "Either of these" is expressed without a second combinator:

* an OR across segments: two rules in the flag, each with its own `inSegment` (rules are
  tried top to bottom and the first match wins), or
* an OR across attributes: use `in` (`country in [IN, US]`) or put the people in the explicit
  members.

A segment with no rules contains exactly its explicit members. A segment with no members
contains exactly the people its rules match. An empty segment contains nobody.

### No nesting

A segment's rules cannot use `inSegment` / `notInSegment` (rejected at save time: *"segments
cannot reference other segments"*). So there is no recursion and no cycle to detect. Evaluation
code also refuses to recurse if bad data ever got in.

## Using a segment in a flag

```json
{ "clauses": [ { "attribute": "", "operator": "inSegment", "value": "beta-testers" } ], "variationId": "on" }
```

* `attribute` must be empty for the segment operators. They cannot be negated (use the opposite
  operator).
* Saving a rule that names a segment this workspace does not have is **rejected**
  (`targetingRules[0]: clause 1: segment "x" does not exist in this workspace`). Another
  workspace's segment is indistinguishable from a missing one.
* **Deleting a segment** that rules still reference is refused (`409 SEGMENT_IN_USE`, naming the
  flags and environments) unless the caller confirms (`?confirm=true`). The console shows the
  usage first and asks for a tick. The rules stay stored and then **match nobody — under
  `inSegment` and under `notInSegment`**: a stale reference must never widen a rule. Creating a
  segment with the same key again makes the stored rules live again.
* Which flags use a segment: `GET /workspaces/{wsId}/segments/{key}/usage`.

## How segments are loaded for evaluation

`/evaluate` reads Postgres on every request (there is no in-memory ruleset yet). When the flags
requested have rules that name segments, the handler runs **one extra query**
(`evaluation.SegmentsQuery`) that returns, for the segments named:

* each segment's rules (a JSON array, at most 8 KiB, 20 clauses), and
* whether **this request's subject** is an explicit member — a single probe of the primary key of
  `segment_members (workspace_id, segment_id, subject_key)`, an index-only lookup.

**No member list is ever loaded.** The cost of evaluating does not depend on how many members a
segment has, up to the limit: it is O(log n) in the B-tree. A test asks Postgres for the plan of
that exact query on a 10,000-member segment and fails if it is not an index lookup that uses the
subject key. Requests whose flags name no segment run no extra query and a nil segment map, so
their evaluation is unchanged.

`evaluation.Evaluate` stays a pure function. It receives `Context.Segments`; each `Segment` has a
`MemberSet` (an interface). `/evaluate` supplies a one-subject answer; an in-process SDK could
supply `MapMembers`, a hash set (O(1) lookup — a test checks 200,000 members × 200,000
evaluations finish in a couple of seconds, i.e. nothing scans).

**How big can a segment get before it matters?** For *evaluation*, size barely matters (index
depth grows with the log of the rows in the table). What grows with size is *saving* and
*reading*: updating a segment loads and diffs its member list (10,000 rows), `GET` returns them
all (about 250 KB at typical key lengths, 2.6 MB at the worst case), and the console draws only a
screenful. Hence the limits: **10,000 members per segment, 20 segments per workspace** (so at most
200,000 member rows per workspace), 20 rule clauses and 8,192 bytes of rule JSON per segment, and a
4 MiB request body. They are constants in `internal/controlplane/quota`; the schema carries
`CHECK`s for the rule size and key lengths as a backstop. Beyond 10,000 members per segment, the
next step would be an import job and streamed reads, not a bigger request.

## Propagation, versions and audit

A change to a segment's **rules or members**:

* bumps `segments.version`;
* bumps `flag_configs.version` (and `updated_at`) of **every flag configuration that references
  the segment**, because who they serve has changed. This is "the version the evaluator uses for
  cache invalidation": today `/evaluate` has no cache, so the version is what an SDK or a future
  in-memory ruleset would key on, and what the console shows;
* after the transaction commits, publishes `segment_updated` (or `segment_deleted`) on the Redis
  flag channel `flags:{workspace}:{environment}` of **each environment that has an affected flag**,
  with the segment key, its new version and the flag keys. `/sdk/stream` relays it like any flag
  event. As for flag events, it is a cue to re-pull, not state, and a Redis outage never fails the
  change (evaluation reads Postgres, so it is correct the moment the transaction commits).

A rename or description change does none of that. An update that changes nothing does not even
bump the version.

Audit rows (`segment.create`, `segment.update`, `segment.delete`, `segment.force_delete` —
critical when the segment was in use) record who, what, and a before/after **summary**: name,
description, rules, member count, version, and for members only **how many were added and removed
plus the first 10 keys of each** — never the whole list (a test checks that a 400-member segment
leaves an audit row under 4 KB and that no key beyond the sample appears).

Lock order, everywhere: segment rows first, flag configurations second (see the `segments`
package comment). A flag save that names segments takes them `FOR SHARE` so a segment cannot be
deleted between the existence check and the save; segment create takes the workspace row (the
quota). A race test runs edits, flag saves and deletes together and fails on any 500.

## Operators

Existing, unchanged: `equals`, `in`, `contains`, `greaterThan`, `exists`.

| Operator | Value | Notes |
|---|---|---|
| `startsWith`, `endsWith` | non-empty text | case-sensitive, byte-wise like `contains` |
| `lessThan`, `lessThanOrEqual`, `greaterThanOrEqual` | number | numbers only; the text `"5"` is not 5 |
| `semverEquals`, `semverLessThan`, `semverLessThanOrEqual`, `semverGreaterThan`, `semverGreaterThanOrEqual` | version | strict semver.org 2.0.0: `MAJOR.MINOR.PATCH[-pre][+build]`, no `v` prefix, no leading zeros, ≤128 chars; precedence per §11 (pre-releases sort below the release; build metadata ignored). An attribute that is not valid semver does not match |
| `dateBefore`, `dateAfter`, `dateEquals` | RFC 3339 timestamp | `2024-05-01T00:00:00Z`, offsets and fractions allowed; compared as instants. A bare date or any other layout is not a date: no match |
| `matchesRegex` | pattern | **Go `regexp` (RE2)**: matching is linear in the input, there is no backtracking, so no pattern can run away. Pattern ≤256 bytes; attribute values over 2,048 bytes do not match; invalid patterns are rejected at save time (`invalid regular expression: …`). Matches anywhere unless anchored with `^…$`. Compiled patterns are cached (bounded) |
| `inSegment`, `notInSegment` | segment key | see above |

**`negate: true`** on a clause inverts the operator, for any operator except `exists` and the
segment operators. It inverts the answer **only when the operator could be evaluated**.
Everything that makes a plain clause a non-match stays a non-match when negated: a missing or null
attribute (the normative US-02 AC-2 rule), a value of the wrong type (`startsWith` on a number,
`lessThan` on text), an invalid version or date, a too-long regex input. So `NOT equals "free"`
does not match someone whose `plan` is the number 5. `exists` is rejected with `negate`, since a
missing attribute never matches anything, so "does not exist" can never be true.

**Never an error:** wrong types, unknown values and huge inputs are clean non-matches (a test
runs every operator against every kind of JSON value, with and without negation, and requires no
panic). Unknown operators are rejected on save; a stored rule with an operator an older evaluator
does not know simply does not match.

**Evaluation of flags that use none of this is unchanged**, proved by
`evaluation/testdata/legacy_golden.txt`: ~7,700 results from a corpus of configurations and
contexts, recorded from the evaluator *before* this change, compared byte for byte.

## Console

* **Segments** page (`/console/segments`): list with member and rule counts, create, edit,
  delete (with the usage warning). Viewers see the list and a read-only view; editors and above
  change things. Loading, empty and error states; works at phone width.
* **Member editor**: paste a list (one per line, or separated by commas/tabs); entries are
  trimmed, repeats ignored, bad entries (over 256 characters, control characters) reported by
  name, the 10,000 limit explained, and a paste is **all or nothing** (nothing is lost: the text
  stays in the box when it is refused). Only a screenful of members is drawn; a filter finds the
  rest.
* **Targeting editor**: the new operators in groups, a **NOT** toggle (a button that shows its
  state in text, not only colour), per-operator hints and validation, and a segment picker. A
  rule that names a deleted segment keeps showing its key, flagged "does not exist", and cannot
  be saved until fixed.
