package container

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInput = errors.New("invalid container input")
	ErrForbidden    = errors.New("container access denied")
	ErrNotFound     = errors.New("container not found")
	ErrConflict     = errors.New("container conflict")
)

type Service struct {
	Store  *store.Store
	Policy authz.WorkspacePolicy
}

type Item struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	ParentID    *string   `json:"parent_id,omitempty"`
	Name        string    `json:"name"`
	SortKey     string    `json:"sort_key"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	Visibility  string    `json:"visibility"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Children    []Item    `json:"children,omitempty"`
}

type CreateInput struct {
	ParentID   *string `json:"parent_id"`
	Name       string  `json:"name"`
	SortKey    string  `json:"sort_key"`
	Kind       string  `json:"kind"`
	Visibility string  `json:"visibility"`
}
type PatchInput struct {
	Name    *string `json:"name"`
	SortKey *string `json:"sort_key"`
	Status  *string `json:"status"`
}

func (s Service) require(ctx context.Context, principal auth.Principal, workspaceID, action string) error {
	if principal.UserType != "member" || s.Store == nil || s.Store.Pool == nil || s.Policy == nil {
		return ErrForbidden
	}
	_, err := s.Policy.Require(ctx, principal, workspaceID, "", action)
	if errors.Is(err, authz.ErrWorkspaceForbidden) || errors.Is(err, authz.ErrWorkspaceNotFound) {
		return ErrForbidden
	}
	return err
}

func (s Service) Tree(ctx context.Context, principal auth.Principal, workspaceID string) ([]Item, error) {
	if err := s.require(ctx, principal, workspaceID, "container.manage"); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `SELECT id::text, workspace_id::text, parent_id::text, title, sort_key, kind, status, visibility, created_by::text, created_at, updated_at FROM content.containers WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND status = 'active' ORDER BY sort_key, title, id`, principal.OrganizationID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list container tree: %w", err)
	}
	defer rows.Close()
	all := make([]Item, 0)
	byParent := map[string][]Item{}
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.ParentID, &item.Name, &item.SortKey, &item.Kind, &item.Status, &item.Visibility, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan container: %w", err)
		}
		parent := ""
		if item.ParentID != nil {
			parent = *item.ParentID
		}
		byParent[parent] = append(byParent[parent], item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var build func(string) []Item
	build = func(parent string) []Item {
		items := byParent[parent]
		for index := range items {
			items[index].Children = build(items[index].ID)
		}
		return items
	}
	all = build("")
	return all, nil
}

func (s Service) Create(ctx context.Context, principal auth.Principal, workspaceID string, input CreateInput) (Item, error) {
	if err := s.require(ctx, principal, workspaceID, "container.manage"); err != nil {
		return Item{}, err
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Kind = strings.TrimSpace(input.Kind)
	input.Visibility = strings.TrimSpace(input.Visibility)
	// A container is the product-level folder primitive. Keep the storage
	// vocabulary (`custom`) while accepting the documented `folder` alias and
	// omitted kind from older clients.
	if input.Kind == "" || input.Kind == "folder" {
		input.Kind = "custom"
	}
	if input.Name == "" || (input.Kind != "document" && input.Kind != "faq" && input.Kind != "chat" && input.Kind != "note" && input.Kind != "custom") {
		return Item{}, ErrInvalidInput
	}
	if input.Visibility == "" {
		input.Visibility = "workspace"
	}
	if input.Visibility != "public" && input.Visibility != "login" && input.Visibility != "private" && input.Visibility != "workspace" && input.Visibility != "internal" {
		return Item{}, ErrInvalidInput
	}
	if input.ParentID != nil && !validID(*input.ParentID) {
		return Item{}, ErrInvalidInput
	}
	if input.ParentID != nil {
		var parentWorkspace, parentStatus string
		if err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id::text, status FROM content.containers WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, *input.ParentID).Scan(&parentWorkspace, &parentStatus); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Item{}, ErrNotFound
			}
			return Item{}, fmt.Errorf("load container parent: %w", err)
		}
		if parentWorkspace != workspaceID || parentStatus != "active" {
			return Item{}, ErrConflict
		}
	}
	var item Item
	err := s.Store.Pool.QueryRow(ctx, `INSERT INTO content.containers (organization_id, workspace_id, parent_id, kind, title, visibility, created_by) VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4, $5, $6, $7::uuid) RETURNING id::text, workspace_id::text, parent_id::text, title, sort_key, kind, status, visibility, created_by::text, created_at, updated_at`, principal.OrganizationID, workspaceID, optionalID(input.ParentID), input.Kind, input.Name, input.Visibility, principal.UserID).Scan(&item.ID, &item.WorkspaceID, &item.ParentID, &item.Name, &item.SortKey, &item.Kind, &item.Status, &item.Visibility, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return Item{}, fmt.Errorf("create container: %w", err)
	}
	return item, nil
}

