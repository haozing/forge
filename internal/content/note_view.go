package content

// Note reading and block-editing surface. The live block tree of the note
// document container is the note's single editable source of truth; the
// views here render it on demand, and the freeze happens on the asset
// commit path (asset.freezeNoteTx).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/noteblocks"

	"github.com/jackc/pgx/v5"
)

// NoteView is the composed note document of one conversation: the frozen
// working version plus the live tree render and its dirty state.
type NoteView struct {
	ConversationID     string         `json:"conversation_id"`
	NoteAssetID        string         `json:"note_asset_id"`
	ContainerID        string         `json:"container_id"`
	AssetVersionID     string         `json:"asset_version_id"`
	Title              *string        `json:"title"`
	Markdown           *string        `json:"markdown"`
	Fields             map[string]any `json:"fields"`
	DraftRevision      int64          `json:"revision"`
	CommittedRevision  int64          `json:"committed_revision"`
	DraftMarkdown      string         `json:"draft_markdown"`
	Dirty              bool           `json:"dirty"`
	PublicationStatus  string         `json:"publication_status"`
	ConfirmationStatus string         `json:"confirmation_status"`
	MessageCount       int64          `json:"message_count"`
}

// NoteView composes the note document view for one conversation.
func (s Service) NoteView(ctx context.Context, principal auth.Principal, conversationID string) (NoteView, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) || !validID(conversationID) {
		return NoteView{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return NoteView{}, errors.New("database store is not initialized")
	}
	var view NoteView
	var title, markdown, confirmation *string
	var fields []byte
	var revision, committed int64
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT nb.conversation_id::text, nb.note_asset_id::text, cc.id::text,
		       COALESCE(a.current_working_version_id::text, ''),
		       v.title, v.markdown, v.fields, COALESCE(v.confirmation_status, ''),
		       a.publication_status,
		       d.revision, d.committed_revision,
		       count(mb.block_revision_id)
		FROM content.note_bindings nb
		JOIN content.conversations c ON c.id = nb.conversation_id
		JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
		JOIN content.containers cc ON cc.organization_id = nb.organization_id AND cc.asset_id = nb.note_asset_id
		JOIN asset.assets a ON a.organization_id = c.organization_id AND a.id = nb.note_asset_id
		LEFT JOIN asset.asset_versions v ON v.id = a.current_working_version_id
		LEFT JOIN asset.asset_drafts d ON d.organization_id = a.organization_id AND d.asset_id = a.id
		LEFT JOIN content.message_blocks mb ON mb.organization_id = c.organization_id AND mb.conversation_id = c.id
		WHERE nb.organization_id = $1::uuid AND nb.conversation_id = $2::uuid
		GROUP BY nb.conversation_id, nb.note_asset_id, cc.id, a.current_working_version_id,
		         v.title, v.markdown, v.fields, v.confirmation_status, a.publication_status,
		         d.revision, d.committed_revision
	`, principal.OrganizationID, conversationID, principal.UserID).Scan(
		&view.ConversationID, &view.NoteAssetID, &view.ContainerID,
		&view.AssetVersionID,
		&title, &markdown, &fields, &confirmation,
		&view.PublicationStatus,
		&revision, &committed,
		&view.MessageCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return NoteView{}, ErrNotFound
	}
	if err != nil {
		return NoteView{}, fmt.Errorf("load note view: %w", err)
	}
	view.Title, view.Markdown, view.ConfirmationStatus = title, markdown, derefString(confirmation)
	decoded := map[string]any{}
	if len(fields) > 0 {
		_ = json.Unmarshal(fields, &decoded)
	}
	view.Fields = decoded
	view.DraftRevision, view.CommittedRevision = revision, committed
	tree, found, err := noteblocks.LoadTreeByAssetTx(ctx, s.Store.Pool, principal.OrganizationID, view.NoteAssetID)
	if err != nil {
		return NoteView{}, err
	}
	if found {
		view.DraftMarkdown = noteblocks.RenderMarkdown(tree)
	}
	view.Dirty = revision != committed
	return view, nil
}

// NoteBlockEntry is one block of the live tree as served to the editor.
type NoteBlockEntry struct {
	BlockID      string  `json:"block_id"`
	RevisionID   string  `json:"block_revision_id"`
	Position     float64 `json:"position"`
	Kind         string  `json:"kind"`
	Role         string  `json:"role,omitempty"`
	Status       string  `json:"status,omitempty"`
	Content      string  `json:"content"`
	EditableKind bool    `json:"editable"`
}

// NoteBlocks serves the live tree for editing.
func (s Service) NoteBlocks(ctx context.Context, principal auth.Principal, conversationID string) ([]NoteBlockEntry, int64, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) || !validID(conversationID) {
		return nil, 0, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return nil, 0, errors.New("database store is not initialized")
	}
	containerID, revision, err := s.noteContainer(ctx, principal, conversationID)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT b.id::text, br.id::text, bp.position, b.block_type,
		       COALESCE(mb.role, ''), COALESCE(mb.status, ''), br.content
		FROM content.block_placements bp
		JOIN content.block_revisions br ON br.organization_id = bp.organization_id AND br.id = bp.block_revision_id
		JOIN content.blocks b ON b.organization_id = br.organization_id AND b.id = br.block_id
		LEFT JOIN content.message_blocks mb ON mb.organization_id = bp.organization_id AND mb.block_revision_id = br.id
		WHERE bp.organization_id = $1::uuid AND bp.container_id = $2::uuid
		ORDER BY bp.position
	`, principal.OrganizationID, containerID)
	if err != nil {
		return nil, 0, fmt.Errorf("load note blocks: %w", err)
	}
	defer rows.Close()
	entries := []NoteBlockEntry{}
	for rows.Next() {
		var entry NoteBlockEntry
		if err := rows.Scan(&entry.BlockID, &entry.RevisionID, &entry.Position, &entry.Kind, &entry.Role, &entry.Status, &entry.Content); err != nil {
			return nil, 0, fmt.Errorf("scan note block: %w", err)
		}
		entry.EditableKind = editableNoteKind(entry.Kind)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate note blocks: %w", err)
	}
	return entries, revision, nil
}

