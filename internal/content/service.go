package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	assetservice "agentchunzhi/internal/asset"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInput = errors.New("invalid content input")
	ErrForbidden    = errors.New("content access denied")
	ErrNotFound     = errors.New("content not found")
	ErrConflict     = errors.New("content conflict")
)

type Service struct {
	Store  *store.Store
	Events eventing.EventStore
}

type CreateConversationInput struct {
	WorkspaceID        string
	AgentApplicationID string
	ContainerID        string
	Title              string
	Source             string
	Visibility         string
}

type ConversationResult struct {
	ConversationID     string `json:"conversation_id"`
	WorkspaceID        string `json:"workspace_id"`
	ContainerID        string `json:"container_id"` // the note document container (live tree)
	NoteAssetID        string `json:"note_asset_id"`
	AgentApplicationID string `json:"agent_application_id"`
	BoundAgentUserID   string `json:"bound_agent_user_id"`
	Status             string `json:"status"`
}

type Conversation struct {
	ConversationResult
	Title                string `json:"title"`
	Source               string `json:"source"`
	Visibility           string `json:"visibility"`
	ParentConversationID string `json:"parent_conversation_id"`
	OriginDerivationID   string `json:"origin_derivation_id"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
}

type RegisterMediaInput struct {
	ConversationID string
	AttachmentID   string
	MediaKind      string
	Language       string
	DurationMS     *int64
}

type MediaResult struct {
	MediaID                      string `json:"media_id"`
	ConversationID               string `json:"conversation_id"`
	AttachmentID                 string `json:"attachment_id"`
	MediaKind                    string `json:"media_kind"`
	Status                       string `json:"status"`
	Language                     string `json:"language,omitempty"`
	DurationMS                   *int64 `json:"duration_ms,omitempty"`
	TranscriptionJobID           string `json:"transcription_job_id,omitempty"`
	TranscriptionBlockRevisionID string `json:"transcription_block_revision_id,omitempty"`
	CreatedAt                    string `json:"created_at"`
	UpdatedAt                    string `json:"updated_at"`
}

type AppendMessageInput struct {
	ConversationID         string
	Role                   string
	Content                string
	ContentFormat          string
	ProviderConversationID string
	ProviderMessageID      string
	Status                 string
	ReplyToBlockID         string
	References             []MessageReferenceInput
}

type MessageReferenceInput struct {
	AssetID        string
	AssetVersionID string
	Title          string
	URL            string
	SourceExcerpt  string
	UpdatedAt      string
}

type MessageResult struct {
	BlockRevisionID string `json:"block_revision_id"`
	BlockID         string `json:"block_id"`
	ConversationID  string `json:"conversation_id"`
	SequenceNo      int64  `json:"sequence_no"`
	Role            string `json:"role"`
	Status          string `json:"status"`
}

type Message struct {
	BlockRevisionID      string             `json:"block_revision_id"`
	BlockID              string             `json:"block_id"`
	ConversationID       string             `json:"conversation_id"`
	Role                 string             `json:"role"`
	Content              string             `json:"content"`
	ContentFormat        string             `json:"content_format"`
	Status               string             `json:"status"`
	ProviderConversation string             `json:"provider_conversation_id,omitempty"`
	ProviderMessage      string             `json:"provider_message_id,omitempty"`
	SequenceNo           int64              `json:"sequence_no"`
	CreatedAt            string             `json:"created_at"`
	References           []MessageReference `json:"references"`
}

type MessageReference struct {
	AssetID        string `json:"asset_id"`
	AssetVersionID string `json:"asset_version_id"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	SourceExcerpt  string `json:"source_excerpt,omitempty"`
	UpdatedAt      string `json:"updated_at"`
}

type CreateDerivationInput struct {
	SourceConversationID   string
	SourceBlockRevisionIDs []string
	ContextPolicy          string
	Title                  string
}

type DerivationSource struct {
	Ordinal               int    `json:"ordinal"`
	Origin                string `json:"origin"`
	SourceContainerID     string `json:"source_container_id"`
	SourceBlockRevisionID string `json:"source_block_revision_id"`
	SourceExcerpt         string `json:"source_excerpt"`
	ContextRole           string `json:"context_role"`
}

// derivationSourceBlock is one selected source block of a derivation: the
// excerpt plus where it came from (message / chat container / note
// container), kept for the context snapshot and the opening seed message.
type derivationSourceBlock struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	Origin      string `json:"origin"`
	ContainerID string `json:"-"`
}

type DerivationResult struct {
	DerivationID         string              `json:"derivation_id"`
	SourceConversationID string              `json:"source_conversation_id"`
	TargetConversationID string              `json:"target_conversation_id"`
	TargetNoteAssetID    string              `json:"target_note_asset_id"`
	Operation            string              `json:"operation"`
	ContextPolicy        string              `json:"context_policy"`
	Status               string              `json:"status"`
	CreatedAt            string              `json:"created_at"`
	CompletedAt          string              `json:"completed_at"`
	Sources              []DerivationSource  `json:"sources,omitempty"`
}

