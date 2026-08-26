package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"strings"
	"time"
	"unicode"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/objectstore"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("attachment not found")
var ErrInvalidUpload = errors.New("invalid attachment upload")
var ErrUploadTooLarge = errors.New("attachment is too large")

type Service struct {
	Store        *store.Store
	Events       eventing.EventStore
	Objects      objectstore.ObjectStore
	ObjectPrefix string
	MaxBytes     int64
}

type UploadInput struct {
	AssetVersionID string
	Filename       string
	MediaType      string
	Size           int64
	Body           io.ReadSeeker
}

type UploadResult struct {
	ID             string    `json:"id"`
	AssetVersionID string    `json:"asset_version_id"`
	Filename       string    `json:"filename"`
	MediaType      string    `json:"media_type"`
	ByteSize       int64     `json:"size"`
	SHA256         string    `json:"checksum"`
	ScanStatus     string    `json:"scan_status"`
	Status         string    `json:"status"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
}

type StatusResult struct {
	ID             string    `json:"id"`
	AssetVersionID string    `json:"asset_version_id"`
	Filename       string    `json:"filename"`
	MediaType      string    `json:"media_type"`
	ByteSize       int64     `json:"size"`
	SHA256         string    `json:"checksum"`
	ScanStatus     string    `json:"scan_status"`
	Status         string    `json:"status"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
}

type LinkInput struct {
	AttachmentID   string
	AssetVersionID string
}

type Download struct {
	Body          io.ReadCloser
	Filename      string
	MediaType     string
	ContentLength int64
	ETag          string
}

// Status performs the same model-scope check used by attachment upload. It
// returns attachment metadata only, never an OSS key or provider URL.
func (s Service) Status(ctx context.Context, principal auth.Principal, attachmentID string, allowedModelIDs []string) (StatusResult, error) {
	if len(allowedModelIDs) == 0 {
		return StatusResult{}, ErrNotFound
	}
	if s.Store == nil || s.Store.Pool == nil {
		return StatusResult{}, errors.New("database store is not initialized")
	}
	var result StatusResult
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT at.id::text, at.asset_version_id::text, at.original_filename,
		       at.media_type, at.byte_size, at.sha256, at.scan_status,
		       at.created_by::text, at.created_at
		FROM asset.attachments at
		JOIN asset.asset_versions av ON av.id = at.asset_version_id
		JOIN asset.assets a ON a.id = av.asset_id
		JOIN asset.asset_versions working_version ON working_version.id = a.current_working_version_id
		WHERE at.id = $1::uuid
		  AND at.deleted_at IS NULL
		  AND at.organization_id = $2::uuid
		  AND a.organization_id = $2::uuid
		  AND a.resource_model_id::text = ANY($3::text[])
		AND (
			(working_version.workflow_status IN ('draft', 'submitted') AND EXISTS (
				WITH RECURSIVE version_lineage AS (
					SELECT a.current_working_version_id AS id
					UNION ALL
					SELECT parent.parent_version_id
					FROM asset.asset_versions parent
					JOIN version_lineage child ON parent.id = child.id
					WHERE parent.parent_version_id IS NOT NULL
				)
				SELECT 1 FROM version_lineage WHERE id = at.asset_version_id
			))
			OR (a.publication_status = 'published' AND EXISTS (
				WITH RECURSIVE version_lineage AS (
					SELECT a.current_published_version_id AS id
					UNION ALL
					SELECT parent.parent_version_id
					FROM asset.asset_versions parent
					JOIN version_lineage child ON parent.id = child.id
					WHERE parent.parent_version_id IS NOT NULL
				)
				SELECT 1 FROM version_lineage WHERE id = at.asset_version_id
			))
		)
	`, attachmentID, principal.OrganizationID, allowedModelIDs).Scan(
		&result.ID,
		&result.AssetVersionID,
		&result.Filename,
		&result.MediaType,
		&result.ByteSize,
		&result.SHA256,
		&result.ScanStatus,
		&result.CreatedBy,
		&result.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return StatusResult{}, ErrNotFound
	}
	if err != nil {
		return StatusResult{}, fmt.Errorf("authorize attachment status: %w", err)
	}
	result.Status = "available"
	return result, nil
}

func (s Service) List(ctx context.Context, principal auth.Principal, assetVersionID string, allowedModelIDs []string) ([]StatusResult, error) {
	if len(allowedModelIDs) == 0 || s.Store == nil || s.Store.Pool == nil {
		return nil, ErrNotFound
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT at.id::text, at.asset_version_id::text, at.original_filename, at.media_type,
		       at.byte_size, at.sha256, at.scan_status, at.created_by::text, at.created_at
		FROM asset.attachments at JOIN asset.asset_versions av ON av.id = at.asset_version_id JOIN asset.assets a ON a.id = av.asset_id
		WHERE at.organization_id = $1::uuid AND at.asset_version_id = $2::uuid AND at.deleted_at IS NULL AND a.resource_model_id::text = ANY($3::text[])
		ORDER BY at.created_at DESC, at.id
	`, principal.OrganizationID, assetVersionID, allowedModelIDs)
	if err != nil {
		return nil, fmt.Errorf("list attachment metadata: %w", err)
	}
	defer rows.Close()
	items := make([]StatusResult, 0)
	for rows.Next() {
		var item StatusResult
		if err := rows.Scan(&item.ID, &item.AssetVersionID, &item.Filename, &item.MediaType, &item.ByteSize, &item.SHA256, &item.ScanStatus, &item.CreatedBy, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.Status = "available"
		items = append(items, item)
	}
	return items, rows.Err()
}

