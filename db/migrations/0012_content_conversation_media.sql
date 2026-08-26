CREATE TABLE content.conversation_media (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    conversation_id uuid NOT NULL REFERENCES content.conversations(id),
    attachment_id uuid NOT NULL REFERENCES asset.attachments(id),
    media_kind text NOT NULL CHECK (media_kind IN ('audio', 'video')),
    status text NOT NULL DEFAULT 'uploaded'
        CHECK (status IN ('uploaded', 'transcription_queued', 'transcribing', 'completed', 'failed')),
    language text,
    duration_ms bigint CHECK (duration_ms IS NULL OR duration_ms >= 0),
    transcription_block_revision_id uuid,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, attachment_id)
);

CREATE INDEX content_conversation_media_conversation_idx
    ON content.conversation_media (organization_id, conversation_id, created_at DESC);

CREATE INDEX content_conversation_media_status_idx
    ON content.conversation_media (organization_id, status, updated_at);
