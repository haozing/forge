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

	"agentchunzhi/internal/access"
	"agentchunzhi/internal/auth"

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
