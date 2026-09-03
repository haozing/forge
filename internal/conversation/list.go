package conversation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	contentservice "agentchunzhi/internal/content"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrForbidden     = errors.New("conversation access denied")
	ErrNotFound      = errors.New("conversation not found")
	ErrInvalidCursor = errors.New("invalid conversation cursor")
	ErrInvalidID     = errors.New("invalid conversation id")
)

type Service struct {
	Store   *store.Store
	Policy  authz.WorkspacePolicy
	Content contentservice.Service
}

func (s Service) contentService() contentservice.Service {
	if s.Content.Store != nil {
		return s.Content
	}
	s.Content.Store = s.Store
	return s.Content
}

// Conversation operations stay behind this application-service boundary. The
// content package remains the persistence implementation while HTTP depends
// on this workspace-aware facade.
func (s Service) CreateConversation(ctx context.Context, principal auth.Principal, key string, input contentservice.CreateConversationInput) (contentservice.ConversationResult, error) {
	return s.contentService().CreateConversation(ctx, principal, key, input)
}
func (s Service) GetConversation(ctx context.Context, principal auth.Principal, id string) (contentservice.Conversation, error) {
	return s.contentService().GetConversation(ctx, principal, id)
}
func (s Service) ListMessages(ctx context.Context, principal auth.Principal, id string) ([]contentservice.Message, error) {
	return s.contentService().ListMessages(ctx, principal, id)
}
func (s Service) AppendMessage(ctx context.Context, principal auth.Principal, key string, input contentservice.AppendMessageInput) (contentservice.MessageResult, error) {
	return s.contentService().AppendMessage(ctx, principal, key, input)
}
func (s Service) NoteView(ctx context.Context, principal auth.Principal, id string) (contentservice.NoteView, error) {
	return s.contentService().NoteView(ctx, principal, id)
}
func (s Service) NoteBlocks(ctx context.Context, principal auth.Principal, id string) ([]contentservice.NoteBlockEntry, int64, error) {
	return s.contentService().NoteBlocks(ctx, principal, id)
}
func (s Service) AddNoteBlock(ctx context.Context, principal auth.Principal, key, id, kind, blockContent string) (contentservice.NoteBlockEntry, error) {
	return s.contentService().AddNoteBlock(ctx, principal, key, id, kind, blockContent)
}
func (s Service) UpdateNoteBlock(ctx context.Context, principal auth.Principal, key, id, blockID, blockContent string) (contentservice.NoteBlockEntry, error) {
	return s.contentService().UpdateNoteBlock(ctx, principal, key, id, blockID, blockContent)
}
func (s Service) DeleteNoteBlock(ctx context.Context, principal auth.Principal, key, id, blockID string) (string, error) {
	return s.contentService().DeleteNoteBlock(ctx, principal, key, id, blockID)
}
func (s Service) CreateDerivation(ctx context.Context, principal auth.Principal, key string, input contentservice.CreateDerivationInput) (contentservice.DerivationResult, error) {
	return s.contentService().CreateDerivation(ctx, principal, key, input)
}
func (s Service) GetDerivation(ctx context.Context, principal auth.Principal, id string) (contentservice.DerivationResult, error) {
	return s.contentService().GetDerivation(ctx, principal, id)
}
func (s Service) FinalizeDerivation(ctx context.Context, principal auth.Principal, key, id string, input contentservice.FinalizeDerivationInput) (contentservice.FinalizeResult, error) {
	return s.contentService().FinalizeDerivation(ctx, principal, key, id, input)
}
func (s Service) RegisterMedia(ctx context.Context, principal auth.Principal, key string, input contentservice.RegisterMediaInput) (contentservice.MediaResult, error) {
	return s.contentService().RegisterMedia(ctx, principal, key, input)
}
func (s Service) GetMedia(ctx context.Context, principal auth.Principal, id string) (contentservice.MediaResult, error) {
	return s.contentService().GetMedia(ctx, principal, id)
}
func (s Service) RequestTranscription(ctx context.Context, principal auth.Principal, key, id string) (contentservice.MediaResult, error) {
	return s.contentService().RequestTranscription(ctx, principal, key, id)
}

