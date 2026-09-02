package asset

// suggestion_review.go — the phase 4 member confirmation surface. Agent runs
// leave suggestions as side records; this service is the only place a member
// moves them into the shared AssetDraft (accept) or closes them (reject).
// Accepting never creates versions: field/summary values merge into the draft,
// tags park in asset_draft_tags via tag.SuggestionService.AcceptTx, relations
// park in asset_draft_relations, and the commit transaction materializes all
// three. Every accept call advances the draft revision exactly once so the
// next commit is guaranteed to produce the snapshot that carries them.

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
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"

	"github.com/jackc/pgx/v5"
)

const (
	// MaxSuggestionPageSize caps the suggestion list; the default page is 50.
	MaxSuggestionPageSize = 100
	defaultSuggestionPage = 50
	// MaxAcceptBatchSize caps one accept-batch call so the single-transaction
	// loop stays bounded.
	MaxAcceptBatchSize = 100
)

var (
	ErrSuggestionNotFound     = errors.New("suggestion not found")
	ErrSuggestionStateInvalid = errors.New("suggestion already decided")
	ErrSuggestionKindInvalid  = errors.New("unknown suggestion kind")
)

// SuggestionReviewService answers the member review queue. It sits beside
// MemberService and mirrors its membership gate; httpapi wires it in a later
// wave.
type SuggestionReviewService struct {
	Store  *store.Store
	Policy authz.WorkspacePolicy
}

// require mirrors MemberService.require: members only, workspace membership
// through the shared policy service.
func (s SuggestionReviewService) require(ctx context.Context, principal auth.Principal, workspaceID, modelID, action string) (authz.Scope, error) {
	if principal.UserType != auth.UserTypeMember || s.Store == nil || s.Store.Pool == nil {
		return authz.Scope{}, ErrForbidden
	}
	if s.Policy == nil {
		return authz.Scope{}, ErrForbidden
	}
	scope, err := s.Policy.Require(ctx, principal, workspaceID, modelID, action)
	if errors.Is(err, authz.ErrWorkspaceForbidden) || errors.Is(err, authz.ErrWorkspaceNotFound) {
		return authz.Scope{}, ErrForbidden
	}
	return scope, err
}

// loadAssetScope performs the two-stage gate used by the draft surface: read
// the asset's workspace and model, then judge membership on those facts.
func (s SuggestionReviewService) loadAssetScope(ctx context.Context, principal auth.Principal, assetID, action string) (string, string, error) {
	if !validID(assetID) {
		return "", "", ErrInvalidInput
	}
	var workspaceID, modelID string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT workspace_id::text, resource_model_id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, principal.OrganizationID, assetID).Scan(&workspaceID, &modelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	if _, err := s.require(ctx, principal, workspaceID, modelID, action); err != nil {
		return "", "", err
	}
	return workspaceID, modelID, nil
}

// SuggestionItem is one row of the unified review queue: field, summary, tag
// and relation suggestions share one shape.
type SuggestionItem struct {
	ID                   string          `json:"id"`
	Kind                 string          `json:"kind"`
	SourceVersionID      string          `json:"source_version_id"`
	RunID                string          `json:"run_id"`
	FieldKey             string          `json:"field_key,omitempty"`
	Value                json.RawMessage `json:"value,omitempty"`
	PreviousValue        json.RawMessage `json:"previous_value,omitempty"`
	SuggestedKey         string          `json:"suggested_key,omitempty"`
	SuggestedDisplayName string          `json:"suggested_display_name,omitempty"`
	IsNew                bool            `json:"is_new,omitempty"`
	TargetAssetID        string          `json:"target_asset_id,omitempty"`
	RelationType         string          `json:"relation_type,omitempty"`
	Confidence           float64         `json:"confidence"`
	Status               string          `json:"status"`
	Citation             json.RawMessage `json:"citation,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	ReviewedBy           *string         `json:"reviewed_by,omitempty"`
}

// ProcessingResultSummary is the compact view of one agent run over the asset:
// counts, field diff, confidence and the versions it consumed/produced.
type ProcessingResultSummary struct {
	ID                string          `json:"id"`
	RunID             string          `json:"run_id"`
	InputVersionID    string          `json:"input_version_id"`
	OutputVersionID   string          `json:"output_version_id,omitempty"`
	SuggestionSummary json.RawMessage `json:"suggestion_summary,omitempty"`
	FieldDiff         json.RawMessage `json:"field_diff,omitempty"`
	OverallConfidence *float64        `json:"overall_confidence,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

// SuggestionPage is the review queue slice plus the recent processing results
// of the same asset, in one response.
type SuggestionPage struct {
	Items             []SuggestionItem          `json:"items"`
	HasMore           bool                      `json:"has_more"`
	NextCursor        string                    `json:"next_cursor,omitempty"`
	ProcessingResults []ProcessingResultSummary `json:"processing_results"`
}

type suggestionCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func encodeSuggestionCursor(item SuggestionItem) string {
	raw, _ := json.Marshal(suggestionCursor{
		CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano),
		ID:        item.ID,
	})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeSuggestionCursor(value string) (suggestionCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return suggestionCursor{}, ErrInvalidInput
	}
	var cursor suggestionCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return suggestionCursor{}, ErrInvalidInput
	}
	if !validID(cursor.ID) {
		return suggestionCursor{}, ErrInvalidInput
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt); err != nil {
		return suggestionCursor{}, ErrInvalidInput
	}
	return cursor, nil
}

