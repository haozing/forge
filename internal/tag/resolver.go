package tag

// resolver.go — controlled tag resolution for import and webhook channels.
// Webhook technical identities may only reference existing active tags;
// import rows may create unknown keys when the batch policy explicitly says
// create and the submitter holds tag.manage, and restore archived hits so an
// export→import round trip never loses a tag.

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// ResolveForImportTx implements the import channel semantics inside the
// caller's transaction: an active tag is reused as-is, an unknown key is
// created fresh, and an archived hit is restored to active. Restore is forced
// by the schema — normalized_key is UNIQUE per workspace so a same-key import
// can never insert a replacement, and version sealing rejects non-active tags,
// so linking the archived row as-is would fail the row later anyway. The
// restore UPDATE mirrors Service.Restore (status/archived_at/archived_by/
// revision/updated_at) without its audit and event fan-out: reactivation here
// is a side effect of row intake, not a management action.
//
// actorID fills tags.created_by (NOT NULL) on the insert branch; callers pass
// the import batch submitter. Created is reported exactly for the call that
// inserted the row so batch created-tag budgets stay exact — including in the
// concurrent-creator race, where the loser reuses the winner's row and must
// not be charged.
func ResolveForImportTx(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, key, displayName, actorID string) (ResolvedTag, error) {
	normalized, err := NormalizeKey(key)
	if err != nil {
		return ResolvedTag{}, ErrInvalidInput
	}
	var id, status string
	err = tx.QueryRow(ctx, `
		SELECT id::text, status FROM asset.tags
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND normalized_key = $3
	`, organizationID, workspaceID, normalized).Scan(&id, &status)
	if err == nil {
		if status == StatusArchived {
			// Same SQL shape as Service.Restore: identity never changes, only
			// the lifecycle flips back to active.
			if _, err := tx.Exec(ctx, `
				UPDATE asset.tags
				SET status = 'active', archived_at = NULL, archived_by = NULL,
				    revision = revision + 1, updated_at = now()
				WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
			`, organizationID, workspaceID, id); err != nil {
				return ResolvedTag{}, fmt.Errorf("restore archived tag for import: %w", err)
			}
		}
		return ResolvedTag{ID: id, Key: normalized}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ResolvedTag{}, err
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		// Import cells carry normalized keys only, so the normalized key is
		// the deterministic display name for a tag created on import.
		name = normalized
	}
	if _, err := ValidateDisplayName(name); err != nil {
		return ResolvedTag{}, ErrInvalidInput
	}
	newID := uuid.NewString()
	slug := DeriveSlug(normalized, newID)
	// Distinct keys can fold onto one slug; keep the per-workspace slug UNIQUE
	// constraint satisfiable the same way Service.Create does — suffix with an
	// id fragment. A check-then-insert race on the slug itself remains
	// possible and fails the row, matching Service.Create.
	var slugTaken bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM asset.tags
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND slug = $3
		)
	`, organizationID, workspaceID, slug).Scan(&slugTaken); err != nil {
		return ResolvedTag{}, err
	}
	if slugTaken {
		slug = DeriveSlug(normalized, newID) + "-" + newID[:8]
	}
	result, err := tx.Exec(ctx, `
		INSERT INTO asset.tags
			(id, organization_id, workspace_id, normalized_key, display_name, slug, status, revision, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, 'active', 1, $7::uuid)
		ON CONFLICT (organization_id, workspace_id, normalized_key) DO NOTHING
	`, newID, organizationID, workspaceID, normalized, name, slug, actorID)
	if err != nil {
		return ResolvedTag{}, err
	}
	if result.RowsAffected() == 1 {
		return ResolvedTag{ID: newID, Key: normalized, Created: true}, nil
	}
	// A concurrent creator won the key race after our SELECT saw nothing; its
	// row is freshly inserted and therefore active.
	err = tx.QueryRow(ctx, `
		SELECT id::text FROM asset.tags
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND normalized_key = $3
	`, organizationID, workspaceID, normalized).Scan(&id)
	if err != nil {
		return ResolvedTag{}, err
	}
	return ResolvedTag{ID: id, Key: normalized}, nil
}
