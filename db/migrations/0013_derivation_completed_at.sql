-- 0013_derivation_completed_at.sql
-- The derivation contract exposes created_at/completed_at on the derivation
-- object. Finalize stamps completion explicitly instead of overloading
-- updated_at, which keeps the timestamp stable if a derivation row is ever
-- touched again after completion.

ALTER TABLE content.derivations
    ADD COLUMN completed_at timestamptz;

-- Existing rows already in a terminal state get their completion time
-- backfilled from updated_at, the last write that moved them there.
UPDATE content.derivations
SET completed_at = updated_at
WHERE status IN ('completed', 'failed', 'cancelled');
