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
)

// Cancel reasons recorded when a pending request stops being actionable.
const (
	CancelUserCancelled  = "user_cancelled"
	CancelNewVersion     = "new_version"
	CancelAssetArchived  = "asset_archived"
	CancelAdminCancelled = "admin_cancelled"
)

// ValidRequestStatus mirrors the publication_requests CHECK constraint.
func ValidRequestStatus(value string) bool {
	switch value {
	case RequestPending, RequestApproved, RequestRejected, RequestCancelled:
		return true
	default:
		return false
	}
}

// ValidCancelReason mirrors the publication_requests CHECK constraint.
func ValidCancelReason(value string) bool {
	switch value {
	case CancelUserCancelled, CancelNewVersion, CancelAssetArchived, CancelAdminCancelled:
		return true
	default:
		return false
	}
}