func (s Service) Get(ctx context.Context, principal auth.Principal, containerID string) (Item, error) {
	if !validID(containerID) {
		return Item{}, ErrInvalidInput
	}
	var workspaceID string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id::text FROM content.containers WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, containerID).Scan(&workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Item{}, ErrNotFound
		}
		return Item{}, err
	}
	if err := s.require(ctx, principal, workspaceID, "container.manage"); err != nil {
		return Item{}, err
	}
	var item Item
	err := s.Store.Pool.QueryRow(ctx, `SELECT id::text, workspace_id::text, parent_id::text, title, sort_key, kind, status, visibility, created_by::text, created_at, updated_at FROM content.containers WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, containerID).Scan(&item.ID, &item.WorkspaceID, &item.ParentID, &item.Name, &item.SortKey, &item.Kind, &item.Status, &item.Visibility, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	return item, err
}

func (s Service) Patch(ctx context.Context, principal auth.Principal, containerID string, input PatchInput) (Item, error) {
	item, err := s.Get(ctx, principal, containerID)
	if err != nil {
		return Item{}, err
	}
	name, sortKey, status := item.Name, item.SortKey, item.Status
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	if input.SortKey != nil {
		sortKey = strings.TrimSpace(*input.SortKey)
	}
	if input.Status != nil {
		status = strings.TrimSpace(*input.Status)
	}
	if name == "" || (status != "active" && status != "archived") {
		return Item{}, ErrInvalidInput
	}
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE content.containers SET title = $3, sort_key = $4, status = $5, updated_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, containerID, name, sortKey, status); err != nil {
		return Item{}, fmt.Errorf("update container: %w", err)
	}
	return s.Get(ctx, principal, containerID)
}

func (s Service) Delete(ctx context.Context, principal auth.Principal, containerID string) error {
	item, err := s.Get(ctx, principal, containerID)
	if err != nil {
		return err
	}
	var childCount, assetCount int64
	if err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM content.containers WHERE organization_id = $1::uuid AND parent_id = $2::uuid AND status = 'active'`, principal.OrganizationID, item.ID).Scan(&childCount); err != nil {
		return err
	}
	if err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM content.container_assets WHERE organization_id = $1::uuid AND container_id = $2::uuid`, principal.OrganizationID, item.ID).Scan(&assetCount); err != nil {
		return err
	}
	if childCount > 0 || assetCount > 0 {
		return fmt.Errorf("%w: container_not_empty", ErrConflict)
	}
	_, err = s.Store.Pool.Exec(ctx, `UPDATE content.containers SET status = 'archived', updated_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, containerID)
	return err
}

