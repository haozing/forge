package content

// pattern.go — org-level reusable block-tree skeletons (G8,
// 公开站点投递补齐与Agent技能扩展实施方案 §10). A pattern stores plain
// {kind, role, content} blocks (self-contained copy semantics); applying
// appends them into a note's live tree in one transaction. Freezing rides
// the normal commit flow, so applied patterns enter version governance.
//
// Authorization: save/list/delete answer member principals (org-scoped)
// and the agent tool channel (capability content.patterns, granted through
// agent_access_policies). Apply stays a member command: the note-tree write
// path authorizes through workspace membership (lockNoteContainer), and no
// agent surface may bypass that predicate (audit red line).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"

	"github.com/jackc/pgx/v5"
)

const (
	maxPatternBlocks = 200
	maxPatternBlockContent = 32 * 1024
)

// patternKinds is the block-type whitelist for pattern payloads: editable
// content shapes only. attachment blocks reference per-asset attachment ids
// (dangling when copied) and message blocks are conversation records — both
// stay out of templates.
var patternKinds = map[string]bool{
	"text": true, "paragraph": true, "heading": true, "list": true,
	"code": true, "quote": true, "qa": true, "question": true,
	"answer": true, "link": true, "callout": true,
}

// PatternBlock is one self-contained block of a pattern.
type PatternBlock struct {
	Kind    string `json:"kind"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content"`
}

// Pattern is one saved skeleton.
type Pattern struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	Blocks        []PatternBlock `json:"blocks"`
	SourceAssetID string        `json:"source_asset_id,omitempty"`
	CreatedBy     string        `json:"created_by"`
	CreatedAt     time.Time     `json:"created_at"`
}

// validPatternPrincipal accepts the member surface and the agent runtime
// (whose agent user lives in the organization's identity space).
func validPatternPrincipal(principal auth.Principal) bool {
	if principal.UserType != "member" && principal.UserType != "agent" {
		return false
	}
	return validID(principal.OrganizationID) && validID(principal.UserID)
}

func validPatternBlocks(blocks []PatternBlock) error {
	if len(blocks) == 0 || len(blocks) > maxPatternBlocks {
		return fmt.Errorf("pattern must carry 1-%d blocks", maxPatternBlocks)
	}
	for _, block := range blocks {
		if !patternKinds[block.Kind] {
			return fmt.Errorf("block kind %q is not allowed in patterns", block.Kind)
		}
		if strings.TrimSpace(block.Content) == "" || len(block.Content) > maxPatternBlockContent {
			return fmt.Errorf("block content must be 1-%d bytes", maxPatternBlockContent)
		}
	}
	return nil
}

