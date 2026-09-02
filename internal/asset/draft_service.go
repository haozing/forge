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
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"

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
	// An empty expected revision or the If-Match wildcard "*" only demands the
	// draft exists; any concrete revision must match exactly.
	if expectedRevision != "" && !ifMatchAnyRevision(expectedRevision) {
		var expected int64
		if _, err := fmt.Sscanf(expectedRevision, "%d", &expected); err != nil || expected != draft.Revision {
			return draft, ErrDraftRevisionMismatch
		}
	}
	return draft, nil
}

var ErrDraftRevisionMismatch = errors.New("draft revision mismatch")

// ifMatchAnyRevision reports whether an expected-revision token is the If-Match
// wildcard "*" (bare or ETag-quoted): the optimistic revision equality check is
// then skipped because the precondition only requires the row to exist.
func ifMatchAnyRevision(expectedRevision string) bool {
	return strings.Trim(expectedRevision, "\"") == "*"
}

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
	if patch.Visibility != nil {
		// An explicit visibility must pass the contract and the bound model
		// policy's allow-list; invalid values fail loudly instead of being
		// silently dropped by the update guard below.
		if !access.Valid(*patch.Visibility) {
			return Draft{}, ErrInvalidVisibility
		}
		policyRaw, err := loadModelPolicyTx(ctx, tx, principal.OrganizationID, row.ResourceModelID)
		if err != nil {
			return Draft{}, err
		}
		if _, err := memberAssetVisibility(policyRaw, *patch.Visibility); err != nil {
			return Draft{}, ErrInvalidVisibility
		}
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
	if patch.Visibility != nil && *patch.Visibility != row.Visibility {
		if _, err := tx.Exec(ctx, `
			UPDATE asset.assets SET visibility = $3, revision = revision + 1, updated_at = now()
			WHERE organization_id = $1::uuid AND id = $2::uuid
		`, row.OrganizationID, row.ID, *patch.Visibility); err != nil {
			return Draft{}, fmt.Errorf("update asset visibility: %w", err)
		}
		// Visibility is an Asset fact: emit the domain event in the same commit
		// so downstream scope compilers react; the lifecycle revision already
		// moved with the UPDATE above.
		next := row
		next.Visibility = *patch.Visibility
		next.Revision++
		if err := AppendAssetEventTx(ctx, tx, s.Events, next, principal, eventing.EventAssetVisibilityChanged, eventing.PayloadVersionV1, eventing.AssetVisibilityChangedPayload{
			AssetID:            row.ID,
			Visibility:         *patch.Visibility,
			PublishedVersionID: derefOrEmpty(row.CurrentPublishedVersionID),
		}); err != nil {
			return Draft{}, err
		}
		RecordAssetAuditTx(ctx, tx, row.OrganizationID, row.WorkspaceID, principal, "asset.visibility_changed", row.ID, map[string]any{
			"workspace_id": row.WorkspaceID,
			"visibility":   *patch.Visibility,
		})
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

// RelationMaterial is one out-edge that materializes with the version. The
// target resolves to the target asset's current working version at materiali-
// zation time; AI-suggested edges carry their confidence, citation and
// originating suggestion identity for the backfill.
type RelationMaterial struct {
	TargetAssetID string
	RelationType  string
	Confidence    float64
	Citation      []byte
	SuggestionID  string
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
	// TagSource is the fallback provenance for version tag relations when the
	// draft carries none of its own (import and webhook channels materialize
	// the first version before their draft exists). Empty means "manual".
	TagSource        string
	AttachmentIDs    []string           // must reference clean, unexpired attachments
	// CoverAttachmentID marks one of AttachmentIDs as the version cover
	// (二期 §6); it flows into asset_version_attachments.role.
	CoverAttachmentID string
	Relations        []RelationMaterial // materialize as asset_relations rows (source='ai')
	SourceRawInputID string
	CreatedBy        string
}

// CreateVersionTx is the only version factory. It locks the asset, appends
// the sealed snapshot, materializes draft provenance for tags/attachments/
// relations, advances the working pointer and bumps the asset revision. The
// caller owns the surrounding transaction and any asset lifecycle change.
// Materialized relation identities stay internal; use createVersionTx when a
// commit path needs to backfill suggestion rows.
func CreateVersionTx(ctx context.Context, tx pgx.Tx, material VersionMaterial) (string, int64, error) {
	versionID, versionNo, _, err := createVersionTx(ctx, tx, material)
	return versionID, versionNo, err
}

// materializedRelation pairs one relation input with the asset_relations row
// it produced; an empty ID marks a skipped or already-present edge.
type materializedRelation struct {
	material RelationMaterial
	id       string
}

// createVersionTx is CreateVersionTx plus the materialized relation
// identities, which the draft commit uses to backfill
// asset_relation_suggestions.materialized_relation_id.
func createVersionTx(ctx context.Context, tx pgx.Tx, material VersionMaterial) (string, int64, []materializedRelation, error) {
	if !ValidOrigin(material.Origin) {
		return "", 0, nil, ErrInvalidOrigin
	}
	if material.ConfirmationStatus == "" {
		material.ConfirmationStatus = ConfirmationUnconfirmed
	}
	if !ValidConfirmation(material.ConfirmationStatus) {
		return "", 0, nil, ErrInvalidConfirmation
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
			return "", 0, nil, fmt.Errorf("verify version attachments: %w", err)
		}
		if bad > 0 {
			return "", 0, nil, ErrAttachmentNotClean
		}
	}
	// Tags must be workspace-scoped identities; archived tags are rejected on
	// commit because they can no longer enter new versions.
	tagIDs := dedupeSort(material.TagIDs)
	if len(tagIDs) > MaxTagsPerDraft {
		return "", 0, nil, ErrTooManyTags
	}
	if len(tagIDs) > 0 {
		var bad int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM asset.tags
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
			  AND id = ANY($3::uuid[]) AND status <> 'active'
		`, material.OrganizationID, material.WorkspaceID, tagIDs).Scan(&bad); err != nil {
			return "", 0, nil, fmt.Errorf("verify version tags: %w", err)
		}
		if bad > 0 {
			return "", 0, nil, ErrTagArchived
		}
	}

	// Lock the asset row to serialize version_no allocation. The working
	// pointer is NULL for a brand-new asset: the deferred pointer constraint
	// lets this transaction insert asset, version and draft in order.
	var workingVersion *string
	var revision int64
	err := tx.QueryRow(ctx, `
		SELECT current_working_version_id::text, revision FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid FOR UPDATE
	`, material.OrganizationID, material.AssetID).Scan(&workingVersion, &revision)
	if err != nil {
		return "", 0, nil, fmt.Errorf("lock asset for version: %w", err)
	}
	parent := material.ParentVersionID
	if parent == "" && workingVersion != nil {
		parent = *workingVersion
	}
	var nextNo int64
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(max(version_no), 0) + 1 FROM asset.asset_versions WHERE asset_id = $1::uuid
	`, material.AssetID).Scan(&nextNo)
	if err != nil {
		return "", 0, nil, fmt.Errorf("allocate version number: %w", err)
	}
	fieldsJSON, err := json.Marshal(material.Fields)
	if err != nil {
		return "", 0, nil, fmt.Errorf("encode version fields: %w", err)
	}
	checksum := ContentChecksum(material.Title, material.Summary, material.Markdown, material.Fields, tagIDs, material.AttachmentIDs)
	var versionID string
	err = tx.QueryRow(ctx, `
		INSERT INTO asset.asset_versions
			(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id,
			 version_no, origin, confirmation_status, confirmed_by, confirmed_at, title, summary, markdown, fields,
			 source_raw_input_id, parent_version_id, content_checksum, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8,
			CASE WHEN $8 = 'human_confirmed' THEN NULLIF($16,'')::uuid END,
			CASE WHEN $8 = 'human_confirmed' THEN now() END,
			$9, $10, $11, $12::jsonb,
			NULLIF($13,'')::uuid, NULLIF($14,'')::uuid, $15, NULLIF($16,'')::uuid)
		RETURNING id::text
	`, material.OrganizationID, material.WorkspaceID, material.AssetID, material.ResourceModelID,
		material.ResourceModelVersionID, nextNo, material.Origin, material.ConfirmationStatus,
		material.Title, material.Summary, material.Markdown, string(fieldsJSON),
		material.SourceRawInputID, parent, checksum, material.CreatedBy).Scan(&versionID)
	if err != nil {
		return "", 0, nil, fmt.Errorf("insert version: %w", err)
	}
	// Materialize version provenance only through the sealed boundary. Draft
	// relations carry their source and confidence into the snapshot; channels
	// without a draft yet fall back to the material's channel source.
	tagSource := material.TagSource
	if tagSource == "" {
		tagSource = tag.SourceManual
	}
	for _, tagID := range tagIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO asset.asset_version_tags
				(organization_id, workspace_id, asset_version_id, tag_id, source, confidence, created_by)
			SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid,
			       COALESCE(dt.source, $7), dt.confidence, NULLIF($5,'')::uuid
			FROM asset.asset_drafts d
			LEFT JOIN asset.asset_draft_tags dt ON dt.organization_id = d.organization_id AND dt.asset_draft_id = d.id AND dt.tag_id = $4::uuid
			WHERE d.organization_id = $1::uuid AND d.asset_id = $6::uuid
			ON CONFLICT DO NOTHING
		`, material.OrganizationID, material.WorkspaceID, versionID, tagID, material.CreatedBy, material.AssetID, tagSource); err != nil {
			return "", 0, nil, fmt.Errorf("insert version tag: %w", err)
		}
	}
	for _, attachmentID := range dedupeSort(material.AttachmentIDs) {
		role := "body"
		if attachmentID == material.CoverAttachmentID {
			role = "cover"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO asset.asset_version_attachments (organization_id, workspace_id, asset_version_id, attachment_id, created_by, role)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, NULLIF($5,'')::uuid, $6)
			ON CONFLICT DO NOTHING
		`, material.OrganizationID, material.WorkspaceID, versionID, attachmentID, material.CreatedBy, role); err != nil {
			return "", 0, nil, fmt.Errorf("insert version attachment: %w", err)
		}
	}
	// Relations materialize before the snapshot seals: the sealed-endpoint
	// guard only constrains the source side, and the source (this version) is
	// still unsealed here. Targets resolve to their asset's current working
	// version, which is necessarily an already-sealed snapshot.
	relations, err := materializeVersionRelationsTx(ctx, tx, material, versionID)
	if err != nil {
		return "", 0, nil, err
	}
	// Seal the snapshot inside the creating transaction; the deferred trigger
	// refuses to commit an unsealed version.
	if _, err := tx.Exec(ctx, `
		UPDATE asset.asset_versions SET sealed_at = now() WHERE id = $1::uuid
	`, versionID); err != nil {
		return "", 0, nil, fmt.Errorf("seal version: %w", err)
	}
	// Advance the working pointer and the asset revision.
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET current_working_version_id = $3::uuid, revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, material.OrganizationID, material.AssetID, versionID); err != nil {
		return "", 0, nil, fmt.Errorf("advance working pointer: %w", err)
	}
	return versionID, nextNo, relations, nil
}

