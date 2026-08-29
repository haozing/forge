package tag

// resolver.go — controlled tag resolution for import and webhook channels.
// Webhook technical identities may only reference existing active tags;
// import rows may create unknown keys when the batch policy explicitly says
// create and the submitter holds tag.manage.

import (
	"context"

	"github.com/google/uuid"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// ResolvedTag is the outcome of resolving one normalized key.
type ResolvedTag struct {
	ID      string
	Key     string
	Created bool
}

// ResolveExisting maps every key to an active workspace tag. Unknown or
// archived keys fail with ErrUnknownTag / ErrArchived — webhook callers
// cannot expand the catalog.
func ResolveExisting(ctx context.Context, db *store.Store, principal auth.Principal, workspaceID string, keys []string) ([]ResolvedTag, error) {
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		value, err := NormalizeKey(key)
		if err != nil {
			return nil, ErrUnknownTag
		}
		normalized = append(normalized, value)
	}
	normalized = dedupeSorted(normalized)
	result := make([]ResolvedTag, 0, len(normalized))
	for _, key := range normalized {
		var id string
		var status string
		err := db.Pool.QueryRow(ctx, `
			SELECT id::text, status FROM asset.tags
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND normalized_key = $3
		`, principal.OrganizationID, workspaceID, key).Scan(&id, &status)
		if err != nil {
			return nil, ErrUnknownTag
		}
		if status != StatusActive {
			return nil, ErrArchived
		}
		result = append(result, ResolvedTag{ID: id, Key: key})
	}
	return result, nil
}

// CreateOrReuseTx implements the import create policy inside the caller's
// transaction: reuse the active tag or insert it, returning created=true
// exactly for the inserting call so batch counters stay exact.
func CreateOrReuseTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, workspaceID, key string) (ResolvedTag, error) {
	normalized, err := NormalizeKey(key)
	if err != nil {
		return ResolvedTag{}, ErrInvalidInput
	}
	var id string
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM asset.tags
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND normalized_key = $3
	`, principal.OrganizationID, workspaceID, normalized).Scan(&id)
	if err == nil {
		return ResolvedTag{ID: id, Key: normalized}, nil
	}
	newID := uuid.NewString()
	slug := DeriveSlug(normalized, newID)
	if _, err := tx.Exec(ctx, `
		INSERT INTO asset.tags
			(id, organization_id, workspace_id, normalized_key, display_name, slug, status, revision, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $4, $5, 'active', 1, $6::uuid)
		ON CONFLICT (organization_id, workspace_id, normalized_key) DO NOTHING
	`, newID, principal.OrganizationID, workspaceID, normalized, slug, principal.UserID); err != nil {
		return ResolvedTag{}, err
	}
	// Read back after the conflict guard: a concurrent creator wins identity.
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM asset.tags
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND normalized_key = $3
	`, principal.OrganizationID, workspaceID, normalized).Scan(&id)
	if err != nil {
		return ResolvedTag{}, err
	}
	return ResolvedTag{ID: id, Key: normalized, Created: true}, nil
}