// List returns the unified suggestion queue of one asset (status defaults to
// pending) plus the asset's recent processing results. Pagination is keyset
// over (created_at, id) descending.
func (s SuggestionReviewService) List(ctx context.Context, principal auth.Principal, workspaceID, assetID, status, runID, cursor string, limit int) (SuggestionPage, error) {
	if !validID(workspaceID) || !validID(assetID) {
		return SuggestionPage{}, ErrInvalidInput
	}
	if status == "" {
		status = SuggestionStatusPending
	}
	if status != "all" && status != SuggestionStatusPending && status != SuggestionStatusAccepted && status != SuggestionStatusRejected {
		return SuggestionPage{}, ErrInvalidInput
	}
	if runID != "" && !validID(runID) {
		return SuggestionPage{}, ErrInvalidInput
	}
	if limit <= 0 || limit > MaxSuggestionPageSize {
		limit = defaultSuggestionPage
	}
	assetWorkspace, _, err := s.loadAssetScope(ctx, principal, assetID, authz.ActionAssetRead)
	if err != nil {
		return SuggestionPage{}, err
	}
	// The routed workspace must own the asset; a cross-workspace asset id
	// hides as NotFound instead of leaking existence.
	if assetWorkspace != workspaceID {
		return SuggestionPage{}, ErrNotFound
	}
	var pageCursor suggestionCursor
	if strings.TrimSpace(cursor) != "" {
		pageCursor, err = decodeSuggestionCursor(cursor)
		if err != nil {
			return SuggestionPage{}, err
		}
	}
	// One union across the three suggestion tables; the source version join
	// scopes every branch to the requested asset inside the routed workspace.
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT s.id, s.kind, s.source_version_id, s.run_id, s.field_key, s.value, s.previous_value,
		       s.suggested_key, s.suggested_display_name, s.is_new, s.target_asset_id, s.relation_type,
		       s.confidence, s.status, s.citation, s.created_at, s.reviewed_by
		FROM (
			SELECT fs.id::text, fs.kind, fs.source_version_id::text, fs.run_id::text,
			       fs.field_key, fs.value, fs.previous_value,
			       ''::text AS suggested_key, ''::text AS suggested_display_name, false AS is_new,
			       ''::text AS target_asset_id, ''::text AS relation_type,
			       fs.confidence, fs.status, fs.citation, fs.created_at, fs.reviewed_by::text
			FROM asset.asset_field_suggestions fs
			WHERE fs.organization_id = $1::uuid
			  AND EXISTS (SELECT 1 FROM asset.asset_versions v
			              WHERE v.organization_id = fs.organization_id AND v.id = fs.source_version_id
			                AND v.asset_id = $2::uuid AND v.workspace_id = $3::uuid)
			UNION ALL
			SELECT ts.id::text, 'tag', ts.source_version_id::text, ts.run_id::text,
			       ''::text AS field_key, NULL::jsonb AS value, NULL::jsonb AS previous_value,
			       ts.suggested_key, ts.suggested_display_name, ts.is_new,
			       ''::text AS target_asset_id, ''::text AS relation_type,
			       ts.confidence, ts.status, ts.citation, ts.created_at, ts.reviewed_by::text
			FROM asset.asset_version_tag_suggestions ts
			WHERE ts.organization_id = $1::uuid
			  AND EXISTS (SELECT 1 FROM asset.asset_versions v
			              WHERE v.organization_id = ts.organization_id AND v.id = ts.source_version_id
			                AND v.asset_id = $2::uuid AND v.workspace_id = $3::uuid)
			UNION ALL
			SELECT rs.id::text, 'relation', rs.source_version_id::text, rs.run_id::text,
			       ''::text AS field_key, NULL::jsonb AS value, NULL::jsonb AS previous_value,
			       ''::text AS suggested_key, ''::text AS suggested_display_name, false AS is_new,
			       rs.target_asset_id::text, rs.relation_type,
			       rs.confidence, rs.status, rs.citation, rs.created_at, rs.reviewed_by::text
			FROM asset.asset_relation_suggestions rs
			WHERE rs.organization_id = $1::uuid
			  AND EXISTS (SELECT 1 FROM asset.asset_versions v
			              WHERE v.organization_id = rs.organization_id AND v.id = rs.source_version_id
			                AND v.asset_id = $2::uuid AND v.workspace_id = $3::uuid)
		) s
		WHERE ($4::text = '' OR s.status = $4::text)
		  AND ($5::text = '' OR s.run_id = $5::text)
		  AND ($6::timestamptz IS NULL OR (s.created_at, s.id) < ($6::timestamptz, $7::text))
		ORDER BY s.created_at DESC, s.id DESC
		LIMIT $8::int
	`, principal.OrganizationID, assetID, workspaceID, allToEmpty(status), runID, cursorTimeArg(pageCursor), pageCursor.ID, limit+1)
	if err != nil {
		return SuggestionPage{}, fmt.Errorf("list asset suggestions: %w", err)
	}
	defer rows.Close()
	page := SuggestionPage{Items: []SuggestionItem{}, ProcessingResults: []ProcessingResultSummary{}}
	for rows.Next() {
		item, err := scanSuggestionItem(rows)
		if err != nil {
			return SuggestionPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return SuggestionPage{}, fmt.Errorf("iterate asset suggestions: %w", err)
	}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
		page.NextCursor = encodeSuggestionCursor(page.Items[len(page.Items)-1])
	}
	page.ProcessingResults, err = s.listProcessingResults(ctx, principal, assetID)
	if err != nil {
		return SuggestionPage{}, err
	}
	return page, nil
}

// allToEmpty maps the "all" pseudo status onto the empty no-filter marker the
// SQL expects.
func allToEmpty(status string) string {
	if status == "all" {
		return ""
	}
	return status
}

// cursorTimeArg converts the decoded cursor timestamp into a driver argument;
// a zero cursor means "first page" and passes NULL.
func cursorTimeArg(cursor suggestionCursor) any {
	if cursor.ID == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
	if err != nil {
		return nil
	}
	return parsed
}

func scanSuggestionItem(rows interface{ Scan(...any) error }) (SuggestionItem, error) {
	var item SuggestionItem
	var value, previousValue, citation []byte
	if err := rows.Scan(&item.ID, &item.Kind, &item.SourceVersionID, &item.RunID, &item.FieldKey,
		&value, &previousValue, &item.SuggestedKey, &item.SuggestedDisplayName, &item.IsNew,
		&item.TargetAssetID, &item.RelationType, &item.Confidence, &item.Status,
		&citation, &item.CreatedAt, &item.ReviewedBy); err != nil {
		return SuggestionItem{}, fmt.Errorf("scan suggestion item: %w", err)
	}
	if value != nil {
		item.Value = json.RawMessage(value)
	}
	if previousValue != nil {
		item.PreviousValue = json.RawMessage(previousValue)
	}
	if citation != nil {
		item.Citation = json.RawMessage(citation)
	}
	return item, nil
}

// listProcessingResults returns the ten most recent processing-result records
// of the asset for the queue header.
func (s SuggestionReviewService) listProcessingResults(ctx context.Context, principal auth.Principal, assetID string) ([]ProcessingResultSummary, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT r.id::text, r.run_id::text, r.input_version_id::text, COALESCE(r.output_version_id::text, ''),
		       r.suggestion_summary, r.field_diff, r.overall_confidence, r.created_at
		FROM integration.agent_processing_results r
		WHERE r.organization_id = $1::uuid AND r.asset_id = $2::uuid
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT 10
	`, principal.OrganizationID, assetID)
	if err != nil {
		return nil, fmt.Errorf("list processing results: %w", err)
	}
	defer rows.Close()
	results := []ProcessingResultSummary{}
	for rows.Next() {
		var item ProcessingResultSummary
		var summary, diff []byte
		if err := rows.Scan(&item.ID, &item.RunID, &item.InputVersionID, &item.OutputVersionID,
			&summary, &diff, &item.OverallConfidence, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan processing result: %w", err)
		}
		if summary != nil {
			item.SuggestionSummary = json.RawMessage(summary)
		}
		if diff != nil {
			item.FieldDiff = json.RawMessage(diff)
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

// ReviewOutcome reports one accepted suggestion and the draft it entered.
type ReviewOutcome struct {
	SuggestionID  string `json:"suggestion_id"`
	Kind          string `json:"kind"`
	DraftID       string `json:"draft_id"`
	DraftRevision int64  `json:"draft_revision"`
}

// AcceptRef addresses one suggestion inside a batch; tag refs may carry an
// explicit resolved tag identity.
type AcceptRef struct {
	Kind          string `json:"kind"`
	SuggestionID  string `json:"suggestion_id"`
	OverrideTagID string `json:"override_tag_id,omitempty"`
}

// AcceptFailure is one batch entry that did not land, with the reason.
type AcceptFailure struct {
	Ref   AcceptRef `json:"ref"`
	Error string    `json:"error"`
}

// BatchOutcome reports a batch accept: the suggestions that entered the draft,
// the ones that failed validation, and the single draft revision they share.
type BatchOutcome struct {
	Accepted      []ReviewOutcome `json:"accepted"`
	Failed        []AcceptFailure `json:"failed,omitempty"`
	DraftID       string          `json:"draft_id"`
	DraftRevision int64           `json:"draft_revision"`
}

// Accept moves one pending suggestion into the shared draft inside a single
// transaction. The draft revision advances exactly once; an error rolls the
// whole call back.
func (s SuggestionReviewService) Accept(ctx context.Context, principal auth.Principal, workspaceID, assetID, kind, suggestionID, overrideTagID string) (ReviewOutcome, error) {
	if !ValidSuggestionKind(kind) || !validID(suggestionID) {
		return ReviewOutcome{}, ErrInvalidInput
	}
	if overrideTagID != "" && !validID(overrideTagID) {
		return ReviewOutcome{}, ErrInvalidInput
	}
	if _, _, err := s.loadAssetScope(ctx, principal, assetID, authz.ActionAssetWrite); err != nil {
		return ReviewOutcome{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return ReviewOutcome{}, err
	}
	defer tx.Rollback(ctx)
	row, draft, err := s.loadReviewTargetTx(ctx, tx, principal, workspaceID, assetID)
	if err != nil {
		return ReviewOutcome{}, err
	}
	plan := newDraftMergePlan(draft)
	outcome, err := s.acceptOneTx(ctx, tx, principal, row, draft, plan, AcceptRef{Kind: kind, SuggestionID: suggestionID, OverrideTagID: overrideTagID})
	if err != nil {
		return ReviewOutcome{}, err
	}
	kept, _, err := s.persistMergedDraftTx(ctx, tx, principal, row, draft, plan, []ReviewOutcome{outcome})
	if err != nil {
		return ReviewOutcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ReviewOutcome{}, err
	}
	return kept[0], nil
}

// AcceptBatch applies many accepts inside one transaction. Suggestion rows
// flip individually, but the draft content merges in memory and persists with
// a single UPDATE, so the revision advances exactly once per call. Entries
// that fail validation land in Failed; the rest commit.
func (s SuggestionReviewService) AcceptBatch(ctx context.Context, principal auth.Principal, workspaceID, assetID string, refs []AcceptRef) (BatchOutcome, error) {
	if len(refs) == 0 || len(refs) > MaxAcceptBatchSize {
		return BatchOutcome{}, ErrInvalidInput
	}
	if _, _, err := s.loadAssetScope(ctx, principal, assetID, authz.ActionAssetWrite); err != nil {
		return BatchOutcome{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return BatchOutcome{}, err
	}
	defer tx.Rollback(ctx)
	row, draft, err := s.loadReviewTargetTx(ctx, tx, principal, workspaceID, assetID)
	if err != nil {
		return BatchOutcome{}, err
	}
	plan := newDraftMergePlan(draft)
	outcome := BatchOutcome{Accepted: []ReviewOutcome{}, Failed: []AcceptFailure{}, DraftID: draft.DraftID, DraftRevision: draft.Revision}
	refsByID := make(map[string]AcceptRef, len(refs))
	for _, ref := range refs {
		refsByID[ref.SuggestionID] = ref
		accepted, err := s.acceptOneTx(ctx, tx, principal, row, draft, plan, ref)
		if err != nil {
			outcome.Failed = append(outcome.Failed, AcceptFailure{Ref: ref, Error: err.Error()})
			continue
		}
		outcome.Accepted = append(outcome.Accepted, ReviewOutcome{
			SuggestionID: accepted.SuggestionID,
			Kind:         accepted.Kind,
			DraftID:      draft.DraftID,
		})
	}
	if len(outcome.Accepted) > 0 {
		kept, reverted, err := s.persistMergedDraftTx(ctx, tx, principal, row, draft, plan, outcome.Accepted)
		if err != nil {
			return BatchOutcome{}, err
		}
		outcome.Accepted = kept
		for _, item := range reverted {
			outcome.Failed = append(outcome.Failed, AcceptFailure{
				Ref:   refsByID[item.SuggestionID],
				Error: ErrInvalidInput.Error(),
			})
		}
		outcome.DraftRevision = kept[0].DraftRevision
	}
	if err := tx.Commit(ctx); err != nil {
		return BatchOutcome{}, err
	}
	return outcome, nil
}

// loadReviewTargetTx loads and locks the asset and its shared draft for a
// review transaction, hiding cross-workspace assets as NotFound and refusing
// archived ones.
func (s SuggestionReviewService) loadReviewTargetTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, workspaceID, assetID string) (LifecycleRow, Draft, error) {
	if !validID(workspaceID) || !validID(assetID) {
		return LifecycleRow{}, Draft{}, ErrInvalidInput
	}
	row, err := LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return LifecycleRow{}, Draft{}, err
	}
	if row.WorkspaceID != workspaceID {
		return LifecycleRow{}, Draft{}, ErrNotFound
	}
	if row.PublicationStatus == PublicationArchived {
		return LifecycleRow{}, Draft{}, ErrAssetArchived
	}
	draft, err := LoadDraftTx(ctx, tx, principal.OrganizationID, assetID, "")
	if err != nil {
		return LifecycleRow{}, Draft{}, err
	}
	return row, draft, nil
}

// acceptOneTx validates and flips a single suggestion inside the caller's
// transaction. Field/summary values merge into plan; tags resolve to a
// workspace tag and go through tag.SuggestionService.AcceptTx, which locks the
// tag suggestion row itself (pre-locking it here would take the same row lock
// twice); relations park in asset_draft_relations. The draft row itself is
// never updated here — the caller persists plan once so its revision moves
// exactly once per review call.
func (s SuggestionReviewService) acceptOneTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, row LifecycleRow, draft Draft, plan *draftMergePlan, ref AcceptRef) (ReviewOutcome, error) {
	if !validID(ref.SuggestionID) {
		return ReviewOutcome{}, ErrInvalidInput
	}
	switch ref.Kind {
	case SuggestionKindField, SuggestionKindSummary:
		return s.acceptFieldTx(ctx, tx, principal, row, draft, plan, ref)
	case SuggestionKindTag:
		return s.acceptTagTx(ctx, tx, principal, row, draft, ref)
	case SuggestionKindRelation:
		return s.acceptRelationTx(ctx, tx, principal, row, draft, ref)
	default:
		return ReviewOutcome{}, ErrSuggestionKindInvalid
	}
}

func (s SuggestionReviewService) acceptFieldTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, row LifecycleRow, draft Draft, plan *draftMergePlan, ref AcceptRef) (ReviewOutcome, error) {
	var kind, fieldKey, status string
	var value []byte
	err := tx.QueryRow(ctx, `
		SELECT fs.kind, fs.field_key, fs.status, fs.value
		FROM asset.asset_field_suggestions fs
		WHERE fs.organization_id = $1::uuid AND fs.id = $4::uuid
		  AND EXISTS (SELECT 1 FROM asset.asset_versions v
		              WHERE v.organization_id = fs.organization_id AND v.id = fs.source_version_id
		                AND v.asset_id = $2::uuid AND v.workspace_id = $3::uuid)
		FOR UPDATE
	`, principal.OrganizationID, row.ID, row.WorkspaceID, ref.SuggestionID).Scan(&kind, &fieldKey, &status, &value)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewOutcome{}, ErrSuggestionNotFound
	}
	if err != nil {
		return ReviewOutcome{}, fmt.Errorf("load field suggestion: %w", err)
	}
	if status != SuggestionStatusPending {
		return ReviewOutcome{}, ErrSuggestionStateInvalid
	}
	if kind != SuggestionKindField && kind != SuggestionKindSummary {
		return ReviewOutcome{}, ErrSuggestionStateInvalid
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return ReviewOutcome{}, ErrInvalidInput
	}
	if kind == SuggestionKindField {
		if strings.TrimSpace(fieldKey) == "" {
			return ReviewOutcome{}, ErrInvalidInput
		}
		plan.mergeField(fieldKey, decoded)
	} else {
		text, ok := decoded.(string)
		if !ok {
			return ReviewOutcome{}, ErrInvalidInput
		}
		plan.mergeSummary(strings.TrimSpace(text))
	}
	commandTag, err := tx.Exec(ctx, `
		UPDATE asset.asset_field_suggestions
		SET status = 'accepted', reviewed_by = $3::uuid, reviewed_at = now(), accepted_into_draft_id = $4::uuid
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'pending'
	`, principal.OrganizationID, ref.SuggestionID, principal.UserID, draft.DraftID)
	if err != nil {
		return ReviewOutcome{}, fmt.Errorf("accept field suggestion: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return ReviewOutcome{}, ErrSuggestionStateInvalid
	}
	return ReviewOutcome{SuggestionID: ref.SuggestionID, Kind: kind, DraftID: draft.DraftID}, nil
}

