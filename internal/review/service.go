package review

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

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInput = errors.New("invalid review input")
	ErrForbidden    = errors.New("review access denied")
	ErrNotFound     = errors.New("review not found")
	ErrConflict     = errors.New("review conflict")
)

type Service struct {
	Store  *store.Store
	Policy authz.WorkspacePolicy
}

type Item struct {
	ID                string         `json:"review_id"`
	WorkspaceID       string         `json:"workspace_id"`
	AssetID           string         `json:"asset_id"`
	AssetVersionID    string         `json:"asset_version_id"`
	ResourceModelID   string         `json:"resource_model_id"`
	ResourceModelName string         `json:"resource_model_name"`
	Title             *string        `json:"title"`
	Fields            map[string]any `json:"fields"`
	Quality           string         `json:"quality"`
	Status            string         `json:"status"`
	Comment           string         `json:"comment"`
	SubmittedBy       Actor          `json:"submitted_by"`
	ReviewedBy        *Actor         `json:"reviewed_by,omitempty"`
	SubmittedAt       time.Time      `json:"submitted_at"`
	ReviewedAt        *time.Time     `json:"reviewed_at,omitempty"`
	ETag              string         `json:"etag"`
}

type Actor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type DecisionInput struct {
	Comment           string `json:"comment"`
	ExpectedVersionID string `json:"expected_version_id"`
}

type DecisionResult struct {
	ReviewID       string `json:"review_id"`
	AssetID        string `json:"asset_id"`
	AssetVersionID string `json:"asset_version_id"`
	Status         string `json:"status"`
	Decision       string `json:"decision"`
}

type ListInput struct {
	Status          string
	ResourceModelID string
	SubmittedBy     string
	CreatedFrom     string
	CreatedTo       string
	Limit           int
	Cursor          string
}

type Page struct {
	Items      []Item
	HasMore    bool
	NextCursor string
}

func (s Service) require(ctx context.Context, principal auth.Principal, workspaceID, modelID, action string) error {
	if principal.UserType != "member" || s.Store == nil || s.Store.Pool == nil || s.Policy == nil {
		return ErrForbidden
	}
	_, err := s.Policy.Require(ctx, principal, workspaceID, modelID, action)
	if errors.Is(err, authz.ErrWorkspaceForbidden) || errors.Is(err, authz.ErrWorkspaceNotFound) {
		return ErrForbidden
	}
	return err
}