func (s Service) CreateDerivation(ctx context.Context, principal auth.Principal, idempotencyKey string, input CreateDerivationInput) (DerivationResult, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) ||
		!validID(input.SourceConversationID) || !validIdempotencyKey(idempotencyKey) || len(input.SourceBlockRevisionIDs) == 0 || len(input.SourceBlockRevisionIDs) > 50 {
		return DerivationResult{}, ErrInvalidInput
	}
	input.ContextPolicy = strings.TrimSpace(input.ContextPolicy)
	if input.ContextPolicy == "" {
		input.ContextPolicy = "summary_only"
	}
	if input.ContextPolicy != "summary_only" && input.ContextPolicy != "selected_only" && input.ContextPolicy != "full" {
		return DerivationResult{}, ErrInvalidInput
	}
	for _, id := range input.SourceBlockRevisionIDs {
		if !validID(id) {
			return DerivationResult{}, ErrInvalidInput
		}
	}
	if s.Store == nil || s.Store.Pool == nil {
		return DerivationResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return DerivationResult{}, fmt.Errorf("begin derivation create: %w", err)
	}
	defer tx.Rollback(ctx)
	state, err := reserveIdempotency(ctx, tx, principal, "conversation.derivation.create", idempotencyKey, hashRequest(input))
	if err != nil {
		return DerivationResult{}, err
	}
	if state.replay {
		var result DerivationResult
		if err := json.Unmarshal(state.body, &result); err != nil {
			return DerivationResult{}, fmt.Errorf("decode idempotent derivation: %w", err)
		}
		return result, nil
	}
	var workspaceID, sourceContainerID, sourceTitle, sourceVisibility, appID, boundAgent string
	var noteModelID, noteModelVersionID string
	var sourceMarkdown string
	err = tx.QueryRow(ctx, `
		SELECT c.workspace_id::text, cc.id::text, c.title, c.visibility,
		       c.agent_application_id::text, c.bound_agent_user_id::text,
		       COALESCE(av.markdown, ''),
		       av.resource_model_id::text, av.resource_model_version_id::text
		FROM content.conversations c
                JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
		JOIN content.note_bindings nb ON nb.conversation_id = c.id
		JOIN content.containers cc ON cc.organization_id = nb.organization_id AND cc.asset_id = nb.note_asset_id
		JOIN asset.assets a ON a.id = nb.note_asset_id AND a.organization_id = c.organization_id
		JOIN asset.asset_versions av ON av.id = a.current_working_version_id
		WHERE c.organization_id = $1::uuid AND c.id = $2::uuid AND c.status = 'active'
		FOR UPDATE OF c, cc, nb, a
	`, principal.OrganizationID, input.SourceConversationID, principal.UserID).Scan(
		&workspaceID, &sourceContainerID, &sourceTitle, &sourceVisibility, &appID, &boundAgent,
		&sourceMarkdown,
		&noteModelID, &noteModelVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DerivationResult{}, ErrNotFound
	}
	if err != nil {
		return DerivationResult{}, fmt.Errorf("load derivation source conversation: %w", err)
	}
	sources := make(map[string]derivationSourceBlock, len(input.SourceBlockRevisionIDs))
	rows, err := tx.Query(ctx, `
		SELECT br.id::text, br.content,
		       CASE WHEN mb.block_revision_id IS NULL THEN 'manual' ELSE 'message' END
		FROM content.block_placements bp
		JOIN content.block_revisions br ON br.organization_id = bp.organization_id AND br.id = bp.block_revision_id
		LEFT JOIN content.message_blocks mb ON mb.organization_id = bp.organization_id AND mb.block_revision_id = br.id
		WHERE bp.organization_id = $1::uuid AND bp.container_id = $3::uuid AND br.id = ANY($2::uuid[])
	`, principal.OrganizationID, input.SourceBlockRevisionIDs, sourceContainerID)
	if err != nil {
		return DerivationResult{}, fmt.Errorf("load derivation source blocks: %w", err)
	}
	for rows.Next() {
		var item derivationSourceBlock
		if err := rows.Scan(&item.ID, &item.Content, &item.Origin); err != nil {
			rows.Close()
			return DerivationResult{}, fmt.Errorf("scan derivation source block: %w", err)
		}
		item.ContainerID = sourceContainerID
		sources[item.ID] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return DerivationResult{}, fmt.Errorf("iterate derivation source blocks: %w", err)
	}
	rows.Close()
	for _, id := range input.SourceBlockRevisionIDs {
		if _, ok := sources[id]; !ok {
			return DerivationResult{}, ErrNotFound
		}
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = "Derived: " + sourceTitle
	}
	contextItems := make([]derivationSourceBlock, 0, len(input.SourceBlockRevisionIDs))
	for _, id := range input.SourceBlockRevisionIDs {
		item := sources[id]
		if len(item.Content) > 2000 {
			item.Content = item.Content[:2000]
		}
		contextItems = append(contextItems, item)
	}
	contextSnapshot, _ := json.Marshal(map[string]any{
		"source_conversation_id":    input.SourceConversationID,
		"source_container_id":       sourceContainerID,
		"context_policy":            input.ContextPolicy,
		"source_block_revision_ids": input.SourceBlockRevisionIDs,
		"blocks":                    contextItems,
	})
	var derivationID, derivationCreatedAt string
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.derivations
			(organization_id, workspace_id, source_conversation_id, operation, context_policy, context_snapshot, created_by, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'create_chat', $4, $5::jsonb, $6::uuid, 'requested')
		RETURNING id::text, created_at::text
	`, principal.OrganizationID, workspaceID, input.SourceConversationID, input.ContextPolicy, string(contextSnapshot), principal.UserID).Scan(&derivationID, &derivationCreatedAt); err != nil {
		return DerivationResult{}, fmt.Errorf("create derivation: %w", err)
	}
	var noteAsset string
	if err := tx.QueryRow(ctx, `
			INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, created_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid) RETURNING id::text
		`, principal.OrganizationID, workspaceID, noteModelID, principal.UserID).Scan(&noteAsset); err != nil {
		return DerivationResult{}, fmt.Errorf("create derived note asset: %w", err)
	}
	if _, err := createAssetVersionWithDraft(ctx, tx, principal.OrganizationID, workspaceID, noteAsset, noteModelID, noteModelVersionID, title, "", nil, principal.UserID); err != nil {
		return DerivationResult{}, err
	}
	var noteContainer string
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.containers (organization_id, workspace_id, kind, title, visibility, created_by, asset_id, revision)
		VALUES ($1::uuid, $2::uuid, 'note', $3, $4, $5::uuid, $6::uuid, 1) RETURNING id::text
	`, principal.OrganizationID, workspaceID, title, sourceVisibility, principal.UserID, noteAsset).Scan(&noteContainer); err != nil {
		return DerivationResult{}, fmt.Errorf("create derived note container: %w", err)
	}
	var targetConversation string
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.conversations
			(organization_id, workspace_id, initiator_user_id, agent_application_id,
			 bound_agent_user_id, parent_conversation_id, origin_derivation_id, title, source, visibility, source_summary)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid, $8, 'chat_interface', $9, $10::jsonb)
		RETURNING id::text
	`, principal.OrganizationID, workspaceID, principal.UserID, appID, boundAgent, input.SourceConversationID, derivationID, title, sourceVisibility, string(contextSnapshot)).Scan(&targetConversation); err != nil {
		return DerivationResult{}, fmt.Errorf("create derived conversation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO content.note_bindings (organization_id, conversation_id, note_asset_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid)
	`, principal.OrganizationID, targetConversation, noteAsset); err != nil {
		return DerivationResult{}, fmt.Errorf("bind derived note: %w", err)
	}
	for ordinal, id := range input.SourceBlockRevisionIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO content.derivation_sources
				(derivation_id, ordinal, source_container_id, source_block_revision_id, source_excerpt, context_role)
			VALUES ($1::uuid, $2, $3::uuid, $4::uuid, $5, 'selected')
		`, derivationID, ordinal, sourceContainerID, id, sources[id].Content); err != nil {
			return DerivationResult{}, fmt.Errorf("save derivation source: %w", err)
		}
	}
	if err := injectDerivationContext(ctx, tx, principal.OrganizationID, targetConversation, noteContainer, noteAsset, input.ContextPolicy, sourceTitle, sourceMarkdown, contextItems, principal.UserID); err != nil {
		return DerivationResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE content.derivations SET target_conversation_id = $2::uuid, status = 'discussing', updated_at = now() WHERE id = $1::uuid`, derivationID, targetConversation); err != nil {
		return DerivationResult{}, fmt.Errorf("advance derivation status: %w", err)
	}
	result := DerivationResult{DerivationID: derivationID, SourceConversationID: input.SourceConversationID, TargetConversationID: targetConversation, TargetNoteAssetID: noteAsset, Operation: "create_chat", ContextPolicy: input.ContextPolicy, Status: "discussing", CreatedAt: derivationCreatedAt}
	body, _ := json.Marshal(result)
	if err := saveIdempotency(ctx, tx, principal, "conversation.derivation.create", idempotencyKey, body); err != nil {
		return DerivationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DerivationResult{}, fmt.Errorf("commit derivation create: %w", err)
	}
	return result, nil
}

// appendMessageBlockTx appends one message as a block of the conversation's
// live note tree: block + revision + placement at the tree tail, tree
// revision bump (the draft epoch advances with it so publish sees the dirty
// state), message metadata and references. Callers must hold the transaction
// and have locked the container row.
func appendMessageBlockTx(ctx context.Context, tx pgx.Tx, organizationID, conversationID, containerID, noteAssetID string, input AppendMessageInput, userID string) (MessageResult, error) {
	var nextSequence int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(sequence_no), 0) + 1
		FROM content.message_blocks
		WHERE organization_id = $1::uuid AND conversation_id = $2::uuid
	`, organizationID, conversationID).Scan(&nextSequence); err != nil {
		return MessageResult{}, fmt.Errorf("allocate message sequence: %w", err)
	}
	var nextPosition float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(position) + 1, 0)
		FROM content.block_placements
		WHERE organization_id = $1::uuid AND container_id = $2::uuid
	`, organizationID, containerID).Scan(&nextPosition); err != nil {
		return MessageResult{}, fmt.Errorf("allocate block position: %w", err)
	}
	checksum := hashBytes(input.Content)
	var blockID, revisionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.blocks (organization_id, block_type, created_by)
		VALUES ($1::uuid, 'message', $2::uuid) RETURNING id::text
	`, organizationID, userID).Scan(&blockID); err != nil {
		return MessageResult{}, fmt.Errorf("create message block: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.block_revisions
			(organization_id, block_id, revision_no, content, content_format, created_by, content_checksum)
		VALUES ($1::uuid, $2::uuid, 1, $3, $4, $5::uuid, $6)
		RETURNING id::text
	`, organizationID, blockID, input.Content, input.ContentFormat, userID, checksum).Scan(&revisionID); err != nil {
		return MessageResult{}, fmt.Errorf("create message revision: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO content.block_placements
			(organization_id, container_id, block_revision_id, position)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4)
	`, organizationID, containerID, revisionID, nextPosition); err != nil {
		return MessageResult{}, fmt.Errorf("place message block: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE content.containers SET revision = revision + 1, updated_at = now() WHERE id = $1::uuid
	`, containerID); err != nil {
		return MessageResult{}, fmt.Errorf("advance note tree: %w", err)
	}
	if noteAssetID != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE asset.asset_drafts SET revision = revision + 1, updated_at = now() WHERE organization_id = $1::uuid AND asset_id = $2::uuid
		`, organizationID, noteAssetID); err != nil {
			return MessageResult{}, fmt.Errorf("advance note draft epoch: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO content.message_blocks
			(organization_id, block_revision_id, conversation_id, role, provider_conversation_id,
			 provider_message_id, status, reply_to_block_id, sequence_no)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, NULLIF($5, ''), NULLIF($6, ''), $7, NULLIF($8, '')::uuid, $9)
	`, organizationID, revisionID, conversationID, input.Role, input.ProviderConversationID, input.ProviderMessageID, input.Status, input.ReplyToBlockID, nextSequence); err != nil {
		return MessageResult{}, fmt.Errorf("persist message metadata: %w", err)
	}
	for ordinal, reference := range input.References {
		if _, err := tx.Exec(ctx, `
			INSERT INTO content.message_references
				(organization_id, block_revision_id, ordinal, asset_id, asset_version_id, title, url, source_excerpt, updated_at_snapshot)
			VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5::uuid, $6, $7, $8, $9)
		`, organizationID, revisionID, ordinal, reference.AssetID, reference.AssetVersionID,
			reference.Title, reference.URL, reference.SourceExcerpt, reference.UpdatedAt); err != nil {
			return MessageResult{}, fmt.Errorf("persist message reference: %w", err)
		}
	}
	return MessageResult{BlockRevisionID: revisionID, BlockID: blockID, ConversationID: conversationID, SequenceNo: nextSequence, Role: input.Role, Status: input.Status}, nil
}