func (s SuggestionReviewService) acceptTagTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, row LifecycleRow, draft Draft, ref AcceptRef) (ReviewOutcome, error) {
	// No FOR UPDATE: AcceptTx locks the tag suggestion row itself; taking the
	// lock here first would self-deadlock the two-step.
	var suggestedKey, status string
	var confidence float64
	err := tx.QueryRow(ctx, `
		SELECT ts.suggested_key, ts.status, ts.confidence
		FROM asset.asset_version_tag_suggestions ts
		WHERE ts.organization_id = $1::uuid AND ts.id = $4::uuid
		  AND EXISTS (SELECT 1 FROM asset.asset_versions v
		              WHERE v.organization_id = ts.organization_id AND v.id = ts.source_version_id
		                AND v.asset_id = $2::uuid AND v.workspace_id = $3::uuid)
	`, principal.OrganizationID, row.ID, row.WorkspaceID, ref.SuggestionID).Scan(&suggestedKey, &status, &confidence)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewOutcome{}, ErrSuggestionNotFound
	}
	if err != nil {
		return ReviewOutcome{}, fmt.Errorf("load tag suggestion: %w", err)
	}
	if status != SuggestionStatusPending {
		return ReviewOutcome{}, ErrSuggestionStateInvalid
	}
	tagID, err := s.resolveTagTx(ctx, tx, principal, row.WorkspaceID, ref.OverrideTagID, suggestedKey)
	if err != nil {
		return ReviewOutcome{}, err
	}
	// AcceptTx flips the suggestion, records the resolved tag and writes the
	// asset_draft_tags row with source='ai'; the draft revision increment
	// stays here with the draft service.
	if err := (tag.SuggestionService{Store: s.Store}).AcceptTx(ctx, tx, tag.AcceptDecision{
		SuggestionID: ref.SuggestionID,
		TagID:        tagID,
		DraftID:      draft.DraftID,
		Actor:        principal,
		Source:       tag.SourceAI,
		Confidence:   confidence,
	}); err != nil {
		return ReviewOutcome{}, mapTagSuggestionError(err)
	}
	return ReviewOutcome{SuggestionID: ref.SuggestionID, Kind: SuggestionKindTag, DraftID: draft.DraftID}, nil
}

