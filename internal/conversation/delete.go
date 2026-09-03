package conversation

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/eventing"
	assetservice "agentchunzhi/internal/asset"
	"github.com/jackc/pgx/v5"
)

var ErrHasChildren = fmt.Errorf("conversation has derived children")

type DeleteResult struct {
	ConversationID  string   `json:"conversation_id"`
	DeletedChildren []string `json:"deleted_children"`
}

// Delete removes a conversation. Thoughts are the process layer, so this is a
// hard delete inside one transaction. The bound note assets soft-delete with
// it (the conversation was their only entry point; harvest outputs live on as
// separate assets); asset relation edges survive with the derivation pointer
// nulled.
//
// Deleting a thought with derived children is blocked (container-delete
// precedent: not-empty refuses) unless cascade is requested, in which case the
// whole subtree goes. FK order matters: derivation sources, media, message
// blocks and note bindings must go before derivations/conversations, and
// container versions/placements/revisions/blocks before the containers
// themselves.
func (s Service) Delete(ctx context.Context, principal auth.Principal, conversationID string, cascade bool) (DeleteResult, error) {
	if principal.UserType != "member" {
		return DeleteResult{}, ErrForbidden
	}
	if !validID(conversationID) {
		return DeleteResult{}, ErrInvalidID
	}
	if s.Store == nil || s.Store.Pool == nil {
		return DeleteResult{}, ErrForbidden
	}

	// Resolve workspace, role and visibility in one pass. Unlike reads, delete
	// refuses viewers: it is a destructive action.
	var workspaceID, role string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT c.workspace_id::text, wm.role
		FROM content.conversations c
		JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
		WHERE c.organization_id = $1::uuid AND c.id = $2::uuid
	`, principal.OrganizationID, conversationID, principal.UserID).Scan(&workspaceID, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeleteResult{}, ErrNotFound
	}
	if err != nil {
		return DeleteResult{}, fmt.Errorf("resolve conversation for delete: %w", err)
	}
	if err := s.require(ctx, principal, workspaceID); err != nil {
		return DeleteResult{}, err
	}
	if role == "viewer" {
		return DeleteResult{}, ErrForbidden
	}

	// Collect the subtree children-first. UNION (not ALL) guards against
	// parent cycles, which the schema's plain self-FK cannot prevent.
	rows, err := s.Store.Pool.Query(ctx, `
		WITH RECURSIVE tree AS (
			SELECT c.id, 0 AS depth
			FROM content.conversations c
			WHERE c.organization_id = $1::uuid AND c.id = $2::uuid
			UNION
			SELECT ch.id, t.depth + 1
			FROM content.conversations ch
			JOIN tree t ON ch.parent_conversation_id = t.id
			WHERE ch.organization_id = $1::uuid
		)
		SELECT id::text, depth FROM tree
	`, principal.OrganizationID, conversationID)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("collect conversation subtree: %w", err)
	}
	type node struct {
		id    string
		depth int
	}
	subtree := []node{}
	for rows.Next() {
		var item node
		if err := rows.Scan(&item.id, &item.depth); err != nil {
			rows.Close()
			return DeleteResult{}, fmt.Errorf("scan conversation subtree: %w", err)
		}
		subtree = append(subtree, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return DeleteResult{}, err
	}
	if len(subtree) == 0 {
		return DeleteResult{}, ErrNotFound
	}
	sort.Slice(subtree, func(i, j int) bool { return subtree[i].depth > subtree[j].depth })

	ids := make([]string, 0, len(subtree))
	children := []string{}
	for _, item := range subtree {
		ids = append(ids, item.id)
		if item.depth > 0 {
			children = append(children, item.id)
		}
	}
	if len(children) > 0 && !cascade {
		return DeleteResult{ConversationID: conversationID, DeletedChildren: children}, ErrHasChildren
	}

	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("begin conversation delete: %w", err)
	}
	defer tx.Rollback(ctx)
	// Lock order is conversation -> tree container -> asset -> draft (the
	// edit and commit paths take the same order), so a concurrent message
	// append or note freeze can never cycle with the subtree delete.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM content.conversations WHERE organization_id = $1::uuid AND id = ANY($2::uuid[]) FOR UPDATE`, principal.OrganizationID, ids); err != nil {
		return DeleteResult{}, fmt.Errorf("lock conversations for delete: %w", err)
	}

	// Derivations touching the subtree (as source or target) lose their rows.
	// Asset relation edges are sealed facts on asset.asset_versions and stay;
	// the citation payload keeps the (now dangling) derivation id.
	var derivationIDs []string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(array_agg(id::text), '{}') FROM content.derivations
		WHERE organization_id = $1::uuid
		  AND (source_conversation_id = ANY($2::uuid[]) OR target_conversation_id = ANY($2::uuid[]))
	`, principal.OrganizationID, ids).Scan(&derivationIDs); err != nil {
		return DeleteResult{}, fmt.Errorf("collect derivations: %w", err)
	}
	derivationIDs = nonEmpty(derivationIDs)
	if len(derivationIDs) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM content.derivation_sources WHERE derivation_id = ANY($1::uuid[])`, derivationIDs); err != nil {
			return DeleteResult{}, fmt.Errorf("delete derivation sources: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM content.derivations WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])`, principal.OrganizationID, derivationIDs); err != nil {
			return DeleteResult{}, fmt.Errorf("delete derivations: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM content.conversation_media WHERE organization_id = $1::uuid AND conversation_id = ANY($2::uuid[])`, principal.OrganizationID, ids); err != nil {
		return DeleteResult{}, fmt.Errorf("delete conversation media: %w", err)
	}

	// Message revisions must be collected before their message_blocks rows go;
	// snapshot revisions come from the container placements collected below.
	var messageRevisions []string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(array_agg(block_revision_id::text), '{}') FROM content.message_blocks
		WHERE organization_id = $1::uuid AND conversation_id = ANY($2::uuid[])
	`, principal.OrganizationID, ids).Scan(&messageRevisions); err != nil {
		return DeleteResult{}, fmt.Errorf("collect message revisions: %w", err)
	}
	_ = messageRevisions
	if _, err := tx.Exec(ctx, `DELETE FROM content.message_blocks WHERE organization_id = $1::uuid AND conversation_id = ANY($2::uuid[])`, principal.OrganizationID, ids); err != nil {
		return DeleteResult{}, fmt.Errorf("delete message blocks: %w", err)
	}

	// Containers owned by the subtree: the note documents reachable through
	// the bindings. Collect them before dropping the bindings.
	var containerIDs []string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(array_agg(cc.id::text), '{}')
		FROM content.note_bindings nb
		JOIN content.containers cc ON cc.organization_id = nb.organization_id AND cc.asset_id = nb.note_asset_id
		WHERE nb.organization_id = $1::uuid AND nb.conversation_id = ANY($2::uuid[])
	`, principal.OrganizationID, ids).Scan(&containerIDs); err != nil {
		return DeleteResult{}, fmt.Errorf("collect containers: %w", err)
	}
	containerIDs = nonEmpty(containerIDs)
	if len(containerIDs) > 0 {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM content.containers WHERE organization_id = $1::uuid AND id = ANY($2::uuid[]) FOR UPDATE`, principal.OrganizationID, containerIDs); err != nil {
			return DeleteResult{}, fmt.Errorf("lock note containers for delete: %w", err)
		}
	}
	// Note assets bound to the subtree die with it: the conversation was their
	// only entry point (harvest outputs survive as separate assets). Soft
	// delete keeps version history auditable; pending publication requests of
	// those assets are cancelled, and one asset.archived fact per asset fans
	// out to the retrieval projection and site cache invalidation.
	var noteAssets []struct {
		id          string
		revision    int64
		workspaceID string
		published   string
	}
	noteRows, err := tx.Query(ctx, `
		SELECT a.id::text, a.revision, a.workspace_id::text, COALESCE(a.current_published_version_id::text, '')
		FROM content.note_bindings nb
		JOIN asset.assets a ON a.organization_id = nb.organization_id AND a.id = nb.note_asset_id
		WHERE nb.organization_id = $1::uuid AND nb.conversation_id = ANY($2::uuid[]) AND a.deleted_at IS NULL
	`, principal.OrganizationID, ids)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("collect note assets: %w", err)
	}
	for noteRows.Next() {
		var item struct {
			id          string
			revision    int64
			workspaceID string
			published   string
		}
		if err := noteRows.Scan(&item.id, &item.revision, &item.workspaceID, &item.published); err != nil {
			noteRows.Close()
			return DeleteResult{}, fmt.Errorf("scan note asset: %w", err)
		}
		noteAssets = append(noteAssets, item)
	}
	noteRows.Close()
	if err := noteRows.Err(); err != nil {
		return DeleteResult{}, fmt.Errorf("iterate note assets: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM content.note_bindings WHERE conversation_id = ANY($1::uuid[])`, ids); err != nil {
		return DeleteResult{}, fmt.Errorf("delete note bindings: %w", err)
	}
	if len(noteAssets) > 0 {
		assetIDs := make([]string, 0, len(noteAssets))
		for _, item := range noteAssets {
			assetIDs = append(assetIDs, item.id)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE asset.assets
			SET deleted_at = now(), publication_status = 'archived',
			    current_published_version_id = NULL, published_at = NULL, updated_at = now()
			WHERE organization_id = $1::uuid AND id = ANY($2::uuid[]) AND deleted_at IS NULL
		`, principal.OrganizationID, assetIDs); err != nil {
			return DeleteResult{}, fmt.Errorf("archive note assets: %w", err)
		}
		for _, item := range noteAssets {
			if _, err := assetservice.CancelPendingRequestsTx(ctx, tx, &s.Content.Events, assetservice.LifecycleRow{
				ID: item.id, OrganizationID: principal.OrganizationID, WorkspaceID: item.workspaceID, Revision: item.revision,
			}, principal, "asset_archived"); err != nil {
				return DeleteResult{}, fmt.Errorf("cancel note publication requests: %w", err)
			}
		}
		if s.Content.Events.Queue != nil {
			for _, item := range noteAssets {
				if _, err := s.Content.Events.AppendTx(ctx, tx, eventing.Event{
					OrganizationID:   principal.OrganizationID,
					WorkspaceID:      item.workspaceID,
					EventType:        eventing.EventAssetArchived,
					AggregateType:    "asset",
					AggregateID:      item.id,
					AggregateVersion: item.revision,
					PayloadVersion:   eventing.PayloadVersionV1,
					Actor:            eventing.ActorFromPrincipal(principal),
					Payload: eventing.AssetArchivedPayload{
						AssetID:           item.id,
						PreviousVersionID: item.published,
						WorkspaceID:       item.workspaceID,
					},
				}); err != nil {
					return DeleteResult{}, fmt.Errorf("record note archive fact: %w", err)
				}
			}
		}
	}
	if len(containerIDs) > 0 {
		// An asset may have been moved into one of these containers; unlink it
		// (the asset itself survives) before the container row goes.
		if _, err := tx.Exec(ctx, `DELETE FROM content.container_assets WHERE organization_id = $1::uuid AND container_id = ANY($2::uuid[])`, principal.OrganizationID, containerIDs); err != nil {
			return DeleteResult{}, fmt.Errorf("unlink container assets: %w", err)
		}
		var placedRevisions []string
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(array_agg(DISTINCT bp.block_revision_id::text), '{}')
			FROM content.block_placements bp
			WHERE bp.organization_id = $1::uuid AND bp.container_id = ANY($2::uuid[])
		`, principal.OrganizationID, containerIDs).Scan(&placedRevisions); err != nil {
			return DeleteResult{}, fmt.Errorf("collect placed revisions: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM content.block_placements WHERE organization_id = $1::uuid AND container_id = ANY($2::uuid[])`, principal.OrganizationID, containerIDs); err != nil {
			return DeleteResult{}, fmt.Errorf("delete placements: %w", err)
		}
		messageRevisions = append(messageRevisions, nonEmpty(placedRevisions)...)
	}

	// Block revisions and blocks survive the subtree: the frozen version
	// snapshots (asset_versions.blocks) reference them by id and the note
	// assets keep their version history for audit. Reclaiming them needs a
	// reference-counting GC (design §8), not a cascade delete.

	// Self-referencing parent FK validates per row, so drop deepest levels
	// first; within one level order is irrelevant.
	start := 0
	for start < len(subtree) {
		end := start
		for end < len(subtree) && subtree[end].depth == subtree[start].depth {
			end++
		}
		level := make([]string, 0, end-start)
		for _, item := range subtree[start:end] {
			level = append(level, item.id)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM content.conversations WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])`, principal.OrganizationID, level); err != nil {
			return DeleteResult{}, fmt.Errorf("delete conversations: %w", err)
		}
		start = end
	}

	// The owned note containers go last: containers.asset_id references the
	// (already soft-deleted) assets, so drop them after the conversations.
	if len(containerIDs) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM content.containers WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])`, principal.OrganizationID, containerIDs); err != nil {
			return DeleteResult{}, fmt.Errorf("delete containers: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return DeleteResult{}, fmt.Errorf("commit conversation delete: %w", err)
	}
	return DeleteResult{ConversationID: conversationID, DeletedChildren: children}, nil
}

// nonEmpty keeps pgx from receiving an empty array as SQL NULL.
func nonEmpty(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return values
}
