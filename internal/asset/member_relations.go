package asset

// member_relations.go — container/parent relations for member assets and the
// visibility gate. Visibility is a closed three-value contract; unknown or
// policy-disallowed values fail instead of falling back.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/access"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"

	"github.com/jackc/pgx/v5"
)

// memberAssetVisibility validates the requested visibility against the
// contract and the model policy's allowed set. An empty allowed set means the
// policy did not restrict visibility beyond the contract default (workspace).
// An empty request resolves to the policy's visibility.default when present
// (schema validation guarantees default is a contract value inside allowed);
// otherwise it falls back to workspace.
func memberAssetVisibility(rawPolicy []byte, requested string) (string, error) {
	if requested == "" {
		if len(rawPolicy) > 0 {
			var policy struct {
				Visibility struct {
					Default string `json:"default"`
				} `json:"visibility"`
			}
			if err := jsonUnmarshal(rawPolicy, &policy); err == nil && access.Valid(policy.Visibility.Default) {
				return policy.Visibility.Default, nil
			}
		}
		return access.VisibilityWorkspace, nil
	}
	if !access.Valid(requested) {
		return "", ErrInvalidVisibility
	}
	if len(rawPolicy) > 0 {
		var policy struct {
			Visibility struct {
				Allowed []string `json:"allowed"`
			} `json:"visibility"`
		}
		if err := jsonUnmarshal(rawPolicy, &policy); err == nil && len(policy.Visibility.Allowed) > 0 {
			if !access.Allowed(policy.Visibility.Allowed, requested) {
				return "", ErrInvalidVisibility
			}
		}
	}
	return requested, nil
}

func jsonUnmarshal(raw []byte, target any) error {
	return json.Unmarshal(raw, target)
}

// attachRelationsTx replaces the container and document-parent relations of
// one asset inside the caller's transaction.
func attachRelationsTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, workspaceID, assetID string, containerIDs []string, parentAssetID *string) error {
	cleanIDs, parentID, err := normalizeMemberAssetRelations(containerIDs, parentAssetID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM content.container_assets
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND asset_id = $3::uuid
		  AND NOT (container_id = ANY($4::uuid[]))
	`, principal.OrganizationID, workspaceID, assetID, cleanIDs); err != nil {
		return fmt.Errorf("reset container relations: %w", err)
	}
	for _, containerID := range cleanIDs {
		var ok bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM content.containers
				WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
				  AND (workspace_id IS NULL OR workspace_id = $3::uuid)
			)
		`, principal.OrganizationID, containerID, workspaceID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrInvalidInput
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO content.container_assets
				(organization_id, workspace_id, container_id, asset_id, created_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid)
			ON CONFLICT (container_id, asset_id) DO NOTHING
		`, principal.OrganizationID, workspaceID, containerID, assetID, principal.UserID); err != nil {
			return fmt.Errorf("link container: %w", err)
		}
	}
	if parentID == nil {
		if _, err := tx.Exec(ctx, `
			DELETE FROM content.document_parents
			WHERE organization_id = $1::uuid AND child_asset_id = $2::uuid
		`, principal.OrganizationID, assetID); err != nil {
			return err
		}
		return nil
	}
	if parentID != nil && *parentID == assetID {
		return ErrInvalidInput
	}
	// The parent must be an asset of the same workspace; cycle checks walk the
	// ancestor chain within the workspace scope.
	if err := checkParentCycleTx(ctx, tx, principal.OrganizationID, workspaceID, assetID, *parentID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO content.document_parents
			(organization_id, workspace_id, child_asset_id, parent_asset_id, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid)
		ON CONFLICT (child_asset_id) DO UPDATE
		SET parent_asset_id = EXCLUDED.parent_asset_id, created_by = EXCLUDED.created_by
	`, principal.OrganizationID, workspaceID, assetID, *parentID, principal.UserID); err != nil {
		return fmt.Errorf("link document parent: %w", err)
	}
	return nil
}

func checkParentCycleTx(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, childAssetID, parentAssetID string) error {
	const maxDepth = 32
	current := parentAssetID
	for depth := 0; depth < maxDepth; depth++ {
		if current == childAssetID {
			return ErrInvalidInput
		}
		var next *string
		err := tx.QueryRow(ctx, `
			SELECT parent_asset_id::text FROM content.document_parents
			WHERE organization_id = $1::uuid AND child_asset_id = $2::uuid
			  AND workspace_id = $3::uuid
		`, organizationID, current, workspaceID).Scan(&next)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		if next == nil {
			return nil
		}
		current = *next
	}
	return ErrInvalidInput
}

func normalizeMemberAssetRelations(containerIDs []string, parentID *string) ([]string, *string, error) {
	clean := make([]string, 0, len(containerIDs))
	seen := map[string]bool{}
	for _, raw := range containerIDs {
		id := trim(raw)
		if id == "" {
			continue
		}
		if !validID(id) || seen[id] {
			return nil, nil, ErrInvalidInput
		}
		seen[id] = true
		clean = append(clean, id)
	}
	if parentID != nil {
		trimmed := trim(*parentID)
		if trimmed == "" {
			parentID = nil
		} else if !validID(trimmed) {
			return nil, nil, ErrInvalidInput
		} else {
			parentID = &trimmed
		}
	}
	return clean, parentID, nil
}

func trim(value string) string {
	return strings.TrimSpace(value)
}