// resolveTagTx resolves the accepted tag: an explicit override must be an
// active workspace tag; otherwise the suggested key resolves with the import
// semantics of tag.CreateOrReuseTx, which may create the missing definition.
// A suggested key that hits an archived definition is rejected — tag.restore
// is an admin act, so accepting a suggestion must not resurrect it silently
// only to fail at commit with ErrTagArchived.
func (s SuggestionReviewService) resolveTagTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, workspaceID, overrideTagID, suggestedKey string) (string, error) {
	if overrideTagID != "" {
		var status string
		err := tx.QueryRow(ctx, `
			SELECT status FROM asset.tags
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
		`, principal.OrganizationID, workspaceID, overrideTagID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrInvalidInput
		}
		if err != nil {
			return "", err
		}
		if status != tag.StatusActive {
			return "", ErrTagArchived
		}
		return overrideTagID, nil
	}
	resolved, err := tag.CreateOrReuseTx(ctx, tx, principal, workspaceID, suggestedKey)
	if err != nil {
		if errors.Is(err, tag.ErrInvalidInput) {
			return "", ErrInvalidInput
		}
		return "", err
	}
	if !resolved.Created {
		var status string
		if err := tx.QueryRow(ctx, `
			SELECT status FROM asset.tags WHERE id = $1::uuid
		`, resolved.ID).Scan(&status); err != nil {
			return "", err
		}
		if status != tag.StatusActive {
			return "", ErrTagArchived
		}
	}
	return resolved.ID, nil
}