// materializeVersionRelationsTx writes the version's out-edges (source='ai')
// against the target assets' current working versions. A target without a
// working pointer, a deleted or cross-workspace target, or an edge that
// already exists is skipped and reported with an empty identity — a commit
// never fails because a suggested target stopped being relatable.
func materializeVersionRelationsTx(ctx context.Context, tx pgx.Tx, material VersionMaterial, versionID string) ([]materializedRelation, error) {
	relations, err := normalizeRelationMaterials(material.Relations)
	if err != nil {
		return nil, err
	}
	materialized := make([]materializedRelation, 0, len(relations))
	for _, relation := range relations {
		var targetVersionID string
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(current_working_version_id::text, '')
			FROM asset.assets
			WHERE organization_id = $1::uuid AND id = $2::uuid AND workspace_id = $3::uuid
			  AND deleted_at IS NULL AND id <> $4::uuid
		`, material.OrganizationID, relation.TargetAssetID, material.WorkspaceID, material.AssetID).Scan(&targetVersionID)
		if errors.Is(err, pgx.ErrNoRows) {
			materialized = append(materialized, materializedRelation{material: relation})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve relation target: %w", err)
		}
		if targetVersionID == "" {
			materialized = append(materialized, materializedRelation{material: relation})
			continue
		}
		citation := relation.Citation
		if len(citation) == 0 {
			citation = []byte("{}")
		}
		var relationID string
		err = tx.QueryRow(ctx, `
			INSERT INTO asset.asset_relations
				(organization_id, source_asset_version_id, target_asset_version_id,
				 relation_type, source, confidence, citation, suggestion_id, created_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'ai', $5, $6::jsonb, NULLIF($7, '')::uuid, $8::uuid)
			ON CONFLICT (source_asset_version_id, target_asset_version_id, relation_type) DO NOTHING
			RETURNING id::text
		`, material.OrganizationID, versionID, targetVersionID, relation.RelationType,
			relation.Confidence, string(citation), relation.SuggestionID, material.CreatedBy).Scan(&relationID)
		if errors.Is(err, pgx.ErrNoRows) {
			materialized = append(materialized, materializedRelation{material: relation})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("insert version relation: %w", err)
		}
		materialized = append(materialized, materializedRelation{material: relation, id: relationID})
	}
	return materialized, nil
}

// normalizeRelationMaterials validates relation inputs, drops duplicates (same
// target and type keep the first entry) and sorts by (target, type) so the
// materialization order — and therefore the returned identity list — is
// deterministic.
func normalizeRelationMaterials(relations []RelationMaterial) ([]RelationMaterial, error) {
	normalized := make([]RelationMaterial, 0, len(relations))
	seen := make(map[string]bool, len(relations))
	for _, relation := range relations {
		if !ValidRelationType(relation.RelationType) {
			return nil, ErrInvalidInput
		}
		if !validID(relation.TargetAssetID) {
			return nil, ErrInvalidInput
		}
		key := relation.TargetAssetID + "\x00" + relation.RelationType
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, relation)
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].TargetAssetID != normalized[j].TargetAssetID {
			return normalized[i].TargetAssetID < normalized[j].TargetAssetID
		}
		return normalized[i].RelationType < normalized[j].RelationType
	})
	return normalized, nil
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
	// The routed workspace must own the asset: the require() call above only
	// judged membership in the routed workspace, so a cross-workspace asset id
	// must hide as NotFound instead of leaking existence.
	if row.WorkspaceID != workspaceID {
		return CommitResult{}, ErrNotFound
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
	attachmentIDs, draftCover, err := loadDraftAttachments(ctx, tx, draft.DraftID)
	if err != nil {
		return CommitResult{}, err
	}
	// The draft's explicit cover wins; otherwise the previous version's
	// cover carries into the new snapshot (republishing keeps the image).
	coverID := draftCover
	if coverID == "" && row.CurrentWorkingVersionID != "" {
		coverID, err = loadVersionCoverID(ctx, tx, row.CurrentWorkingVersionID)
		if err != nil {
			return CommitResult{}, err
		}
	}
	relations, err := loadDraftRelationMaterials(ctx, tx, row.OrganizationID, draft.DraftID)
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
		CoverAttachmentID: coverID,
		Relations:       relations,
		CreatedBy:       principal.UserID,
	}
	// A commit that lands any accepted AI suggestion seals as ai_assisted
	// (doc §10: 用户 commit 产生 origin=ai_assisted 未发布版本); a suggestion-free
	// human draft keeps the origin it was created with.
	aiAssisted, err := draftCarriesAcceptedSuggestionsTx(ctx, tx, row.OrganizationID, draft.DraftID)
	if err != nil {
		return CommitResult{}, err
	}
	if aiAssisted {
		material.Origin = OriginAIAssisted
	}
	// The bound immutable model version is the model head at commit time; its
	// field schema gates the snapshot inside the same transaction.
	var schemaBytes []byte
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(m.current_version_id::text, ''), COALESCE(v.field_schema, '{}'::jsonb)
		FROM model.resource_models m
		LEFT JOIN model.resource_model_versions v ON v.organization_id = m.organization_id AND v.id = m.current_version_id
		WHERE m.organization_id = $1::uuid AND m.id = $2::uuid
	`, row.OrganizationID, row.ResourceModelID).Scan(&material.ResourceModelVersionID, &schemaBytes); err != nil {
		return CommitResult{}, fmt.Errorf("resolve model version: %w", err)
	}
	if material.ResourceModelVersionID == "" {
		return CommitResult{}, ErrConflict
	}
	if err := ValidateFields(schemaBytes, material.Fields); err != nil {
		return CommitResult{}, ErrInvalidInput
	}
	// Confirmation never inherits from the base version: a new snapshot is
	// judged afresh.
	material.ConfirmationStatus = ConfirmationUnconfirmed
	versionID, versionNo, materializedRelations, err := createVersionTx(ctx, tx, material)
	if err != nil {
		return CommitResult{}, err
	}
	// Accepted suggestions that actually entered this draft backfill the
	// version that finally carried them; source versions stay immutable.
	// 1. Tags: the tag service keeps its own draft-relation join so only
	//    suggestions whose tag survived on the draft count as materialized.
	if err := (tag.SuggestionService{}).MarkSuggestionsMaterialized(ctx, tx, draft.DraftID, versionID); err != nil {
		return CommitResult{}, fmt.Errorf("materialize tag suggestions: %w", err)
	}
	// 2. Field/summary suggestions.
	if _, err := tx.Exec(ctx, `
		UPDATE asset.asset_field_suggestions
		SET materialized_version_id = $3::uuid
		WHERE organization_id = $1::uuid AND accepted_into_draft_id = $2::uuid
		  AND status = 'accepted'
		  AND materialized_version_id IS DISTINCT FROM $3::uuid
	`, row.OrganizationID, draft.DraftID, versionID); err != nil {
		return CommitResult{}, fmt.Errorf("materialize field suggestions: %w", err)
	}
	// 3. Relation suggestions: createVersionTx returned the edge identities in
	//    input order; skipped edges (empty id) leave the suggestion unbacked
	//    so a later commit can still land it.
	for _, materialized := range materializedRelations {
		if materialized.material.SuggestionID == "" || materialized.id == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE asset.asset_relation_suggestions
			SET materialized_version_id = $3::uuid, materialized_relation_id = $4::uuid
			WHERE organization_id = $1::uuid AND id = $2::uuid
			  AND accepted_into_draft_id = $5::uuid AND status = 'accepted'
		`, row.OrganizationID, materialized.material.SuggestionID, versionID,
			materialized.id, draft.DraftID); err != nil {
			return CommitResult{}, fmt.Errorf("materialize relation suggestions: %w", err)
		}
	}
	// 4. Processing results record the version the user's commit produced.
	//    The precise join is the draft's pre-commit base version: a run read
	//    that version, its accepted suggestions entered this draft, and this
	//    commit is the output. Runs whose input version was already superseded
	//    (an intermediate commit without accepts) keep a NULL output and are
	//    reported by their own lifecycle. This must run before the rebase
	//    below rewrites base_version_id.
	if _, err := tx.Exec(ctx, `
		UPDATE integration.agent_processing_results
		SET output_version_id = $3::uuid
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
		  AND output_version_id IS NULL
		  AND input_version_id = (
		      SELECT base_version_id FROM asset.asset_drafts
		      WHERE organization_id = $1::uuid AND id = $4::uuid
		  )
	`, row.OrganizationID, row.ID, versionID, draft.DraftID); err != nil {
		return CommitResult{}, fmt.Errorf("backfill processing results: %w", err)
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

// draftCarriesAcceptedSuggestionsTx reports whether any accepted suggestion is
// attached to this draft: a parked relation (asset_draft_relations only holds
// accepted AI relations), an ai-source draft tag, or a field/summary
// suggestion accepted into this draft. It feeds the commit's origin choice
// (ai_assisted vs. the draft's original origin).
func draftCarriesAcceptedSuggestionsTx(ctx context.Context, tx pgx.Tx, organizationID, draftID string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM asset.asset_draft_relations
		               WHERE organization_id = $1::uuid AND asset_draft_id = $2::uuid)
		    OR EXISTS (SELECT 1 FROM asset.asset_draft_tags
		               WHERE organization_id = $1::uuid AND asset_draft_id = $2::uuid AND source = 'ai')
		    OR EXISTS (SELECT 1 FROM asset.asset_field_suggestions
		               WHERE organization_id = $1::uuid AND accepted_into_draft_id = $2::uuid
		                 AND status = 'accepted')
	`, organizationID, draftID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("load draft suggestion adoption: %w", err)
	}
	return ok, nil
}

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
	ids, _, err := loadDraftAttachments(ctx, tx, draftID)
	return ids, err
}

