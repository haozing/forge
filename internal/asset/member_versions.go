package asset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"github.com/jackc/pgx/v5"
)

type MemberAssetVersion struct {
	ID                   string         `json:"id"`
	AssetID              string         `json:"asset_id"`
	VersionNo            int            `json:"version_no"`
	ParentVersionID      *string        `json:"parent_version_id,omitempty"`
	ResourceModelVersion string         `json:"resource_model_version_id"`
	WorkflowStatus       string         `json:"workflow_status"`
	Quality              string         `json:"quality"`
	ReviewStatus         string         `json:"review_status"`
	Title                *string        `json:"title,omitempty"`
	Markdown             *string        `json:"markdown,omitempty"`
	Fields               map[string]any `json:"fields"`
	Tags                 []string       `json:"tags"`
	Source               map[string]any `json:"source"`
	ContentChecksum      string         `json:"content_checksum"`
	CreatedBy            string         `json:"created_by"`
	CreatedAt            time.Time      `json:"created_at"`
	ETag                 string         `json:"etag"`
}

type MemberAssetVersionInput struct {
	Title    *string        `json:"title"`
	Markdown *string        `json:"markdown"`
	Fields   map[string]any `json:"fields"`
	Tags     []string       `json:"tags"`
	Source   map[string]any `json:"source"`
}

func (s MemberService) ListVersions(ctx context.Context, principal auth.Principal, assetID string) ([]MemberAssetVersion, error) {
	current, err := s.Get(ctx, principal, assetID)
	if err != nil {
		return nil, err
	}
	if _, err := s.require(ctx, principal, current.WorkspaceID, current.ResourceModelID, "asset.read"); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, asset_id::text, version_no, parent_version_id::text, resource_model_version_id::text,
		       workflow_status, quality, review_status, title, markdown, fields, tags, source,
		       content_checksum, created_by::text, created_at
		FROM asset.asset_versions
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
		ORDER BY version_no DESC, id
	`, principal.OrganizationID, assetID)
	if err != nil {
		return nil, fmt.Errorf("list asset versions: %w", err)
	}
	defer rows.Close()
	items := make([]MemberAssetVersion, 0)
	for rows.Next() {
		item, err := scanMemberAssetVersion(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s MemberService) GetVersion(ctx context.Context, principal auth.Principal, versionID string) (MemberAssetVersion, error) {
	if !validID(versionID) {
		return MemberAssetVersion{}, ErrInvalidInput
	}
	var workspaceID, modelID string
	err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id::text, resource_model_id::text FROM asset.asset_versions WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, versionID).Scan(&workspaceID, &modelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberAssetVersion{}, ErrNotFound
	}
	if err != nil {
		return MemberAssetVersion{}, err
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, "asset.read"); err != nil {
		return MemberAssetVersion{}, err
	}
	row := s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, asset_id::text, version_no, parent_version_id::text, resource_model_version_id::text,
		       workflow_status, quality, review_status, title, markdown, fields, tags, source,
		       content_checksum, created_by::text, created_at
		FROM asset.asset_versions WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, versionID)
	return scanMemberAssetVersion(row)
}

func (s MemberService) CreateVersion(ctx context.Context, principal auth.Principal, assetID, idempotencyKey string, input MemberAssetVersionInput) (MemberAssetVersion, error) {
	if !validID(assetID) || len(strings.TrimSpace(idempotencyKey)) < 16 {
		return MemberAssetVersion{}, ErrInvalidInput
	}
	current, err := s.Get(ctx, principal, assetID)
	if err != nil {
		return MemberAssetVersion{}, err
	}
	assetItem, err := s.Get(ctx, principal, assetID)
	if err != nil {
		return MemberAssetVersion{}, err
	}
	if _, err := s.require(ctx, principal, assetItem.WorkspaceID, assetItem.ResourceModelID, "asset.write"); err != nil {
		return MemberAssetVersion{}, err
	}
	if input.Fields == nil {
		input.Fields = current.Fields
	}
	if input.Tags == nil {
		input.Tags = current.Tags
	}
	if input.Source == nil {
		input.Source = current.Source
	}
	if input.Title == nil {
		input.Title = current.Title
	}
	if input.Markdown == nil {
		input.Markdown = current.Markdown
	}
	checksum := versionChecksum(input)
	var item MemberAssetVersion
	var fields, tags, source []byte
	err = s.Store.Pool.QueryRow(ctx, `
		WITH next_version AS (
			SELECT COALESCE(max(version_no), 0) + 1 AS version_no
			FROM asset.asset_versions WHERE organization_id = $1::uuid AND asset_id = $2::uuid
		)
		INSERT INTO asset.asset_versions
			(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id, version_no,
			 workflow_status, quality, title, markdown, fields, tags, source, parent_version_id, content_checksum, created_by)
		SELECT $1::uuid, a.workspace_id, a.id, a.resource_model_id, a.resource_model_version_id, n.version_no,
		       'draft', 'raw', $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, a.current_working_version_id, $8, $9::uuid
		FROM asset.assets a
		JOIN asset.asset_versions current_version ON current_version.id = a.current_working_version_id,
		next_version n
		WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
		RETURNING id::text, asset_id::text, version_no, parent_version_id::text, resource_model_version_id::text,
		          workflow_status, quality, review_status, title, markdown, fields, tags, source,
		          content_checksum, created_by::text, created_at
	`, principal.OrganizationID, assetID, input.Title, input.Markdown, mustJSON(input.Fields), mustJSON(input.Tags), mustJSON(input.Source), checksum, principal.UserID).Scan(
		&item.ID, &item.AssetID, &item.VersionNo, &item.ParentVersionID, &item.ResourceModelVersion, &item.WorkflowStatus, &item.Quality, &item.ReviewStatus, &item.Title, &item.Markdown, &fields, &tags, &source, &item.ContentChecksum, &item.CreatedBy, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberAssetVersion{}, ErrNotFound
	}
	if err != nil {
		return MemberAssetVersion{}, fmt.Errorf("create asset version: %w", err)
	}
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE asset.assets SET current_working_version_id = $2::uuid, publication_status = 'draft', updated_at = now() WHERE organization_id = $1::uuid AND id = $3::uuid`, principal.OrganizationID, item.ID, assetID); err != nil {
		return MemberAssetVersion{}, err
	}
	item.Fields = ensureMap(fields)
	item.Tags = ensureStrings(tags)
	item.Source = ensureMap(source)
	item.ETag = item.ContentChecksum
	return item, nil
}