// injectDerivationContext seeds the derived conversation with an opening
// assistant message carrying the selected source material, so the new chat
// starts from the harvested context instead of a blank slate. The context
// policy decides how much travels: summary_only keeps a short digest of each
// selected block, selected_only carries the excerpts verbatim, full prepends
// the whole source note markdown.
func injectDerivationContext(ctx context.Context, tx pgx.Tx, organizationID, conversationID, containerID, noteAssetID, policy, sourceTitle, sourceMarkdown string, blocks []derivationSourceBlock, userID string) error {
	excerptLimit := 2000
	if policy == "summary_only" {
		excerptLimit = 200
	}
	var body strings.Builder
	if policy == "full" {
		if trimmed := strings.TrimSpace(sourceMarkdown); trimmed != "" {
			body.WriteString("## Source note\n\n")
			body.WriteString(trimmed)
			body.WriteString("\n\n")
		}
	}
	for index, block := range blocks {
		excerpt := block.Content
		if runes := []rune(excerpt); len(runes) > excerptLimit {
			excerpt = string(runes[:excerptLimit]) + " …"
		}
		if strings.TrimSpace(excerpt) == "" {
			continue
		}
		body.WriteString(fmt.Sprintf("### Excerpt %d · %s\n\n%s\n\n", index+1, block.Origin, excerpt))
	}
	content := strings.TrimSpace(body.String())
	if content == "" {
		return nil
	}
	if runes := []rune(content); len(runes) > 24000 {
		content = string(runes[:24000]) + "\n\n…(truncated)"
	}
	message := fmt.Sprintf("> Derived from %q · context policy: %s\n\n%s", sourceTitle, policy, content)
	_, err := appendMessageBlockTx(ctx, tx, organizationID, conversationID, containerID, noteAssetID, AppendMessageInput{
		Role: "assistant", Content: message, ContentFormat: "markdown", Status: "completed",
	}, userID)
	return err
}

func (s Service) GetDerivation(ctx context.Context, principal auth.Principal, derivationID string) (DerivationResult, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) || !validID(derivationID) {
		return DerivationResult{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return DerivationResult{}, errors.New("database store is not initialized")
	}
	var result DerivationResult
	var contextSnapshot []byte
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT d.id::text, d.source_conversation_id::text, d.target_conversation_id::text,
		       COALESCE(nb.note_asset_id::text, ''), d.operation, d.context_policy, d.status,
		       d.created_at::text, COALESCE(d.completed_at::text, ''), d.context_snapshot
		FROM content.derivations d
		JOIN content.workspace_members wm ON wm.workspace_id = d.workspace_id AND wm.user_id = $3::uuid
		LEFT JOIN content.note_bindings nb ON nb.conversation_id = d.target_conversation_id
		WHERE d.organization_id = $1::uuid AND d.id = $2::uuid
	`, principal.OrganizationID, derivationID, principal.UserID).Scan(
		&result.DerivationID, &result.SourceConversationID, &result.TargetConversationID,
		&result.TargetNoteAssetID, &result.Operation, &result.ContextPolicy, &result.Status,
		&result.CreatedAt, &result.CompletedAt, &contextSnapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return DerivationResult{}, ErrNotFound
	}
	if err != nil {
		return DerivationResult{}, fmt.Errorf("load derivation: %w", err)
	}
	// The per-block origin (message / chat / note) only lives in the context
	// snapshot; derivation_sources keeps the container pointers and excerpts.
	var snapshot struct {
		Blocks []derivationSourceBlock `json:"blocks"`
	}
	_ = json.Unmarshal(contextSnapshot, &snapshot)
	origins := make(map[string]string, len(snapshot.Blocks))
	for _, block := range snapshot.Blocks {
		origins[block.ID] = block.Origin
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT ordinal, source_container_id::text,
		       source_block_revision_id::text, COALESCE(source_excerpt, ''), context_role
		FROM content.derivation_sources
		WHERE derivation_id = $1::uuid
		ORDER BY ordinal
	`, derivationID)
	if err != nil {
		return DerivationResult{}, fmt.Errorf("load derivation sources: %w", err)
	}
	defer rows.Close()
	result.Sources = []DerivationSource{}
	for rows.Next() {
		var item DerivationSource
		if err := rows.Scan(&item.Ordinal, &item.SourceContainerID,
			&item.SourceBlockRevisionID, &item.SourceExcerpt, &item.ContextRole); err != nil {
			return DerivationResult{}, fmt.Errorf("scan derivation source: %w", err)
		}
		item.Origin = origins[item.SourceBlockRevisionID]
		result.Sources = append(result.Sources, item)
	}
	if err := rows.Err(); err != nil {
		return DerivationResult{}, fmt.Errorf("iterate derivation sources: %w", err)
	}
	return result, nil
}

type FinalizeDerivationInput struct {
	Disposition                  string
	TargetAssetID                string
	ExpectedSourceAssetVersionID string
	ExpectedTargetAssetVersionID string
	ExpectedContainerVersionID   string
	MergeMode                    string
	TargetBlockID                string
	// AutoArchive archives the derived conversation after a successful
	// harvest; nil counts as true.
	AutoArchive *bool
}

type FinalizeResult struct {
	DerivationID   string `json:"derivation_id"`
	Disposition    string `json:"disposition"`
	AssetID        string `json:"asset_id"`
	AssetVersionID string `json:"asset_version_id"`
	RelationID     string `json:"relation_id,omitempty"`
	Status         string `json:"status"`
}