type Summary struct {
	ConversationID       string    `json:"conversation_id"`
	WorkspaceID          string    `json:"workspace_id"`
	Title                string    `json:"title"`
	Source               string    `json:"source"`
	Visibility           string    `json:"visibility"`
	Status               string    `json:"status"`
	ContainerID          string    `json:"container_id"`
	NoteAssetID          string    `json:"note_asset_id"`
	ParentConversationID string    `json:"parent_conversation_id"`
	OriginDerivationID   string    `json:"origin_derivation_id"`
	LastMessagePreview   string    `json:"last_message_preview"`
	MessageCount         int64     `json:"message_count"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func (s Service) require(ctx context.Context, principal auth.Principal, workspaceID string) error {
	if principal.UserType != "member" || s.Store == nil || s.Store.Pool == nil || s.Policy == nil {
		return ErrForbidden
	}
	_, err := s.Policy.Require(ctx, principal, workspaceID, "", "conversation.use")
	if errors.Is(err, authz.ErrWorkspaceForbidden) || errors.Is(err, authz.ErrWorkspaceNotFound) {
		return ErrForbidden
	}
	return err
}

func (s Service) List(ctx context.Context, principal auth.Principal, workspaceID, query string, limit int) ([]Summary, error) {
	items, _, _, err := s.ListPage(ctx, principal, workspaceID, query, limit, "")
	return items, err
}

func (s Service) ListPage(ctx context.Context, principal auth.Principal, workspaceID, query string, limit int, cursor string) ([]Summary, bool, string, error) {
	if err := s.require(ctx, principal, workspaceID); err != nil {
		return nil, false, "", err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query = strings.TrimSpace(query)
	cursorTime, cursorID, err := decodeConversationCursor(cursor)
	if err != nil {
		return nil, false, "", err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT c.id::text, c.workspace_id::text, c.title, c.source, c.visibility, c.status,
		       COALESCE(cc.id::text, ''), COALESCE(nb.note_asset_id::text, ''),
		       COALESCE(c.parent_conversation_id::text, ''), COALESCE(c.origin_derivation_id::text, ''),
		       COALESCE(last_message.content, ''), COALESCE(message_counts.message_count, 0), c.updated_at
		FROM content.conversations c
		LEFT JOIN content.note_bindings nb ON nb.conversation_id = c.id
		LEFT JOIN content.containers cc ON cc.organization_id = nb.organization_id AND cc.asset_id = nb.note_asset_id
		LEFT JOIN LATERAL (SELECT br.content FROM content.message_blocks mb JOIN content.block_revisions br ON br.id = mb.block_revision_id WHERE mb.conversation_id = c.id ORDER BY mb.sequence_no DESC LIMIT 1) last_message ON true
		LEFT JOIN LATERAL (SELECT count(*) AS message_count FROM content.message_blocks mb WHERE mb.conversation_id = c.id) message_counts ON true
		WHERE c.organization_id = $1::uuid AND c.workspace_id = $2::uuid AND c.status <> 'archived'
		  AND ($4 = '' OR c.title ILIKE '%' || $4 || '%' OR last_message.content ILIKE '%' || $4 || '%')
		  AND ($5 = '' OR c.updated_at < NULLIF($5, '')::timestamptz OR (c.updated_at = NULLIF($5, '')::timestamptz AND c.id > NULLIF($6, '')::uuid))
		ORDER BY c.updated_at DESC, c.id LIMIT $7
	`, principal.OrganizationID, workspaceID, principal.UserID, query, cursorTime, cursorID, limit+1)
	if err != nil {
		return nil, false, "", fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	items := make([]Summary, 0, limit+1)
	for rows.Next() {
		var item Summary
		if err := rows.Scan(&item.ConversationID, &item.WorkspaceID, &item.Title, &item.Source, &item.Visibility, &item.Status, &item.ContainerID, &item.NoteAssetID, &item.ParentConversationID, &item.OriginDerivationID, &item.LastMessagePreview, &item.MessageCount, &item.UpdatedAt); err != nil {
			return nil, false, "", fmt.Errorf("scan conversation summary: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, "", err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	nextCursor := ""
	if hasMore && len(items) > 0 {
		nextCursor = encodeConversationCursor(items[len(items)-1].UpdatedAt, items[len(items)-1].ConversationID)
	}
	return items, hasMore, nextCursor, nil
}

// ListChildren returns the conversations derived from the given one. The
// parent itself is only used to resolve the workspace and 404 semantics; each
// child is filtered by the same visibility rule as the workspace list.
func (s Service) ListChildren(ctx context.Context, principal auth.Principal, conversationID string) ([]Summary, error) {
	if !validID(conversationID) {
		return nil, ErrInvalidID
	}
	workspaceID, err := s.GetWorkspace(ctx, principal, conversationID)
	if err != nil {
		return nil, err
	}
	if err := s.require(ctx, principal, workspaceID); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT c.id::text, c.workspace_id::text, c.title, c.source, c.visibility, c.status,
		       COALESCE(cc.id::text, ''), COALESCE(nb.note_asset_id::text, ''),
		       COALESCE(c.parent_conversation_id::text, ''), COALESCE(c.origin_derivation_id::text, ''),
		       COALESCE(last_message.content, ''), COALESCE(message_counts.message_count, 0), c.updated_at
		FROM content.conversations c
		LEFT JOIN content.note_bindings nb ON nb.conversation_id = c.id
		LEFT JOIN content.containers cc ON cc.organization_id = nb.organization_id AND cc.asset_id = nb.note_asset_id
		LEFT JOIN LATERAL (SELECT br.content FROM content.message_blocks mb JOIN content.block_revisions br ON br.id = mb.block_revision_id WHERE mb.conversation_id = c.id ORDER BY mb.sequence_no DESC LIMIT 1) last_message ON true
		LEFT JOIN LATERAL (SELECT count(*) AS message_count FROM content.message_blocks mb WHERE mb.conversation_id = c.id) message_counts ON true
		WHERE c.organization_id = $1::uuid AND c.parent_conversation_id = $2::uuid AND c.status <> 'archived'
		ORDER BY c.created_at, c.id
	`, principal.OrganizationID, conversationID, principal.UserID)
	if err != nil {
		return nil, fmt.Errorf("list conversation children: %w", err)
	}
	defer rows.Close()
	items := []Summary{}
	for rows.Next() {
		var item Summary
		if err := rows.Scan(&item.ConversationID, &item.WorkspaceID, &item.Title, &item.Source, &item.Visibility, &item.Status, &item.ContainerID, &item.NoteAssetID, &item.ParentConversationID, &item.OriginDerivationID, &item.LastMessagePreview, &item.MessageCount, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan conversation child summary: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
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

func encodeConversationCursor(updatedAt time.Time, id string) string {
	raw, _ := json.Marshal(map[string]string{"updated_at": updatedAt.UTC().Format(time.RFC3339Nano), "id": id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeConversationCursor(value string) (string, string, error) {
	if strings.TrimSpace(value) == "" {
		return "", "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", "", ErrInvalidCursor
	}
	var payload struct {
		UpdatedAt string `json:"updated_at"`
		ID        string `json:"id"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.ID == "" {
		return "", "", ErrInvalidCursor
	}
	if _, err := time.Parse(time.RFC3339Nano, payload.UpdatedAt); err != nil {
		return "", "", ErrInvalidCursor
	}
	return payload.UpdatedAt, payload.ID, nil
}

func (s Service) GetWorkspace(ctx context.Context, principal auth.Principal, conversationID string) (string, error) {
	var workspaceID string
	err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id::text FROM content.conversations WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, conversationID).Scan(&workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return workspaceID, err
}