func (s Service) Assets(ctx context.Context, principal auth.Principal, containerID string) ([]string, error) {
	item, err := s.Get(ctx, principal, containerID)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `SELECT asset_id::text FROM content.container_assets WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND container_id = $3::uuid ORDER BY created_at, asset_id`, principal.OrganizationID, item.WorkspaceID, containerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (s Service) MoveAsset(ctx context.Context, principal auth.Principal, workspaceID, assetID, containerID, operation, idempotencyKey string) error {
	if err := s.require(ctx, principal, workspaceID, "container.manage"); err != nil {
		return err
	}
	if !validID(assetID) || !validID(containerID) || len(strings.TrimSpace(idempotencyKey)) < 16 {
		return ErrInvalidInput
	}
	if operation != "add" && operation != "remove" && operation != "replace" {
		return ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin asset move: %w", err)
	}
	defer tx.Rollback(ctx)
	var containerWorkspace, assetWorkspace string
	if err := tx.QueryRow(ctx, `SELECT workspace_id::text FROM content.containers WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'`, principal.OrganizationID, containerID).Scan(&containerWorkspace); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT workspace_id::text FROM asset.assets WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, assetID).Scan(&assetWorkspace); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if containerWorkspace != workspaceID || assetWorkspace != workspaceID {
		return ErrForbidden
	}
	if operation == "remove" || operation == "replace" {
		if _, err := tx.Exec(ctx, `DELETE FROM content.container_assets WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND asset_id = $3::uuid AND ($4 = 'replace' OR container_id = $5::uuid)`, principal.OrganizationID, workspaceID, assetID, operation, containerID); err != nil {
			return err
		}
	}
	if operation == "add" || operation == "replace" {
		if _, err := tx.Exec(ctx, `INSERT INTO content.container_assets (organization_id, workspace_id, container_id, asset_id, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid) ON CONFLICT DO NOTHING`, principal.OrganizationID, workspaceID, containerID, assetID, principal.UserID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s Service) SetDocumentParent(ctx context.Context, principal auth.Principal, workspaceID, childID string, parentID *string, idempotencyKey string) error {
	if err := s.require(ctx, principal, workspaceID, "asset.write"); err != nil {
		return err
	}
	if !validID(childID) || len(strings.TrimSpace(idempotencyKey)) < 16 {
		return ErrInvalidInput
	}
	if parentID != nil && !validID(*parentID) {
		return ErrInvalidInput
	}
	if parentID == nil {
		_, err := s.Store.Pool.Exec(ctx, `DELETE FROM content.document_parents WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND child_asset_id = $3::uuid`, principal.OrganizationID, workspaceID, childID)
		return err
	}
	var childKind, parentKind, childWorkspace, parentWorkspace string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT a.workspace_id::text, rm.content_kind FROM asset.assets a JOIN model.resource_models rm ON rm.id = a.resource_model_id WHERE a.organization_id = $1::uuid AND a.id = $2::uuid`, principal.OrganizationID, childID).Scan(&childWorkspace, &childKind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := s.Store.Pool.QueryRow(ctx, `SELECT a.workspace_id::text, rm.content_kind FROM asset.assets a JOIN model.resource_models rm ON rm.id = a.resource_model_id WHERE a.organization_id = $1::uuid AND a.id = $2::uuid`, principal.OrganizationID, *parentID).Scan(&parentWorkspace, &parentKind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if childWorkspace != workspaceID || parentWorkspace != workspaceID || childKind != "document" || parentKind != "document" {
		return ErrConflict
	}
	var cycle bool
	if err := s.Store.Pool.QueryRow(ctx, `WITH RECURSIVE chain AS (SELECT parent_asset_id FROM content.document_parents WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND child_asset_id = $3::uuid UNION ALL SELECT dp.parent_asset_id FROM content.document_parents dp JOIN chain c ON dp.child_asset_id = c.parent_asset_id WHERE dp.organization_id = $1::uuid AND dp.workspace_id = $2::uuid) SELECT EXISTS (SELECT 1 FROM chain WHERE parent_asset_id = $4::uuid)`, principal.OrganizationID, workspaceID, childID, *parentID).Scan(&cycle); err != nil {
		return err
	}
	if cycle || *parentID == childID {
		return ErrConflict
	}
	_, err := s.Store.Pool.Exec(ctx, `INSERT INTO content.document_parents (organization_id, workspace_id, child_asset_id, parent_asset_id, created_by) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid) ON CONFLICT (child_asset_id) DO UPDATE SET parent_asset_id = EXCLUDED.parent_asset_id, created_by = EXCLUDED.created_by, created_at = now()`, principal.OrganizationID, workspaceID, childID, *parentID, principal.UserID)
	return err
}

func optionalID(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