func (s MemberService) UpdateVersion(ctx context.Context, principal auth.Principal, versionID, expectedETag, idempotencyKey string, input MemberAssetVersionInput) (MemberAssetVersion, error) {
	if !validID(versionID) || len(strings.TrimSpace(idempotencyKey)) < 16 || strings.TrimSpace(expectedETag) == "" {
		return MemberAssetVersion{}, ErrInvalidInput
	}
	current, err := s.GetVersion(ctx, principal, versionID)
	if err != nil {
		return MemberAssetVersion{}, err
	}
	if strings.Trim(expectedETag, "\"") != current.ContentChecksum {
		return MemberAssetVersion{}, ErrConflict
	}
	if current.WorkflowStatus != "draft" {
		return MemberAssetVersion{}, ErrConflict
	}
	if input.Fields == nil {
		input.Fields = current.Fields
	}
	if input.Tags == nil {
		input.Tags = current.Tags
	}
	if input.Source == nil {
		input.Source = current.Source
	}
	if input.Title == nil {
		input.Title = current.Title
	}
	if input.Markdown == nil {
		input.Markdown = current.Markdown
	}
	checksum := versionChecksum(input)
	_, err = s.Store.Pool.Exec(ctx, `
		UPDATE asset.asset_versions SET title = $3, markdown = $4, fields = $5::jsonb, tags = $6::jsonb,
		 source = $7::jsonb, content_checksum = $8, review_status = 'none'
		WHERE organization_id = $1::uuid AND id = $2::uuid AND content_checksum = $9 AND workflow_status = 'draft'
	`, principal.OrganizationID, versionID, input.Title, input.Markdown, mustJSON(input.Fields), mustJSON(input.Tags), mustJSON(input.Source), checksum, strings.Trim(expectedETag, "\""))
	if err != nil {
		return MemberAssetVersion{}, err
	}
	return s.GetVersion(ctx, principal, versionID)
}

func scanMemberAssetVersion(row interface{ Scan(...any) error }) (MemberAssetVersion, error) {
	var item MemberAssetVersion
	var fields, tags, source []byte
	err := row.Scan(&item.ID, &item.AssetID, &item.VersionNo, &item.ParentVersionID, &item.ResourceModelVersion, &item.WorkflowStatus, &item.Quality, &item.ReviewStatus, &item.Title, &item.Markdown, &fields, &tags, &source, &item.ContentChecksum, &item.CreatedBy, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberAssetVersion{}, ErrNotFound
	}
	if err != nil {
		return MemberAssetVersion{}, err
	}
	item.Fields = ensureMap(fields)
	item.Tags = ensureStrings(tags)
	item.Source = ensureMap(source)
	item.ETag = item.ContentChecksum
	return item, nil
}

func versionChecksum(input MemberAssetVersionInput) string {
	body, _ := json.Marshal(struct {
		Title    *string        `json:"title"`
		Markdown *string        `json:"markdown"`
		Fields   map[string]any `json:"fields"`
		Tags     []string       `json:"tags"`
		Source   map[string]any `json:"source"`
	}{input.Title, input.Markdown, input.Fields, input.Tags, input.Source})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func ensureMap(raw []byte) map[string]any {
	result := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}
func ensureStrings(raw []byte) []string {
	result := []string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}
