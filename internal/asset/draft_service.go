package asset

// draft_service.go — AssetDraft autosave and the single sealed-version
// materializer. Every write path (member edits, direct publish, publication
// submit, import rows, webhook intake, agent candidates, model migrations)
// creates versions through CreateVersionTx; nothing else may INSERT into
// asset.asset_versions, and nothing may UPDATE version content afterwards.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"agentchunzhi/internal/access"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"

	"github.com/jackc/pgx/v5"
)

const (
	MaxTitleRunes    = 500
	MaxMarkdownBytes = 2 << 20
	MaxTagsPerDraft  = 100
)

// Draft is the shared editable working copy of one asset.
type Draft struct {
	AssetID           string         `json:"asset_id"`
	DraftID           string         `json:"-"`
	BaseVersionID     string         `json:"base_version_id"`
	Revision          int64          `json:"revision"`
	CommittedRevision int64          `json:"committed_revision"`
	Title             string         `json:"title"`
	Summary           string         `json:"summary"`
	Markdown          string         `json:"markdown"`
	Fields            map[string]any `json:"fields"`
	Origin            string         `json:"origin"`
	UpdatedBy         string         `json:"updated_by,omitempty"`
	UpdatedAt         time.Time      `json:"updated_at"`
	Dirty             bool           `json:"dirty"`
}

// LoadDraftTx reads the draft FOR UPDATE and verifies the expected revision.
// An empty expectedRevision skips the optimistic check.
func LoadDraftTx(ctx context.Context, tx pgx.Tx, organizationID, assetID, expectedRevision string) (Draft, error) {
	var draft Draft
	var updatedBy *string
	var fields []byte
	err := tx.QueryRow(ctx, `
		SELECT d.id::text, d.asset_id::text, d.base_version_id::text, d.revision, d.committed_revision,
		       d.title, d.summary, d.markdown, d.fields, d.origin, d.updated_by, d.updated_at
		FROM asset.asset_drafts d
		WHERE d.organization_id = $1::uuid AND d.asset_id = $2::uuid
		FOR UPDATE
	`, organizationID, assetID).Scan(&draft.DraftID, &draft.AssetID, &draft.BaseVersionID,
		&draft.Revision, &draft.CommittedRevision, &draft.Title, &draft.Summary,
		&draft.Markdown, &fields, &draft.Origin, &updatedBy, &draft.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, fmt.Errorf("load asset draft: %w", err)
	}
	draft.Fields = ensureMap(fields)
	if updatedBy != nil {
		draft.UpdatedBy = *updatedBy
	}
	draft.Dirty = draft.Revision != draft.CommittedRevision
	if expectedRevision != "" {
		var expected int64
		if _, err := fmt.Sscanf(expectedRevision, "%d", &expected); err != nil || expected != draft.Revision {
			return draft, ErrDraftRevisionMismatch
		}
	}
	return draft, nil
}

var ErrDraftRevisionMismatch = errors.New("draft revision mismatch")

// DraftPatch is an autosave payload. Omitted fields keep their values.
type DraftPatch struct {
	Title      *string         `json:"title"`
	Summary    *string         `json:"summary"`
	Markdown   *string         `json:"markdown"`
	Fields     *map[string]any `json:"fields"`
	Visibility *string         `json:"visibility"`
}

