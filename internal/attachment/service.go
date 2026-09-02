// Package attachment implements the standalone attachment model: files are
// uploaded workspace-scoped first, scanned asynchronously, and only bound to
// drafts (asset.asset_draft_attachments) and, through a commit transaction, to
// sealed versions (asset.asset_version_attachments). Attachments never carry
// an asset_version_id of their own.
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("attachment not found")
var ErrInvalidInput = errors.New("attachment input invalid")
var ErrInvalidUpload = errors.New("invalid attachment upload")
var ErrUploadTooLarge = errors.New("attachment is too large")
var ErrForbidden = errors.New("attachment access denied")
var ErrNotClean = errors.New("attachment is not clean")

type Service struct {
	Store        *store.Store
	Events       *eventing.EventStore
	Objects      objectstore.ObjectStore
	ObjectPrefix string
	MaxBytes     int64
}

// Attachment is the metadata projection of asset.attachments. ObjectKey is
// deliberately not part of the API surface.
type Attachment struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Filename    string    `json:"filename"`
	MediaType   string    `json:"media_type"`
	ByteSize    int64     `json:"size"`
	SHA256      string    `json:"checksum"`
	Status      string    `json:"status"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

type Download struct {
	Body          io.ReadCloser
	Filename      string
	MediaType     string
	ContentLength int64
	ETag          string
}

func (s Service) validateStore() error {
	if s.Store == nil || s.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	return nil
}

// requireWorkspaceMember ensures the principal is a non-viewer member of the
// workspace the attachment operation targets.
func (s Service) requireWorkspaceMember(ctx context.Context, principal auth.Principal, workspaceID string) error {
	if err := s.validateStore(); err != nil {
		return err
	}
	var allowed bool
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM content.workspace_members
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
			  AND user_id = $3::uuid AND role <> 'viewer'
		)
	`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&allowed); err != nil {
		return fmt.Errorf("check workspace membership: %w", err)
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

// Upload records a standalone attachment, transfers it to the object store and
// queues the scan pipeline. The attachment is never bound to an asset version
// here; callers attach it to an asset draft through Link.
func (s Service) Upload(ctx context.Context, principal auth.Principal, workspaceID string, filename, mediaType string, size int64, reader io.ReadSeeker) (Attachment, error) {
	if reader == nil || size < 0 || !validID(workspaceID) {
		return Attachment{}, ErrInvalidUpload
	}
	if err := s.validateStore(); err != nil {
		return Attachment{}, err
	}
	if s.Objects == nil {
		return Attachment{}, errors.New("object store is not initialized")
	}
	maxBytes := s.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 50 * 1024 * 1024
	}
	if size > maxBytes {
		return Attachment{}, ErrUploadTooLarge
	}
	cleanName, err := cleanFilename(filename)
	if err != nil {
		return Attachment{}, err
	}
	cleanType, err := cleanMediaType(mediaType, cleanName)
	if err != nil {
		return Attachment{}, err
	}
	if err := s.requireWorkspaceMember(ctx, principal, workspaceID); err != nil {
		return Attachment{}, err
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return Attachment{}, fmt.Errorf("rewind attachment: %w", err)
	}
	hash := sha256.New()
	bytesRead, err := io.Copy(hash, reader)
	if err != nil {
		return Attachment{}, fmt.Errorf("hash attachment: %w", err)
	}
	if bytesRead != size {
		return Attachment{}, ErrInvalidUpload
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return Attachment{}, fmt.Errorf("rewind attachment for OSS: %w", err)
	}
	sha256Hex := hex.EncodeToString(hash.Sum(nil))
	objectID := uuid.NewString()
	objectKey := buildObjectKey(s.ObjectPrefix, principal.OrganizationID, objectID)

	var attachmentID, createdAt string
	if err := s.Store.Pool.QueryRow(ctx, `
		INSERT INTO asset.attachments
			(organization_id, workspace_id, uploader_user_id, object_key, original_filename,
			 media_type, byte_size, sha256, status, expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, 'uploading', now() + interval '24 hours')
		RETURNING id::text, created_at::text
	`, principal.OrganizationID, workspaceID, principal.UserID, objectKey, cleanName, cleanType, size, sha256Hex).Scan(&attachmentID, &createdAt); err != nil {
		return Attachment{}, fmt.Errorf("record attachment metadata: %w", err)
	}
	if _, err := s.Objects.Put(ctx, objectstore.Object{
		Key:           objectKey,
		Body:          reader,
		ContentType:   cleanType,
		ContentLength: size,
	}); err != nil {
		s.markFailed(ctx, principal.OrganizationID, attachmentID)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Objects.Delete(cleanupCtx, objectstore.ObjectRef{Key: objectKey})
		return Attachment{}, fmt.Errorf("store attachment object: %w", err)
	}
	if err := s.markScanning(ctx, principal, workspaceID, attachmentID); err != nil {
		return Attachment{}, err
	}
	created := parseTime(createdAt)
	return Attachment{
		ID:          attachmentID,
		WorkspaceID: workspaceID,
		Filename:    cleanName,
		MediaType:   cleanType,
		ByteSize:    size,
		SHA256:      sha256Hex,
		Status:      "scanning",
		CreatedBy:   principal.UserID,
		CreatedAt:   created,
	}, nil
}