func (s SuggestionReviewService) acceptRelationTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, row LifecycleRow, draft Draft, ref AcceptRef) (ReviewOutcome, error) {
	var targetAssetID, relationType, status string
	var confidence float64
	var citation []byte
	err := tx.QueryRow(ctx, `
		SELECT rs.target_asset_id::text, rs.relation_type, rs.status, rs.confidence, rs.citation
		FROM asset.asset_relation_suggestions rs
		WHERE rs.organization_id = $1::uuid AND rs.id = $4::uuid
		  AND EXISTS (SELECT 1 FROM asset.asset_versions v
		              WHERE v.organization_id = rs.organization_id AND v.id = rs.source_version_id
		                AND v.asset_id = $2::uuid AND v.workspace_id = $3::uuid)
		FOR UPDATE
	`, principal.OrganizationID, row.ID, row.WorkspaceID, ref.SuggestionID).Scan(&targetAssetID, &relationType, &status, &confidence, &citation)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewOutcome{}, ErrSuggestionNotFound
	}
	if err != nil {
		return ReviewOutcome{}, fmt.Errorf("load relation suggestion: %w", err)
	}
	if status != SuggestionStatusPending {
		return ReviewOutcome{}, ErrSuggestionStateInvalid
	}
	if !ValidRelationType(relationType) {
		return ReviewOutcome{}, ErrInvalidInput
	}
	if targetAssetID == row.ID {
		return ReviewOutcome{}, ErrInvalidInput
	}
	// The target must be a live asset of the same workspace: the retrieval
	// candidate list is workspace-scoped, and the draft relation inherits it.
	var ok bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM asset.assets a
			WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
			  AND a.workspace_id = $3::uuid AND a.deleted_at IS NULL
		)
	`, principal.OrganizationID, targetAssetID, row.WorkspaceID).Scan(&ok); err != nil {
		return ReviewOutcome{}, err
	}
	if !ok {
		return ReviewOutcome{}, ErrInvalidInput
	}
	if err := insertDraftRelationTx(ctx, tx, principal, row, draft, targetAssetID, relationType, confidence, citation, ref.SuggestionID); err != nil {
		return ReviewOutcome{}, err
	}
	commandTag, err := tx.Exec(ctx, `
		UPDATE asset.asset_relation_suggestions
		SET status = 'accepted', reviewed_by = $3::uuid, reviewed_at = now(), accepted_into_draft_id = $4::uuid
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'pending'
	`, principal.OrganizationID, ref.SuggestionID, principal.UserID, draft.DraftID)
	if err != nil {
		return ReviewOutcome{}, fmt.Errorf("accept relation suggestion: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return ReviewOutcome{}, ErrSuggestionStateInvalid
	}
	return ReviewOutcome{SuggestionID: ref.SuggestionID, Kind: SuggestionKindRelation, DraftID: draft.DraftID}, nil
}

// insertDraftRelationTx parks an accepted relation on the draft. Rows are
// keyed by (draft, target, type); a repeated accept keeps the first
// provenance. Only source='ai' rows live here (phase 4 decision 2).
func insertDraftRelationTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, row LifecycleRow, draft Draft, targetAssetID, relationType string, confidence float64, citation []byte, suggestionID string) error {
	if len(citation) == 0 {
		citation = []byte("{}")
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO asset.asset_draft_relations
			(organization_id, workspace_id, asset_draft_id, asset_id, target_asset_id,
			 relation_type, source, confidence, citation, suggestion_id, added_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 'ai', $7, $8::jsonb,
		        NULLIF($9, '')::uuid, $10::uuid)
		ON CONFLICT (asset_draft_id, target_asset_id, relation_type) DO NOTHING
	`, row.OrganizationID, row.WorkspaceID, draft.DraftID, row.ID, targetAssetID,
		relationType, confidence, string(citation), suggestionID, principal.UserID)
	if err != nil {
		return fmt.Errorf("insert draft relation: %w", err)
	}
	return nil
}

