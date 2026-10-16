-- Outbox, webhooks and invite email. NOT PART OF EARLIER RELEASES: see docs/ROLLOUT.md.
--
-- Part 1 (this commit): scheduled changes remember the API token that created
-- them, so revoking or expiring that token can cancel what it left pending and
-- the executor can refuse a change whose token is no longer valid.
--
-- LOCKS: scheduled_changes gets one nullable column (a catalog change, no
-- rewrite). The composite foreign key is added NOT VALID and then validated, so
-- the table is only read-locked while existing rows are checked (there are none
-- with a token yet, so it is instant). A brief ACCESS EXCLUSIVE lock is taken for
-- the ADD COLUMN and SHARE ROW EXCLUSIVE on api_tokens for the constraint.

BEGIN;

ALTER TABLE scheduled_changes ADD COLUMN created_by_token_id UUID;
ALTER TABLE scheduled_changes
    ADD CONSTRAINT scheduled_changes_token_workspace_fkey
    FOREIGN KEY (created_by_token_id, workspace_id) REFERENCES api_tokens (id, workspace_id) NOT VALID;
ALTER TABLE scheduled_changes VALIDATE CONSTRAINT scheduled_changes_token_workspace_fkey;
CREATE INDEX idx_scheduled_changes_token ON scheduled_changes (workspace_id, created_by_token_id) WHERE created_by_token_id IS NOT NULL;

COMMIT;
