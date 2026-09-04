// Package review contract values for the single-level PublicationRequest
// aggregate fixed by phase 0. The aggregate owns publication approval; it
// never mutates AssetVersion content.
package review

// PublicationRequest statuses.
const (
	RequestPending   = "pending"
	RequestApproved  = "approved"
	RequestRejected  = "rejected"
	RequestCancelled = "cancelled"
	// RequestScheduled is an approved publication intent waiting for its
	// scheduled_at moment; the worker flips the published pointer then (G4).
	RequestScheduled = "scheduled"
)

// Cancel reasons recorded when a pending or scheduled request stops being
// actionable.
const (
	CancelUserCancelled  = "user_cancelled"
	CancelNewVersion     = "new_version"
	CancelAssetArchived  = "asset_archived"
	CancelAdminCancelled = "admin_cancelled"
	// CancelExecutionFailed ends a scheduled row whose due execution kept
	// failing past the bounded retry window (G4).
	CancelExecutionFailed = "execution_failed"
)

// ValidRequestStatus mirrors the publication_requests CHECK constraint.
func ValidRequestStatus(value string) bool {
	switch value {
	case RequestPending, RequestApproved, RequestRejected, RequestCancelled, RequestScheduled:
		return true
	default:
		return false
	}
}

// ValidCancelReason mirrors the publication_requests CHECK constraint.
func ValidCancelReason(value string) bool {
	switch value {
	case CancelUserCancelled, CancelNewVersion, CancelAssetArchived, CancelAdminCancelled, CancelExecutionFailed:
		return true
	default:
		return false
	}
}
