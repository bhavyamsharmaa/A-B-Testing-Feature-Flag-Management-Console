-- Signed exposure tokens, "report" mode: remember whether an $exposure's token
-- verified, so the results can warn about unverified exposures.
--
-- NOT PART OF THE MULTI-TENANCY RELEASE. Apply it after 0008 (which created the
-- columns it extends), which itself comes after the multi-tenancy release. See
-- docs/ROLLOUT.md. The multi-tenancy backend never reads or writes this column.
--
-- events.exposure_verified:
--   NULL   not checked (verification off, or not an $exposure)
--   true   the token verified
--   false  accepted in "report" mode although it did not verify (or was missing)
--
-- LOCKS: ADD COLUMN of a nullable column with no default is a catalog-only
-- change (no table rewrite). The CHECK is added NOT VALID and then validated, so
-- the validating scan takes only a SHARE UPDATE EXCLUSIVE lock (writes continue).
-- The partial index only holds rows with exposure_verified = false (normally
-- none), so building it is quick; on a large events table build it by hand first
-- with CREATE INDEX CONCURRENTLY idx_events_unverified_exposures ... (same
-- definition) and this statement then does nothing.

BEGIN;

ALTER TABLE events ADD COLUMN exposure_verified BOOLEAN;

ALTER TABLE events
    ADD CONSTRAINT events_exposure_verified_only_on_exposures
    CHECK (exposure_verified IS NULL OR event_name = '$exposure') NOT VALID;
ALTER TABLE events VALIDATE CONSTRAINT events_exposure_verified_only_on_exposures;

CREATE INDEX IF NOT EXISTS idx_events_unverified_exposures
    ON events (workspace_id, experiment_id) WHERE exposure_verified = false;

COMMIT;

-- ============================================================
-- ROLLBACK (not run automatically; execute by hand)
-- ============================================================
-- Deploy a backend without exposure verification first (the current one writes
-- the column).
--
-- BEGIN;
-- DROP INDEX idx_events_unverified_exposures;
-- ALTER TABLE events DROP CONSTRAINT events_exposure_verified_only_on_exposures;
-- ALTER TABLE events DROP COLUMN exposure_verified;
-- COMMIT;