func (s Service) FinalizeDerivation(ctx context.Context, principal auth.Principal, idempotencyKey, derivationID string, input FinalizeDerivationInput) (FinalizeResult, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) ||
		!validID(derivationID) || !validIdempotencyKey(idempotencyKey) {
		return FinalizeResult{}, ErrInvalidInput
	}
	input.Disposition = strings.TrimSpace(input.Disposition)
	input.MergeMode = strings.TrimSpace(input.MergeMode)
	if input.Disposition != "independent" && input.Disposition != "reference" && input.Disposition != "merge" {
		return FinalizeResult{}, ErrInvalidInput
	}
	if input.ExpectedSourceAssetVersionID != "" && !validID(input.ExpectedSourceAssetVersionID) {
		return FinalizeResult{}, ErrInvalidInput
	}
	if input.TargetAssetID != "" && !validID(input.TargetAssetID) {
		return FinalizeResult{}, ErrInvalidInput
	}
	if input.ExpectedTargetAssetVersionID != "" && !validID(input.ExpectedTargetAssetVersionID) {
		return FinalizeResult{}, ErrInvalidInput
	}
	if input.ExpectedContainerVersionID != "" && !validID(input.ExpectedContainerVersionID) {
		return FinalizeResult{}, ErrInvalidInput
	}
	if input.TargetBlockID != "" && !validID(input.TargetBlockID) {
		return FinalizeResult{}, ErrInvalidInput
	}
	if input.Disposition == "merge" && (input.TargetAssetID == "" || input.ExpectedTargetAssetVersionID == "" || input.MergeMode == "") {
		return FinalizeResult{}, ErrInvalidInput
	}
	if input.Disposition == "merge" && input.MergeMode != "append" {
		return FinalizeResult{}, fmt.Errorf("%w: only append merge mode is currently supported", ErrConflict)
	}
	if s.Store == nil || s.Store.Pool == nil {
		return FinalizeResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("begin derivation finalize: %w", err)
	}
	defer tx.Rollback(ctx)
	state, err := reserveIdempotency(ctx, tx, principal, "conversation.derivation.finalize", idempotencyKey, hashRequest(input))
	if err != nil {
		return FinalizeResult{}, err
	}
	if state.replay {
		var result FinalizeResult
		if err := json.Unmarshal(state.body, &result); err != nil {
			return FinalizeResult{}, fmt.Errorf("decode idempotent derivation finalize: %w", err)
		}
		return result, nil
	}
	var workspaceID, sourceConversationID, targetConversationID, sourceNoteAssetID, targetNoteAssetID, derivationStatus string
	if err := tx.QueryRow(ctx, `
		SELECT d.workspace_id::text, d.source_conversation_id::text, d.target_conversation_id::text,
		       snb.note_asset_id::text, tnb.note_asset_id::text, d.status
		FROM content.derivations d
		JOIN content.workspace_members wm ON wm.workspace_id = d.workspace_id AND wm.user_id = $3::uuid
		JOIN content.note_bindings snb ON snb.conversation_id = d.source_conversation_id
		JOIN content.note_bindings tnb ON tnb.conversation_id = d.target_conversation_id
		WHERE d.organization_id = $1::uuid AND d.id = $2::uuid
		FOR UPDATE OF d, snb, tnb
	`, principal.OrganizationID, derivationID, principal.UserID).Scan(&workspaceID, &sourceConversationID, &targetConversationID, &sourceNoteAssetID, &targetNoteAssetID, &derivationStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return FinalizeResult{}, ErrNotFound
		}
		return FinalizeResult{}, fmt.Errorf("load derivation for finalize: %w", err)
	}
	if derivationStatus == "completed" {
		return FinalizeResult{}, ErrConflict
	}
	if derivationStatus != "discussing" && derivationStatus != "result_ready" && derivationStatus != "finalizing" {
		return FinalizeResult{}, fmt.Errorf("%w: derivation is not ready", ErrConflict)
	}
	// The harvest output must be the committed state of the derivation's
	// discussion: freeze its note first when the live tree moved since the
	// last save (title/fields flow through the draft row).
	targetRow, err := assetservice.LoadLifecycleTx(ctx, tx, principal.OrganizationID, targetNoteAssetID)
	if err != nil {
		return FinalizeResult{}, err
	}
	targetDraft, err := assetservice.LoadDraftTx(ctx, tx, principal.OrganizationID, targetNoteAssetID, "")
	if err != nil {
		return FinalizeResult{}, err
	}
	if targetDraft.Revision != targetDraft.CommittedRevision {
		if _, err := assetservice.FreezeNoteDraftTx(ctx, tx, &s.Events, principal, targetRow, targetDraft); err != nil {
			return FinalizeResult{}, fmt.Errorf("freeze derivation note: %w", err)
		}
	}
	// Harvest reads the DERIVED conversation's note (target): the derivation
	// exists so its discussion can grow into a result; the trunk note was
	// already harvested when the derivation was created.
	var sourceVersionID, sourceModelID, sourceModelVersionID, sourceTitle string
	var sourceMarkdown, sourceFields []byte
	if err := tx.QueryRow(ctx, `
		SELECT av.id::text, av.resource_model_id::text, av.resource_model_version_id::text,
		       COALESCE(av.title, ''), COALESCE(av.markdown, ''), av.fields
		FROM asset.assets a JOIN asset.asset_versions av ON av.organization_id = a.organization_id AND av.id = a.current_working_version_id
		WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
		FOR UPDATE OF a, av
	`, principal.OrganizationID, targetNoteAssetID).Scan(&sourceVersionID, &sourceModelID, &sourceModelVersionID, &sourceTitle, &sourceMarkdown, &sourceFields); err != nil {
		return FinalizeResult{}, fmt.Errorf("load derivation note version: %w", err)
	}
	if input.ExpectedSourceAssetVersionID != "" && input.ExpectedSourceAssetVersionID != sourceVersionID {
		return FinalizeResult{}, fmt.Errorf("%w: derivation note version changed", ErrConflict)
	}
	if strings.TrimSpace(string(sourceMarkdown)) == "" {
		return FinalizeResult{}, fmt.Errorf("%w: derivation note is empty; save the derived conversation into its note first", ErrConflict)
	}
	// Harvest output is a standalone document: it always uses the
	// organization's builtin_document model (form-authored, markdown source
	// of truth after the copy), never the note model it grew out of.
	var docModelID, docModelVersionID string
	if err := tx.QueryRow(ctx, `
		SELECT m.id::text, COALESCE(m.current_version_id::text, '')
		FROM model.resource_models m
		WHERE m.organization_id = $1::uuid AND m.model_key = 'builtin_document'
		  AND m.status = 'active' AND m.workspace_id IS NULL
	`, principal.OrganizationID).Scan(&docModelID, &docModelVersionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return FinalizeResult{}, fmt.Errorf("%w: builtin_document model is not provisioned", ErrConflict)
		}
		return FinalizeResult{}, fmt.Errorf("resolve document model: %w", err)
	}
	if docModelVersionID == "" {
		return FinalizeResult{}, fmt.Errorf("%w: builtin_document model has no bound version", ErrConflict)
	}
	result := FinalizeResult{DerivationID: derivationID, Disposition: input.Disposition, Status: "completed"}
	if input.Disposition == "merge" {
		var targetVersionID, targetModelID, targetModelVersionID, targetTitle string
		var targetMarkdown, targetFields []byte
		if err := tx.QueryRow(ctx, `
		SELECT av.id::text, av.resource_model_id::text, av.resource_model_version_id::text,
		       COALESCE(av.title, ''), COALESCE(av.markdown, ''), av.fields
		FROM asset.assets a JOIN asset.asset_versions av ON av.organization_id = a.organization_id AND av.id = a.current_working_version_id
		WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
		FOR UPDATE OF a, av
	`, principal.OrganizationID, input.TargetAssetID).Scan(&targetVersionID, &targetModelID, &targetModelVersionID, &targetTitle, &targetMarkdown, &targetFields); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return FinalizeResult{}, ErrNotFound
			}
			return FinalizeResult{}, fmt.Errorf("load merge target: %w", err)
		}
		if targetVersionID != input.ExpectedTargetAssetVersionID {
			return FinalizeResult{}, fmt.Errorf("%w: target version changed", ErrConflict)
		}
		var targetFieldsMap map[string]any
		if len(targetFields) > 0 {
			_ = json.Unmarshal(targetFields, &targetFieldsMap)
		}
		mergedMarkdown := strings.TrimSpace(string(targetMarkdown)) + "\n\n" + strings.TrimSpace(string(sourceMarkdown))
		mergeVersionID, _, err := assetservice.CreateVersionTx(ctx, tx, assetservice.VersionMaterial{
			OrganizationID:         principal.OrganizationID,
			WorkspaceID:            workspaceID,
			AssetID:                input.TargetAssetID,
			ResourceModelID:        targetModelID,
			ResourceModelVersionID: targetModelVersionID,
			ParentVersionID:        targetVersionID,
			Origin:                 assetservice.OriginHuman,
			ConfirmationStatus:     assetservice.ConfirmationUnconfirmed,
			Title:                  targetTitle,
			Markdown:               mergedMarkdown,
			Fields:                 targetFieldsMap,
			CreatedBy:              principal.UserID,
			Relations:              []assetservice.RelationMaterial{derivationRelation(targetNoteAssetID, assetservice.RelationContinuesFrom, derivationID)},
		})
		if err != nil {
			return FinalizeResult{}, fmt.Errorf("create merge version: %w", err)
		}
		result.AssetVersionID = mergeVersionID
		if err := advanceAssetDraft(ctx, tx, principal.OrganizationID, workspaceID, input.TargetAssetID, mergeVersionID, targetTitle, mergedMarkdown, targetFieldsMap, principal.UserID); err != nil {
			return FinalizeResult{}, err
		}
		result.AssetID = input.TargetAssetID
		if err := loadMaterializedRelationID(ctx, tx, principal.OrganizationID, result.AssetVersionID, assetservice.RelationContinuesFrom, &result.RelationID); err != nil {
			return FinalizeResult{}, err
		}
	} else {
		var title string
		if err := tx.QueryRow(ctx, `SELECT title FROM content.conversations WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, targetConversationID).Scan(&title); err != nil {
			return FinalizeResult{}, fmt.Errorf("load result note title: %w", err)
		}
		// The harvest output is a form-authored document asset: its markdown
		// render is the source of truth; no container rides along.
		if err := tx.QueryRow(ctx, `
					INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, created_by)
					VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid) RETURNING id::text
				`, principal.OrganizationID, workspaceID, docModelID, principal.UserID).Scan(&result.AssetID); err != nil {
			return FinalizeResult{}, fmt.Errorf("create result document asset: %w", err)
		}
		var sourceFieldsMap map[string]any
		if len(sourceFields) > 0 {
			_ = json.Unmarshal(sourceFields, &sourceFieldsMap)
		}
		relations := []assetservice.RelationMaterial(nil)
		if input.Disposition == "reference" {
			relations = append(relations, derivationRelation(targetNoteAssetID, assetservice.RelationDerivedFrom, derivationID))
		}
		resultVersionID, _, err := assetservice.CreateVersionTx(ctx, tx, assetservice.VersionMaterial{
			OrganizationID:         principal.OrganizationID,
			WorkspaceID:            workspaceID,
			AssetID:                result.AssetID,
			ResourceModelID:        docModelID,
			ResourceModelVersionID: docModelVersionID,
			Origin:                 assetservice.OriginHuman,
			ConfirmationStatus:     assetservice.ConfirmationUnconfirmed,
			Title:                  title,
			Markdown:               string(sourceMarkdown),
			Fields:                 sourceFieldsMap,
			CreatedBy:              principal.UserID,
			Relations:              relations,
		})
		if err != nil {
			return FinalizeResult{}, fmt.Errorf("create result document version: %w", err)
		}
		if err := insertAssetDraft(ctx, tx, principal.OrganizationID, workspaceID, result.AssetID, resultVersionID, title, string(sourceMarkdown), sourceFieldsMap, principal.UserID); err != nil {
			return FinalizeResult{}, err
		}
		result.AssetVersionID = resultVersionID
		if input.Disposition == "reference" {
			if err := loadMaterializedRelationID(ctx, tx, principal.OrganizationID, result.AssetVersionID, assetservice.RelationDerivedFrom, &result.RelationID); err != nil {
				return FinalizeResult{}, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE content.derivations SET status = 'completed', completed_at = now(), updated_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, derivationID); err != nil {
		return FinalizeResult{}, fmt.Errorf("complete derivation: %w", err)
	}
	// A harvested thought leaves the intake list: archive the derived
	// conversation unless the caller explicitly wants to keep talking.
	autoArchive := input.AutoArchive == nil || *input.AutoArchive
	if autoArchive && targetConversationID != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE content.conversations
			SET status = 'archived', completed_at = COALESCE(completed_at, now()), updated_at = now()
			WHERE organization_id = $1::uuid AND id = $2::uuid
		`, principal.OrganizationID, targetConversationID); err != nil {
			return FinalizeResult{}, fmt.Errorf("archive derived conversation: %w", err)
		}
	}
	body, _ := json.Marshal(result)
	if err := saveIdempotency(ctx, tx, principal, "conversation.derivation.finalize", idempotencyKey, body); err != nil {
		return FinalizeResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FinalizeResult{}, fmt.Errorf("commit derivation finalize: %w", err)
	}
	return result, nil
}

