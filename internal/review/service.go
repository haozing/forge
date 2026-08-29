package review

// service.go — the single-level PublicationRequest aggregate. Approval is the
// only command that flips the published pointer under the approval policy;
// no review action ever mutates AssetVersion content.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/access"
	"agentchunzhi/internal/asset"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInput      = errors.New("invalid publication request input")
	ErrForbidden         = errors.New("publication action forbidden")
	ErrNotFound          = errors.New("publication request not found")
	ErrConflict          = errors.New("publication request conflict")
	ErrSelfApproval      = errors.New("submitter cannot approve own request")
	ErrVersionSuperseded = errors.New("request version is no longer the working version")
)

// VersionCommitter lets the review service materialize a dirty draft into a
// working version without importing the HTTP or asset-internal details.
type VersionCommitter interface {
	CommitDraft(ctx context.Context, principal auth.Principal, workspaceID, assetID, expectedDraftRevision string) (asset.CommitResult, error)
}

type Service struct {
	Store     *store.Store
	Policy    authz.WorkspacePolicy
	Events    *eventing.EventStore
	Committer VersionCommitter
}

type Request struct {
	ID              string     `json:"id"`
	WorkspaceID     string     `json:"workspace_id"`
	AssetID         string     `json:"asset_id"`
	AssetVersionID  string     `json:"asset_version_id"`
	Status          string     `json:"status"`
	SubmittedBy     string     `json:"submitted_by"`
	DecidedBy       *string    `json:"decided_by"`
	DecisionComment *string    `json:"decision_comment"`
	CancelledBy     *string    `json:"cancelled_by"`
	CancelReason    *string    `json:"cancel_reason"`
	SubmittedAt     time.Time  `json:"submitted_at"`
	DecidedAt       *time.Time `json:"decided_at"`
	Revision        int64      `json:"revision"`
	ETag            string     `json:"etag"`
	Title           string     `json:"title,omitempty"`
	AssetVisibility string     `json:"asset_visibility,omitempty"`
}

type Comment struct {
	ID                   string    `json:"id"`
	PublicationRequestID string    `json:"publication_request_id"`
	Body                 string    `json:"body"`
	AuthorUserID         string    `json:"author_user_id"`
	CreatedAt            time.Time `json:"created_at"`
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
	Items      []Request
	HasMore    bool
	NextCursor string
}

type DecisionInput struct {
	Comment string
}

type DecisionResult struct {
	Request Request
}

type BatchItem struct {
	RequestID string `json:"request_id"`
	Comment   string `json:"comment"`
}

type BatchResult struct {
	Items []BatchItemResult `json:"items"`
}

type BatchItemResult struct {
	RequestID string   `json:"request_id"`
	OK        bool     `json:"ok"`
	ErrorCode string   `json:"error_code,omitempty"`
	Request   *Request `json:"request,omitempty"`
}

func (s Service) require(ctx context.Context, principal auth.Principal, workspaceID, action string) (authz.Scope, error) {
	if principal.UserType != auth.UserTypeMember || s.Store == nil || s.Store.Pool == nil {
		return authz.Scope{}, ErrForbidden
	}
	if s.Policy == nil {
		return authz.Scope{}, ErrForbidden
	}
	scope, err := s.Policy.Require(ctx, principal, workspaceID, "", action)
	if errors.Is(err, authz.ErrWorkspaceForbidden) || errors.Is(err, authz.ErrWorkspaceNotFound) {
		return authz.Scope{}, ErrForbidden
	}
	return scope, err
}

