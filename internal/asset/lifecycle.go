package asset

// lifecycle.go — the only place Asset publication status and pointers change.
// Every transition validates the state machine from contract.go, bumps the
// Asset revision, keeps the pointer invariants and appends audit + domain
// facts inside the caller's transaction.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrNotFound      = errors.New("asset not found")
	ErrConflict      = errors.New("asset state conflict")
	ErrInvalidInput  = errors.New("invalid asset input")
	ErrForbidden     = errors.New("asset action forbidden")
	ErrAssetArchived      = errors.New("asset is archived")
	ErrNoteBlocksManaged = errors.New("note content is managed as blocks")
	ErrDraftDirty    = errors.New("asset draft revision mismatch")
)

// LifecycleRow is the mutable asset state a transition needs.
type LifecycleRow struct {
	ID                        string
	OrganizationID            string
	WorkspaceID               string
	ResourceModelID           string
	PublicationStatus         string
	Visibility                string
	CurrentWorkingVersionID   string
	CurrentPublishedVersionID *string
	Revision                  int64
	PublishedAt               *time.Time
}

// LoadLifecycleTx reads the asset row FOR UPDATE inside the caller's
// transaction.
func LoadLifecycleTx(ctx context.Context, tx pgx.Tx, organizationID, assetID string) (LifecycleRow, error) {
	var row LifecycleRow
	var published *string
	err := tx.QueryRow(ctx, `
		SELECT id::text, organization_id::text, workspace_id::text, resource_model_id::text,
		       publication_status, visibility,
		       current_working_version_id::text, current_published_version_id::text,
		       revision, published_at
		FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
		FOR UPDATE
	`, organizationID, assetID).Scan(&row.ID, &row.OrganizationID, &row.WorkspaceID, &row.ResourceModelID,
		&row.PublicationStatus, &row.Visibility,
		&row.CurrentWorkingVersionID, &published, &row.Revision, &row.PublishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LifecycleRow{}, ErrNotFound
	}
	if err != nil {
		return LifecycleRow{}, fmt.Errorf("load asset lifecycle: %w", err)
	}
	if published != nil {
		row.CurrentPublishedVersionID = published
	}
	return row, nil
}