// loadDraftAttachments returns the draft's attachment ids and its explicit
// cover id (二期 §6: the cover rides the commit into the sealed version).
func loadDraftAttachments(ctx context.Context, tx pgx.Tx, draftID string) ([]string, string, error) {
	rows, err := tx.Query(ctx, `SELECT attachment_id::text, role FROM asset.asset_draft_attachments WHERE asset_draft_id = $1::uuid ORDER BY attachment_id`, draftID)
	if err != nil {
		return nil, "", fmt.Errorf("load draft attachments: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	cover := ""
	for rows.Next() {
		var id, role string
		if err := rows.Scan(&id, &role); err != nil {
			return nil, "", err
		}
		ids = append(ids, id)
		if role == "cover" {
			cover = id
		}
	}
	return ids, cover, rows.Err()
}

// loadDraftRelationMaterials reads the AI relations accepted onto the draft,
// in deterministic (target, type) order, for the commit's VersionMaterial.
// Only source='ai' rows live in asset_draft_relations (phase 4 decision 2);
// a row without confidence carries no AI provenance and does not materialize.
func loadDraftRelationMaterials(ctx context.Context, tx pgx.Tx, organizationID, draftID string) ([]RelationMaterial, error) {
	rows, err := tx.Query(ctx, `
		SELECT target_asset_id::text, relation_type, confidence, citation, COALESCE(suggestion_id::text, '')
		FROM asset.asset_draft_relations
		WHERE organization_id = $1::uuid AND asset_draft_id = $2::uuid
		ORDER BY target_asset_id, relation_type
	`, organizationID, draftID)
	if err != nil {
		return nil, fmt.Errorf("load draft relations: %w", err)
	}
	defer rows.Close()
	materials := []RelationMaterial{}
	for rows.Next() {
		var material RelationMaterial
		var confidence *float64
		if err := rows.Scan(&material.TargetAssetID, &material.RelationType, &confidence, &material.Citation, &material.SuggestionID); err != nil {
			return nil, err
		}
		if confidence == nil {
			continue
		}
		material.Confidence = *confidence
		materials = append(materials, material)
	}
	return materials, rows.Err()
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
		WHERE d.organization_id = $1::uuid AND d.asset_id = $2::uuid
		  AND a.publication_status IN ('draft', 'published', 'archived')
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

// DraftTags returns the sorted tag summaries currently attached to the shared
// draft. It backs the draft representation, where tags ride along with the
// draft payload instead of a separate lookup.
func (s MemberService) DraftTags(ctx context.Context, principal auth.Principal, assetID string) ([]tag.Summary, error) {
	if !validID(assetID) {
		return nil, ErrInvalidInput
	}
	var workspaceID, modelID string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT workspace_id::text, resource_model_id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, principal.OrganizationID, assetID).Scan(&workspaceID, &modelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, authz.ActionAssetRead); err != nil {
		return nil, err
	}
	var draftID string
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT id::text FROM asset.asset_drafts
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
	`, principal.OrganizationID, assetID).Scan(&draftID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return loadDraftTagSummariesPool(ctx, s.Store, draftID)
}

// DraftTagEntry is one tag binding in a replacement request.
type DraftTagEntry struct {
	TagID         string
	Source        string
	Confidence    float64
	HasConfidence bool
}

// SetDraftTags replaces the draft tag set with revision control. Surviving
// tags keep their provenance; new tags use the caller-provided source;
// removed tags are deleted. Archived tags fail the commit-time validation but
// are accepted here only if they were already present (carry-over semantics
// happen at initialization, not edit time).
func (s MemberService) SetDraftTags(ctx context.Context, principal auth.Principal, workspaceID, assetID, expectedRevision string, entries []DraftTagEntry) (Draft, []tag.Summary, error) {
	var modelID string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT resource_model_id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, principal.OrganizationID, assetID).Scan(&modelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Draft{}, nil, ErrNotFound
		}
		return Draft{}, nil, err
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, authz.ActionAssetWrite); err != nil {
		return Draft{}, nil, err
	}
	if len(entries) > MaxTagsPerDraft {
		return Draft{}, nil, ErrTooManyTags
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !validID(entry.TagID) {
			return Draft{}, nil, ErrInvalidInput
		}
		ids = append(ids, entry.TagID)
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Draft{}, nil, err
	}
	defer tx.Rollback(ctx)
	row, err := LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return Draft{}, nil, err
	}
	if row.PublicationStatus == PublicationArchived {
		return Draft{}, nil, ErrAssetArchived
	}
	draft, err := LoadDraftTx(ctx, tx, principal.OrganizationID, assetID, expectedRevision)
	if err != nil {
		return Draft{}, nil, err
	}
	// Validate every incoming tag: same workspace, and active unless it is
	// already on the draft (carry-over from initialization).
	for _, id := range dedupeSort(ids) {
		var ok bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM asset.tags t
				WHERE t.organization_id = $1::uuid AND t.workspace_id = $2::uuid AND t.id = $3::uuid
				  AND (t.status = 'active'
				       OR EXISTS (SELECT 1 FROM asset.asset_draft_tags dt
					        WHERE dt.asset_draft_id = $4::uuid AND dt.tag_id = t.id))
			)
		`, principal.OrganizationID, workspaceID, id, draft.DraftID).Scan(&ok); err != nil {
			return Draft{}, nil, err
		}
		if !ok {
			return Draft{}, nil, ErrTagArchived
		}
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM asset.asset_draft_tags
		WHERE organization_id = $1::uuid AND asset_draft_id = $2::uuid
		  AND NOT (tag_id = ANY($3::uuid[]))
	`, principal.OrganizationID, draft.DraftID, dedupeSort(ids)); err != nil {
		return Draft{}, nil, fmt.Errorf("remove draft tags: %w", err)
	}
	for _, entry := range entries {
		source := entry.Source
		if source == "" {
			source = tag.SourceManual
		}
		if source == tag.SourceAI && !entry.HasConfidence {
			return Draft{}, nil, ErrInvalidInput
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO asset.asset_draft_tags
				(organization_id, workspace_id, asset_draft_id, tag_id, source, confidence, added_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, NULLIF($6::numeric, -1), $7::uuid)
			ON CONFLICT (asset_draft_id, tag_id) DO NOTHING
		`, principal.OrganizationID, workspaceID, draft.DraftID, entry.TagID, source,
			confidenceOrNull(entry), principal.UserID); err != nil {
			return Draft{}, nil, fmt.Errorf("insert draft tag: %w", err)
		}
	}
	if err := persistDraftPatch(ctx, tx, principal.OrganizationID, draft, principal.UserID); err != nil {
		return Draft{}, nil, err
	}
	summaries, err := loadDraftTagSummaries(ctx, tx, draft.DraftID)
	if err != nil {
		return Draft{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Draft{}, nil, err
	}
	draft.Revision++
	draft.Dirty = true
	return draft, summaries, nil
}

