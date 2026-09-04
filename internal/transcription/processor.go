package transcription

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/objectstore"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var ErrNotConfigured = errors.New("transcription provider is not configured")

type Processor struct {
	Store    *store.Store
	Objects  objectstore.ObjectStore
	Provider Provider
	Timeout  time.Duration
}

func (p Processor) Process(ctx context.Context, jobID, mediaID string) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	if p.Objects == nil || p.Provider == nil {
		return ErrNotConfigured
	}
	if jobID == "" || mediaID == "" {
		return errors.New("transcription job and media ids are required")
	}
	jobCtx := ctx
	if p.Timeout > 0 {
		var cancel context.CancelFunc
		jobCtx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}
	var organizationID, conversationID, attachmentID, objectKey, filename, mediaType, language, createdBy, status string
	var bodyRevision string
	err := p.Store.Pool.QueryRow(jobCtx, `
		SELECT j.organization_id::text, cm.conversation_id::text, cm.attachment_id::text,
		       at.object_key, at.original_filename, at.media_type, COALESCE(cm.language, ''),
		       cm.created_by::text, cm.status, COALESCE(cm.transcription_block_revision_id::text, '')
		FROM content.processing_jobs j
		JOIN content.conversation_media cm ON cm.id = j.source_id AND cm.organization_id = j.organization_id
		JOIN content.conversations c ON c.id = cm.conversation_id
		JOIN asset.attachments at ON at.id = cm.attachment_id
		WHERE j.id = $1::uuid AND j.job_type = 'transcription' AND j.source_id = $2::uuid
	`, jobID, mediaID).Scan(&organizationID, &conversationID, &attachmentID, &objectKey, &filename, &mediaType, &language, &createdBy, &status, &bodyRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("transcription job not found")
	}
	if err != nil {
		return fmt.Errorf("load transcription job: %w", err)
	}
	if status == "transcribed" && bodyRevision != "" {
		return nil
	}
	if _, err := p.Store.Pool.Exec(jobCtx, `
		UPDATE content.processing_jobs SET status = 'running', started_at = COALESCE(started_at, now()), error_code = NULL
		WHERE id = $1::uuid AND status IN ('queued', 'running', 'failed')
	`, jobID); err != nil {
		return fmt.Errorf("mark transcription running: %w", err)
	}
	if _, err := p.Store.Pool.Exec(jobCtx, `UPDATE content.conversation_media SET status = 'transcribing', updated_at = now() WHERE id = $1::uuid AND status <> 'transcribed'`, mediaID); err != nil {
		return fmt.Errorf("mark media transcribing: %w", err)
	}
	object, err := p.Objects.Get(jobCtx, objectstore.ObjectRef{Key: objectKey})
	if err != nil {
		return fmt.Errorf("open transcription object: %w", err)
	}
	result, providerErr := p.Provider.Transcribe(jobCtx, object.Body, filename, mediaType, language)
	_ = object.Body.Close()
	if providerErr != nil {
		_, _ = p.Store.Pool.Exec(ctx, `UPDATE content.processing_jobs SET status = 'queued', error_code = $2 WHERE id = $1::uuid`, jobID, truncateError(providerErr.Error()))
		_, _ = p.Store.Pool.Exec(ctx, `UPDATE content.conversation_media SET status = 'transcribing', updated_at = now() WHERE id = $1::uuid`, mediaID)
		return providerErr
	}
	tx, err := p.Store.Pool.Begin(jobCtx)
	if err != nil {
		return fmt.Errorf("begin transcription completion: %w", err)
	}
	defer tx.Rollback(jobCtx)
	var revisionID string
	err = tx.QueryRow(jobCtx, `
		SELECT COALESCE(cm.transcription_block_revision_id::text, '')
		FROM content.conversation_media cm WHERE cm.id = $1::uuid FOR UPDATE
	`, mediaID).Scan(&revisionID)
	if err != nil {
		return fmt.Errorf("lock transcription media: %w", err)
	}
	if revisionID == "" {
		var blockID string
		var sequence int64
		if err := tx.QueryRow(jobCtx, `SELECT COALESCE(MAX(sequence_no), 0) + 1 FROM content.message_blocks WHERE conversation_id = $1::uuid`, conversationID).Scan(&sequence); err != nil {
			return fmt.Errorf("allocate transcription sequence: %w", err)
		}
		checksum := checksumText(result.Text)
		// The transcript is a conversation message (immutable transcript
		// record), not note content: it never enters the note tree; members
		// save it into the note explicitly like any other message.
		if err := tx.QueryRow(jobCtx, `INSERT INTO content.blocks (organization_id, block_type, created_by) VALUES ($1::uuid, 'message', $2::uuid) RETURNING id::text`, organizationID, createdBy).Scan(&blockID); err != nil {
			return fmt.Errorf("create transcription block: %w", err)
		}
		if err := tx.QueryRow(jobCtx, `
			INSERT INTO content.block_revisions (organization_id, block_id, revision_no, content, content_format, props, created_by, content_checksum)
			VALUES ($1::uuid, $2::uuid, 1, $3, 'plain_text', jsonb_build_object('language', NULLIF($4, '')), $5::uuid, $6)
			RETURNING id::text
		`, organizationID, blockID, result.Text, result.Language, createdBy, checksum).Scan(&revisionID); err != nil {
			return fmt.Errorf("create transcription revision: %w", err)
		}
		if _, err := tx.Exec(jobCtx, `
			INSERT INTO content.message_blocks (organization_id, block_revision_id, conversation_id, role, status, sequence_no, reference_metadata)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'transcription', 'completed', $4, jsonb_build_object('media_id', $5::uuid, 'attachment_id', $6::uuid, 'language', NULLIF($7, '')))
		`, organizationID, revisionID, conversationID, sequence, mediaID, attachmentID, result.Language); err != nil {
			return fmt.Errorf("create transcription message: %w", err)
		}
		if _, err := tx.Exec(jobCtx, `UPDATE content.conversation_media SET status = 'transcribed', language = COALESCE(NULLIF($2, ''), language), transcription_block_revision_id = $3::uuid, updated_at = now() WHERE id = $1::uuid`, mediaID, result.Language, revisionID); err != nil {
			return fmt.Errorf("complete transcription media: %w", err)
		}
	}
	if _, err := tx.Exec(jobCtx, `UPDATE content.processing_jobs SET status = 'succeeded', output_snapshot = jsonb_build_object('transcription_block_revision_id', $2::uuid, 'language', NULLIF($3, '')), completed_at = now() WHERE id = $1::uuid`, jobID, revisionID, result.Language); err != nil {
		return fmt.Errorf("complete transcription job: %w", err)
	}
	if err := tx.Commit(jobCtx); err != nil {
		return fmt.Errorf("commit transcription completion: %w", err)
	}
	return nil
}

func (p Processor) Fail(ctx context.Context, jobID, mediaID, reason string) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	tx, err := p.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE content.processing_jobs SET status = 'failed', error_code = $2, completed_at = now() WHERE id = $1::uuid AND status <> 'succeeded'`, jobID, truncateError(reason)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE content.conversation_media SET status = 'failed', updated_at = now() WHERE id = $1::uuid AND status <> 'transcribed'`, mediaID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func checksumText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

func truncateError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 500 {
		return value[:500]
	}
	return value
}
