CREATE TABLE IF NOT EXISTS content.review_comments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    review_id uuid NOT NULL REFERENCES asset.asset_reviews(id) ON DELETE CASCADE,
    author_user_id uuid NOT NULL REFERENCES identity.users(id),
    body text NOT NULL CHECK (length(btrim(body)) > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id)
);

CREATE INDEX IF NOT EXISTS review_comments_review_idx
    ON content.review_comments (organization_id, review_id, created_at, id);