// Autosave applies a draft patch and increments only the draft revision; no
// AssetVersion is created. It requires asset.write and a matching If-Match
// draft revision.
func (s MemberService) AutosaveDraft(ctx context.Context, principal auth.Principal, assetID, expectedRevision string, patch DraftPatch) (Draft, error) {
	if !validID(assetID) {
		return Draft{}, ErrInvalidInput
	}
	var modelID, workspaceID string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT resource_model_id::text, workspace_id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, principal.OrganizationID, assetID).Scan(&modelID, &workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Draft{}, ErrNotFound
		}
		return Draft{}, err
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, authz.ActionAssetWrite); err != nil {
		return Draft{}, err
	}
	if err := validateDraftPatch(patch); err != nil {
		return Draft{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Draft{}, err
	}
	defer tx.Rollback(ctx)
	row, err := LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return Draft{}, err
	}
	if row.PublicationStatus == PublicationArchived {
		return Draft{}, ErrAssetArchived
	}
	draft, err := LoadDraftTx(ctx, tx, principal.OrganizationID, assetID, expectedRevision)
	if err != nil {
		return Draft{}, err
	}
	if patch.Title != nil {
		draft.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.Summary != nil {
		draft.Summary = strings.TrimSpace(*patch.Summary)
	}
	if patch.Markdown != nil {
		draft.Markdown = *patch.Markdown
	}
	if patch.Fields != nil {
		draft.Fields = *patch.Fields
	}
	if err := persistDraftPatch(ctx, tx, principal.OrganizationID, draft, principal.UserID); err != nil {
		return Draft{}, err
	}
	if patch.Visibility != nil && access.Valid(*patch.Visibility) && *patch.Visibility != row.Visibility {
		if _, err := tx.Exec(ctx, `
			UPDATE asset.assets SET visibility = $3, revision = revision + 1, updated_at = now()
			WHERE organization_id = $1::uuid AND id = $2::uuid
		`, row.OrganizationID, row.ID, *patch.Visibility); err != nil {
			return Draft{}, fmt.Errorf("update asset visibility: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func validateDraftPatch(patch DraftPatch) error {
	if patch.Title != nil && len([]rune(strings.TrimSpace(*patch.Title))) > MaxTitleRunes {
		return ErrInvalidInput
	}
	if patch.Markdown != nil && len(*patch.Markdown) > MaxMarkdownBytes {
		return ErrInvalidInput
	}
	return nil
}

func persistDraftPatch(ctx context.Context, tx pgx.Tx, organizationID string, draft Draft, actorID string) error {
	fieldsJSON, err := json.Marshal(draft.Fields)
	if err != nil {
		return fmt.Errorf("encode draft fields: %w", err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE asset.asset_drafts
		SET title = $3, summary = $4, markdown = $5, fields = $6::jsonb,
		    revision = revision + 1, updated_by = $7::uuid, updated_at = now()
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
	`, organizationID, draft.AssetID, draft.Title, draft.Summary, draft.Markdown,
		string(fieldsJSON), actorID)
	if err != nil {
		return fmt.Errorf("autosave draft: %w", err)
	}
	draft.Revision++
	draft.Dirty = true
	return nil
}

// VersionMaterial is the content snapshot a commit turns into an immutable
// AssetVersion. It is produced from the draft, an import row, a webhook
// payload or an agent candidate.
type VersionMaterial struct {
	OrganizationID         string
	WorkspaceID            string
	AssetID                string
	ResourceModelID        string
	ResourceModelVersionID string
	ParentVersionID        string
	Origin                 string
	ConfirmationStatus     string
	Title                  string
	Summary                string
	Markdown               string
	Fields                 map[string]any
	TagIDs                 []string // sorted, workspace-scoped tag identities
	AttachmentIDs          []string // must reference clean, unexpired attachments
	SourceRawInputID       string
	CreatedBy              string
}

// CreateVersionTx is the only version factory. It locks the asset, appends
// the sealed snapshot, materializes draft provenance for tags/attachments,
// advances the working pointer and bumps the asset revision. The caller owns
// the surrounding transaction and any asset lifecycle change.
func CreateVersionTx(ctx context.Context, tx pgx.Tx, material VersionMaterial) (string, int64, error) {
	if !ValidOrigin(material.Origin) {
		return "", 0, ErrInvalidOrigin
	}
	if material.ConfirmationStatus == "" {
		material.ConfirmationStatus = ConfirmationUnconfirmed
	}
	if !ValidConfirmation(material.ConfirmationStatus) {
		return "", 0, ErrInvalidConfirmation
	}
	// Attachments must be clean, unexpired and workspace-scoped.
	if len(material.AttachmentIDs) > 0 {
		var bad int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM asset.attachments
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
			  AND id = ANY($3::uuid[])
			  AND (status <> 'clean' OR deleted_at IS NOT NULL
			       OR (expires_at IS NOT NULL AND expires_at <= now()))
		`, material.OrganizationID, material.WorkspaceID, material.AttachmentIDs).Scan(&bad); err != nil {
			return "", 0, fmt.Errorf("verify version attachments: %w", err)
		}
		if bad > 0 {
			return "", 0, ErrAttachmentNotClean
		}
	}
	// Tags must be workspace-scoped identities; archived tags are rejected on
	// commit because they can no longer enter new versions.
	tagIDs := dedupeSort(material.TagIDs)
	if len(tagIDs) > MaxTagsPerDraft {
		return "", 0, ErrTooManyTags
	}
	if len(tagIDs) > 0 {
		var bad int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM asset.tags
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
			  AND id = ANY($3::uuid[]) AND status <> 'active'
		`, material.OrganizationID, material.WorkspaceID, tagIDs).Scan(&bad); err != nil {
			return "", 0, fmt.Errorf("verify version tags: %w", err)
		}
		if bad > 0 {
			return "", 0, ErrTagArchived
		}
	}

	// Lock the asset row to serialize version_no allocation.
	var workingVersion string
	var revision int64
	err := tx.QueryRow(ctx, `
		SELECT current_working_version_id::text, revision FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid FOR UPDATE
	`, material.OrganizationID, material.AssetID).Scan(&workingVersion, &revision)
	if err != nil {
		return "", 0, fmt.Errorf("lock asset for version: %w", err)
	}
	parent := material.ParentVersionID
	if parent == "" {
		parent = workingVersion
	}
	var nextNo int64
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(max(version_no), 0) + 1 FROM asset.asset_versions WHERE asset_id = $1::uuid
	`, material.AssetID).Scan(&nextNo)
	if err != nil {
		return "", 0, fmt.Errorf("allocate version number: %w", err)
	}
	fieldsJSON, err := json.Marshal(material.Fields)
	if err != nil {
		return "", 0, fmt.Errorf("encode version fields: %w", err)
	}
	checksum := ContentChecksum(material.Title, material.Summary, material.Markdown, material.Fields, tagIDs, material.AttachmentIDs)
	var versionID string
	err = tx.QueryRow(ctx, `
		INSERT INTO asset.asset_versions
			(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id,
			 version_no, origin, confirmation_status, title, summary, markdown, fields,
			 source_raw_input_id, parent_version_id, content_checksum, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8, $9, $10, $11, $12::jsonb,
			NULLIF($13,'')::uuid, NULLIF($14,'')::uuid, $15, NULLIF($16,'')::uuid)
		RETURNING id::text
	`, material.OrganizationID, material.WorkspaceID, material.AssetID, material.ResourceModelID,
		material.ResourceModelVersionID, nextNo, material.Origin, material.ConfirmationStatus,
		material.Title, material.Summary, material.Markdown, string(fieldsJSON),
		material.SourceRawInputID, parent, checksum, material.CreatedBy).Scan(&versionID)
	if err != nil {
		return "", 0, fmt.Errorf("insert version: %w", err)
	}
	// Materialize version provenance only through the sealed boundary.
	for _, tagID := range tagIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO asset.asset_version_tags (organization_id, workspace_id, asset_version_id, tag_id, created_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, NULLIF($5,'')::uuid)
			ON CONFLICT DO NOTHING
		`, material.OrganizationID, material.WorkspaceID, versionID, tagID, material.CreatedBy); err != nil {
			return "", 0, fmt.Errorf("insert version tag: %w", err)
		}
	}
	for _, attachmentID := range dedupeSort(material.AttachmentIDs) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO asset.asset_version_attachments (organization_id, workspace_id, asset_version_id, attachment_id, created_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, NULLIF($5,'')::uuid)
			ON CONFLICT DO NOTHING
		`, material.OrganizationID, material.WorkspaceID, versionID, attachmentID, material.CreatedBy); err != nil {
			return "", 0, fmt.Errorf("insert version attachment: %w", err)
		}
	}
	// Seal the snapshot inside the creating transaction; the deferred trigger
	// refuses to commit an unsealed version.
	if _, err := tx.Exec(ctx, `
		UPDATE asset.asset_versions SET sealed_at = now() WHERE id = $1::uuid
	`, versionID); err != nil {
		return "", 0, fmt.Errorf("seal version: %w", err)
	}
	// Advance the working pointer and the asset revision.
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET current_working_version_id = $3::uuid, revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, material.OrganizationID, material.AssetID, versionID); err != nil {
		return "", 0, fmt.Errorf("advance working pointer: %w", err)
	}
	return versionID, nextNo, nil
}