func (s Service) validID(value string) bool {
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

// Submit creates the single pending PublicationRequest for an asset. A dirty
// draft is materialized into a working version first, so the request always
// references a sealed snapshot.
func (s Service) Submit(ctx context.Context, principal auth.Principal, workspaceID, assetID, expectedDraftRevision, idempotencyKey, comment string) (Request, error) {
	if !s.validID(assetID) {
		return Request{}, ErrInvalidInput
	}
	scope, err := s.require(ctx, principal, workspaceID, authz.ActionPublicationSubmit)
	if err != nil {
		return Request{}, err
	}
	_ = scope
	// Materialize the draft snapshot; CommitDraft reverts to the current
	// working version when the draft is clean.
	commit, err := s.Committer.CommitDraft(ctx, principal, workspaceID, assetID, expectedDraftRevision)
	if err != nil {
		if errors.Is(err, asset.ErrDraftRevisionMismatch) {
			return Request{}, ErrConflict
		}
		return Request{}, mapCommitError(err)
	}
	versionID := commit.VersionID
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	var assetWorkspace, assetVisibility string
	err = tx.QueryRow(ctx, `
		SELECT workspace_id::text, visibility FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, principal.OrganizationID, assetID).Scan(&assetWorkspace, &assetVisibility)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, err
	}
	// The asset must live in the routed workspace; a mismatch hides as
	// NotFound (CommitDraft already blocks it — this is the redundant
	// defense-in-depth re-check on the aggregate's own transaction).
	if assetWorkspace != workspaceID {
		return Request{}, ErrNotFound
	}
	if assetVisibility == access.VisibilityPublic && !access.Valid(assetVisibility) {
		return Request{}, ErrInvalidInput
	}
	// The immutable publishing policy bound to the materialized version gates
	// submission: human confirmation and clean attachments are re-checked in
	// this transaction because the working version may have moved since the
	// draft commit.
	policy, err := enforcePublishPolicyTx(ctx, tx, principal.OrganizationID, assetID, versionID)
	if err != nil {
		return Request{}, err
	}
	// Only approval-policy assets enter the queue: a direct-policy submission
	// would strand a permanently pending request on the per-asset unique index
	// (mirrors the Approve-side mode check below).
	if policy.Mode != asset.PublishingModeApproval {
		return Request{}, ErrConflict
	}
	var request Request
	var decisionComment, cancelReason *string
	err = tx.QueryRow(ctx, `
		INSERT INTO asset.publication_requests
			(organization_id, workspace_id, asset_id, asset_version_id, status, submitted_by, decision_comment)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'pending', $5::uuid, NULLIF($6, ''))
		ON CONFLICT DO NOTHING
		RETURNING id::text, workspace_id::text, asset_id::text, asset_version_id::text, status,
		          submitted_by::text, decided_by::text, decision_comment, cancelled_by::text,
		          cancel_reason, submitted_at, decided_at, revision
	`, principal.OrganizationID, assetWorkspace, assetID, versionID, principal.UserID, comment).Scan(
		&request.ID, &request.WorkspaceID, &request.AssetID, &request.AssetVersionID, &request.Status,
		&request.SubmittedBy, &request.DecidedBy, &decisionComment, &request.CancelledBy,
		&cancelReason, &request.SubmittedAt, &request.DecidedAt, &request.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		// Partial unique index: one pending request per asset.
		return Request{}, ErrConflict
	}
	if err != nil {
		return Request{}, fmt.Errorf("insert publication request: %w", err)
	}
	request.DecisionComment = decisionComment
	request.CancelReason = cancelReason
	request.ETag = request.ID
	if err := s.appendEventTx(ctx, tx, principal, request, publicationEventFor(request.Status, ""), nil); err != nil {
		return Request{}, err
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("publication.submit", principal.OrganizationID, principal.UserID, "publication_request", request.ID, map[string]any{
		"workspace_id":     workspaceID,
		"asset_id":         assetID,
		"asset_version_id": versionID,
	}), workspaceID)
	if err := tx.Commit(ctx); err != nil {
		return Request{}, err
	}
	return request, nil
}

func mapCommitError(err error) error {
	switch {
	case errors.Is(err, asset.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, asset.ErrAssetArchived):
		return ErrConflict
	case errors.Is(err, asset.ErrForbidden):
		return ErrForbidden
	default:
		return err
	}
}

// enforcePublishPolicyTx re-reads the immutable publishing policy bound to the
// asset's current working version and enforces the human-confirmation and
// clean-attachment gates against the request's sealed version. The asset
// sentinels pass through unwrapped so every surface maps them alike.
func enforcePublishPolicyTx(ctx context.Context, tx pgx.Tx, organizationID, assetID, versionID string) (asset.PublishingPolicy, error) {
	policy, err := asset.PublishPolicyForAssetTx(ctx, tx, organizationID, assetID)
	if err != nil {
		return asset.PublishingPolicy{}, err
	}
	if err := asset.EnsurePublishableVersionTx(ctx, tx, organizationID, versionID, policy); err != nil {
		return asset.PublishingPolicy{}, err
	}
	return policy, nil
}

func publicationEventFor(status, _ string) string {
	switch status {
	case RequestApproved:
		return eventing.EventPublicationApproved
	case RequestRejected:
		return eventing.EventPublicationRejected
	case RequestCancelled:
		return eventing.EventPublicationCancelled
	default:
		return eventing.EventPublicationSubmitted
	}
}

// appendEventTx writes the publication_request fact + notification rows in the
// caller's transaction.
func (s Service) appendEventTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, request Request, eventType string, extra map[string]any) error {
	if s.Events == nil {
		return errors.New("event store is not initialized")
	}
	payload := eventing.PublicationRequestPayload{
		RequestID:      request.ID,
		AssetID:        request.AssetID,
		AssetVersionID: request.AssetVersionID,
		WorkspaceID:    request.WorkspaceID,
	}
	if request.CancelReason != nil {
		payload.CancelReason = *request.CancelReason
	}
	raw, err := eventing.EncodePayload(payload)
	if err != nil {
		return err
	}
	actor := eventing.ActorFromPrincipal(principal)
	_, err = s.Events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   principal.OrganizationID,
		WorkspaceID:      request.WorkspaceID,
		EventType:        eventType,
		AggregateType:    "publication_request",
		AggregateID:      request.ID,
		AggregateVersion: request.Revision,
		PayloadVersion:   eventing.PayloadVersionV1,
		Actor:            actor,
		Payload:          raw,
	})
	if err != nil {
		return err
	}
	// Notify the submitter (and approver for decisions) through the
	// notification table; consumers keep delivery idempotent by request id.
	kind := "publication." + strings.TrimPrefix(eventType, "publication_request.")
	_, err = tx.Exec(ctx, `
		INSERT INTO content.notifications (organization_id, workspace_id, recipient_user_id, kind, payload)
		SELECT organization_id, workspace_id, submitted_by, $3, $4::jsonb
		FROM asset.publication_requests WHERE id = $1::uuid AND organization_id = $2::uuid
		  AND submitted_by <> NULLIF($5,'')::uuid
	`, request.ID, principal.OrganizationID, kind, mustJSON(map[string]any{
		"request_id": request.ID,
		"asset_id":   request.AssetID,
		"status":     request.Status,
	}), principal.UserID)
	if err != nil {
		return fmt.Errorf("record publication notification: %w", err)
	}
	_ = extra
	return nil
}

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// ListPage is the review queue. Editors only see their own submissions;
// reviewer/admin see the workspace queue.
func (s Service) ListPage(ctx context.Context, principal auth.Principal, workspaceID string, input ListInput) (Page, error) {
	scope, err := s.require(ctx, principal, workspaceID, authz.ActionPublicationRead)
	if err != nil {
		return Page{}, err
	}
	limit := input.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	switch input.Status {
	case "", RequestPending, RequestApproved, RequestRejected, RequestCancelled:
	default:
		return Page{}, ErrInvalidInput
	}
	var cursor struct {
		SubmittedAt time.Time `json:"submitted_at"`
		ID          string    `json:"id"`
	}
	if strings.TrimSpace(input.Cursor) != "" {
		raw, err := base64.RawURLEncoding.DecodeString(input.Cursor)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || !s.validID(cursor.ID) {
			return Page{}, ErrInvalidInput
		}
	}
	where := []string{
		"pr.organization_id = $1::uuid",
		"pr.workspace_id = $2::uuid",
	}
	args := []any{principal.OrganizationID, workspaceID}
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if input.Status != "" {
		where = append(where, "pr.status = "+arg(input.Status))
	}
	if input.ResourceModelID != "" && s.validID(input.ResourceModelID) {
		where = append(where, "a.resource_model_id = "+arg(input.ResourceModelID)+"::uuid")
	}
	if scope.Role == authz.WorkspaceRoleEditor {
		// Editors narrow to their own submissions.
		where = append(where, "pr.submitted_by = "+arg(principal.UserID)+"::uuid")
	} else if input.SubmittedBy != "" && s.validID(input.SubmittedBy) {
		where = append(where, "pr.submitted_by = "+arg(input.SubmittedBy)+"::uuid")
	}
	if input.CreatedFrom != "" {
		where = append(where, "pr.submitted_at >= "+arg(input.CreatedFrom)+"::timestamptz")
	}
	if input.CreatedTo != "" {
		where = append(where, "pr.submitted_at <= "+arg(input.CreatedTo)+"::timestamptz")
	}
	if cursor.ID != "" {
		where = append(where, fmt.Sprintf("(pr.submitted_at, pr.id) < (%s::timestamptz, %s::uuid)", arg(cursor.SubmittedAt.UTC().Format(time.RFC3339Nano)), arg(cursor.ID)))
	}
	query := `
		SELECT pr.id::text, pr.workspace_id::text, pr.asset_id::text, pr.asset_version_id::text,
		       pr.status, pr.submitted_by::text, pr.decided_by::text, pr.decision_comment,
		       pr.cancelled_by::text, pr.cancel_reason, pr.submitted_at, pr.decided_at, pr.revision,
		       COALESCE(v.title, '')
		FROM asset.publication_requests pr
		JOIN asset.assets a ON a.organization_id = pr.organization_id AND a.id = pr.asset_id
		LEFT JOIN asset.asset_versions v ON v.id = pr.asset_version_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY pr.submitted_at DESC, pr.id DESC
		LIMIT ` + fmt.Sprint(limit+1)
	rows, err := s.Store.Pool.Query(ctx, query, args...)
	if err != nil {
		return Page{}, fmt.Errorf("list publication requests: %w", err)
	}
	defer rows.Close()
	page := Page{Items: make([]Request, 0, limit+1)}
	for rows.Next() {
		item, err := scanRequest(rows)
		if err != nil {
			return Page{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		raw, _ := json.Marshal(map[string]string{
			"submitted_at": last.SubmittedAt.UTC().Format(time.RFC3339Nano),
			"id":           last.ID,
		})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}

func scanRequest(row interface{ Scan(...any) error }) (Request, error) {
	var item Request
	var decisionComment, cancelReason *string
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.AssetID, &item.AssetVersionID,
		&item.Status, &item.SubmittedBy, &item.DecidedBy, &decisionComment,
		&item.CancelledBy, &cancelReason, &item.SubmittedAt, &item.DecidedAt, &item.Revision,
		&item.Title); err != nil {
		return Request{}, fmt.Errorf("scan publication request: %w", err)
	}
	item.DecisionComment = decisionComment
	item.CancelReason = cancelReason
	item.ETag = item.ID
	return item, nil
}

// Get returns one request; editors only reach their own submissions.
func (s Service) Get(ctx context.Context, principal auth.Principal, workspaceID, requestID string) (Request, error) {
	scope, err := s.require(ctx, principal, workspaceID, authz.ActionPublicationRead)
	if err != nil {
		return Request{}, err
	}
	if !s.validID(requestID) {
		return Request{}, ErrInvalidInput
	}
	row := s.Store.Pool.QueryRow(ctx, `
		SELECT pr.id::text, pr.workspace_id::text, pr.asset_id::text, pr.asset_version_id::text,
		       pr.status, pr.submitted_by::text, pr.decided_by::text, pr.decision_comment,
		       pr.cancelled_by::text, pr.cancel_reason, pr.submitted_at, pr.decided_at, pr.revision,
		       COALESCE(v.title, '')
		FROM asset.publication_requests pr
		LEFT JOIN asset.asset_versions v ON v.id = pr.asset_version_id
		WHERE pr.organization_id = $1::uuid AND pr.id = $2::uuid AND pr.workspace_id = $3::uuid
	`, principal.OrganizationID, requestID, workspaceID)
	item, err := scanRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, err
	}
	if scope.Role == authz.WorkspaceRoleEditor && item.SubmittedBy != principal.UserID {
		return Request{}, ErrNotFound
	}
	return item, nil
}

// decide performs approve or reject with every invariant inside one
// transaction: pending state, no self-approval, working pointer still matches,
// policy re-check, pointer switch on approval and full audit/event fan-out.
func (s Service) decide(ctx context.Context, principal auth.Principal, workspaceID, requestID, decision, comment string) (Request, error) {
	action := authz.ActionPublicationApprove
	if decision == "reject" {
		action = authz.ActionPublicationReject
	}
	if _, err := s.require(ctx, principal, workspaceID, action); err != nil {
		return Request{}, err
	}
	if !s.validID(requestID) {
		return Request{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	var status, submittedBy, assetID, versionID, assetWorkspace, assetModel string
	var revision int64
	err = tx.QueryRow(ctx, `
		SELECT pr.status, pr.submitted_by::text, pr.asset_id::text, pr.asset_version_id::text, pr.revision,
		       a.workspace_id::text, a.resource_model_id::text
		FROM asset.publication_requests pr
		JOIN asset.assets a ON a.organization_id = pr.organization_id AND a.id = pr.asset_id
		WHERE pr.organization_id = $1::uuid AND pr.id = $2::uuid AND pr.workspace_id = $3::uuid
		FOR UPDATE OF pr
	`, principal.OrganizationID, requestID, workspaceID).Scan(&status, &submittedBy, &assetID, &versionID, &revision, &assetWorkspace, &assetModel)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, err
	}
	if status != RequestPending {
		return Request{}, ErrConflict
	}
	if decision == "approve" && submittedBy == principal.UserID {
		return Request{}, ErrSelfApproval
	}
	// The request must still reference the asset's current working version.
	var workingVersion string
	if err := tx.QueryRow(ctx, `
		SELECT current_working_version_id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, assetID).Scan(&workingVersion); err != nil {
		return Request{}, err
	}
	if workingVersion != versionID {
		return Request{}, ErrVersionSuperseded
	}
	nextStatus := RequestRejected
	if decision == "approve" {
		nextStatus = RequestApproved
	}
	commandTag, err := tx.Exec(ctx, `
		UPDATE asset.publication_requests
		SET status = $3, decided_by = $4::uuid, decision_comment = NULLIF($5, ''),
		    decided_at = now(), revision = revision + 1
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'pending'
	`, principal.OrganizationID, requestID, nextStatus, principal.UserID, comment)
	if err != nil {
		return Request{}, fmt.Errorf("decide publication request: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return Request{}, ErrConflict
	}
	request := Request{
		ID: requestID, WorkspaceID: assetWorkspace, AssetID: assetID, AssetVersionID: versionID,
		Status: nextStatus, SubmittedBy: submittedBy, DecidedBy: strPtr(principal.UserID),
		Revision: revision + 1,
	}
	if comment != "" {
		request.DecisionComment = strPtr(comment)
	}
	now := time.Now().UTC()
	request.DecidedAt = &now
	request.ETag = request.ID
	if decision == "approve" {
		// Approval re-reads the policy inside the decision transaction: the
		// asset may have switched to a direct policy (the request must go
		// through direct publish instead) and the version's confirmation or
		// attachment facts may have moved since submission.
		policy, err := enforcePublishPolicyTx(ctx, tx, principal.OrganizationID, assetID, versionID)
		if err != nil {
			return Request{}, err
		}
		if policy.Mode != asset.PublishingModeApproval {
			return Request{}, ErrConflict
		}
		// Approval switches the published pointer inside the same transaction.
		row, err := asset.LoadLifecycleTx(ctx, tx, principal.OrganizationID, assetID)
		if err != nil {
			return Request{}, err
		}
		previous := row.CurrentPublishedVersionID
		row, err = asset.SetPublishedPointerTx(ctx, tx, row, versionID)
		if err != nil {
			if errors.Is(err, asset.ErrInvalidTransition) {
				return Request{}, ErrVersionSuperseded
			}
			return Request{}, err
		}
		if err := asset.AppendAssetEventTx(ctx, tx, s.Events, row, principal, eventing.EventAssetPublished, eventing.PayloadVersionV1, eventing.AssetPublishedPayload{
			AssetID:           row.ID,
			VersionID:         versionID,
			PreviousVersionID: deref(previous),
			WorkspaceID:       row.WorkspaceID,
		}); err != nil {
			return Request{}, err
		}
	}
	if err := s.appendEventTx(ctx, tx, principal, request, publicationEventFor(nextStatus, ""), nil); err != nil {
		return Request{}, err
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("publication."+decision, principal.OrganizationID, principal.UserID, "publication_request", requestID, map[string]any{
		"workspace_id": assetWorkspace,
		"asset_id":     assetID,
		"decision":     decision,
	}), assetWorkspace)
	if err := tx.Commit(ctx); err != nil {
		return Request{}, err
	}
	return request, nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func strPtr(value string) *string { return &value }

// Approve publishes the requested version.
func (s Service) Approve(ctx context.Context, principal auth.Principal, workspaceID, requestID, comment string) (Request, error) {
	return s.decide(ctx, principal, workspaceID, requestID, "approve", comment)
}

// Reject returns the asset to its previous state without touching versions.
func (s Service) Reject(ctx context.Context, principal auth.Principal, workspaceID, requestID, comment string) (Request, error) {
	return s.decide(ctx, principal, workspaceID, requestID, "reject", comment)
}

// Cancel lets the submitter withdraw a pending request; workspace admins may
// cancel any pending request in their workspace.
func (s Service) Cancel(ctx context.Context, principal auth.Principal, workspaceID, requestID, reason string) (Request, error) {
	scope, err := s.require(ctx, principal, workspaceID, authz.ActionPublicationCancel)
	if err != nil {
		return Request{}, err
	}
	if !s.validID(requestID) {
		return Request{}, ErrInvalidInput
	}
	cancelReason := "user_cancelled"
	if scope.Role == authz.WorkspaceRoleAdmin && reason == "admin_cancelled" {
		cancelReason = "admin_cancelled"
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	var status, submittedBy, assetWorkspace string
	var revision int64
	err = tx.QueryRow(ctx, `
		SELECT status, submitted_by::text, revision, workspace_id::text
		FROM asset.publication_requests
		WHERE organization_id = $1::uuid AND id = $2::uuid AND workspace_id = $3::uuid
		FOR UPDATE
	`, principal.OrganizationID, requestID, workspaceID).Scan(&status, &submittedBy, &revision, &assetWorkspace)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, err
	}
	if status != RequestPending {
		return Request{}, ErrConflict
	}
	if submittedBy != principal.UserID && scope.Role != authz.WorkspaceRoleAdmin {
		return Request{}, ErrNotFound
	}
	commandTag, err := tx.Exec(ctx, `
		UPDATE asset.publication_requests
		SET status = 'cancelled', cancelled_by = $3::uuid, cancel_reason = $4,
		    decided_at = now(), revision = revision + 1
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'pending'
	`, principal.OrganizationID, requestID, principal.UserID, cancelReason)
	if err != nil {
		return Request{}, err
	}
	if commandTag.RowsAffected() == 0 {
		return Request{}, ErrConflict
	}
	request := Request{
		ID: requestID, WorkspaceID: assetWorkspace, Status: RequestCancelled,
		SubmittedBy: submittedBy, CancelledBy: strPtr(principal.UserID),
		CancelReason: strPtr(cancelReason), Revision: revision + 1, ETag: requestID,
	}
	if err := s.appendEventTx(ctx, tx, principal, request, eventing.EventPublicationCancelled, nil); err != nil {
		return Request{}, err
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("publication.cancel", principal.OrganizationID, principal.UserID, "publication_request", requestID, map[string]any{
		"workspace_id":  assetWorkspace,
		"cancel_reason": cancelReason,
	}), assetWorkspace)
	if err := tx.Commit(ctx); err != nil {
		return Request{}, err
	}
	return request, nil
}

// Batch applies the same domain command to every item and reports per-item
// results; failures never abort the remaining items.
func (s Service) Batch(ctx context.Context, principal auth.Principal, workspaceID, decision string, items []BatchItem) (BatchResult, error) {
	switch decision {
	case "approve", "reject":
	default:
		return BatchResult{}, ErrInvalidInput
	}
	result := BatchResult{Items: make([]BatchItemResult, 0, len(items))}
	for _, item := range items {
		var request Request
		var err error
		if decision == "approve" {
			request, err = s.Approve(ctx, principal, workspaceID, item.RequestID, item.Comment)
		} else {
			request, err = s.Reject(ctx, principal, workspaceID, item.RequestID, item.Comment)
		}
		entry := BatchItemResult{RequestID: item.RequestID}
		if code := batchErrorCode(err); code != "" {
			entry.ErrorCode = code
		} else {
			entry.OK = true
			entry.Request = &request
		}
		result.Items = append(result.Items, entry)
	}
	return result, nil
}

// batchErrorCode maps a decision failure onto the per-item error-code
// contract. The publishing-policy gates surface the shared asset sentinels so
// batch and HTTP responses agree on one vocabulary.
func batchErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotFound):
		return "publication_request_not_found"
	case errors.Is(err, ErrConflict):
		return "publication_request_not_pending"
	case errors.Is(err, asset.ErrConfirmationRequired):
		return "human_confirmation_required"
	case errors.Is(err, asset.ErrAttachmentNotClean):
		return "attachments_not_clean"
	case errors.Is(err, asset.ErrRequiredFieldMissing):
		return "required_field_missing"
	case errors.Is(err, ErrSelfApproval):
		return "self_approval_not_allowed"
	case errors.Is(err, ErrVersionSuperseded):
		return "request_version_superseded"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	default:
		return "publication_request_error"
	}
}

