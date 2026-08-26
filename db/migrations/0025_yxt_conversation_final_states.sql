BEGIN;

ALTER TABLE content.message_blocks
    DROP CONSTRAINT IF EXISTS message_blocks_role_check;
UPDATE content.message_blocks
SET role = 'user'
WHERE role = 'human';
ALTER TABLE content.message_blocks
    ADD CONSTRAINT message_blocks_role_check
    CHECK (role IN ('user', 'assistant', 'system', 'tool', 'transcription'));

ALTER TABLE content.conversations
    DROP CONSTRAINT IF EXISTS conversations_visibility_check;
UPDATE content.conversations
SET visibility = 'workspace'
WHERE visibility = 'internal';
ALTER TABLE content.conversations
    ADD CONSTRAINT conversations_visibility_check
    CHECK (visibility IN ('private', 'workspace'));

ALTER TABLE content.conversation_media
    DROP CONSTRAINT IF EXISTS conversation_media_status_check;
UPDATE content.conversation_media
SET status = CASE status
    WHEN 'uploaded' THEN 'registered'
    WHEN 'transcription_queued' THEN 'transcribing'
    WHEN 'completed' THEN 'transcribed'
    ELSE status
END
WHERE status IN ('uploaded', 'transcription_queued', 'completed');
ALTER TABLE content.conversation_media
    ALTER COLUMN status SET DEFAULT 'registered';
ALTER TABLE content.conversation_media
    ADD CONSTRAINT conversation_media_status_check
    CHECK (status IN ('registered', 'transcribing', 'transcribed', 'failed'));

ALTER TABLE asset.attachments
    DROP CONSTRAINT IF EXISTS attachments_scan_status_check;
UPDATE asset.attachments
SET scan_status = 'rejected'
WHERE scan_status = 'blocked';
ALTER TABLE asset.attachments
    ADD CONSTRAINT attachments_scan_status_check
    CHECK (scan_status IN ('pending', 'clean', 'rejected', 'failed'));

COMMIT;