// CreatePattern saves a skeleton: either explicit blocks or a snapshot of an
// asset's live note tree (fromAssetID) — the "照我上一篇的格式" move.
func (s Service) CreatePattern(ctx context.Context, principal auth.Principal, name, description string, blocks []PatternBlock, fromAssetID string) (Pattern, error) {
	if !validPatternPrincipal(principal) {
		return Pattern{}, ErrInvalidInput
	}
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if len(name) < 2 || len(name) > 32 || len(description) > 500 {
		return Pattern{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return Pattern{}, errors.New("database store is not initialized")
	}
	if len(blocks) == 0 {
		if !validID(fromAssetID) {
			return Pattern{}, ErrInvalidInput
		}
		snapshot, err := s.snapshotAssetBlocks(ctx, principal, fromAssetID)
		if err != nil {
			return Pattern{}, err
		}
		blocks = snapshot
	}
	if err := validPatternBlocks(blocks); err != nil {
		return Pattern{}, err
	}
	payload, err := json.Marshal(blocks)
	if err != nil {
		return Pattern{}, err
	}
	var sourceAsset *string
	if validID(fromAssetID) {
		sourceAsset = &fromAssetID
	}
	item := Pattern{}
	err = s.Store.Pool.QueryRow(ctx, `
		INSERT INTO content.patterns (organization_id, name, description, blocks, source_asset_id, created_by)
		VALUES ($1::uuid, $2, $3, $4::jsonb, $5, $6::uuid)
		ON CONFLICT (organization_id, name) DO UPDATE
		  SET description = EXCLUDED.description, blocks = EXCLUDED.blocks,
		      source_asset_id = EXCLUDED.source_asset_id, updated_at = now()
		RETURNING id::text, name, description, blocks, COALESCE(source_asset_id::text, ''), created_by::text, created_at
	`, principal.OrganizationID, name, description, string(payload), sourceAsset, principal.UserID).Scan(
		&item.ID, &item.Name, &item.Description, &payload, &item.SourceAssetID, &item.CreatedBy, &item.CreatedAt)
	if err != nil {
		return Pattern{}, fmt.Errorf("create pattern: %w", err)
	}
	if err := json.Unmarshal(payload, &item.Blocks); err != nil {
		return Pattern{}, fmt.Errorf("decode pattern blocks: %w", err)
	}
	return item, nil
}

// snapshotAssetBlocks reads one asset's live note tree as plain blocks.
func (s Service) snapshotAssetBlocks(ctx context.Context, principal auth.Principal, assetID string) ([]PatternBlock, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT b.block_type, COALESCE(mb.role, ''), br.content
		FROM content.containers cc
		JOIN content.block_placements bp ON bp.organization_id = cc.organization_id AND bp.container_id = cc.id
		JOIN content.block_revisions br ON br.organization_id = bp.organization_id AND br.id = bp.block_revision_id
		JOIN content.blocks b ON b.organization_id = br.organization_id AND b.id = br.block_id
		LEFT JOIN content.message_blocks mb ON mb.organization_id = br.organization_id AND mb.block_revision_id = br.id
		WHERE cc.organization_id = $1::uuid AND cc.asset_id = $2::uuid
		ORDER BY bp.position
	`, principal.OrganizationID, assetID)
	if err != nil {
		return nil, fmt.Errorf("snapshot asset blocks: %w", err)
	}
	defer rows.Close()
	blocks := []PatternBlock{}
	for rows.Next() {
		var block PatternBlock
		if err := rows.Scan(&block.Kind, &block.Role, &block.Content); err != nil {
			return nil, err
		}
		if !patternKinds[block.Kind] {
			continue
		}
		if len(block.Content) > maxPatternBlockContent {
			block.Content = block.Content[:maxPatternBlockContent]
		}
		blocks = append(blocks, block)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, ErrNotFound
	}
	return blocks, nil
}

// ListPatterns lists the organization's skeletons (name, description,
// block count, source).
func (s Service) ListPatterns(ctx context.Context, principal auth.Principal, limit int) ([]Pattern, error) {
	if !validPatternPrincipal(principal) {
		return nil, ErrInvalidInput
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if s.Store == nil || s.Store.Pool == nil {
		return nil, errors.New("database store is not initialized")
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, name, description, blocks, COALESCE(source_asset_id::text, ''), created_by::text, created_at
		FROM content.patterns
		WHERE organization_id = $1::uuid
		ORDER BY updated_at DESC, id
		LIMIT $2
	`, principal.OrganizationID, limit)
	if err != nil {
		return nil, fmt.Errorf("list patterns: %w", err)
	}
	defer rows.Close()
	items := []Pattern{}
	for rows.Next() {
		var item Pattern
		var payload []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &payload, &item.SourceAssetID, &item.CreatedBy, &item.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &item.Blocks); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DeletePattern removes a skeleton by name or id; applied history is
// unaffected (its blocks were copied into versioned trees).
func (s Service) DeletePattern(ctx context.Context, principal auth.Principal, identifier string) error {
	if !validPatternPrincipal(principal) {
		return ErrInvalidInput
	}
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	commandTag, err := s.Store.Pool.Exec(ctx, `
		DELETE FROM content.patterns
		WHERE organization_id = $1::uuid AND (id::text = $2 OR name = $2)
	`, principal.OrganizationID, identifier)
	if err != nil {
		return fmt.Errorf("delete pattern: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ApplyPattern appends a skeleton's blocks onto a note's live tree in one
// transaction (member command — see the file header for the authorization
// boundary). The tree revision and the draft epoch advance once; the commit
// flow freezes the result into the next version.
func (s Service) ApplyPattern(ctx context.Context, principal auth.Principal, idempotencyKey, conversationID, identifier string) (int, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) ||
		!validID(conversationID) || !validIdempotencyKey(idempotencyKey) || strings.TrimSpace(identifier) == "" {
		return 0, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return 0, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin pattern apply: %w", err)
	}
	defer tx.Rollback(ctx)
	state, err := reserveIdempotency(ctx, tx, principal, "conversation.pattern.apply", idempotencyKey,
		hashRequest(struct {
			ConversationID string
			Identifier     string
		}{conversationID, identifier}))
	if err != nil {
		return 0, err
	}
	if state.replay {
		var applied int
		if err := json.Unmarshal(state.body, &applied); err != nil {
			return 0, fmt.Errorf("decode idempotent pattern apply: %w", err)
		}
		return applied, nil
	}
	containerID, noteAssetID, revision, err := lockNoteContainer(ctx, tx, principal, conversationID)
	if err != nil {
		return 0, err
	}
	var payload []byte
	err = tx.QueryRow(ctx, `
		SELECT blocks FROM content.patterns
		WHERE organization_id = $1::uuid AND (id::text = $2 OR name = $2)
	`, principal.OrganizationID, strings.TrimSpace(identifier)).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("load pattern: %w", err)
	}
	blocks := []PatternBlock{}
	if err := json.Unmarshal(payload, &blocks); err != nil {
		return 0, fmt.Errorf("decode pattern blocks: %w", err)
	}
	if err := validPatternBlocks(blocks); err != nil {
		return 0, err
	}
	for _, block := range blocks {
		if _, err := insertManualBlock(ctx, tx, principal.OrganizationID, containerID, noteAssetID, revision, block.Kind, block.Content, "", principal.UserID); err != nil {
			return 0, err
		}
		revision++
	}
	if err := saveIdempotency(ctx, tx, principal, "conversation.pattern.apply", idempotencyKey, jsonMarshalValue(len(blocks))); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit pattern apply: %w", err)
	}
	return len(blocks), nil
}