// AddComment appends a discussion entry; the thread stays readable and open
// under any request status.
func (s Service) AddComment(ctx context.Context, principal auth.Principal, workspaceID, requestID, body string) (Comment, error) {
	scope, err := s.require(ctx, principal, workspaceID, authz.ActionPublicationComment)
	if err != nil {
		return Comment{}, err
	}
	body = strings.TrimSpace(body)
	if body == "" || !s.validID(requestID) {
		return Comment{}, ErrInvalidInput
	}
	var submittedBy, requestWorkspace string
	var status string
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT submitted_by::text, workspace_id::text, status FROM asset.publication_requests
		WHERE organization_id = $1::uuid AND id = $2::uuid AND workspace_id = $3::uuid
	`, principal.OrganizationID, requestID, workspaceID).Scan(&submittedBy, &requestWorkspace, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Comment{}, ErrNotFound
	}
	if err != nil {
		return Comment{}, err
	}
	if scope.Role == authz.WorkspaceRoleEditor && submittedBy != principal.UserID {
		return Comment{}, ErrNotFound
	}
	var comment Comment
	err = s.Store.Pool.QueryRow(ctx, `
		INSERT INTO asset.publication_request_comments
			(organization_id, workspace_id, publication_request_id, body, author_user_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid)
		RETURNING id::text, publication_request_id::text, body, author_user_id::text, created_at
	`, principal.OrganizationID, requestWorkspace, requestID, body, principal.UserID).Scan(
		&comment.ID, &comment.PublicationRequestID, &comment.Body, &comment.AuthorUserID, &comment.CreatedAt)
	if err != nil {
		return Comment{}, fmt.Errorf("insert publication comment: %w", err)
	}
	return comment, nil
}

// ListComments returns one page of a request's discussion thread, ordered by
// (created_at, id) with keyset pagination.
func (s Service) ListComments(ctx context.Context, principal auth.Principal, workspaceID, requestID, cursor string, limit int) ([]Comment, string, error) {
	scope, err := s.require(ctx, principal, workspaceID, authz.ActionPublicationRead)
	if err != nil {
		return nil, "", err
	}
	if !s.validID(requestID) {
		return nil, "", ErrInvalidInput
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var pageCursor struct {
		CreatedAt time.Time `json:"created_at"`
		ID        string    `json:"id"`
	}
	if strings.TrimSpace(cursor) != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(raw, &pageCursor) != nil || !s.validID(pageCursor.ID) {
			return nil, "", ErrInvalidInput
		}
	}
	if scope.Role == authz.WorkspaceRoleEditor {
		var submittedBy string
		err := s.Store.Pool.QueryRow(ctx, `
			SELECT submitted_by::text FROM asset.publication_requests
			WHERE organization_id = $1::uuid AND id = $2::uuid AND workspace_id = $3::uuid
		`, principal.OrganizationID, requestID, workspaceID).Scan(&submittedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		if err != nil {
			return nil, "", err
		}
		if submittedBy != principal.UserID {
			return nil, "", ErrNotFound
		}
	}
	query := `
		SELECT id::text, publication_request_id::text, body, author_user_id::text, created_at
		FROM asset.publication_request_comments
		WHERE organization_id = $1::uuid AND publication_request_id = $2::uuid
		  AND workspace_id = $3::uuid`
	args := []any{principal.OrganizationID, requestID, workspaceID}
	if pageCursor.ID != "" {
		query += fmt.Sprintf(" AND (created_at, id) > ($%d::timestamptz, $%d::uuid)", len(args)+1, len(args)+2)
		args = append(args, pageCursor.CreatedAt.UTC().Format(time.RFC3339Nano), pageCursor.ID)
	}
	query += " ORDER BY created_at, id LIMIT " + fmt.Sprint(limit+1)
	rows, err := s.Store.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	comments := make([]Comment, 0, limit)
	for rows.Next() {
		var comment Comment
		if err := rows.Scan(&comment.ID, &comment.PublicationRequestID, &comment.Body, &comment.AuthorUserID, &comment.CreatedAt); err != nil {
			return nil, "", err
		}
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(comments) > limit {
		comments = comments[:limit]
		last := comments[len(comments)-1]
		raw, _ := json.Marshal(map[string]string{
			"created_at": last.CreatedAt.UTC().Format(time.RFC3339Nano),
			"id":         last.ID,
		})
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return comments, next, nil
}

// PendingCount feeds the workspace statistics.
func (s Service) PendingCount(ctx context.Context, principal auth.Principal, workspaceID string) (int64, error) {
	if _, err := s.require(ctx, principal, workspaceID, authz.ActionPublicationRead); err != nil {
		return 0, err
	}
	var count int64
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT count(*) FROM asset.publication_requests
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND status = 'pending'
	`, principal.OrganizationID, workspaceID).Scan(&count)
	return count, err
}