var (
	ErrAttachmentNotClean = errors.New("version requires clean attachments")
	ErrTagArchived        = errors.New("archived tag cannot enter a new version")
	ErrTooManyTags        = errors.New("too many tags")
)

// CommitDraft turns the draft into a new sealed working version. When the
// draft is clean the current working version is returned unchanged instead of
// creating a duplicate. Pending publication requests are cancelled with
// reason new_version because the working version moved.
func (s MemberService) CommitDraft(ctx context.Context, principal auth.Principal, workspaceID, assetID, expectedDraftRevision string) (CommitResult, error) {
	scope, err := s.require(ctx, principal, workspaceID, "", authz.ActionAssetWrite)
	if err != nil {
		return CommitResult{}, err
	}
	_ = scope
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return CommitResult{}, err
	}
	defer tx.Rollback(ctx)
	row, err := LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return CommitResult{}, err
	}
	if row.PublicationStatus == PublicationArchived {
		return CommitResult{}, ErrAssetArchived
	}
	draft, err := LoadDraftTx(ctx, tx, principal.OrganizationID, assetID, expectedDraftRevision)
	if err != nil {
		return CommitResult{}, err
	}
	result, err := commitDraftTx(ctx, tx, s.Events, principal, row, draft)
	if err != nil {
		return CommitResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CommitResult{}, err
	}
	return result, nil
}

