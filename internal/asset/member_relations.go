package asset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/auth"

	"github.com/jackc/pgx/v5"
)

func replaceMemberAssetRelationsTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, workspaceID, assetID, contentKind string, rawContainerIDs []string, rawParentID *string) error {
	containerIDs, parentID, err := normalizeMemberAssetRelations(rawContainerIDs, rawParentID)
	if err != nil {
		return err
	}
	if len(containerIDs) > 0 {
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT count(*)::int
			FROM content.containers
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
			  AND id = ANY($3::uuid[]) AND status = 'active'
		`, principal.OrganizationID, workspaceID, containerIDs).Scan(&count); err != nil {
			return fmt.Errorf("validate member asset containers: %w", err)
		}
		if count != len(containerIDs) {
			return ErrInvalidInput
		}
	}
	if parentID != nil {
		if contentKind != "document" {
			return ErrConflict
		}
		var parentKind string
		err := tx.QueryRow(ctx, `
			SELECT rm.content_kind
			FROM asset.assets a
			JOIN model.resource_models rm ON rm.id = a.resource_model_id
			WHERE a.organization_id = $1::uuid AND a.workspace_id = $2::uuid
			  AND a.id = $3::uuid AND a.deleted_at IS NULL
		`, principal.OrganizationID, workspaceID, *parentID).Scan(&parentKind)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidInput
		}
		if err != nil {
			return fmt.Errorf("validate member asset parent: %w", err)
		}
		if parentKind != "document" {
			return ErrConflict
		}
		var cycle bool
		if err := tx.QueryRow(ctx, `
			WITH RECURSIVE ancestors(id) AS (
				SELECT $3::uuid
				UNION ALL
				SELECT dp.parent_asset_id
				FROM content.document_parents dp
				JOIN ancestors current_parent ON dp.child_asset_id = current_parent.id
				WHERE dp.organization_id = $1::uuid AND dp.workspace_id = $2::uuid
			)
			SELECT EXISTS (SELECT 1 FROM ancestors WHERE id = $4::uuid)
		`, principal.OrganizationID, workspaceID, *parentID, assetID).Scan(&cycle); err != nil {
			return fmt.Errorf("check member asset parent cycle: %w", err)
		}
		if cycle {
			return ErrConflict
		}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM content.container_assets WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND asset_id = $3::uuid`, principal.OrganizationID, workspaceID, assetID); err != nil {
		return fmt.Errorf("clear member asset containers: %w", err)
	}
	for _, containerID := range containerIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO content.container_assets (organization_id, workspace_id, container_id, asset_id, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid)`, principal.OrganizationID, workspaceID, containerID, assetID, principal.UserID); err != nil {
			return fmt.Errorf("set member asset container: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM content.document_parents WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND child_asset_id = $3::uuid`, principal.OrganizationID, workspaceID, assetID); err != nil {
		return fmt.Errorf("clear member asset parent: %w", err)
	}
	if parentID != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO content.document_parents (organization_id, workspace_id, child_asset_id, parent_asset_id, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid)`, principal.OrganizationID, workspaceID, assetID, *parentID, principal.UserID); err != nil {
			return fmt.Errorf("set member asset parent: %w", err)
		}
	}
	return nil
}

func normalizeMemberAssetRelations(containerIDs []string, parentID *string) ([]string, *string, error) {
	if len(containerIDs) > 100 {
		return nil, nil, ErrInvalidInput
	}
	seen := make(map[string]struct{}, len(containerIDs))
	result := make([]string, 0, len(containerIDs))
	for _, raw := range containerIDs {
		id := strings.TrimSpace(raw)
		if !validID(id) {
			return nil, nil, ErrInvalidInput
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	if parentID == nil {
		return result, nil, nil
	}
	id := strings.TrimSpace(*parentID)
	if !validID(id) {
		return nil, nil, ErrInvalidInput
	}
	return result, &id, nil
}

func memberAssetVisibility(rawPolicy []byte, requested string) (string, error) {
	var policy struct {
		Visibility struct {
			Default string   `json:"default"`
			Allowed []string `json:"allowed"`
		} `json:"visibility"`
	}
	if err := json.Unmarshal(rawPolicy, &policy); err != nil {
		return "", ErrInvalidInput
	}
	if len(policy.Visibility.Allowed) == 0 {
		policy.Visibility.Allowed = []string{"public", "login", "workspace", "internal", "private"}
	}
	if policy.Visibility.Default == "" {
		policy.Visibility.Default = "workspace"
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = policy.Visibility.Default
	}
	if requested == "" {
		return "", ErrInvalidInput
	}
	for _, allowed := range policy.Visibility.Allowed {
		if requested == allowed {
			return requested, nil
		}
	}
	return "", ErrInvalidInput
}