// persistMergedDraftTx applies the merge plan with one draft UPDATE — the
// revision moves exactly once per review call — after the model head schema
// has judged the merged fields. When the merged field set fails validation,
// the behavior depends on the call shape: a single accept rolls back, while a
// batch un-accepts the field/summary suggestions (their rows return to
// pending), reverts their merges and still persists the surviving tag/relation
// accepts. It returns the outcomes that landed and the ones reverted.
func (s SuggestionReviewService) persistMergedDraftTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, row LifecycleRow, draft Draft, plan *draftMergePlan, outcomes []ReviewOutcome) ([]ReviewOutcome, []ReviewOutcome, error) {
	next := plan.apply(draft)
	reverted := []ReviewOutcome{}
	if err := s.validateDraftFieldsTx(ctx, tx, row, next.Fields); err != nil {
		// A lone accept has nothing left to persist when its own merge is
		// invalid: the caller rolls the transaction back.
		if !plan.hasFields() || len(outcomes) < 2 {
			return nil, nil, err
		}
		var kept []ReviewOutcome
		reverted, kept = splitFieldOutcomes(outcomes)
		if len(kept) == 0 {
			return nil, nil, err
		}
		if err := unacceptFieldSuggestionsTx(ctx, tx, principal.OrganizationID, reverted); err != nil {
			return nil, nil, err
		}
		plan.revertFields()
		next = plan.apply(draft)
		outcomes = kept
	}
	if err := persistDraftPatch(ctx, tx, principal.OrganizationID, next, principal.UserID); err != nil {
		return nil, nil, err
	}
	for _, outcome := range outcomes {
		RecordAssetAuditTx(ctx, tx, row.OrganizationID, row.WorkspaceID, principal, "asset.suggestion.accepted", row.ID, map[string]any{
			"workspace_id":  row.WorkspaceID,
			"kind":          outcome.Kind,
			"suggestion_id": outcome.SuggestionID,
			"draft_id":      draft.DraftID,
		})
	}
	withRevision := make([]ReviewOutcome, 0, len(outcomes))
	for _, outcome := range outcomes {
		outcome.DraftRevision = draft.Revision + 1
		withRevision = append(withRevision, outcome)
	}
	return withRevision, reverted, nil
}