// AddNoteBlock appends a manual block to the live tree.
func (s Service) AddNoteBlock(ctx context.Context, principal auth.Principal, idempotencyKey, conversationID, kind, content string) (NoteBlockEntry, error) {
	if err := s.noteBlockGuard(principal, idempotencyKey, conversationID, kind); err != nil {
		return NoteBlockEntry{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return NoteBlockEntry{}, fmt.Errorf("begin note block add: %w", err)
	}
	defer tx.Rollback(ctx)
	containerID, noteAssetID, revision, err := lockNoteContainer(ctx, tx, principal, conversationID)
	if err != nil {
		return NoteBlockEntry{}, err
	}
	entry, err := insertManualBlock(ctx, tx, principal.OrganizationID, containerID, noteAssetID, revision, kind, content, principal.UserID)
	if err != nil {
		return NoteBlockEntry{}, err
	}
	if err := saveIdempotency(ctx, tx, principal, "conversation.note.block.add", idempotencyKey, jsonMarshalValue(entry)); err != nil {
		return NoteBlockEntry{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return NoteBlockEntry{}, fmt.Errorf("commit note block add: %w", err)
	}
	return entry, nil
}

// UpdateNoteBlock replaces a manual block's content with a new revision.
// Message blocks are conversation records: their text is immutable, only
// status can change through the chat surface.
func (s Service) UpdateNoteBlock(ctx context.Context, principal auth.Principal, idempotencyKey, conversationID, blockID, content string) (NoteBlockEntry, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) ||
		!validID(conversationID) || !validID(blockID) || !validIdempotencyKey(idempotencyKey) || strings.TrimSpace(content) == "" {
		return NoteBlockEntry{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return NoteBlockEntry{}, fmt.Errorf("begin note block update: %w", err)
	}
	defer tx.Rollback(ctx)
	containerID, noteAssetID, revision, err := lockNoteContainer(ctx, tx, principal, conversationID)
	if err != nil {
		return NoteBlockEntry{}, err
	}
	var blockType string
	var mbID *string
	err = tx.QueryRow(ctx, `
		SELECT b.block_type, mb.block_revision_id::text
		FROM content.blocks b
		JOIN content.block_revisions br ON br.organization_id = $1::uuid AND br.block_id = b.id
		JOIN content.block_placements bp ON bp.organization_id = $1::uuid
		    AND bp.container_id = $2::uuid AND bp.block_revision_id = br.id
		LEFT JOIN content.message_blocks mb ON mb.organization_id = $1::uuid AND mb.block_revision_id = br.id
		WHERE b.organization_id = $1::uuid AND b.id = $3::uuid
		ORDER BY bp.position LIMIT 1
	`, principal.OrganizationID, containerID, blockID).Scan(&blockType, &mbID)
	if errors.Is(err, pgx.ErrNoRows) {
		return NoteBlockEntry{}, ErrNotFound
	}
	if err != nil {
		return NoteBlockEntry{}, fmt.Errorf("load note block: %w", err)
	}
	if mbID != nil {
		return NoteBlockEntry{}, fmt.Errorf("%w: message blocks are immutable", ErrConflict)
	}
	if !editableNoteKind(blockType) {
		return NoteBlockEntry{}, ErrInvalidInput
	}
	checksum := hashBytes(content)
	var nextRevision int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(revision_no), 0) + 1 FROM content.block_revisions
		WHERE organization_id = $1::uuid AND block_id = $2::uuid
	`, principal.OrganizationID, blockID).Scan(&nextRevision); err != nil {
		return NoteBlockEntry{}, fmt.Errorf("allocate block revision: %w", err)
	}
	var revisionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.block_revisions
			(organization_id, block_id, revision_no, content, content_format, created_by, content_checksum)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'markdown', $5::uuid, $6)
		RETURNING id::text
	`, principal.OrganizationID, blockID, nextRevision, content, principal.UserID, checksum).Scan(&revisionID); err != nil {
		return NoteBlockEntry{}, fmt.Errorf("create block revision: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE content.block_placements bp
		SET block_revision_id = $3::uuid
		WHERE bp.organization_id = $1::uuid AND bp.container_id = $2::uuid
		  AND bp.block_revision_id IN (
		      SELECT br.id FROM content.block_revisions br
		      WHERE br.organization_id = $1::uuid AND br.block_id = $4::uuid)
	`, principal.OrganizationID, containerID, revisionID, blockID); err != nil {
		return NoteBlockEntry{}, fmt.Errorf("repoint block placement: %w", err)
	}
	entry, err := touchNoteTree(ctx, tx, principal.OrganizationID, containerID, noteAssetID, revision, blockID, revisionID, blockType)
	if err != nil {
		return NoteBlockEntry{}, err
	}
	if err := saveIdempotency(ctx, tx, principal, "conversation.note.block.update", idempotencyKey, jsonMarshalValue(entry)); err != nil {
		return NoteBlockEntry{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return NoteBlockEntry{}, fmt.Errorf("commit note block update: %w", err)
	}
	return entry, nil
}

// DeleteNoteBlock removes a block from the live tree. The block and its
// revisions stay: frozen version snapshots reference them.
func (s Service) DeleteNoteBlock(ctx context.Context, principal auth.Principal, idempotencyKey, conversationID, blockID string) (string, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) ||
		!validID(conversationID) || !validID(blockID) || !validIdempotencyKey(idempotencyKey) {
		return "", ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin note block delete: %w", err)
	}
	defer tx.Rollback(ctx)
	containerID, noteAssetID, revision, err := lockNoteContainer(ctx, tx, principal, conversationID)
	if err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM content.block_placements bp
		USING content.block_revisions br
		WHERE bp.organization_id = $1::uuid AND bp.container_id = $2::uuid
		  AND br.id = bp.block_revision_id AND br.block_id = $3::uuid
	`, principal.OrganizationID, containerID, blockID)
	if err != nil {
		return "", fmt.Errorf("delete block placement: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	if _, err := tx.Exec(ctx, `
		UPDATE content.containers SET revision = revision + 1, updated_at = now() WHERE id = $1::uuid
	`, containerID); err != nil {
		return "", fmt.Errorf("advance note tree: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.asset_drafts SET revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
	`, principal.OrganizationID, noteAssetID); err != nil {
		return "", fmt.Errorf("advance note draft epoch: %w", err)
	}
	result := map[string]any{"block_id": blockID, "revision": revision + 1}
	if err := saveIdempotency(ctx, tx, principal, "conversation.note.block.delete", idempotencyKey, jsonMarshalValue(result)); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit note block delete: %w", err)
	}
	return blockID, nil
}

func (s Service) noteBlockGuard(principal auth.Principal, idempotencyKey, conversationID, kind string) error {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) ||
		!validID(conversationID) || !validIdempotencyKey(idempotencyKey) {
		return ErrInvalidInput
	}
	if !editableNoteKind(kind) {
		return ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	return nil
}

func (s Service) noteContainer(ctx context.Context, principal auth.Principal, conversationID string) (string, int64, error) {
	var containerID string
	var revision int64
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT cc.id::text, cc.revision
		FROM content.note_bindings nb
		JOIN content.conversations c ON c.id = nb.conversation_id
		JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
		JOIN content.containers cc ON cc.organization_id = nb.organization_id AND cc.asset_id = nb.note_asset_id
		WHERE nb.organization_id = $1::uuid AND nb.conversation_id = $2::uuid
	`, principal.OrganizationID, conversationID, principal.UserID).Scan(&containerID, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	if err != nil {
		return "", 0, fmt.Errorf("load note container: %w", err)
	}
	return containerID, revision, nil
}

// lockNoteContainer locks the note document row inside a write transaction
// and returns the container, the bound asset and the current tree revision.
func lockNoteContainer(ctx context.Context, tx pgx.Tx, principal auth.Principal, conversationID string) (string, string, int64, error) {
	var containerID, noteAssetID string
	var revision int64
	err := tx.QueryRow(ctx, `
		SELECT cc.id::text, cc.revision, nb.note_asset_id::text
		FROM content.note_bindings nb
		JOIN content.conversations c ON c.id = nb.conversation_id
		JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid AND wm.role <> 'viewer'
		JOIN content.containers cc ON cc.organization_id = nb.organization_id AND cc.asset_id = nb.note_asset_id
		WHERE nb.organization_id = $1::uuid AND nb.conversation_id = $2::uuid
		FOR UPDATE OF cc
	`, principal.OrganizationID, conversationID, principal.UserID).Scan(&containerID, &revision, &noteAssetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", 0, ErrNotFound
	}
	if err != nil {
		return "", "", 0, fmt.Errorf("lock note container: %w", err)
	}
	return containerID, noteAssetID, revision, nil
}

func insertManualBlock(ctx context.Context, tx pgx.Tx, organizationID, containerID, noteAssetID string, revision int64, kind, content, userID string) (NoteBlockEntry, error) {
	var nextPosition float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(position) + 1, 0) FROM content.block_placements
		WHERE organization_id = $1::uuid AND container_id = $2::uuid
	`, organizationID, containerID).Scan(&nextPosition); err != nil {
		return NoteBlockEntry{}, fmt.Errorf("allocate block position: %w", err)
	}
	checksum := hashBytes(content)
	var blockID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.blocks (organization_id, block_type, created_by)
		VALUES ($1::uuid, $2, $3::uuid) RETURNING id::text
	`, organizationID, kind, userID).Scan(&blockID); err != nil {
		return NoteBlockEntry{}, fmt.Errorf("create block: %w", err)
	}
	var revisionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.block_revisions
			(organization_id, block_id, revision_no, content, content_format, created_by, content_checksum)
		VALUES ($1::uuid, $2::uuid, 1, $3, 'markdown', $4::uuid, $5)
		RETURNING id::text
	`, organizationID, blockID, content, userID, checksum).Scan(&revisionID); err != nil {
		return NoteBlockEntry{}, fmt.Errorf("create block revision: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO content.block_placements
			(organization_id, container_id, block_revision_id, position)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4)
	`, organizationID, containerID, revisionID, nextPosition); err != nil {
		return NoteBlockEntry{}, fmt.Errorf("place block: %w", err)
	}
	return touchNoteTree(ctx, tx, organizationID, containerID, noteAssetID, revision, blockID, revisionID, kind)
}

func touchNoteTree(ctx context.Context, tx pgx.Tx, organizationID, containerID, noteAssetID string, revision int64, blockID, revisionID, kind string) (NoteBlockEntry, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE content.containers SET revision = revision + 1, updated_at = now() WHERE id = $1::uuid
	`, containerID); err != nil {
		return NoteBlockEntry{}, fmt.Errorf("advance note tree: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.asset_drafts SET revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
	`, organizationID, noteAssetID); err != nil {
		return NoteBlockEntry{}, fmt.Errorf("advance note draft epoch: %w", err)
	}
	var content string
	var position float64
	if err := tx.QueryRow(ctx, `
		SELECT br.content, bp.position
		FROM content.block_revisions br
		JOIN content.block_placements bp ON bp.organization_id = br.organization_id AND bp.block_revision_id = br.id
		WHERE br.organization_id = $1::uuid AND br.id = $2::uuid AND bp.container_id = $3::uuid
	`, organizationID, revisionID, containerID).Scan(&content, &position); err != nil {
		return NoteBlockEntry{}, fmt.Errorf("load block content: %w", err)
	}
	_ = revision
	return NoteBlockEntry{
		BlockID: blockID, RevisionID: revisionID, Position: position,
		Kind: kind, Content: content, EditableKind: true,
	}, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func jsonMarshalValue(value any) []byte {
	body, _ := json.Marshal(value)
	return body
}

func editableNoteKind(kind string) bool {
	switch kind {
	case "paragraph", "heading", "quote", "code", "list", "callout":
		return true
	}
	return false
}