// SetPublishedPointerTx switches the published pointer. The version must
// belong to the same asset. published_at follows doc §5.4: switching to a
// different version stamps now(); replaying the same version (idempotent
// re-publish or publish-after-restore of the same pointer) keeps the existing
// timestamp.
func SetPublishedPointerTx(ctx context.Context, tx pgx.Tx, row LifecycleRow, versionID string) (LifecycleRow, error) {
	if row.PublicationStatus != PublicationDraft && row.PublicationStatus != PublicationPublished {
		return row, ErrInvalidTransition
	}
	commandTag, err := tx.Exec(ctx, `
		UPDATE asset.assets a
		SET publication_status = 'published',
		    current_published_version_id = $3::uuid,
		    published_at = CASE
		        WHEN a.current_published_version_id IS DISTINCT FROM $3::uuid THEN now()
		        ELSE COALESCE(a.published_at, now())
		    END,
		    revision = a.revision + 1,
		    updated_at = now()
		WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
		  AND EXISTS (
		    SELECT 1 FROM asset.asset_versions v
		    WHERE v.organization_id = a.organization_id AND v.asset_id = a.id AND v.id = $3::uuid
		  )
	`, row.OrganizationID, row.ID, versionID)
	if err != nil {
		return row, fmt.Errorf("publish asset pointer: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return row, ErrConflict
	}
	next := row
	status := PublicationPublished
	next.PublicationStatus = status
	versionCopy := versionID
	previous := ""
	if row.CurrentPublishedVersionID != nil {
		previous = *row.CurrentPublishedVersionID
	}
	next.CurrentPublishedVersionID = &versionCopy
	next.Revision++
	if previous != versionID || row.PublishedAt == nil {
		now := time.Now().UTC()
		next.PublishedAt = &now
	}
	return next, nil
}

// ClearPublishedPointerTx archives the asset: status archived, both the
// published pointer and published_at cleared.
func ClearPublishedPointerTx(ctx context.Context, tx pgx.Tx, row LifecycleRow) (LifecycleRow, error) {
	if row.PublicationStatus != PublicationDraft && row.PublicationStatus != PublicationPublished {
		return row, ErrInvalidTransition
	}
	commandTag, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET publication_status = 'archived',
		    current_published_version_id = NULL,
		    published_at = NULL,
		    revision = revision + 1,
		    updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, row.OrganizationID, row.ID)
	if err != nil {
		return row, fmt.Errorf("archive asset: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return row, ErrConflict
	}
	next := row
	next.PublicationStatus = PublicationArchived
	next.CurrentPublishedVersionID = nil
	next.PublishedAt = nil
	next.Revision++
	return next, nil
}

// RestoreToDraftTx returns an archived asset to draft without republishing.
func RestoreToDraftTx(ctx context.Context, tx pgx.Tx, row LifecycleRow) (LifecycleRow, error) {
	if row.PublicationStatus != PublicationArchived {
		return row, ErrInvalidTransition
	}
	commandTag, err := tx.Exec(ctx, `
		UPDATE asset.assets
		SET publication_status = 'draft',
		    revision = revision + 1,
		    updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, row.OrganizationID, row.ID)
	if err != nil {
		return row, fmt.Errorf("restore asset: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return row, ErrConflict
	}
	next := row
	next.PublicationStatus = PublicationDraft
	next.Revision++
	return next, nil
}

// CancelPendingRequestsTx cancels any pending PublicationRequest for the
// asset with an explicit reason. Every auto-cancel carries the same facts as a
// manual one — publication_request.cancelled event, submitter notification and
// audit row — inside the caller's transaction (doc §18-18). Returns the number
// of cancelled requests.
func CancelPendingRequestsTx(ctx context.Context, tx pgx.Tx, events *eventing.EventStore, row LifecycleRow, actor auth.Principal, reason string) (int64, error) {
	requests, err := tx.Query(ctx, `
		UPDATE asset.publication_requests
		SET status = 'cancelled',
		    cancelled_by = NULLIF($3, '')::uuid,
		    cancel_reason = $4,
		    revision = revision + 1,
		    decided_at = now()
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
		  AND status IN ('pending', 'scheduled')
		RETURNING id::text, workspace_id::text, asset_id::text, asset_version_id::text,
		          submitted_by::text, revision, cancel_reason
	`, row.OrganizationID, row.ID, actor.UserID, reason)
	if err != nil {
		return 0, fmt.Errorf("cancel pending publication requests: %w", err)
	}
	// Drain the RETURNING set before issuing any side-effect statement on the
	// same connection: pgx refuses concurrent use of one tx conn ("conn
	// busy"). The pending-only era never entered this loop; scheduled
	// requests (G4) made it reachable, surfacing the latent defect.
	type cancelledRequest struct {
		id, workspaceID, assetID, versionID, submittedBy, cancelReason string
		revision                                                       int64
	}
	cancelledRows := make([]cancelledRequest, 0, 2)
	for requests.Next() {
		var item cancelledRequest
		if err := requests.Scan(&item.id, &item.workspaceID, &item.assetID, &item.versionID, &item.submittedBy, &item.revision, &item.cancelReason); err != nil {
			requests.Close()
			return 0, fmt.Errorf("scan cancelled publication request: %w", err)
		}
		cancelledRows = append(cancelledRows, item)
	}
	if err := requests.Err(); err != nil {
		requests.Close()
		return 0, fmt.Errorf("cancel pending publication requests: %w", err)
	}
	requests.Close()
	for _, item := range cancelledRows {
		if events != nil {
			raw, err := eventing.EncodePayload(eventing.PublicationRequestPayload{
				RequestID:      item.id,
				AssetID:        item.assetID,
				AssetVersionID: item.versionID,
				WorkspaceID:    item.workspaceID,
				CancelReason:   item.cancelReason,
			})
			if err == nil {
				_, err = events.AppendTx(ctx, tx, eventing.Event{
					OrganizationID:   row.OrganizationID,
					WorkspaceID:      item.workspaceID,
					EventType:        eventing.EventPublicationCancelled,
					AggregateType:    "publication_request",
					AggregateID:      item.id,
					AggregateVersion: item.revision,
					PayloadVersion:   eventing.PayloadVersionV1,
					Actor:            eventing.ActorFromPrincipal(actor),
					Payload:          raw,
				})
			}
			if err != nil {
				return 0, fmt.Errorf("append auto-cancel event: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO content.notifications (organization_id, workspace_id, recipient_user_id, kind, payload)
			SELECT $1::uuid, $2::uuid, $3::uuid, 'publication.cancelled', $4::jsonb
			WHERE $3::uuid IS DISTINCT FROM $5::uuid
		`, row.OrganizationID, item.workspaceID, item.submittedBy, []byte(fmt.Sprintf(
			`{"request_id":%q,"asset_id":%q,"status":"cancelled","reason":%q}`, item.id, item.assetID, item.cancelReason,
		)), actor.UserID); err != nil {
			return 0, fmt.Errorf("record auto-cancel notification: %w", err)
		}
		store.AppendAuditTx(ctx, tx, store.NewAuditEntry("publication.cancel", row.OrganizationID, actor.UserID, "publication_request", item.id, map[string]any{
			"workspace_id": item.workspaceID,
			"asset_id":     item.assetID,
			"auto":         true,
			"reason":       item.cancelReason,
		}), item.workspaceID)
	}
	return int64(len(cancelledRows)), nil
}

// AppendAssetEventTx appends a domain fact with the asset revision as
// aggregate version. The envelope matches internal/eventing.Event.
func AppendAssetEventTx(ctx context.Context, tx pgx.Tx, events *eventing.EventStore, row LifecycleRow, actor auth.Principal, eventType string, payloadVersion int, payload any) error {
	if events == nil {
		return errors.New("event store is not initialized")
	}
	raw, err := eventing.EncodePayload(payload)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", eventType, err)
	}
	if _, err := events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   row.OrganizationID,
		WorkspaceID:      row.WorkspaceID,
		EventType:        eventType,
		AggregateType:    "asset",
		AggregateID:      row.ID,
		AggregateVersion: row.Revision,
		PayloadVersion:   payloadVersion,
		Actor:            eventing.ActorFromPrincipal(actor),
		Payload:          raw,
	}); err != nil {
		return err
	}
	return nil
}

// RecordAssetAuditTx writes the audit row inside the business transaction.
func RecordAssetAuditTx(ctx context.Context, tx pgx.Tx, organizationID, workspaceID string, actor auth.Principal, action, resourceID string, metadata map[string]any) {
	entry := store.NewAuditEntry(action, organizationID, actor.UserID, "asset", resourceID, metadata)
	_ = store.AppendAuditTx(ctx, tx, entry, workspaceID)
}