// unacceptFieldSuggestionsTx returns suggestion rows flipped by acceptOneTx
// back to pending, keeping the batch consistent when the merged field set
// fails schema validation.
func unacceptFieldSuggestionsTx(ctx context.Context, tx pgx.Tx, organizationID string, outcomes []ReviewOutcome) error {
	ids := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		ids = append(ids, outcome.SuggestionID)
	}
	_, err := tx.Exec(ctx, `
		UPDATE asset.asset_field_suggestions
		SET status = 'pending', reviewed_by = NULL, reviewed_at = NULL, accepted_into_draft_id = NULL
		WHERE organization_id = $1::uuid AND id = ANY($2::uuid[]) AND status = 'accepted'
	`, organizationID, ids)
	if err != nil {
		return fmt.Errorf("unaccept field suggestions: %w", err)
	}
	return nil
}

// splitFieldOutcomes partitions outcomes into field/summary entries (failed)
// and the rest (kept).
func splitFieldOutcomes(outcomes []ReviewOutcome) (failed []ReviewOutcome, kept []ReviewOutcome) {
	for _, outcome := range outcomes {
		if outcome.Kind == SuggestionKindField || outcome.Kind == SuggestionKindSummary {
			failed = append(failed, outcome)
			continue
		}
		kept = append(kept, outcome)
	}
	return failed, kept
}