// derivationRelation builds the lineage out-edge of a finalized derivation
// result: the new version points back at the source note asset (its current
// working version at materialization), with the derivation id in the citation
// payload. It rides the version's creating transaction, which is the only
// window the sealed-endpoint guard allows on asset.asset_relations.
func derivationRelation(sourceNoteAssetID, relationType, derivationID string) assetservice.RelationMaterial {
	citation, _ := json.Marshal(map[string]string{"derivation_id": derivationID})
	return assetservice.RelationMaterial{
		TargetAssetID: sourceNoteAssetID,
		RelationType:  relationType,
		Source:        "manual",
		Citation:      citation,
	}
}

func loadMaterializedRelationID(ctx context.Context, tx pgx.Tx, organizationID, versionID, relationType string, out *string) error {
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM asset.asset_relations
		WHERE organization_id = $1::uuid AND source_asset_version_id = $2::uuid AND relation_type = $3
		ORDER BY created_at
		LIMIT 1
	`, organizationID, versionID, relationType).Scan(out); err != nil {
		return fmt.Errorf("load materialized derivation relation: %w", err)
	}
	return nil
}

func (s Service) AppendMessage(ctx context.Context, principal auth.Principal, idempotencyKey string, input AppendMessageInput) (MessageResult, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) ||
		!validID(input.ConversationID) || !validIdempotencyKey(idempotencyKey) {
		return MessageResult{}, ErrInvalidInput
	}
	input.Role = strings.TrimSpace(input.Role)
	input.ContentFormat = strings.TrimSpace(input.ContentFormat)
	input.Status = strings.TrimSpace(input.Status)
	if input.ContentFormat == "" {
		input.ContentFormat = "plain_text"
	}
	if input.Status == "" {
		input.Status = "completed"
	}
	if input.Role != "user" && input.Role != "assistant" && input.Role != "system" && input.Role != "tool" {
		return MessageResult{}, ErrInvalidInput
	}
	if input.ContentFormat != "plain_text" && input.ContentFormat != "markdown" && input.ContentFormat != "json" {
		return MessageResult{}, ErrInvalidInput
	}
	if input.Status != "pending" && input.Status != "completed" && input.Status != "failed" && input.Status != "cancelled" {
		return MessageResult{}, ErrInvalidInput
	}
	if input.ReplyToBlockID != "" && !validID(input.ReplyToBlockID) {
		return MessageResult{}, ErrInvalidInput
	}
	if len(input.References) > 100 {
		return MessageResult{}, ErrInvalidInput
	}
	for _, reference := range input.References {
		if !validID(reference.AssetID) || !validID(reference.AssetVersionID) {
			return MessageResult{}, ErrInvalidInput
		}
	}
	if s.Store == nil || s.Store.Pool == nil {
		return MessageResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MessageResult{}, fmt.Errorf("begin message create: %w", err)
	}
	defer tx.Rollback(ctx)
	state, err := reserveIdempotency(ctx, tx, principal, "conversation.message.create", idempotencyKey, hashRequest(input))
	if err != nil {
		return MessageResult{}, err
	}
	if state.replay {
		var result MessageResult
		if err := json.Unmarshal(state.body, &result); err != nil {
			return MessageResult{}, fmt.Errorf("decode idempotent message: %w", err)
		}
		return result, nil
	}
	var workspaceID, containerID, noteAssetID string
	if err := tx.QueryRow(ctx, `
		SELECT c.workspace_id::text, cc.id::text, nb.note_asset_id::text
		FROM content.conversations c
				JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
				JOIN content.note_bindings nb ON nb.conversation_id = c.id
				JOIN content.containers cc ON cc.organization_id = nb.organization_id AND cc.asset_id = nb.note_asset_id
				WHERE c.organization_id = $1::uuid AND c.id = $2::uuid AND c.status = 'active'
				  AND wm.role <> 'viewer'
				FOR UPDATE OF c, cc
	`, principal.OrganizationID, input.ConversationID, principal.UserID).Scan(&workspaceID, &containerID, &noteAssetID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MessageResult{}, ErrNotFound
		}
		return MessageResult{}, fmt.Errorf("load conversation for message: %w", err)
	}
	result, err := appendMessageBlockTx(ctx, tx, principal.OrganizationID, input.ConversationID, containerID, noteAssetID, input, principal.UserID)
	if err != nil {
		return MessageResult{}, err
	}
	body, _ := json.Marshal(result)
	if err := saveIdempotency(ctx, tx, principal, "conversation.message.create", idempotencyKey, body); err != nil {
		return MessageResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MessageResult{}, fmt.Errorf("commit message create: %w", err)
	}
	return result, nil
}

func (s Service) ListMessages(ctx context.Context, principal auth.Principal, conversationID string) ([]Message, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) || !validID(conversationID) {
		return nil, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return nil, errors.New("database store is not initialized")
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT br.id::text, b.id::text, mb.conversation_id::text, mb.role,
		       br.content, br.content_format, mb.status,
		       COALESCE(mb.provider_conversation_id, ''), COALESCE(mb.provider_message_id, ''),
		       mb.sequence_no, br.created_at::text,
		       COALESCE((
		           SELECT jsonb_agg(jsonb_build_object(
		               'asset_id', mr.asset_id::text,
		               'asset_version_id', mr.asset_version_id::text,
		               'title', mr.title,
		               'url', mr.url,
		               'source_excerpt', mr.source_excerpt,
		               'updated_at', mr.updated_at_snapshot
		           ) ORDER BY mr.ordinal)
		           FROM content.message_references mr
		           WHERE mr.organization_id = mb.organization_id AND mr.block_revision_id = mb.block_revision_id
		       ), '[]'::jsonb)
		FROM content.message_blocks mb
		JOIN content.block_revisions br ON br.id = mb.block_revision_id
		JOIN content.blocks b ON b.id = br.block_id
		JOIN content.conversations c ON c.id = mb.conversation_id
		JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
		WHERE mb.organization_id = $1::uuid AND mb.conversation_id = $2::uuid
		ORDER BY mb.sequence_no
	`, principal.OrganizationID, conversationID, principal.UserID)
	if err != nil {
		return nil, fmt.Errorf("list conversation messages: %w", err)
	}
	defer rows.Close()
	result := make([]Message, 0)
	for rows.Next() {
		var message Message
		var references []byte
		if err := rows.Scan(&message.BlockRevisionID, &message.BlockID, &message.ConversationID, &message.Role,
			&message.Content, &message.ContentFormat, &message.Status, &message.ProviderConversation,
			&message.ProviderMessage, &message.SequenceNo, &message.CreatedAt, &references); err != nil {
			return nil, fmt.Errorf("scan conversation message: %w", err)
		}
		message.References = []MessageReference{}
		if err := json.Unmarshal(references, &message.References); err != nil {
			return nil, fmt.Errorf("decode conversation message references: %w", err)
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation messages: %w", err)
	}
	if len(result) == 0 {
		var exists bool
		if err := s.Store.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM content.conversations c
				JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
					WHERE c.organization_id = $1::uuid AND c.id = $2::uuid
			)
		`, principal.OrganizationID, conversationID, principal.UserID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check conversation access: %w", err)
		}
		if !exists {
			return nil, ErrNotFound
		}
	}
	return result, nil
}

func (s Service) CreateConversation(ctx context.Context, principal auth.Principal, idempotencyKey string, input CreateConversationInput) (ConversationResult, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) ||
		!validID(input.WorkspaceID) || !validIdempotencyKey(idempotencyKey) {
		return ConversationResult{}, ErrInvalidInput
	}
	input.Title = strings.TrimSpace(input.Title)
	input.Source = strings.TrimSpace(input.Source)
	input.Visibility = strings.TrimSpace(input.Visibility)
	if input.Source == "" {
		input.Source = "chat_interface"
	}
	if input.Visibility == "" {
		input.Visibility = "workspace"
	}
	if input.Source != "chat_interface" && input.Source != "document" && input.Source != "asset" && input.Source != "automation" {
		return ConversationResult{}, ErrInvalidInput
	}
	if input.Visibility != "workspace" && input.Visibility != "organization" && input.Visibility != "public" {
		return ConversationResult{}, ErrInvalidInput
	}
	if input.AgentApplicationID != "" && !validID(input.AgentApplicationID) {
		return ConversationResult{}, ErrInvalidInput
	}
	if input.ContainerID != "" && !validID(input.ContainerID) {
		return ConversationResult{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return ConversationResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return ConversationResult{}, fmt.Errorf("begin conversation create: %w", err)
	}
	defer tx.Rollback(ctx)
	requestHash := hashRequest(input)
	state, err := reserveIdempotency(ctx, tx, principal, "conversation.create", idempotencyKey, requestHash)
	if err != nil {
		return ConversationResult{}, err
	}
	if state.replay {
		var result ConversationResult
		if err := json.Unmarshal(state.body, &result); err != nil {
			return ConversationResult{}, fmt.Errorf("decode idempotent conversation: %w", err)
		}
		return result, nil
	}
	var defaultApp, defaultModel string
	var memberRole string
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(w.default_agent_application_id::text, ''), COALESCE(w.default_resource_model_id::text, ''), wm.role
		FROM content.workspaces w
		JOIN content.workspace_members wm ON wm.workspace_id = w.id AND wm.user_id = $3::uuid
		WHERE w.organization_id = $1::uuid AND w.id = $2::uuid AND w.status = 'active'
	`, principal.OrganizationID, input.WorkspaceID, principal.UserID).Scan(&defaultApp, &defaultModel, &memberRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConversationResult{}, ErrNotFound
	}
	if err != nil {
		return ConversationResult{}, fmt.Errorf("load workspace membership: %w", err)
	}
	if memberRole == "viewer" {
		return ConversationResult{}, ErrForbidden
	}
	appID := input.AgentApplicationID
	if appID == "" {
		appID = defaultApp
	}
	if !validID(appID) || !validID(defaultModel) {
		return ConversationResult{}, ErrConflict
	}
	var boundAgent string
	err = tx.QueryRow(ctx, `
		SELECT aa.bound_agent_user_id::text
		FROM content.workspace_agent_applications wa
		JOIN integration.agent_applications aa ON aa.id = wa.agent_application_id
		WHERE wa.organization_id = $1::uuid AND wa.workspace_id = $2::uuid
		  AND wa.agent_application_id = $3::uuid AND wa.enabled = true
		  AND aa.organization_id = $1::uuid AND aa.status = 'active'
	`, principal.OrganizationID, input.WorkspaceID, appID).Scan(&boundAgent)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConversationResult{}, ErrForbidden
	}
	if err != nil {
		return ConversationResult{}, fmt.Errorf("load workspace agent application: %w", err)
	}
	var modelVersion string
	if err := tx.QueryRow(ctx, `
			SELECT COALESCE(current_version_id::text, '')
			FROM model.resource_models
			WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
			  AND (workspace_id = $3::uuid OR workspace_id IS NULL)
		`, principal.OrganizationID, defaultModel, input.WorkspaceID).Scan(&modelVersion); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ConversationResult{}, ErrConflict
		}
		return ConversationResult{}, fmt.Errorf("load default resource model: %w", err)
	}
	if !validID(modelVersion) {
		return ConversationResult{}, ErrConflict
	}
	var noteAsset string
	if err := tx.QueryRow(ctx, `
			INSERT INTO asset.assets
					(organization_id, workspace_id, resource_model_id, created_by)
				VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid) RETURNING id::text
		`, principal.OrganizationID, input.WorkspaceID, defaultModel, principal.UserID).Scan(&noteAsset); err != nil {
		return ConversationResult{}, fmt.Errorf("create note asset: %w", err)
	}
	if _, err := createAssetVersionWithDraft(ctx, tx, principal.OrganizationID, input.WorkspaceID, noteAsset, defaultModel, modelVersion, input.Title, "", nil, principal.UserID); err != nil {
		return ConversationResult{}, err
	}
	// The conversation owns exactly one document: its note. The block tree of
	// that container is the note's single editable source of truth; messages
	// land there as blocks and commits freeze the tree into versions.
	var noteContainer string
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.containers (organization_id, workspace_id, kind, title, visibility, created_by, asset_id, revision)
		VALUES ($1::uuid, $2::uuid, 'note', $3, $4, $5::uuid, $6::uuid, 1) RETURNING id::text
	`, principal.OrganizationID, input.WorkspaceID, input.Title, input.Visibility, principal.UserID, noteAsset).Scan(&noteContainer); err != nil {
		return ConversationResult{}, fmt.Errorf("create note document container: %w", err)
	}
	var conversationID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.conversations
			(organization_id, workspace_id, initiator_user_id, agent_application_id,
			 bound_agent_user_id, title, source, visibility)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8)
		RETURNING id::text
	`, principal.OrganizationID, input.WorkspaceID, principal.UserID, appID, boundAgent, input.Title, input.Source, input.Visibility).Scan(&conversationID); err != nil {
		return ConversationResult{}, fmt.Errorf("create conversation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO content.note_bindings
			(organization_id, conversation_id, note_asset_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid)
	`, principal.OrganizationID, conversationID, noteAsset); err != nil {
		return ConversationResult{}, fmt.Errorf("bind conversation note: %w", err)
	}
	result := ConversationResult{ConversationID: conversationID, WorkspaceID: input.WorkspaceID, ContainerID: noteContainer, NoteAssetID: noteAsset, AgentApplicationID: appID, BoundAgentUserID: boundAgent, Status: "active"}
	body, _ := json.Marshal(result)
	if err := saveIdempotency(ctx, tx, principal, "conversation.create", idempotencyKey, body); err != nil {
		return ConversationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ConversationResult{}, fmt.Errorf("commit conversation create: %w", err)
	}
	return result, nil
}

func (s Service) GetConversation(ctx context.Context, principal auth.Principal, conversationID string) (Conversation, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) || !validID(conversationID) {
		return Conversation{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return Conversation{}, errors.New("database store is not initialized")
	}
	var result Conversation
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT c.id::text, c.workspace_id::text, COALESCE(cc.id::text, ''),
		       nb.note_asset_id::text, c.agent_application_id::text, c.bound_agent_user_id::text,
		       c.status, c.title, c.source, c.visibility,
		       COALESCE(c.parent_conversation_id::text, ''), COALESCE(c.origin_derivation_id::text, ''),
		       c.created_at::text, c.updated_at::text
		FROM content.conversations c
		JOIN content.note_bindings nb ON nb.conversation_id = c.id
		LEFT JOIN content.containers cc ON cc.organization_id = nb.organization_id AND cc.asset_id = nb.note_asset_id
		JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
		WHERE c.organization_id = $1::uuid AND c.id = $2::uuid
	`, principal.OrganizationID, conversationID, principal.UserID).Scan(
		&result.ConversationID, &result.WorkspaceID, &result.ContainerID,
		&result.NoteAssetID, &result.AgentApplicationID, &result.BoundAgentUserID, &result.Status,
		&result.Title, &result.Source, &result.Visibility, &result.ParentConversationID,
		&result.OriginDerivationID, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("load conversation: %w", err)
	}
	return result, nil
}