func (s Service) ListPage(ctx context.Context, principal auth.Principal, workspaceID string, input ListInput) (Page, error) {
	if !validID(workspaceID) || (input.ResourceModelID != "" && !validID(input.ResourceModelID)) || (input.SubmittedBy != "" && !validID(input.SubmittedBy)) {
		return Page{}, ErrInvalidInput
	}
	input.Status = strings.TrimSpace(input.Status)
	if input.Status != "" && input.Status != "pending" && input.Status != "approved" && input.Status != "rejected" && input.Status != "superseded" {
		return Page{}, ErrInvalidInput
	}
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return Page{}, ErrInvalidInput
	}
	createdFrom, err := parseReviewTime(input.CreatedFrom)
	if err != nil {
		return Page{}, err
	}
	createdTo, err := parseReviewTime(input.CreatedTo)
	if err != nil || (createdFrom != nil && createdTo != nil && createdFrom.After(*createdTo)) {
		return Page{}, ErrInvalidInput
	}
	cursor, err := decodeReviewCursor(input.Cursor)
	if err != nil {
		return Page{}, err
	}
	if err := s.require(ctx, principal, workspaceID, input.ResourceModelID, "asset.review"); err != nil {
		return Page{}, err
	}
	fromValue, toValue := "", ""
	if createdFrom != nil {
		fromValue = createdFrom.UTC().Format(time.RFC3339Nano)
	}
	if createdTo != nil {
		toValue = createdTo.UTC().Format(time.RFC3339Nano)
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT r.id::text, r.workspace_id::text, r.asset_id::text, r.asset_version_id::text,
		       v.resource_model_id::text, rm.name, v.title, v.fields, v.quality, r.status, r.comment,
		       su.id::text, su.display_name, ru.id::text, ru.display_name,
		       r.submitted_at, r.reviewed_at
		FROM asset.asset_reviews r
		JOIN asset.asset_versions v ON v.id = r.asset_version_id
		JOIN model.resource_models rm ON rm.id = v.resource_model_id
		JOIN identity.users su ON su.id = r.submitted_by
		LEFT JOIN identity.users ru ON ru.id = r.reviewed_by
		WHERE r.organization_id = $1::uuid AND r.workspace_id = $2::uuid
		  AND ($3 = '' OR r.status = $3)
		  AND ($4 = '' OR v.resource_model_id = NULLIF($4, '')::uuid)
		  AND ($5 = '' OR r.submitted_by = NULLIF($5, '')::uuid)
		  AND ($6 = '' OR r.submitted_at >= NULLIF($6, '')::timestamptz)
		  AND ($7 = '' OR r.submitted_at <= NULLIF($7, '')::timestamptz)
		  AND ($8 = '' OR (r.submitted_at, r.id) < (NULLIF($9, '')::timestamptz, NULLIF($10, '')::uuid))
		ORDER BY r.submitted_at DESC, r.id DESC LIMIT $11
	`, principal.OrganizationID, workspaceID, input.Status, input.ResourceModelID, input.SubmittedBy, fromValue, toValue, input.Cursor, cursor.SubmittedAt, cursor.ID, limit+1)
	if err != nil {
		return Page{}, fmt.Errorf("list reviews: %w", err)
	}
	defer rows.Close()
	items := make([]Item, 0, limit+1)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return Page{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("iterate reviews: %w", err)
	}
	page := Page{Items: items}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeReviewCursor(last.SubmittedAt, last.ID)
	}
	return page, nil
}

type reviewCursor struct {
	SubmittedAt string `json:"submitted_at"`
	ID          string `json:"id"`
}

func encodeReviewCursor(submittedAt time.Time, id string) string {
	raw, _ := json.Marshal(reviewCursor{SubmittedAt: submittedAt.UTC().Format(time.RFC3339Nano), ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeReviewCursor(value string) (reviewCursor, error) {
	if strings.TrimSpace(value) == "" {
		return reviewCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return reviewCursor{}, ErrInvalidInput
	}
	var cursor reviewCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || !validID(cursor.ID) {
		return reviewCursor{}, ErrInvalidInput
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.SubmittedAt); err != nil {
		return reviewCursor{}, ErrInvalidInput
	}
	return cursor, nil
}

func parseReviewTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return &parsed, nil
}

func (s Service) Get(ctx context.Context, principal auth.Principal, reviewID string) (Item, error) {
	if !validID(reviewID) {
		return Item{}, ErrInvalidInput
	}
	var workspaceID, modelID string
	err := s.Store.Pool.QueryRow(ctx, `SELECT r.workspace_id::text, v.resource_model_id::text FROM asset.asset_reviews r JOIN asset.asset_versions v ON v.id = r.asset_version_id WHERE r.organization_id = $1::uuid AND r.id = $2::uuid`, principal.OrganizationID, reviewID).Scan(&workspaceID, &modelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, fmt.Errorf("load review scope: %w", err)
	}
	if err := s.require(ctx, principal, workspaceID, modelID, "asset.review"); err != nil {
		return Item{}, err
	}
	row := s.Store.Pool.QueryRow(ctx, `
		SELECT r.id::text, r.workspace_id::text, r.asset_id::text, r.asset_version_id::text,
		       v.resource_model_id::text, rm.name, v.title, v.fields, v.quality, r.status, r.comment,
		       su.id::text, su.display_name, ru.id::text, ru.display_name, r.submitted_at, r.reviewed_at
		FROM asset.asset_reviews r JOIN asset.asset_versions v ON v.id = r.asset_version_id JOIN model.resource_models rm ON rm.id = v.resource_model_id
		JOIN identity.users su ON su.id = r.submitted_by LEFT JOIN identity.users ru ON ru.id = r.reviewed_by
		WHERE r.organization_id = $1::uuid AND r.id = $2::uuid
	`, principal.OrganizationID, reviewID)
	return scanItem(row)
}

func (s Service) Decide(ctx context.Context, principal auth.Principal, reviewID, idempotencyKey, decision string, input DecisionInput) (DecisionResult, error) {
	if !validID(reviewID) || len(strings.TrimSpace(idempotencyKey)) < 16 {
		return DecisionResult{}, ErrInvalidInput
	}
	item, err := s.Get(ctx, principal, reviewID)
	if err != nil {
		return DecisionResult{}, err
	}
	if decision != "approve" && decision != "reject" {
		return DecisionResult{}, ErrInvalidInput
	}
	if input.ExpectedVersionID != "" && input.ExpectedVersionID != item.AssetVersionID {
		return DecisionResult{}, ErrConflict
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return DecisionResult{}, fmt.Errorf("begin review decision: %w", err)
	}
	defer tx.Rollback(ctx)
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM asset.asset_reviews WHERE organization_id = $1::uuid AND id = $2::uuid FOR UPDATE`, principal.OrganizationID, reviewID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DecisionResult{}, ErrNotFound
		}
		return DecisionResult{}, fmt.Errorf("lock review: %w", err)
	}
	if status != "pending" {
		return DecisionResult{}, fmt.Errorf("%w: review is already %s", ErrConflict, status)
	}
	newStatus := "approved"
	if decision == "reject" {
		newStatus = "rejected"
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.asset_reviews SET status = $3, reviewed_by = $4::uuid, comment = $5, reviewed_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, reviewID, newStatus, principal.UserID, strings.TrimSpace(input.Comment)); err != nil {
		return DecisionResult{}, fmt.Errorf("update review: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE asset.asset_versions SET review_status = $2 WHERE id = $1::uuid`, item.AssetVersionID, newStatus); err != nil {
		return DecisionResult{}, fmt.Errorf("update asset review status: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return DecisionResult{}, fmt.Errorf("commit review decision: %w", err)
	}
	return DecisionResult{ReviewID: reviewID, AssetID: item.AssetID, AssetVersionID: item.AssetVersionID, Status: newStatus, Decision: decision}, nil
}

type BatchItem struct {
	ReviewID          string `json:"review_id"`
	Decision          string `json:"decision"`
	Comment           string `json:"comment"`
	ExpectedVersionID string `json:"expected_version_id"`
}
type BatchResult struct {
	ReviewID  string `json:"review_id"`
	Status    string `json:"status"`
	Decision  string `json:"decision,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

func (s Service) Batch(ctx context.Context, principal auth.Principal, idempotencyKey string, input []BatchItem) []BatchResult {
	results := make([]BatchResult, 0, len(input))
	for _, item := range input {
		result, err := s.Decide(ctx, principal, item.ReviewID, idempotencyKey+item.ReviewID, item.Decision, DecisionInput{Comment: item.Comment, ExpectedVersionID: item.ExpectedVersionID})
		if err != nil {
			results = append(results, BatchResult{ReviewID: item.ReviewID, Status: "failed", ErrorCode: reviewErrorCode(err)})
			continue
		}
		results = append(results, BatchResult{ReviewID: result.ReviewID, Status: result.Status, Decision: result.Decision})
	}
	return results
}

func reviewErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrInvalidInput):
		return "validation_failed"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrConflict):
		return "conflict"
	default:
		return "review_failed"
	}
}

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	var item Item
	var rawFields []byte
	var reviewerID, reviewerName *string
	err := row.Scan(&item.ID, &item.WorkspaceID, &item.AssetID, &item.AssetVersionID, &item.ResourceModelID, &item.ResourceModelName, &item.Title, &rawFields, &item.Quality, &item.Status, &item.Comment, &item.SubmittedBy.ID, &item.SubmittedBy.DisplayName, &reviewerID, &reviewerName, &item.SubmittedAt, &item.ReviewedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, fmt.Errorf("scan review: %w", err)
	}
	item.Fields = map[string]any{}
	_ = json.Unmarshal(rawFields, &item.Fields)
	item.ETag = item.AssetVersionID
	if reviewerID != nil {
		item.ReviewedBy = &Actor{ID: *reviewerID, DisplayName: deref(reviewerName)}
	}
	return item, nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func validID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}