// UpdateFilename changes attachment metadata only while the attachment is on
// the asset's current draft working version. Published attachment metadata is
// immutable and must be changed by creating a new working version.
func (s Service) UpdateFilename(ctx context.Context, principal auth.Principal, attachmentID, filename string, allowedModelIDs []string) (StatusResult, error) {
	if len(allowedModelIDs) == 0 || s.Store == nil || s.Store.Pool == nil {
		return StatusResult{}, ErrNotFound
	}
	cleaned, err := cleanFilename(filename)
	if err != nil {
		return StatusResult{}, err
	}
	tag, err := s.Store.Pool.Exec(ctx, `
		UPDATE asset.attachments at
		SET original_filename = $1
		FROM asset.asset_versions av
		JOIN asset.assets a ON a.id = av.asset_id
		WHERE at.id = $2::uuid
		  AND at.asset_version_id = av.id
		  AND at.organization_id = $3::uuid
		  AND a.organization_id = $3::uuid
		  AND at.deleted_at IS NULL
		  AND a.current_working_version_id = av.id
		  AND av.workflow_status = 'draft'
		  AND a.resource_model_id::text = ANY($4::text[])
	`, cleaned, attachmentID, principal.OrganizationID, allowedModelIDs)
	if err != nil {
		return StatusResult{}, fmt.Errorf("update attachment filename: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return StatusResult{}, ErrNotFound
	}
	return s.Status(ctx, principal, attachmentID, allowedModelIDs)
}

func (s Service) Delete(ctx context.Context, principal auth.Principal, attachmentID string, allowedModelIDs []string) error {
	if len(allowedModelIDs) == 0 || s.Store == nil || s.Store.Pool == nil {
		return ErrNotFound
	}
	var objectKey string
	err := s.Store.Pool.QueryRow(ctx, `SELECT at.object_key FROM asset.attachments at JOIN asset.asset_versions av ON av.id = at.asset_version_id JOIN asset.assets a ON a.id = av.asset_id WHERE at.id = $1::uuid AND at.organization_id = $2::uuid AND at.deleted_at IS NULL AND a.resource_model_id::text = ANY($3::text[])`, attachmentID, principal.OrganizationID, allowedModelIDs).Scan(&objectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load attachment for delete: %w", err)
	}
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE asset.attachments SET deleted_at = now() WHERE id = $1::uuid AND organization_id = $2::uuid`, attachmentID, principal.OrganizationID); err != nil {
		return fmt.Errorf("soft delete attachment: %w", err)
	}
	if s.Objects != nil && objectKey != "" {
		if err := s.Objects.Delete(ctx, objectstore.ObjectRef{Key: objectKey}); err != nil {
			return fmt.Errorf("delete attachment object: %w", err)
		}
	}
	return nil
}

func (s Service) Link(ctx context.Context, principal auth.Principal, input LinkInput, allowedModelIDs []string) error {
	if len(allowedModelIDs) == 0 || s.Store == nil || s.Store.Pool == nil {
		return ErrNotFound
	}
	if _, err := s.Store.Pool.Exec(ctx, `INSERT INTO asset.attachment_links (attachment_id, asset_version_id, created_by) SELECT $1::uuid, $2::uuid, $3::uuid WHERE EXISTS (SELECT 1 FROM asset.attachments at JOIN asset.asset_versions av ON av.id = at.asset_version_id JOIN asset.assets a ON a.id = av.asset_id WHERE at.id = $1::uuid AND at.organization_id = $4::uuid AND at.deleted_at IS NULL AND a.resource_model_id::text = ANY($5::text[])) AND EXISTS (SELECT 1 FROM asset.asset_versions av JOIN asset.assets a ON a.id = av.asset_id WHERE av.id = $2::uuid AND a.organization_id = $4::uuid AND a.resource_model_id::text = ANY($5::text[])) ON CONFLICT DO NOTHING`, input.AttachmentID, input.AssetVersionID, principal.UserID, principal.OrganizationID, allowedModelIDs); err != nil {
		return fmt.Errorf("link attachment: %w", err)
	}
	return nil
}

// OpenDownload performs the final database authorization check before opening
// the provider stream. Callers never receive an OSS URL.
func (s Service) OpenDownload(ctx context.Context, principal auth.Principal, attachmentID string, allowedModelIDs []string, outlet string) (Download, error) {
	if len(allowedModelIDs) == 0 {
		return Download{}, ErrNotFound
	}
	if s.Store == nil || s.Store.Pool == nil {
		return Download{}, errors.New("database store is not initialized")
	}
	if s.Objects == nil {
		return Download{}, errors.New("object store is not initialized")
	}
	var objectKey, filename, mediaType string
	var byteSize int64
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT at.object_key, at.original_filename, at.media_type, at.byte_size
		FROM asset.attachments at
		JOIN asset.asset_versions source_version ON source_version.id = at.asset_version_id
		JOIN asset.assets a ON a.id = source_version.asset_id
		JOIN asset.asset_versions published_version ON published_version.id = a.current_published_version_id
		JOIN model.resource_model_versions mv ON mv.id = published_version.resource_model_version_id
		WHERE at.id = $1::uuid
		  AND at.deleted_at IS NULL
		  AND at.scan_status = 'clean'
		  AND at.organization_id = $2::uuid
		  AND a.organization_id = $2::uuid
		  AND a.resource_model_id::text = ANY($3::text[])
		  AND a.publication_status = 'published'
		  AND EXISTS (
			WITH RECURSIVE version_lineage AS (
				SELECT a.current_published_version_id AS id
				UNION ALL
				SELECT parent.parent_version_id
				FROM asset.asset_versions parent
				JOIN version_lineage child ON parent.id = child.id
				WHERE parent.parent_version_id IS NOT NULL
			)
			SELECT 1 FROM version_lineage WHERE id = at.asset_version_id
		)
		  AND COALESCE(NULLIF(mv.policy #>> ARRAY['outlets', $4::text, 'enabled'], '')::boolean, false)
		  AND CASE published_version.quality
				WHEN 'raw' THEN 1
				WHEN 'ai_generated' THEN 2
				WHEN 'human_confirmed' THEN 3
				WHEN 'human_confirmed' THEN 3
			END >= CASE COALESCE(NULLIF(mv.policy #>> ARRAY['outlets', $4::text, 'min_quality'], ''), 'raw')
				WHEN 'raw' THEN 1
				WHEN 'ai_generated' THEN 2
				WHEN 'human_confirmed' THEN 3
				WHEN 'human_confirmed' THEN 3
			ELSE 99
			END
		`, attachmentID, principal.OrganizationID, allowedModelIDs, outlet).Scan(&objectKey, &filename, &mediaType, &byteSize)
	if errors.Is(err, pgx.ErrNoRows) {
		return Download{}, ErrNotFound
	}
	if err != nil {
		return Download{}, fmt.Errorf("authorize attachment download: %w", err)
	}
	object, err := s.Objects.Get(ctx, objectstore.ObjectRef{Key: objectKey})
	if err != nil {
		return Download{}, fmt.Errorf("open attachment object: %w", err)
	}
	if mediaType == "" {
		mediaType = object.ContentType
	}
	if object.ContentLength >= 0 && byteSize != object.ContentLength {
		_ = object.Body.Close()
		return Download{}, errors.New("attachment metadata size does not match OSS object")
	}
	if byteSize < 0 {
		byteSize = object.ContentLength
	}
	return Download{
		Body:          object.Body,
		Filename:      filename,
		MediaType:     mediaType,
		ContentLength: byteSize,
		ETag:          object.ETag,
	}, nil
}