// markScanning flips the uploaded attachment into the scanning state and emits
// the attachment.created fact in the same transaction.
func (s Service) markScanning(ctx context.Context, principal auth.Principal, workspaceID, attachmentID string) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin attachment transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE asset.attachments SET status = 'scanning', updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'uploading'
	`, principal.OrganizationID, attachmentID)
	if err != nil {
		return fmt.Errorf("update attachment status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if s.Events == nil {
		return errors.New("event store is not initialized")
	}
	if _, err := s.Events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   principal.OrganizationID,
		WorkspaceID:      workspaceID,
		EventType:        "attachment.created",
		AggregateType:    "attachment",
		AggregateID:      attachmentID,
		AggregateVersion: 1,
		PayloadVersion:   1,
		Actor:            eventing.ActorFromPrincipal(principal),
		Payload: map[string]string{
			"attachment_id": attachmentID,
		},
	}); err != nil {
		return fmt.Errorf("enqueue attachment scan: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit attachment transaction: %w", err)
	}
	return nil
}

func (s Service) markFailed(ctx context.Context, organizationID, attachmentID string) {
	if s.Store == nil || s.Store.Pool == nil {
		return
	}
	_, _ = s.Store.Pool.Exec(ctx, `
		UPDATE asset.attachments SET status = 'failed', updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, attachmentID)
}

