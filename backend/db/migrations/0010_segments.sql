-- Audience segments: reusable sets of subjects that flag targeting rules can
-- reference (inSegment / notInSegment).
--
-- NOT PART OF THE MULTI-TENANCY OR EXPERIMENTS RELEASES. It needs the
-- workspaces of 0004 (and nothing from 0008/0009). See docs/ROLLOUT.md: it
-- ships with a later release than multi-tenancy. The backends of earlier
-- releases never read or write these tables, and no existing table changes.
--
-- segments         one row per segment: a key unique within its workspace, a
--                  name, a description, and optional attribute rules (a JSON
--                  array of clauses, ANDed, in the same shape as the clauses
--                  of a flag rule). The rules are validated by the API; the
--                  database only guarantees an array of bounded size.
-- segment_members  the explicit members, one row per subject key. The primary
--                  key (workspace_id, segment_id, subject_key) makes "is this
--                  one subject in this segment" a single index probe, which is
--                  how /evaluate asks it: it never loads a member list.
--
-- Counts (segments per workspace, members per segment) are quotas enforced by
-- the API under the workspace row lock, not by the schema.
--
-- Everything is workspace-scoped: workspace_id on both tables, a composite
-- foreign key so a member can only belong to a segment of its own workspace,
-- and every index leads with workspace_id. RLS is enabled with no policies, as
-- in 0004 (the backend connects as a role that bypasses it).
--
-- LOCKS: two new, empty tables. The only lock on an existing table is the one
-- a foreign key needs on its target: SHARE ROW EXCLUSIVE on workspaces (and
-- auth.users) while the constraint is created, for milliseconds. That blocks
-- INSERT/UPDATE/DELETE on workspaces during that moment (workspace creation,
-- rename); reads are unaffected. Nothing is rewritten or scanned.
--
-- Rehearse: db/verify_0010.sql (read-only checks) and db/rehearsal.md.

BEGIN;

CREATE TABLE segments (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    key          TEXT NOT NULL,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    -- Array of clauses, ANDed. [] means "explicit members only".
    rules        JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- Bumped on every change (members included); the evaluator's cache key and
    -- the number in the propagation event.
    version      BIGINT NOT NULL DEFAULT 1,
    created_by   UUID REFERENCES auth.users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT segments_key_unique UNIQUE (workspace_id, key),
    -- Target of the composite foreign key on segment_members.
    CONSTRAINT segments_id_workspace_unique UNIQUE (id, workspace_id),
    CONSTRAINT segments_key_format CHECK (key ~ '^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$'),
    CONSTRAINT segments_sizes CHECK (
        char_length(name) BETWEEN 1 AND 120
        AND char_length(description) <= 1000
        AND jsonb_typeof(rules) = 'array'
        AND jsonb_array_length(rules) <= 20
        AND octet_length(rules::text) <= 16384)
);

CREATE TABLE segment_members (
    workspace_id UUID NOT NULL,
    segment_id   UUID NOT NULL,
    subject_key  TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, segment_id, subject_key),
    -- Same workspace or the row cannot exist; deleting a segment deletes its members.
    CONSTRAINT segment_members_segment_workspace_fkey
        FOREIGN KEY (segment_id, workspace_id) REFERENCES segments (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT segment_members_subject_size CHECK (char_length(subject_key) BETWEEN 1 AND 256)
);

ALTER TABLE segments        ENABLE ROW LEVEL SECURITY;
ALTER TABLE segment_members ENABLE ROW LEVEL SECURITY;

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand)
-- ============================================================
-- Deploy a backend without segments FIRST: flag rules that use inSegment or
-- notInSegment would otherwise stop validating on the next save (the older
-- evaluator treats an unknown operator as "no match"). Dropping the tables
-- DISCARDS every segment and member, so take a backup first
-- (backend/scripts/backup_db.sh).
--
-- BEGIN;
-- DROP TABLE segment_members;
-- DROP TABLE segments;
-- COMMIT;