func (s Service) Upload(ctx context.Context, principal auth.Principal, allowedModelIDs []string, input UploadInput) (UploadResult, error) {
	if len(allowedModelIDs) == 0 {
		return UploadResult{}, ErrNotFound
	}
	if s.Store == nil || s.Store.Pool == nil {
		return UploadResult{}, errors.New("database store is not initialized")
	}
	if s.Objects == nil {
		return UploadResult{}, errors.New("object store is not initialized")
	}
	if input.Body == nil || input.Size < 0 {
		return UploadResult{}, ErrInvalidUpload
	}
	maxBytes := s.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 50 * 1024 * 1024
	}
	if input.Size > maxBytes {
		return UploadResult{}, ErrUploadTooLarge
	}
	filename, err := cleanFilename(input.Filename)
	if err != nil {
		return UploadResult{}, err
	}
	mediaType, err := cleanMediaType(input.MediaType, filename)
	if err != nil {
		return UploadResult{}, err
	}
	var attachmentID string
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT gen_random_uuid()::text
		FROM asset.asset_versions av
		JOIN asset.assets a ON a.id = av.asset_id
		WHERE av.id = $1::uuid
		  AND av.organization_id = $2::uuid
		  AND a.organization_id = $2::uuid
		  AND a.current_working_version_id = av.id
		  AND a.resource_model_id::text = ANY($3::text[])
		  AND av.workflow_status = 'draft'
	`, input.AssetVersionID, principal.OrganizationID, allowedModelIDs).Scan(&attachmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadResult{}, ErrNotFound
	}
	if err != nil {
		return UploadResult{}, fmt.Errorf("authorize attachment upload: %w", err)
	}
	if _, err := input.Body.Seek(0, io.SeekStart); err != nil {
		return UploadResult{}, fmt.Errorf("rewind attachment: %w", err)
	}
	hash := sha256.New()
	bytesRead, err := io.Copy(hash, input.Body)
	if err != nil {
		return UploadResult{}, fmt.Errorf("hash attachment: %w", err)
	}
	if bytesRead != input.Size {
		return UploadResult{}, ErrInvalidUpload
	}
	if _, err := input.Body.Seek(0, io.SeekStart); err != nil {
		return UploadResult{}, fmt.Errorf("rewind attachment for OSS: %w", err)
	}
	sha256Hex := hex.EncodeToString(hash.Sum(nil))
	objectKey := buildObjectKey(s.ObjectPrefix, principal.OrganizationID, attachmentID)
	if _, err := s.Objects.Put(ctx, objectstore.Object{
		Key:           objectKey,
		Body:          input.Body,
		ContentType:   mediaType,
		ContentLength: input.Size,
	}); err != nil {
		return UploadResult{}, fmt.Errorf("store attachment object: %w", err)
	}
	result := UploadResult{
		ID:             attachmentID,
		AssetVersionID: input.AssetVersionID,
		Filename:       filename,
		MediaType:      mediaType,
		ByteSize:       input.Size,
		SHA256:         sha256Hex,
		ScanStatus:     "pending",
		Status:         "available",
		CreatedBy:      principal.UserID,
		CreatedAt:      time.Now().UTC(),
	}
	if err := s.persistUpload(ctx, principal, result, objectKey); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Objects.Delete(cleanupCtx, objectstore.ObjectRef{Key: objectKey})
		return UploadResult{}, err
	}
	return result, nil
}

func (s Service) persistUpload(ctx context.Context, principal auth.Principal, result UploadResult, objectKey string) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin attachment transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO asset.attachments
			(id, organization_id, asset_version_id, object_key, original_filename, media_type, byte_size, sha256, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9::uuid)
	`, result.ID, principal.OrganizationID, result.AssetVersionID, objectKey, result.Filename, result.MediaType, result.ByteSize, result.SHA256, principal.UserID); err != nil {
		return fmt.Errorf("record attachment metadata: %w", err)
	}
	if _, err := s.Events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   principal.OrganizationID,
		EventType:        "attachment.created",
		AggregateType:    "attachment",
		AggregateID:      result.ID,
		AggregateVersion: 1,
		PayloadVersion:   1,
		Payload: map[string]string{
			"attachment_id": result.ID,
		},
	}); err != nil {
		return fmt.Errorf("enqueue attachment scan: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit attachment transaction: %w", err)
	}
	return nil
}

func cleanFilename(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = path.Base(value)
	if value == "." || value == "/" || value == "" || len(value) > 255 {
		return "", ErrInvalidUpload
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", ErrInvalidUpload
		}
	}
	return value, nil
}

func cleanMediaType(value, filename string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = mime.TypeByExtension(path.Ext(filename))
	}
	if value == "" {
		value = "application/octet-stream"
	}
	parsed, _, err := mime.ParseMediaType(value)
	if err != nil || !strings.Contains(parsed, "/") || len(parsed) > 128 {
		return "", ErrInvalidUpload
	}
	return parsed, nil
}

func buildObjectKey(prefix, organizationID, attachmentID string) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return organizationID + "/" + attachmentID
	}
	return prefix + "/" + organizationID + "/" + attachmentID
}
