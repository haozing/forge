package conversation

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"agentchunzhi/internal/auth"
	"github.com/jackc/pgx/v5"
)

var ErrHasChildren = fmt.Errorf("conversation has derived children")

type DeleteResult struct {
	ConversationID  string   `json:"conversation_id"`
	DeletedChildren []string `json:"deleted_children"`
}

// Delete removes a conversation. Thoughts are the process layer, so this is a
// hard delete inside one transaction; note assets, attachments and asset
// relations survive (the curated layer outlives the conversation that produced
// them - only the derivation pointer on relations is nulled).
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
		  AND (c.visibility = 'workspace' OR c.initiator_user_id = $3::uuid)
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

	// Derivations touching the subtree (as source or target) lose their rows;
	// asset relations keep their lineage with the dead pointer nulled.
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
		if _, err := tx.Exec(ctx, `UPDATE content.asset_relations SET derivation_id = NULL WHERE organization_id = $1::uuid AND derivation_id = ANY($2::uuid[])`, principal.OrganizationID, derivationIDs); err != nil {
			return DeleteResult{}, fmt.Errorf("detach asset relations: %w", err)
		}
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
	if _, err := tx.Exec(ctx, `DELETE FROM content.message_blocks WHERE organization_id = $1::uuid AND conversation_id = ANY($2::uuid[])`, principal.OrganizationID, ids); err != nil {
		return DeleteResult{}, fmt.Errorf("delete message blocks: %w", err)
	}

	// Containers owned by the subtree: the per-conversation chat container and
	// the note containers from the bindings. Collect note containers before
	// dropping the bindings.
	var containerIDs []string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(array_agg(c::text), '{}') FROM (
			SELECT container_id AS c FROM content.conversations
			WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])
			UNION
			SELECT nb.note_container_id FROM content.note_bindings nb
			JOIN content.conversations cv ON cv.id = nb.conversation_id AND cv.organization_id = $1::uuid
			WHERE nb.conversation_id = ANY($2::uuid[])
		) ids
	`, principal.OrganizationID, ids).Scan(&containerIDs); err != nil {
		return DeleteResult{}, fmt.Errorf("collect containers: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM content.note_bindings WHERE conversation_id = ANY($1::uuid[])`, ids); err != nil {
		return DeleteResult{}, fmt.Errorf("delete note bindings: %w", err)
	}
	containerIDs = nonEmpty(containerIDs)
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
			JOIN content.container_versions cv ON cv.organization_id = bp.organization_id AND cv.id = bp.container_version_id
			WHERE bp.organization_id = $1::uuid AND cv.container_id = ANY($2::uuid[])
		`, principal.OrganizationID, containerIDs).Scan(&placedRevisions); err != nil {
			return DeleteResult{}, fmt.Errorf("collect placed revisions: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE content.containers SET current_version_id = NULL WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])`, principal.OrganizationID, containerIDs); err != nil {
			return DeleteResult{}, fmt.Errorf("detach container versions: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM content.block_placements
			WHERE organization_id = $1::uuid
			  AND container_version_id IN (SELECT id FROM content.container_versions WHERE organization_id = $1::uuid AND container_id = ANY($2::uuid[]))
		`, principal.OrganizationID, containerIDs); err != nil {
			return DeleteResult{}, fmt.Errorf("delete placements: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM content.container_versions WHERE organization_id = $1::uuid AND container_id = ANY($2::uuid[])`, principal.OrganizationID, containerIDs); err != nil {
			return DeleteResult{}, fmt.Errorf("delete container versions: %w", err)
		}
		messageRevisions = append(messageRevisions, nonEmpty(placedRevisions)...)
	}

	// Revisions are now unreferenced: message blocks, derivation sources and
	// placements are gone; asset_relation_blocks cascades by schema.
	revisions := nonEmpty(messageRevisions)
	if len(revisions) > 0 {
		var blockIDs []string
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(array_agg(DISTINCT block_id::text), '{}')
			FROM content.block_revisions
			WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])
		`, principal.OrganizationID, revisions).Scan(&blockIDs); err != nil {
			return DeleteResult{}, fmt.Errorf("collect block ids: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM content.block_revisions WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])`, principal.OrganizationID, revisions); err != nil {
			return DeleteResult{}, fmt.Errorf("delete block revisions: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM content.blocks
			WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])
			  AND NOT EXISTS (SELECT 1 FROM content.block_revisions br WHERE br.organization_id = $1::uuid AND br.block_id = content.blocks.id)
		`, principal.OrganizationID, nonEmpty(blockIDs)); err != nil {
			return DeleteResult{}, fmt.Errorf("delete orphan blocks: %w", err)
		}
	}

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

	// The owned containers go last: conversations.container_id points at them,
	// so the rows must be gone before the container delete is legal.
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