// CommitResult reports the version a commit produced and whether a new
// snapshot was created.
type CommitResult struct {
	VersionID string
	VersionNo int64
	Created   bool
	Asset     LifecycleRow
}

func commitDraftTx(ctx context.Context, tx pgx.Tx, events *eventing.EventStore, principal auth.Principal, row LifecycleRow, draft Draft) (CommitResult, error) {
	if draft.Revision == draft.CommittedRevision {
		return CommitResult{VersionID: row.CurrentWorkingVersionID, Created: false, Asset: row}, nil
	}
	tagIDs, err := loadDraftTagIDs(ctx, tx, draft.DraftID)
	if err != nil {
		return CommitResult{}, err
	}
	attachmentIDs, err := loadDraftAttachmentIDs(ctx, tx, draft.DraftID)
	if err != nil {
		return CommitResult{}, err
	}
	material := VersionMaterial{
		OrganizationID:  row.OrganizationID,
		WorkspaceID:     row.WorkspaceID,
		AssetID:         row.ID,
		ResourceModelID: row.ResourceModelID,
		Origin:          draft.Origin,
		Title:           draft.Title,
		Summary:         draft.Summary,
		Markdown:        draft.Markdown,
		Fields:          draft.Fields,
		TagIDs:          tagIDs,
		AttachmentIDs:   attachmentIDs,
		CreatedBy:       principal.UserID,
	}
	// The bound immutable model version is the model head at commit time.
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(current_version_id::text, '') FROM model.resource_models
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, row.OrganizationID, row.ResourceModelID).Scan(&material.ResourceModelVersionID); err != nil {
		return CommitResult{}, fmt.Errorf("resolve model version: %w", err)
	}
	if material.ResourceModelVersionID == "" {
		return CommitResult{}, ErrConflict
	}
	// Confirmation never inherits from the base version: a new snapshot is
	// judged afresh.
	material.ConfirmationStatus = ConfirmationUnconfirmed
	versionID, versionNo, err := CreateVersionTx(ctx, tx, material)
	if err != nil {
		return CommitResult{}, err
	}
	if _, err := CancelPendingRequestsTx(ctx, tx, row.OrganizationID, row.ID, principal.UserID, reviewCancelReasonNewVersion); err != nil {
		return CommitResult{}, err
	}
	// Re-point the draft at the new snapshot and mark it clean.
	if _, err := tx.Exec(ctx, `
		UPDATE asset.asset_drafts
		SET base_version_id = $3::uuid, committed_revision = revision + 1,
		    revision = revision + 1, updated_by = NULLIF($4,'')::uuid, updated_at = now()
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
	`, row.OrganizationID, row.ID, versionID, principal.UserID); err != nil {
		return CommitResult{}, fmt.Errorf("rebase draft: %w", err)
	}
	next := row
	next.CurrentWorkingVersionID = versionID
	next.Revision++
	if err := AppendAssetEventTx(ctx, tx, events, next, principal, eventing.EventAssetVersionCreated, eventing.PayloadVersionV1, eventing.AssetVersionCreatedPayload{
		AssetID:     row.ID,
		VersionID:   versionID,
		VersionNo:   versionNo,
		WorkspaceID: row.WorkspaceID,
	}); err != nil {
		return CommitResult{}, err
	}
	RecordAssetAuditTx(ctx, tx, row.OrganizationID, row.WorkspaceID, principal, "asset.version.committed", row.ID, map[string]any{
		"workspace_id": row.WorkspaceID,
		"version_id":   versionID,
		"version_no":   versionNo,
	})
	return CommitResult{VersionID: versionID, VersionNo: versionNo, Created: true, Asset: next}, nil
}