func (s Service) RegisterMedia(ctx context.Context, principal auth.Principal, idempotencyKey string, input RegisterMediaInput) (MediaResult, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) ||
		!validIdempotencyKey(idempotencyKey) || !validID(input.ConversationID) || !validID(input.AttachmentID) {
		return MediaResult{}, ErrInvalidInput
	}
	input.MediaKind = strings.TrimSpace(input.MediaKind)
	input.Language = strings.TrimSpace(input.Language)
	if input.MediaKind != "audio" && input.MediaKind != "video" || len(input.Language) > 32 {
		return MediaResult{}, ErrInvalidInput
	}
	if input.DurationMS != nil && (*input.DurationMS < 0 || *input.DurationMS > 86_400_000) {
		return MediaResult{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return MediaResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MediaResult{}, fmt.Errorf("begin media registration: %w", err)
	}
	defer tx.Rollback(ctx)
	state, err := reserveIdempotency(ctx, tx, principal, "conversation.media.register", idempotencyKey, hashRequest(input))
	if err != nil {
		return MediaResult{}, err
	}
	if state.replay {
		var result MediaResult
		if err := json.Unmarshal(state.body, &result); err != nil {
			return MediaResult{}, fmt.Errorf("decode idempotent media registration: %w", err)
		}
		return result, nil
	}
	var conversationActive bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM content.conversations c
			JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
			WHERE c.organization_id = $1::uuid AND c.id = $2::uuid AND c.status = 'active'
			  AND wm.role <> 'viewer'
		)
	`, principal.OrganizationID, input.ConversationID, principal.UserID).Scan(&conversationActive); err != nil {
		return MediaResult{}, fmt.Errorf("load media conversation: %w", err)
	}
	if !conversationActive {
		return MediaResult{}, ErrNotFound
	}
	var mediaType, scanStatus string
	if err := tx.QueryRow(ctx, `
		SELECT at.media_type, at.status
		FROM asset.attachments at
		WHERE at.organization_id = $1::uuid AND at.id = $2::uuid AND at.deleted_at IS NULL
	`, principal.OrganizationID, input.AttachmentID).Scan(&mediaType, &scanStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MediaResult{}, ErrNotFound
		}
		return MediaResult{}, fmt.Errorf("load conversation media attachment: %w", err)
	}
	if !strings.HasPrefix(mediaType, input.MediaKind+"/") || scanStatus != "clean" {
		return MediaResult{}, fmt.Errorf("%w: attachment is not an allowed %s media", ErrConflict, input.MediaKind)
	}
	var result MediaResult
	err = tx.QueryRow(ctx, `
		INSERT INTO content.conversation_media
			(organization_id, conversation_id, attachment_id, media_kind, language, duration_ms, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, NULLIF($5, ''), $6, $7::uuid)
		RETURNING id::text, conversation_id::text, attachment_id::text, media_kind, status,
		          COALESCE(language, ''), duration_ms, created_at::text, updated_at::text
	`, principal.OrganizationID, input.ConversationID, input.AttachmentID, input.MediaKind, input.Language, input.DurationMS, principal.UserID).Scan(
		&result.MediaID, &result.ConversationID, &result.AttachmentID, &result.MediaKind, &result.Status,
		&result.Language, &result.DurationMS, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "conversation_media_organization_id_attachment_id_key") {
			return MediaResult{}, ErrConflict
		}
		return MediaResult{}, fmt.Errorf("register conversation media: %w", err)
	}
	body, _ := json.Marshal(result)
	if err := saveIdempotency(ctx, tx, principal, "conversation.media.register", idempotencyKey, body); err != nil {
		return MediaResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MediaResult{}, fmt.Errorf("commit media registration: %w", err)
	}
	return result, nil
}

func (s Service) GetMedia(ctx context.Context, principal auth.Principal, mediaID string) (MediaResult, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) || !validID(mediaID) {
		return MediaResult{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return MediaResult{}, errors.New("database store is not initialized")
	}
	var result MediaResult
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT cm.id::text, cm.conversation_id::text, cm.attachment_id::text, cm.media_kind, cm.status,
		       COALESCE(cm.language, ''), cm.duration_ms, COALESCE(cm.transcription_block_revision_id::text, ''),
		       cm.created_at::text, cm.updated_at::text
		FROM content.conversation_media cm
		JOIN content.conversations c ON c.id = cm.conversation_id
                JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
                WHERE cm.organization_id = $1::uuid AND cm.id = $2::uuid
	`, principal.OrganizationID, mediaID, principal.UserID).Scan(
		&result.MediaID, &result.ConversationID, &result.AttachmentID, &result.MediaKind, &result.Status,
		&result.Language, &result.DurationMS, &result.TranscriptionBlockRevisionID, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MediaResult{}, ErrNotFound
	}
	if err != nil {
		return MediaResult{}, fmt.Errorf("load conversation media: %w", err)
	}
	return result, nil
}