// AssetRelationEntry is one edge of the asset relation graph (doc §4): either
// materialized on sealed versions or staged on the shared draft pending a
// commit.
type AssetRelationEntry struct {
	ID                       string    `json:"id"`
	RelationType             string    `json:"relation_type"`
	Direction                string    `json:"direction"`
	Status                   string    `json:"status"`
	Source                   string    `json:"source"`
	CounterpartAssetID       string    `json:"counterpart_asset_id"`
	CounterpartTitle         string    `json:"counterpart_title"`
	CounterpartStatus        string    `json:"counterpart_publication_status"`
	CounterpartVisibility    string    `json:"counterpart_visibility"`
	AssetVersionID           string    `json:"asset_version_id,omitempty"`
	CreatedAt                time.Time `json:"created_at"`
}

// Relations returns the full relation graph around an asset: outgoing and
// incoming materialized edges plus the draft-staged ones awaiting commit.
func (s MemberService) Relations(ctx context.Context, principal auth.Principal, assetID string) ([]AssetRelationEntry, error) {
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
	entries := []AssetRelationEntry{}
	// Outgoing: this asset's versions are the source.
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT r.id::text, r.relation_type, 'outgoing', r.source,
		       ta.id::text, COALESCE(tv.title, ''), ta.publication_status, ta.visibility,
		       r.source_asset_version_id::text, r.created_at
		FROM asset.asset_relations r
		JOIN asset.asset_versions sv ON sv.organization_id = r.organization_id AND sv.id = r.source_asset_version_id
		JOIN asset.assets sa ON sa.organization_id = sv.organization_id AND sa.id = sv.asset_id
		JOIN asset.assets ta ON ta.organization_id = r.organization_id AND ta.id = (
		    SELECT tva.asset_id FROM asset.asset_versions tva
		    WHERE tva.organization_id = r.organization_id AND tva.id = r.target_asset_version_id
		)
		LEFT JOIN asset.asset_versions tv ON tv.organization_id = r.organization_id AND tv.id = ta.current_working_version_id
		WHERE r.organization_id = $1::uuid AND sa.id = $2::uuid AND sa.deleted_at IS NULL
		ORDER BY r.relation_type, ta.id
	`, principal.OrganizationID, assetID)
	if err != nil {
		return nil, fmt.Errorf("load outgoing relations: %w", err)
	}
	for rows.Next() {
		entry, err := scanRelationEntry(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		entry.Status = "materialized"
		entries = append(entries, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Incoming: this asset's versions are the target.
	rows, err = s.Store.Pool.Query(ctx, `
		SELECT r.id::text, r.relation_type, 'incoming', r.source,
		       sa.id::text, COALESCE(sv.title, ''), sa.publication_status, sa.visibility,
		       r.source_asset_version_id::text, r.created_at
		FROM asset.asset_relations r
		JOIN asset.asset_versions tv ON tv.organization_id = r.organization_id AND tv.id = r.target_asset_version_id
		JOIN asset.assets ta ON ta.organization_id = tv.organization_id AND ta.id = tv.asset_id
		JOIN asset.assets sa ON sa.organization_id = r.organization_id AND sa.id = (
		    SELECT sva.asset_id FROM asset.asset_versions sva
		    WHERE sva.organization_id = r.organization_id AND sva.id = r.source_asset_version_id
		)
		LEFT JOIN asset.asset_versions sv ON sv.organization_id = r.organization_id AND sv.id = sa.current_working_version_id
		WHERE r.organization_id = $1::uuid AND ta.id = $2::uuid AND ta.deleted_at IS NULL
		ORDER BY r.relation_type, sa.id
	`, principal.OrganizationID, assetID)
	if err != nil {
		return nil, fmt.Errorf("load incoming relations: %w", err)
	}
	for rows.Next() {
		entry, err := scanRelationEntry(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		entry.Status = "materialized"
		entries = append(entries, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Staged: relations parked on the shared draft, materialized at commit.
	// Staged rows have no surrogate identity — the composite PK is
	// (asset_draft_id, target_asset_id, relation_type) — so they surface with
	// an empty id and status=staged until the commit materializes them.
	rows, err = s.Store.Pool.Query(ctx, `
		SELECT ''::text, dr.relation_type, 'outgoing', dr.source,
		       ta.id::text, COALESCE(tv.title, ''), ta.publication_status, ta.visibility,
		       ''::text, dr.created_at
		FROM asset.asset_draft_relations dr
		JOIN asset.asset_drafts d ON d.organization_id = dr.organization_id AND d.id = dr.asset_draft_id
		JOIN asset.assets sa ON sa.organization_id = dr.organization_id AND sa.id = dr.asset_id
		JOIN asset.assets ta ON ta.organization_id = dr.organization_id AND ta.id = dr.target_asset_id
		LEFT JOIN asset.asset_versions tv ON tv.organization_id = dr.organization_id AND tv.id = ta.current_working_version_id
		WHERE dr.organization_id = $1::uuid AND sa.id = $2::uuid
		ORDER BY dr.relation_type, ta.id
	`, principal.OrganizationID, assetID)
	if err != nil {
		return nil, fmt.Errorf("load staged relations: %w", err)
	}
	for rows.Next() {
		entry, err := scanRelationEntry(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		entry.Status = "staged"
		entries = append(entries, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func scanRelationEntry(row interface{ Scan(...any) error }) (AssetRelationEntry, error) {
	var entry AssetRelationEntry
	if err := row.Scan(&entry.ID, &entry.RelationType, &entry.Direction, &entry.Source,
		&entry.CounterpartAssetID, &entry.CounterpartTitle, &entry.CounterpartStatus, &entry.CounterpartVisibility,
		&entry.AssetVersionID, &entry.CreatedAt); err != nil {
		return AssetRelationEntry{}, fmt.Errorf("scan relation entry: %w", err)
	}
	return entry, nil
}