const reviewCancelReasonNewVersion = "new_version"

func loadDraftTagIDs(ctx context.Context, tx pgx.Tx, draftID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT tag_id::text FROM asset.asset_draft_tags WHERE asset_draft_id = $1::uuid ORDER BY tag_id`, draftID)
	if err != nil {
		return nil, fmt.Errorf("load draft tags: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadDraftAttachmentIDs(ctx context.Context, tx pgx.Tx, draftID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT attachment_id::text FROM asset.asset_draft_attachments WHERE asset_draft_id = $1::uuid ORDER BY attachment_id`, draftID)
	if err != nil {
		return nil, fmt.Errorf("load draft attachments: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ContentChecksum covers title, summary, markdown, normalized fields, tag
// identities and attachment identities. Sorted inputs make it order-insensitive.
func ContentChecksum(title, summary, markdown string, fields map[string]any, tagIDs, attachmentIDs []string) string {
	hasher := sha256.New()
	writePart := func(value string) {
		hasher.Write([]byte(value))
		hasher.Write([]byte{0})
	}
	writePart(title)
	writePart(summary)
	writePart(markdown)
	fieldsJSON, _ := json.Marshal(fields)
	writePart(string(fieldsJSON))
	sorted := dedupeSort(tagIDs)
	for _, id := range sorted {
		writePart(id)
	}
	writePart("|")
	for _, id := range dedupeSort(attachmentIDs) {
		writePart(id)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func dedupeSort(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// GetDraft returns the current draft state for a member.
func (s MemberService) GetDraft(ctx context.Context, principal auth.Principal, assetID string) (Draft, error) {
	var modelID, workspaceID string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT resource_model_id::text, workspace_id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, principal.OrganizationID, assetID).Scan(&modelID, &workspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Draft{}, ErrNotFound
		}
		return Draft{}, err
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, authz.ActionAssetRead); err != nil {
		return Draft{}, err
	}
	var draft Draft
	var updatedBy *string
	var fields []byte
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT d.asset_id::text, d.base_version_id::text, d.revision, d.committed_revision,
		       d.title, d.summary, d.markdown, d.fields, d.origin, d.updated_by, d.updated_at
		FROM asset.asset_drafts d
		JOIN asset.assets a ON a.organization_id = d.organization_id AND a.id = d.asset_id
		WHERE d.organization_id = $1::uuid AND d.asset_id = $2::uuid AND a.status IN ('active','archived')
	`, principal.OrganizationID, assetID).Scan(&draft.AssetID, &draft.BaseVersionID,
		&draft.Revision, &draft.CommittedRevision, &draft.Title, &draft.Summary,
		&draft.Markdown, &fields, &draft.Origin, &updatedBy, &draft.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, err
	}
	draft.Fields = ensureMap(fields)
	if updatedBy != nil {
		draft.UpdatedBy = *updatedBy
	}
	draft.Dirty = draft.Revision != draft.CommittedRevision
	return draft, nil
}