func (s Service) RequestTranscription(ctx context.Context, principal auth.Principal, idempotencyKey, mediaID string) (MediaResult, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) ||
		!validID(mediaID) || !validIdempotencyKey(idempotencyKey) {
		return MediaResult{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return MediaResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MediaResult{}, fmt.Errorf("begin transcription request: %w", err)
	}
	defer tx.Rollback(ctx)
	state, err := reserveIdempotency(ctx, tx, principal, "conversation.media.transcribe", idempotencyKey, hashRequest(struct{ MediaID string }{mediaID}))
	if err != nil {
		return MediaResult{}, err
	}
	if state.replay {
		var result MediaResult
		if err := json.Unmarshal(state.body, &result); err != nil {
			return MediaResult{}, fmt.Errorf("decode idempotent transcription request: %w", err)
		}
		return result, nil
	}
	var result MediaResult
	if err := tx.QueryRow(ctx, `
		SELECT cm.id::text, cm.conversation_id::text, cm.attachment_id::text, cm.media_kind, cm.status,
		       COALESCE(cm.language, ''), cm.duration_ms, COALESCE(cm.transcription_block_revision_id::text, ''),
		       cm.created_at::text, cm.updated_at::text
		FROM content.conversation_media cm
		JOIN content.conversations c ON c.id = cm.conversation_id
                JOIN content.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
                WHERE cm.organization_id = $1::uuid AND cm.id = $2::uuid
                  AND wm.role <> 'viewer'
		FOR UPDATE OF cm
	`, principal.OrganizationID, mediaID, principal.UserID).Scan(
		&result.MediaID, &result.ConversationID, &result.AttachmentID, &result.MediaKind, &result.Status,
		&result.Language, &result.DurationMS, &result.TranscriptionBlockRevisionID, &result.CreatedAt, &result.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MediaResult{}, ErrNotFound
		}
		return MediaResult{}, fmt.Errorf("load media for transcription: %w", err)
	}
	if result.Status == "transcribed" {
		return MediaResult{}, fmt.Errorf("%w: media transcription already completed", ErrConflict)
	}
	if result.Status != "registered" && result.Status != "failed" {
		return MediaResult{}, fmt.Errorf("%w: media transcription is already active", ErrConflict)
	}
	var jobID string
	inputSnapshot, _ := json.Marshal(map[string]any{
		"media_id": mediaID, "attachment_id": result.AttachmentID,
		"media_kind": result.MediaKind, "language": result.Language,
	})
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.processing_jobs
			(organization_id, workspace_id, job_type, source_type, source_id, idempotency_key, input_snapshot)
		SELECT $1::uuid, c.workspace_id, 'transcription', 'conversation_media', cm.id, $3, $4::jsonb
		FROM content.conversation_media cm
		JOIN content.conversations c ON c.id = cm.conversation_id
		WHERE cm.organization_id = $1::uuid AND cm.id = $2::uuid
		RETURNING id::text
	`, principal.OrganizationID, mediaID, idempotencyKey, string(inputSnapshot)).Scan(&jobID); err != nil {
		return MediaResult{}, fmt.Errorf("create transcription job: %w", err)
	}
	if s.Events.Queue == nil {
		return MediaResult{}, errors.New("event store is not initialized")
	}
	if _, err := s.Events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   principal.OrganizationID,
		EventType:        "conversation.media.transcription_requested",
		AggregateType:    "conversation_media",
		AggregateID:      mediaID,
		AggregateVersion: 1,
		PayloadVersion:   1,
		Payload:          map[string]string{"job_id": jobID, "media_id": mediaID},
	}); err != nil {
		return MediaResult{}, fmt.Errorf("enqueue transcription event: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE content.conversation_media SET status = 'transcribing', updated_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, mediaID); err != nil {
		return MediaResult{}, fmt.Errorf("queue media transcription: %w", err)
	}
	result.Status = "transcribing"
	result.TranscriptionJobID = jobID
	result.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	body, _ := json.Marshal(result)
	if err := saveIdempotency(ctx, tx, principal, "conversation.media.transcribe", idempotencyKey, body); err != nil {
		return MediaResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MediaResult{}, fmt.Errorf("commit transcription request: %w", err)
	}
	return result, nil
}

type idempotencyState struct {
	replay bool
	body   []byte
}

func reserveIdempotency(ctx context.Context, tx pgx.Tx, principal auth.Principal, operation, key, requestHash string) (idempotencyState, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO system.idempotency_keys (organization_id, subject_id, operation, idempotency_key, request_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, now() + interval '24 hours')
		ON CONFLICT (organization_id, subject_id, operation, idempotency_key) DO NOTHING
		RETURNING id::text
	`, principal.OrganizationID, principal.UserID, operation, key, requestHash).Scan(&id)
	if err == nil {
		return idempotencyState{}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return idempotencyState{}, fmt.Errorf("reserve content idempotency key: %w", err)
	}
	var storedHash string
	var body []byte
	if err := tx.QueryRow(ctx, `
		SELECT request_hash, response_body FROM system.idempotency_keys
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid AND operation = $3 AND idempotency_key = $4
		FOR UPDATE
	`, principal.OrganizationID, principal.UserID, operation, key).Scan(&storedHash, &body); err != nil {
		return idempotencyState{}, fmt.Errorf("load content idempotency key: %w", err)
	}
	if storedHash != requestHash {
		return idempotencyState{}, ErrConflict
	}
	if len(body) == 0 {
		return idempotencyState{}, ErrConflict
	}
	return idempotencyState{replay: true, body: body}, nil
}