// validateDraftFieldsTx gates merged draft fields with the model head schema,
// exactly like the commit path does before materializing a snapshot.
func (s SuggestionReviewService) validateDraftFieldsTx(ctx context.Context, tx pgx.Tx, row LifecycleRow, fields map[string]any) error {
	var schemaBytes []byte
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(v.field_schema, '{}'::jsonb)
		FROM model.resource_models m
		LEFT JOIN model.resource_model_versions v ON v.organization_id = m.organization_id AND v.id = m.current_version_id
		WHERE m.organization_id = $1::uuid AND m.id = $2::uuid
	`, row.OrganizationID, row.ResourceModelID).Scan(&schemaBytes)
	if err != nil {
		return fmt.Errorf("load model field schema: %w", err)
	}
	if err := ValidateFields(schemaBytes, fields); err != nil {
		return ErrInvalidInput
	}
	return nil
}

// Reject closes a pending suggestion as rejected. Nothing enters the draft and
// no relation is written; the terminal state keeps the review audit trail.
func (s SuggestionReviewService) Reject(ctx context.Context, principal auth.Principal, workspaceID, assetID, kind, suggestionID string) error {
	if !validID(workspaceID) || !validID(assetID) || !ValidSuggestionKind(kind) || !validID(suggestionID) {
		return ErrInvalidInput
	}
	if _, _, err := s.loadAssetScope(ctx, principal, assetID, authz.ActionAssetWrite); err != nil {
		return err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	row, err := LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
	if err != nil {
		return err
	}
	if row.WorkspaceID != workspaceID {
		return ErrNotFound
	}
	if err := s.rejectOneTx(ctx, tx, principal, row, kind, suggestionID); err != nil {
		return err
	}
	RecordAssetAuditTx(ctx, tx, row.OrganizationID, row.WorkspaceID, principal, "asset.suggestion.rejected", row.ID, map[string]any{
		"workspace_id":  row.WorkspaceID,
		"kind":          kind,
		"suggestion_id": suggestionID,
	})
	return tx.Commit(ctx)
}

func (s SuggestionReviewService) rejectOneTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, row LifecycleRow, kind, suggestionID string) error {
	switch kind {
	case SuggestionKindField, SuggestionKindSummary:
		return rejectScopedTx(ctx, tx, principal, row, suggestionID, `
			UPDATE asset.asset_field_suggestions
			SET status = 'rejected', reviewed_by = $3::uuid, reviewed_at = now()
			WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'pending'
			  AND EXISTS (SELECT 1 FROM asset.asset_versions v
			              WHERE v.organization_id = asset.asset_field_suggestions.organization_id
			                AND v.id = asset.asset_field_suggestions.source_version_id
			                AND v.asset_id = $4::uuid AND v.workspace_id = $5::uuid)
		`)
	case SuggestionKindTag:
		// resolved_tag_id is cleared: the table CHECK pins rejected rows to an
		// empty resolution, and an eagerly-resolved pending suggestion must
		// still be rejectable.
		return rejectScopedTx(ctx, tx, principal, row, suggestionID, `
			UPDATE asset.asset_version_tag_suggestions
			SET status = 'rejected', resolved_tag_id = NULL, reviewed_by = $3::uuid, reviewed_at = now()
			WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'pending'
			  AND EXISTS (SELECT 1 FROM asset.asset_versions v
			              WHERE v.organization_id = asset.asset_version_tag_suggestions.organization_id
			                AND v.id = asset.asset_version_tag_suggestions.source_version_id
			                AND v.asset_id = $4::uuid AND v.workspace_id = $5::uuid)
		`)
	case SuggestionKindRelation:
		return rejectScopedTx(ctx, tx, principal, row, suggestionID, `
			UPDATE asset.asset_relation_suggestions
			SET status = 'rejected', reviewed_by = $3::uuid, reviewed_at = now()
			WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'pending'
			  AND EXISTS (SELECT 1 FROM asset.asset_versions v
			              WHERE v.organization_id = asset.asset_relation_suggestions.organization_id
			                AND v.id = asset.asset_relation_suggestions.source_version_id
			                AND v.asset_id = $4::uuid AND v.workspace_id = $5::uuid)
		`)
	default:
		return ErrSuggestionKindInvalid
	}
}

// rejectScopedTx runs one scoped rejection UPDATE; zero affected rows means
// the suggestion is missing from this asset or already decided.
func rejectScopedTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, row LifecycleRow, suggestionID, statement string) error {
	commandTag, err := tx.Exec(ctx, statement, principal.OrganizationID, suggestionID, principal.UserID, row.ID, row.WorkspaceID)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		return ErrSuggestionStateInvalid
	}
	return nil
}

// mapTagSuggestionError maps the tag domain sentinels onto the unified review
// errors so every surface reports one error vocabulary.
func mapTagSuggestionError(err error) error {
	switch {
	case errors.Is(err, tag.ErrSuggestionNotFound):
		return ErrSuggestionNotFound
	case errors.Is(err, tag.ErrSuggestionState):
		return ErrSuggestionStateInvalid
	case errors.Is(err, tag.ErrSuggestionNoTag):
		return ErrInvalidInput
	default:
		return err
	}
}

// draftMergePlan accumulates the draft content changes of accepted
// suggestions so one review call applies them with a single draft UPDATE.
// A plan never mutates the draft it was built from; apply() returns the
// persisted projection.
type draftMergePlan struct {
	baseFields map[string]any
	fields     map[string]any // nil until the first field suggestion merges
	summary    *string
}

func newDraftMergePlan(draft Draft) *draftMergePlan {
	return &draftMergePlan{baseFields: draft.Fields}
}

// mergeField stores the proposed value for one field key; within one call the
// later suggestion for a key wins deterministically.
func (p *draftMergePlan) mergeField(key string, value any) {
	if p.fields == nil {
		p.fields = make(map[string]any, len(p.baseFields)+1)
		for existingKey, existingValue := range p.baseFields {
			p.fields[existingKey] = existingValue
		}
	}
	p.fields[key] = value
}

// mergeSummary stores the proposed summary text.
func (p *draftMergePlan) mergeSummary(text string) {
	p.summary = &text
}

// hasFields reports whether any field suggestion merged into the plan.
func (p *draftMergePlan) hasFields() bool {
	return p.fields != nil
}

// revertFields undoes every field merge while keeping summary/tag/relation
// changes; used when the merged field set fails schema validation.
func (p *draftMergePlan) revertFields() {
	p.fields = nil
}

// apply returns the draft as it should be persisted.
func (p *draftMergePlan) apply(draft Draft) Draft {
	next := draft
	if p.fields != nil {
		next.Fields = p.fields
	}
	if p.summary != nil {
		next.Summary = *p.summary
	}
	return next
}