// Status returns attachment metadata for workspace members only. It never
// exposes an OSS key or provider URL.
func (s Service) Status(ctx context.Context, principal auth.Principal, attachmentID string) (Attachment, error) {
	if !validID(attachmentID) {
		return Attachment{}, ErrNotFound
	}
	if err := s.validateStore(); err != nil {
		return Attachment{}, err
	}
	var result Attachment
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT at.id::text, at.workspace_id::text, at.original_filename,
		       at.media_type, at.byte_size, at.sha256, at.status,
		       at.uploader_user_id::text, at.created_at
		FROM asset.attachments at
		WHERE at.id = $1::uuid
		  AND at.deleted_at IS NULL
		  AND at.organization_id = $2::uuid
		  AND EXISTS (
			SELECT 1 FROM content.workspace_members wm
			WHERE wm.organization_id = at.organization_id
			  AND wm.workspace_id = at.workspace_id
			  AND wm.user_id = $3::uuid
		  )
	`, attachmentID, principal.OrganizationID, principal.UserID).Scan(
		&result.ID,
		&result.WorkspaceID,
		&result.Filename,
		&result.MediaType,
		&result.ByteSize,
		&result.SHA256,
		&result.Status,
		&result.CreatedBy,
		&result.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("authorize attachment status: %w", err)
	}
	return result, nil
}

// List returns the attachments currently attached to the asset draft.
func (s Service) List(ctx context.Context, principal auth.Principal, assetID string) ([]Attachment, error) {
	if !validID(assetID) {
		return nil, ErrNotFound
	}
	if err := s.validateStore(); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT at.id::text, at.workspace_id::text, at.original_filename, at.media_type,
		       at.byte_size, at.sha256, at.status, at.uploader_user_id::text, at.created_at
		FROM asset.attachments at
		JOIN asset.asset_draft_attachments lda ON lda.organization_id = at.organization_id AND lda.attachment_id = at.id
		JOIN asset.asset_drafts d ON d.organization_id = lda.organization_id AND d.id = lda.asset_draft_id
		WHERE d.organization_id = $1::uuid AND d.asset_id = $2::uuid
		  AND at.deleted_at IS NULL
		  AND EXISTS (
			SELECT 1 FROM content.workspace_members wm
			WHERE wm.organization_id = at.organization_id
			  AND wm.workspace_id = at.workspace_id
			  AND wm.user_id = $3::uuid
		  )
		ORDER BY at.created_at DESC, at.id
	`, principal.OrganizationID, assetID, principal.UserID)
	if err != nil {
		return nil, fmt.Errorf("list attachment metadata: %w", err)
	}
	defer rows.Close()
	items := make([]Attachment, 0)
	for rows.Next() {
		var item Attachment
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.Filename, &item.MediaType, &item.ByteSize, &item.SHA256, &item.Status, &item.CreatedBy, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// UpdateFilename changes attachment metadata while it is still unbound; once
// an attachment is materialized into a sealed version its provenance is
// immutable and a new upload is required.
func (s Service) UpdateFilename(ctx context.Context, principal auth.Principal, attachmentID, filename string) (Attachment, error) {
	if !validID(attachmentID) {
		return Attachment{}, ErrNotFound
	}
	if err := s.validateStore(); err != nil {
		return Attachment{}, err
	}
	cleaned, err := cleanFilename(filename)
	if err != nil {
		return Attachment{}, err
	}
	tag, err := s.Store.Pool.Exec(ctx, `
		UPDATE asset.attachments at
		SET original_filename = $3, updated_at = now()
		WHERE at.organization_id = $1::uuid AND at.id = $2::uuid
		  AND at.deleted_at IS NULL
		  AND at.uploader_user_id = $4::uuid
		  AND NOT EXISTS (
			SELECT 1 FROM asset.asset_version_attachments lva WHERE lva.attachment_id = at.id
		  )
	`, principal.OrganizationID, attachmentID, cleaned, principal.UserID)
	if err != nil {
		return Attachment{}, fmt.Errorf("update attachment filename: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Attachment{}, ErrNotFound
	}
	return s.Status(ctx, principal, attachmentID)
}

// Delete soft-deletes an unbound attachment and removes its object. Version
// provenance stays intact; the object itself is retained for sealed versions.
func (s Service) Delete(ctx context.Context, principal auth.Principal, attachmentID string) error {
	if !validID(attachmentID) {
		return ErrNotFound
	}
	if err := s.validateStore(); err != nil {
		return err
	}
	var objectKey string
	err := s.Store.Pool.QueryRow(ctx, `
		UPDATE asset.attachments at
		SET deleted_at = now(), updated_at = now()
		WHERE at.organization_id = $1::uuid AND at.id = $2::uuid
		  AND at.deleted_at IS NULL
		  AND at.uploader_user_id = $3::uuid
		  AND NOT EXISTS (
			SELECT 1 FROM asset.asset_version_attachments lva WHERE lva.attachment_id = at.id
		  )
		RETURNING at.object_key
	`, principal.OrganizationID, attachmentID, principal.UserID).Scan(&objectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("soft delete attachment: %w", err)
	}
	if s.Objects != nil && objectKey != "" {
		if err := s.Objects.Delete(ctx, objectstore.ObjectRef{Key: objectKey}); err != nil {
			return fmt.Errorf("delete attachment object: %w", err)
		}
	}
	return nil
}

// Link attaches a clean attachment to the asset's shared draft. The binding is
// materialized into versions only through a commit transaction.
func (s Service) Link(ctx context.Context, principal auth.Principal, attachmentID, assetID, role string) error {
	if !validID(attachmentID) || !validID(assetID) {
		return ErrNotFound
	}
	if role == "" {
		role = "body"
	}
	if role != "body" && role != "cover" {
		return ErrInvalidInput
	}
	if err := s.validateStore(); err != nil {
		return err
	}
	// Cover eligibility (二期 §6): clean image attachments within 5MB; one
	// cover per draft (the unique index on versions is the final backstop).
	extraClause := ""
	if role == "cover" {
		extraClause = ` AND at.media_type LIKE 'image/%' AND at.byte_size <= 5242880
		  AND NOT EXISTS (
			SELECT 1 FROM asset.asset_draft_attachments dc
			WHERE dc.asset_draft_id = d.id AND dc.role = 'cover'
			  AND dc.attachment_id <> at.id
		  )`
	}
	// A changed attachment set (new link or role change) must dirty the
	// draft — commit short-circuits on revision == committed_revision, so
	// without the bump a cover link would never materialize into a version.
	tag, err := s.Store.Pool.Exec(ctx, `
		WITH linked AS (
			INSERT INTO asset.asset_draft_attachments
				(organization_id, workspace_id, asset_draft_id, attachment_id, added_by, role)
			SELECT d.organization_id, d.workspace_id, d.id, at.id, $3::uuid, $5
			FROM asset.attachments at
			JOIN asset.asset_drafts d ON d.organization_id = at.organization_id AND d.workspace_id = at.workspace_id
			JOIN asset.assets a ON a.organization_id = d.organization_id AND a.id = d.asset_id
			WHERE at.organization_id = $1::uuid AND at.id = $2::uuid
			  AND a.id = $4::uuid
			  AND at.deleted_at IS NULL AND at.status = 'clean'
			  AND (at.expires_at IS NULL OR at.expires_at > now())`+extraClause+`
			ON CONFLICT (asset_draft_id, attachment_id) DO UPDATE SET role = EXCLUDED.role
			RETURNING asset_draft_id
		)
		UPDATE asset.asset_drafts d
		SET revision = d.revision + 1
		WHERE d.id IN (SELECT asset_draft_id FROM linked)
	`, principal.OrganizationID, attachmentID, principal.UserID, assetID, role)
	if err != nil {
		return fmt.Errorf("link attachment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// OpenDownload performs the final database authorization check before opening
// the provider stream. Workspace members may download; anyone else only when
// the attachment is materialized on the current published version of a public
// asset. Callers never receive an OSS URL.
func (s Service) OpenDownload(ctx context.Context, principal auth.Principal, attachmentID string) (Download, error) {
	if !validID(attachmentID) {
		return Download{}, ErrNotFound
	}
	if err := s.validateStore(); err != nil {
		return Download{}, err
	}
	if s.Objects == nil {
		return Download{}, errors.New("object store is not initialized")
	}
	var objectKey, filename, mediaType string
	var byteSize int64
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT at.object_key, at.original_filename, at.media_type, at.byte_size
		FROM asset.attachments at
		WHERE at.id = $1::uuid
		  AND at.deleted_at IS NULL
		  AND at.status = 'clean'
		  AND at.organization_id = $2::uuid
		  AND (
			EXISTS (
				SELECT 1 FROM content.workspace_members wm
				WHERE wm.organization_id = at.organization_id
				  AND wm.workspace_id = at.workspace_id
				  AND wm.user_id = $3::uuid
			)
			OR EXISTS (
				SELECT 1
				FROM asset.asset_version_attachments lva
				JOIN asset.asset_versions v ON v.organization_id = lva.organization_id AND v.id = lva.asset_version_id
				JOIN asset.assets a ON a.organization_id = v.organization_id AND a.id = v.asset_id
				WHERE lva.attachment_id = at.id
				  AND a.current_published_version_id = v.id
				  AND a.publication_status = 'published'
				  AND a.deleted_at IS NULL
				  AND a.visibility = 'public'
			)
		  )
	`, attachmentID, principal.OrganizationID, principal.UserID).Scan(&objectKey, &filename, &mediaType, &byteSize)
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

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Now().UTC()
	}
	return parsed
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

func validID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}
