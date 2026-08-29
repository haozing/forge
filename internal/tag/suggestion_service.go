package tag

// suggestion_service.go — AI tag suggestions are records, not relations.
// Accepting a suggestion only mutates the AssetDraft; the source version and
// its tags are never touched. Materialization backfills the version that
// eventually carried the tag when the draft commits.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrSuggestionNotFound  = errors.New("tag suggestion not found")
	ErrSuggestionState     = errors.New("tag suggestion already decided")
	ErrSuggestionNoTag     = errors.New("suggestion requires a resolved tag")
)

type Suggestion struct {
	ID             string     `json:"id"`
	SourceVersionID string    `json:"source_version_id"`
	SuggestedKey   string     `json:"suggested_key"`
	SuggestedName  string     `json:"suggested_display_name"`
	ResolvedTagID  *string    `json:"resolved_tag_id,omitempty"`
	Confidence     float64    `json:"confidence"`
	Status         string     `json:"status"`
	ReviewedBy     *string    `json:"reviewed_by,omitempty"`
	ReviewedAt     *time.Time `json:"reviewed_at,omitempty"`
	AcceptedDraft  *string    `json:"accepted_into_draft_id,omitempty"`
	Materialized   *string    `json:"materialized_version_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type SuggestionService struct {
	Store *store.Store
}

// CreatePending records a model suggestion against an immutable version.
func (s SuggestionService) CreatePending(ctx context.Context, organizationID, workspaceID, sourceVersionID, suggestedKey, suggestedName string, confidence float64) (Suggestion, error) {
	normalized, err := NormalizeKey(suggestedKey)
	if err != nil {
		return Suggestion{}, ErrInvalidInput
	}
	if confidence < 0 || confidence > 1 {
		return Suggestion{}, ErrInvalidInput
	}
	var item Suggestion
	err = s.Store.Pool.QueryRow(ctx, `
		INSERT INTO asset.asset_version_tag_suggestions
			(organization_id, workspace_id, source_version_id, suggested_key, suggested_display_name, confidence)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6)
		RETURNING id::text, source_version_id::text, suggested_key, suggested_display_name,
		          resolved_tag_id::text, confidence, status, reviewed_by::text, reviewed_at,
		          accepted_into_draft_id::text, materialized_version_id::text, created_at
	`, organizationID, workspaceID, sourceVersionID, normalized, suggestedName, confidence).Scan(
		&item.ID, &item.SourceVersionID, &item.SuggestedKey, &item.SuggestedName,
		&item.ResolvedTagID, &item.Confidence, &item.Status, &item.ReviewedBy, &item.ReviewedAt,
		&item.AcceptedDraft, &item.Materialized, &item.CreatedAt)
	if err != nil {
		return Suggestion{}, fmt.Errorf("insert tag suggestion: %w", err)
	}
	return item, nil
}

// AcceptDecision carries everything Accept needs.
type AcceptDecision struct {
	SuggestionID string
	TagID        string
	DraftID      string
	Actor        auth.Principal
	Source       string // relation source recorded on the draft relation
	Confidence   float64
}

// Accept writes the draft relation and marks the suggestion accepted inside
// the CALLER'S transaction: the draft revision increment stays under the
// draft service's control so one autosave increments exactly once.
func (s SuggestionService) AcceptTx(ctx context.Context, tx pgx.Tx, decision AcceptDecision) error {
	var status string
	err := tx.QueryRow(ctx, `
		SELECT status FROM asset.asset_version_tag_suggestions
		WHERE organization_id = $1::uuid AND id = $2::uuid
		FOR UPDATE
	`, decision.Actor.OrganizationID, decision.SuggestionID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSuggestionNotFound
	}
	if err != nil {
		return err
	}
	if status != "pending" {
		return ErrSuggestionState
	}
	if decision.TagID == "" || decision.DraftID == "" {
		return ErrSuggestionNoTag
	}
	source := decision.Source
	if source == "" {
		source = SourceAI
	}
	commandTag, err := tx.Exec(ctx, `
		UPDATE asset.asset_version_tag_suggestions
		SET status = 'accepted', resolved_tag_id = $3::uuid, reviewed_by = $4::uuid, reviewed_at = now(),
		    accepted_into_draft_id = $5::uuid
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'pending'
	`, decision.Actor.OrganizationID, decision.SuggestionID, decision.TagID, decision.Actor.UserID, decision.DraftID)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		return ErrSuggestionState
	}
	// Existing draft relation keeps its provenance; only genuinely new
	// relations are inserted with the AI source.
	if _, err := tx.Exec(ctx, `
		INSERT INTO asset.asset_draft_tags
			(organization_id, workspace_id, asset_draft_id, tag_id, source, confidence, added_by)
		SELECT $1::uuid, workspace_id, $3::uuid, $4::uuid, $5, $6, $7::uuid
		FROM asset.asset_drafts WHERE id = $3::uuid
		ON CONFLICT (asset_draft_id, tag_id) DO NOTHING
	`, decision.Actor.OrganizationID, "", decision.DraftID, decision.TagID, source, decision.Confidence, decision.Actor.UserID); err != nil {
		return fmt.Errorf("accept suggestion into draft: %w", err)
	}
	return nil
}

// Reject marks a suggestion rejected; no relation is written.
func (s SuggestionService) Reject(ctx context.Context, actor auth.Principal, suggestionID string) error {
	commandTag, err := s.Store.Pool.Exec(ctx, `
		UPDATE asset.asset_version_tag_suggestions
		SET status = 'rejected', reviewed_by = $3::uuid, reviewed_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'pending'
	`, actor.OrganizationID, suggestionID, actor.UserID)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		return ErrSuggestionState
	}
	return nil
}

// MarkSuggestionsMaterialized backfills the version that finally carried the
// accepted tags; called by the draft commit transaction.
func (s SuggestionService) MarkSuggestionsMaterialized(ctx context.Context, tx pgx.Tx, draftID, versionID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE asset.asset_version_tag_suggestions s
		SET materialized_version_id = $3::uuid
		FROM asset.asset_draft_tags dt
		WHERE s.accepted_into_draft_id = $1::uuid
		  AND s.status = 'accepted'
		  AND dt.asset_draft_id = s.accepted_into_draft_id
		  AND dt.tag_id = s.resolved_tag_id
		  AND (s.materialized_version_id IS NULL OR s.materialized_version_id <> $3::uuid)
	`, draftID, draftID, versionID)
	return err
}
