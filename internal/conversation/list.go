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
	ErrInvalidStatus = errors.New("invalid conversation status filter")
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
func (s Service) AddNoteBlock(ctx context.Context, principal auth.Principal, key, id, kind, blockContent, fromBlockRevisionID string) (contentservice.NoteBlockEntry, error) {
	return s.contentService().AddNoteBlock(ctx, principal, key, id, kind, blockContent, fromBlockRevisionID)
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
	CardStatus           string    `json:"card_status"`
	CardStatusDetail     string    `json:"card_status_detail,omitempty"`
	HasNewChanges        bool      `json:"has_new_changes"`
	ContainerID          string    `json:"container_id"`
	NoteAssetID          string    `json:"note_asset_id"`
	NoteFirstLine        string    `json:"note_first_line"`
	ParentConversationID string    `json:"parent_conversation_id"`
	OriginDerivationID   string    `json:"origin_derivation_id"`
	LastMessagePreview   string    `json:"last_message_preview"`
	MessageCount         int64     `json:"message_count"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Card status values (product §5.1). The list derives them per row from the
// note asset's publication state — the derivation order matters: a pending
// review wins over everything, a rejection wins over the published state,
// and a published card whose note moved on becomes pending_update.
const (
	CardOrganizing   = "organizing"   // 整理中：从未送审
	CardReviewing    = "reviewing"    // 审核中：有送审在途
	CardRejected     = "rejected"     // 未通过：最近一次送审被驳回
	CardPublished    = "published"    // 已入库：笔记与知识库文档一致
	CardPendingUpdate = "pending_update" // 待入库：已入库后笔记有新变化
)

func validCardStatus(value string) bool {
	switch value {
	case CardOrganizing, CardReviewing, CardRejected, CardPublished, CardPendingUpdate:
		return true
	}
	return false
}

// noteStatusLateral computes every card-status input in one lateral join:
// publication state, the latest finished review, dirty flags and the note's
// first line.
const noteStatusLateral = `
	LEFT JOIN LATERAL (
		SELECT nb.note_asset_id::text AS note_asset_id,
		       cc.id::text AS container_id,
		       (a.current_published_version_id IS NOT NULL) AS published,
		       a.current_working_version_id::text AS working_version,
		       a.current_published_version_id::text AS published_version,
		       (a.current_working_version_id::text IS DISTINCT FROM a.current_published_version_id::text) AS working_moved,
		       (COALESCE(d.revision, 0) <> COALESCE(d.committed_revision, 0)) AS dirty,
		       EXISTS (SELECT 1 FROM asset.publication_requests pr
		               WHERE pr.organization_id = nb.organization_id
		                 AND pr.asset_id = nb.note_asset_id AND pr.status = 'pending') AS pending_review,
		       (SELECT pr.asset_version_id::text
		        FROM asset.publication_requests pr
		        WHERE pr.organization_id = nb.organization_id AND pr.asset_id = nb.note_asset_id
		          AND pr.status = 'pending'
		        ORDER BY pr.submitted_at DESC LIMIT 1) AS submitted_version,
		       (SELECT pr.decision_comment
		        FROM asset.publication_requests pr
		        WHERE pr.organization_id = nb.organization_id AND pr.asset_id = nb.note_asset_id
		          AND pr.status IN ('approved', 'rejected')
		        ORDER BY pr.submitted_at DESC LIMIT 1) AS last_comment,
		       (SELECT pr.status
		        FROM asset.publication_requests pr
		        WHERE pr.organization_id = nb.organization_id AND pr.asset_id = nb.note_asset_id
		          AND pr.status IN ('approved', 'rejected')
		        ORDER BY pr.submitted_at DESC LIMIT 1) AS last_decision,
		       (SELECT left(br.content, 200)
		        FROM content.containers cc2
		        JOIN content.block_placements bp ON bp.organization_id = cc2.organization_id AND bp.container_id = cc2.id
		        JOIN content.block_revisions br ON br.organization_id = bp.organization_id AND br.id = bp.block_revision_id
		        WHERE cc2.organization_id = nb.organization_id AND cc2.asset_id = nb.note_asset_id
		        ORDER BY bp.position LIMIT 1) AS note_first_line
		FROM content.note_bindings nb
		JOIN asset.assets a ON a.organization_id = nb.organization_id AND a.id = nb.note_asset_id
		LEFT JOIN asset.asset_drafts d ON d.organization_id = nb.organization_id AND d.asset_id = nb.note_asset_id
		JOIN content.containers cc ON cc.organization_id = nb.organization_id AND cc.asset_id = nb.note_asset_id
		WHERE nb.conversation_id = c.id
	) note ON true`

// cardStatusCASE derives the five-state badge from the lateral inputs.
const cardStatusCASE = `CASE
		WHEN note.pending_review THEN '%s'
		WHEN note.last_decision = 'rejected' THEN '%s'
		WHEN note.published AND (note.working_moved OR note.dirty) THEN '%s'
		WHEN note.published THEN '%s'
		ELSE '%s'
	END`

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
	items, _, _, err := s.ListPage(ctx, principal, workspaceID, query, limit, "", "")
	return items, err
}

// ListPage serves the "我的灵感" card list: only the member's own cards
// (product: 灵感卡仅自己), each carrying the five-state badge, the note's
// first line and a new-changes flag for cards under review. status filters
// by badge (organizing/reviewing/rejected/published/pending_update).
func (s Service) ListPage(ctx context.Context, principal auth.Principal, workspaceID, query string, limit int, cursor, status string) ([]Summary, bool, string, error) {
	if err := s.require(ctx, principal, workspaceID); err != nil {
		return nil, false, "", err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if status != "" && !validCardStatus(status) {
		return nil, false, "", ErrInvalidStatus
	}
	query = strings.TrimSpace(query)
	cursorTime, cursorID, err := decodeConversationCursor(cursor)
	if err != nil {
		return nil, false, "", err
	}
	cardStatus := fmt.Sprintf(cardStatusCASE, CardReviewing, CardRejected, CardPendingUpdate, CardPublished, CardOrganizing)
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT c.id::text, c.workspace_id::text, c.title, c.source, c.visibility, c.status,
		       `+cardStatus+`,
		       COALESCE(note.last_comment, ''), COALESCE(note.note_first_line, ''),
		       (note.pending_review AND (note.working_version IS DISTINCT FROM note.submitted_version OR note.dirty)),
		       COALESCE(note.container_id, ''), COALESCE(note.note_asset_id, ''),
		       COALESCE(c.parent_conversation_id::text, ''), COALESCE(c.origin_derivation_id::text, ''),
		       COALESCE(last_message.content, ''), COALESCE(message_counts.message_count, 0), c.updated_at
		FROM content.conversations c
		`+noteStatusLateral+`
		LEFT JOIN LATERAL (SELECT br.content FROM content.message_blocks mb JOIN content.block_revisions br ON br.id = mb.block_revision_id WHERE mb.conversation_id = c.id ORDER BY mb.sequence_no DESC LIMIT 1) last_message ON true
		LEFT JOIN LATERAL (SELECT count(*) AS message_count FROM content.message_blocks mb WHERE mb.conversation_id = c.id) message_counts ON true
		WHERE c.organization_id = $1::uuid AND c.workspace_id = $2::uuid AND c.status <> 'archived'
		  AND c.initiator_user_id = $3::uuid
		  AND EXISTS (SELECT 1 FROM content.workspace_members wm
		              WHERE wm.organization_id = c.organization_id AND wm.workspace_id = c.workspace_id
		                AND wm.user_id = $3::uuid)
		  AND ($4 = '' OR `+cardStatus+` = $4)
		  AND ($5 = '' OR c.title ILIKE '%' || $5 || '%' OR last_message.content ILIKE '%' || $5 || '%' OR note.note_first_line ILIKE '%' || $5 || '%')
		  AND ($6 = '' OR c.updated_at < NULLIF($6, '')::timestamptz OR (c.updated_at = NULLIF($6, '')::timestamptz AND c.id > NULLIF($7, '')::uuid))
		ORDER BY c.updated_at DESC, c.id LIMIT $8
	`, principal.OrganizationID, workspaceID, principal.UserID, status, query, cursorTime, cursorID, limit+1)
	if err != nil {
		return nil, false, "", fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	items := make([]Summary, 0, limit+1)
	for rows.Next() {
		var item Summary
		if err := rows.Scan(&item.ConversationID, &item.WorkspaceID, &item.Title, &item.Source, &item.Visibility, &item.Status,
			&item.CardStatus, &item.CardStatusDetail, &item.NoteFirstLine, &item.HasNewChanges,
			&item.ContainerID, &item.NoteAssetID,
			&item.ParentConversationID, &item.OriginDerivationID,
			&item.LastMessagePreview, &item.MessageCount, &item.UpdatedAt); err != nil {
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
		JOIN content.workspace_members wm ON wm.organization_id = c.organization_id AND wm.workspace_id = c.workspace_id AND wm.user_id = $3::uuid
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

// SourceConversation resolves the inspiration card a note asset came from —
// the knowledge-base detail page's "来自灵感卡" backlink. The link is
// author-private: only the card's initiator can resolve it, and it dies with
// the binding when the card is deleted.
func (s Service) SourceConversation(ctx context.Context, principal auth.Principal, assetID string) (string, error) {
	if principal.UserType != "member" || !validID(principal.OrganizationID) || !validID(principal.UserID) || !validID(assetID) {
		return "", ErrInvalidID
	}
	if s.Store == nil || s.Store.Pool == nil {
		return "", ErrForbidden
	}
	var conversationID string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT nb.conversation_id::text
		FROM content.note_bindings nb
		JOIN content.conversations c ON c.organization_id = nb.organization_id AND c.id = nb.conversation_id
		WHERE nb.organization_id = $1::uuid AND nb.note_asset_id = $2::uuid
		  AND c.initiator_user_id = $3::uuid AND c.status <> 'archived'
	`, principal.OrganizationID, assetID, principal.UserID).Scan(&conversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve source conversation: %w", err)
	}
	return conversationID, nil
}

func validID(value string) bool {	value = strings.TrimSpace(value)
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
