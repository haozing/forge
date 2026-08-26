BEGIN;

ALTER TABLE content.review_comments
    ADD COLUMN IF NOT EXISTS idempotency_key text,
    ADD COLUMN IF NOT EXISTS request_hash text;

CREATE UNIQUE INDEX IF NOT EXISTS review_comments_idempotency_idx
    ON content.review_comments (organization_id, review_id, author_user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

COMMIT;