func confidenceOrNull(entry DraftTagEntry) float64 {
	if !entry.HasConfidence {
		return -1
	}
	return entry.Confidence
}

// loadDraftTagSummaries returns the sorted TagSummaries of a draft.
func loadDraftTagSummaries(ctx context.Context, tx pgx.Tx, draftID string) ([]tag.Summary, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id::text, t.normalized_key, t.display_name, t.slug, t.status
		FROM asset.asset_draft_tags dt
		JOIN asset.tags t ON t.organization_id = dt.organization_id AND t.id = dt.tag_id
		WHERE dt.asset_draft_id = $1::uuid
		ORDER BY t.normalized_key, t.id
	`, draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := []tag.Summary{}
	for rows.Next() {
		var summary tag.Summary
		if err := rows.Scan(&summary.ID, &summary.Key, &summary.DisplayName, &summary.Slug, &summary.Status); err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// loadDraftTagSummariesPool reads the sorted tag summaries of a draft outside
// an open transaction.
func loadDraftTagSummariesPool(ctx context.Context, db *store.Store, draftID string) ([]tag.Summary, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT t.id::text, t.normalized_key, t.display_name, t.slug, t.status
		FROM asset.asset_draft_tags dt
		JOIN asset.tags t ON t.organization_id = dt.organization_id AND t.id = dt.tag_id
		WHERE dt.asset_draft_id = $1::uuid
		ORDER BY t.normalized_key, t.id
	`, draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := []tag.Summary{}
	for rows.Next() {
		var summary tag.Summary
		if err := rows.Scan(&summary.ID, &summary.Key, &summary.DisplayName, &summary.Slug, &summary.Status); err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// InitializeDraftTagsFromVersion copies version tag relations and their
// provenance into a fresh draft (create/confirm flows).
func initializeDraftTagsFromVersionTx(ctx context.Context, tx pgx.Tx, organizationID, draftID, versionID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO asset.asset_draft_tags
			(organization_id, workspace_id, asset_draft_id, tag_id, source, confidence, added_by)
		SELECT vt.organization_id, vt.workspace_id, $2::uuid, vt.tag_id, vt.source, vt.confidence, vt.created_by
		FROM asset.asset_version_tags vt
		WHERE vt.organization_id = $1::uuid AND vt.asset_version_id = $3::uuid
		ON CONFLICT (asset_draft_id, tag_id) DO NOTHING
	`, organizationID, draftID, versionID)
	return err
}
