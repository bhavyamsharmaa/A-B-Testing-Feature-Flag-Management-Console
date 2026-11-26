-- A workspace that has members must always have an owner.
--
-- The application already refuses to remove, demote or let go of the last
-- owner (and locks the membership rows while it checks); this makes the same
-- rule a database guarantee, so it holds for any code path, a concurrent
-- request that slips past the locks, or a person with a SQL console.
--
-- It is a DEFERRED constraint trigger: the rule is checked when the
-- transaction commits, not row by row, so ownership can be handed over inside
-- one transaction in either order (promote the new owner then demote the old
-- one, or the other way round).
--
-- What it deliberately allows:
--   * creating a workspace: the owner row is INSERTed, and only UPDATE and
--     DELETE of an owner row are checked;
--   * deleting a workspace: its member rows are removed by the cascade, and by
--     commit time the workspace is gone;
--   * a workspace that ends up with no members at all (its only member's
--     account was deleted): it is abandoned, not "members without an owner";
--   * workspaces that never had an owner row (an unowned legacy workspace).
-- What it does NOT cover: the last members of a workspace all leaving, owners
-- included, so that nobody is left (the application's row locks stop two owners
-- leaving at the same moment when someone else is still there or not; this rule
-- treats an empty workspace as abandoned).
-- What it refuses: deleting a user's account while they are the last owner of a
-- workspace that still has other members. Transfer ownership first.
--
-- Applies on top of 0004-0006.

BEGIN;

CREATE FUNCTION workspace_keeps_an_owner() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    -- Still an owner after an UPDATE (say, last_active_at changed): nothing lost.
    IF TG_OP = 'UPDATE' AND NEW.role = 'owner'
       AND NEW.workspace_id = OLD.workspace_id AND NEW.user_id = OLD.user_id THEN
        RETURN NULL;
    END IF;
    -- The workspace itself was deleted in this transaction.
    IF NOT EXISTS (SELECT 1 FROM workspaces WHERE id = OLD.workspace_id) THEN
        RETURN NULL;
    END IF;
    -- Nobody is left in it.
    IF NOT EXISTS (SELECT 1 FROM workspace_members WHERE workspace_id = OLD.workspace_id) THEN
        RETURN NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM workspace_members WHERE workspace_id = OLD.workspace_id AND role = 'owner') THEN
        RAISE EXCEPTION 'workspace % must keep at least one owner', OLD.workspace_id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'workspace_keeps_an_owner';
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER workspace_keeps_an_owner
    AFTER UPDATE OR DELETE ON workspace_members
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    WHEN (OLD.role = 'owner')
    EXECUTE FUNCTION workspace_keeps_an_owner();

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand)
-- ============================================================
-- Safe at any time: it only removes a guarantee, no data.
--
-- DROP TRIGGER workspace_keeps_an_owner ON workspace_members;
-- DROP FUNCTION workspace_keeps_an_owner();