func saveIdempotency(ctx context.Context, tx pgx.Tx, principal auth.Principal, operation, key string, result []byte) error {
	if _, err := tx.Exec(ctx, `
		UPDATE system.idempotency_keys SET response_status = 201, response_body = $5::jsonb
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid AND operation = $3 AND idempotency_key = $4
	`, principal.OrganizationID, principal.UserID, operation, key, string(result)); err != nil {
		return fmt.Errorf("save content idempotency response: %w", err)
	}
	return nil
}

func createEmptyContainerVersion(ctx context.Context, tx pgx.Tx, organizationID, containerID, userID, checksum string) error {
	var versionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO content.container_versions (organization_id, container_id, version_no, created_by, content_checksum)
		VALUES ($1::uuid, $2::uuid, 1, $3::uuid, $4) RETURNING id::text
	`, organizationID, containerID, userID, checksum).Scan(&versionID); err != nil {
		return fmt.Errorf("create initial container version: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE content.containers SET current_version_id = $2::uuid WHERE id = $1::uuid`, containerID, versionID); err != nil {
		return fmt.Errorf("set initial container version: %w", err)
	}
	return nil
}

// createAssetVersionWithDraft seeds a freshly inserted asset with its first
// immutable working version and the shared draft. Version creation goes
// through asset.CreateVersionTx; the draft starts at revision 1.
func createAssetVersionWithDraft(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, assetID, resourceModelID, resourceModelVersionID, title, markdown string, fields map[string]any, userID string) (string, error) {
	if fields == nil {
		fields = map[string]any{}
	}
	versionID, _, err := assetservice.CreateVersionTx(ctx, tx, assetservice.VersionMaterial{
		OrganizationID:         organizationID,
		WorkspaceID:            workspaceID,
		AssetID:                assetID,
		ResourceModelID:        resourceModelID,
		ResourceModelVersionID: resourceModelVersionID,
		Origin:                 assetservice.OriginHuman,
		ConfirmationStatus:     assetservice.ConfirmationUnconfirmed,
		Title:                  title,
		Markdown:               markdown,
		Fields:                 fields,
		CreatedBy:              userID,
	})
	if err != nil {
		return "", fmt.Errorf("create initial asset version: %w", err)
	}
	if err := insertAssetDraft(ctx, tx, organizationID, workspaceID, assetID, versionID, title, markdown, fields, userID); err != nil {
		return "", err
	}
	return versionID, nil
}

// advanceAssetDraft moves the shared draft base to a newly created version on
// an existing asset, creating the draft when the asset predates the draft.
func advanceAssetDraft(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, assetID, versionID, title, markdown string, fields map[string]any, userID string) error {
	return insertAssetDraft(ctx, tx, organizationID, workspaceID, assetID, versionID, title, markdown, fields, userID)
}

func insertAssetDraft(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, assetID, versionID, title, markdown string, fields map[string]any, userID string) error {
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encode draft fields: %w", err)
	}
	var draftID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO asset.asset_drafts
			(organization_id, workspace_id, asset_id, base_version_id, revision, committed_revision,
			 title, summary, markdown, fields, origin, updated_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 1, 1, $5, '', $6, $7::jsonb, 'human', $8::uuid)
		ON CONFLICT (asset_id) DO UPDATE SET
			base_version_id = EXCLUDED.base_version_id,
			title = EXCLUDED.title,
			markdown = EXCLUDED.markdown,
			fields = EXCLUDED.fields,
			revision = asset.asset_drafts.revision + 1,
			committed_revision = asset.asset_drafts.revision + 1,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()
		RETURNING id::text
	`, organizationID, workspaceID, assetID, versionID, title, markdown, string(fieldsJSON), userID).Scan(&draftID); err != nil {
		return fmt.Errorf("upsert asset draft: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE asset.assets SET draft_id = $3::uuid, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, assetID, draftID); err != nil {
		return fmt.Errorf("bind asset draft: %w", err)
	}
	return nil
}

func emptyChecksum() string {
	sum := sha256.Sum256([]byte(""))
	return hex.EncodeToString(sum[:])
}

func hashBytes(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func hashRequest(value any) string {
	body, _ := json.Marshal(value)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func validID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if (i == 8 || i == 13 || i == 18 || i == 23) && r == '-' {
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func validIdempotencyKey(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) > 0 && len(value) <= 200 && !strings.ContainsRune(value, '\x00')
}
