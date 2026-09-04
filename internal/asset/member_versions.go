package asset

// member_versions.go — version reads and the shared scan helpers. Versions
// are created only through CreateVersionTx; there is no version PATCH.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"

	"github.com/jackc/pgx/v5"
)

type MemberAssetVersion struct {
	ID                     string         `json:"id"`
	AssetID                string         `json:"asset_id"`
	VersionNo              int64          `json:"version_no"`
	ParentVersionID        *string        `json:"parent_version_id"`
	ResourceModelVersionID string         `json:"resource_model_version_id"`
	Origin                 string         `json:"origin"`
	ConfirmationStatus     string         `json:"confirmation_status"`
	ConfirmedBy            *string        `json:"confirmed_by,omitempty"`
	ConfirmedAt            *time.Time     `json:"confirmed_at,omitempty"`
	Title                  *string        `json:"title"`
	Summary                string         `json:"summary"`
	Markdown               *string        `json:"markdown,omitempty"`
	Fields                 map[string]any `json:"fields"`
	ContentChecksum        string         `json:"content_checksum"`
	Sealed                 bool           `json:"sealed"`
	CreatedBy              string         `json:"created_by"`
	CreatedAt              time.Time      `json:"created_at"`
	ETag                   string         `json:"etag"`
}

const memberVersionColumns = `
	v.id::text, v.asset_id::text, v.version_no, v.parent_version_id::text,
	v.resource_model_version_id::text, v.origin, v.confirmation_status,
	v.confirmed_by::text, v.confirmed_at, v.title, v.summary, v.markdown, v.fields,
	v.content_checksum, (v.sealed_at IS NOT NULL), COALESCE(v.created_by::text, ''), v.created_at
`

func scanMemberAssetVersion(row interface{ Scan(...any) error }) (MemberAssetVersion, error) {
	var item MemberAssetVersion
	var fields []byte
	if err := row.Scan(&item.ID, &item.AssetID, &item.VersionNo, &item.ParentVersionID,
		&item.ResourceModelVersionID, &item.Origin, &item.ConfirmationStatus,
		&item.ConfirmedBy, &item.ConfirmedAt, &item.Title, &item.Summary, &item.Markdown, &fields,
		&item.ContentChecksum, &item.Sealed, &item.CreatedBy, &item.CreatedAt); err != nil {
		return MemberAssetVersion{}, fmt.Errorf("scan member asset version: %w", err)
	}
	item.Fields = ensureMap(fields)
	item.ETag = item.ContentChecksum
	return item, nil
}

func ensureMap(raw []byte) map[string]any {
	result := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}

// ListVersions returns the immutable version history of one asset.
func (s MemberService) ListVersions(ctx context.Context, principal auth.Principal, assetID string) ([]MemberAssetVersion, error) {
	if !validID(assetID) {
		return nil, ErrInvalidInput
	}
	if err := s.requireAssetRead(ctx, principal, assetID); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT `+memberVersionColumns+`
		FROM asset.asset_versions v
		JOIN asset.assets a ON a.organization_id = v.organization_id AND a.id = v.asset_id
		WHERE v.organization_id = $1::uuid AND v.asset_id = $2::uuid
		ORDER BY v.version_no DESC
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

// GetVersion reads one version; it must belong to an asset the caller can
// read. Version history is workspace-member territory.
func (s MemberService) GetVersion(ctx context.Context, principal auth.Principal, versionID string) (MemberAssetVersion, error) {
	if !validID(versionID) {
		return MemberAssetVersion{}, ErrInvalidInput
	}
	var assetID string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT asset_id::text FROM asset.asset_versions WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, versionID).Scan(&assetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberAssetVersion{}, ErrNotFound
	}
	if err != nil {
		return MemberAssetVersion{}, err
	}
	if err := s.requireAssetRead(ctx, principal, assetID); err != nil {
		return MemberAssetVersion{}, err
	}
	row := s.Store.Pool.QueryRow(ctx, `
		SELECT `+memberVersionColumns+`
		FROM asset.asset_versions v
		WHERE v.organization_id = $1::uuid AND v.id = $2::uuid
	`, principal.OrganizationID, versionID)
	item, err := scanMemberAssetVersion(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberAssetVersion{}, ErrNotFound
	}
	return item, err
}

func (s MemberService) requireAssetRead(ctx context.Context, principal auth.Principal, assetID string) error {
	var workspaceID, modelID string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT workspace_id::text, resource_model_id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, assetID).Scan(&workspaceID, &modelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = s.require(ctx, principal, workspaceID, modelID, authz.ActionAssetRead)
	return err
}

func loadVersionTagIDs(ctx context.Context, tx pgx.Tx, versionID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT tag_id::text FROM asset.asset_version_tags WHERE asset_version_id = $1::uuid ORDER BY tag_id`, versionID)
	if err != nil {
		return nil, fmt.Errorf("load version tags: %w", err)
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

// loadVersionCoverID returns the version's cover attachment id and its alt
// text (二期 §6 cover; G6 alt rides the same inheritance path).
func loadVersionCoverID(ctx context.Context, tx pgx.Tx, versionID string) (string, string, error) {
	var cover, alt string
	err := tx.QueryRow(ctx, `
		SELECT attachment_id::text, alt_text FROM asset.asset_version_attachments
		WHERE asset_version_id = $1::uuid AND role = 'cover'
		LIMIT 1
	`, versionID).Scan(&cover, &alt)
	if err == nil || errors.Is(err, pgx.ErrNoRows) {
		return cover, alt, nil
	}
	return "", "", fmt.Errorf("load version cover: %w", err)
}

func loadVersionAttachmentIDs(ctx context.Context, tx pgx.Tx, versionID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT attachment_id::text FROM asset.asset_version_attachments WHERE asset_version_id = $1::uuid ORDER BY attachment_id`, versionID)
	if err != nil {
		return nil, fmt.Errorf("load version attachments: %w", err)
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
