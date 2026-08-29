// Package eventing catalog: the closed list of v1 domain fact events fixed by
// phase 0. Event names state facts that already happened — never downstream
// commands (no *_requested). Payloads carry identifiers only, never content
// bodies or secrets. Envelope and payload rules: consumers decode by
// event_name + payload_version and reject unknown versions.
package eventing

// Asset aggregate facts.
const (
	EventAssetVersionCreated    = "asset.version_created"
	EventAssetPublished         = "asset.published"
	EventAssetArchived          = "asset.archived"
	EventAssetRestored          = "asset.restored"
	EventAssetVisibilityChanged = "asset.visibility_changed"
)

// PublicationRequest aggregate facts (single-level publication review).
const (
	EventPublicationSubmitted = "publication_request.submitted"
	EventPublicationApproved  = "publication_request.approved"
	EventPublicationRejected  = "publication_request.rejected"
	EventPublicationCancelled = "publication_request.cancelled"
	EventPublicationCommented = "publication_request.commented"
)

// Tag aggregate facts.
const (
	EventTagCreated  = "tag.created"
	EventTagUpdated  = "tag.updated"
	EventTagArchived = "tag.archived"
	EventTagRestored = "tag.restored"
)

// ResourceModel aggregate facts.
const (
	EventResourceModelPolicyPublished = "resource_model.policy_published"
)

// Workspace aggregate facts.
const (
	EventWorkspaceMembershipChanged = "workspace.membership_changed"
)

// Agent access policy facts.
const (
	EventAgentAccessPolicyChanged = "agent_access_policy.changed"
)

// PublicSite binding facts.
const (
	EventSiteBindingChanged = "site.binding_changed"
)

// Identity/organization facts (extended by phase 1).
const (
	EventOrganizationUpdated             = "organization.updated"
	EventOrganizationMemberInvited       = "organization.member_invited"
	EventOrganizationMemberActivated     = "organization.member_activated"
	EventOrganizationInvitationResent    = "organization.invitation_resent"
	EventOrganizationInvitationRevoked   = "organization.invitation_revoked"
	EventOrganizationMemberRoleChanged   = "organization.member_role_changed"
	EventOrganizationMemberStatusChanged = "organization.member_status_changed"
	EventWorkspaceCreated                = "workspace.created"
	EventWorkspaceUpdated                = "workspace.updated"
	EventWorkspaceArchived               = "workspace.archived"
	EventWorkspaceRestored               = "workspace.restored"
	EventIdentityPasswordChanged         = "identity.password_changed"
)

// PayloadVersionV1 is the payload schema version for every phase 0 event.
const PayloadVersionV1 = 1

// AssetVersionCreatedPayload is the minimal payload for asset.version_created.
type AssetVersionCreatedPayload struct {
	AssetID     string `json:"asset_id"`
	VersionID   string `json:"version_id"`
	VersionNo   int64  `json:"version_no"`
	WorkspaceID string `json:"workspace_id"`
}

// AssetPublishedPayload carries the published pointer switch.
type AssetPublishedPayload struct {
	AssetID           string `json:"asset_id"`
	VersionID         string `json:"version_id"`
	PreviousVersionID string `json:"previous_version_id,omitempty"`
	WorkspaceID       string `json:"workspace_id"`
}

// AssetArchivedPayload carries the retired published pointer.
type AssetArchivedPayload struct {
	AssetID           string `json:"asset_id"`
	PreviousVersionID string `json:"previous_version_id,omitempty"`
	WorkspaceID       string `json:"workspace_id"`
}

// AssetRestoredPayload marks the return to draft.
type AssetRestoredPayload struct {
	AssetID     string `json:"asset_id"`
	WorkspaceID string `json:"workspace_id"`
}

// AssetVisibilityChangedPayload carries the visibility re-evaluation fact.
type AssetVisibilityChangedPayload struct {
	AssetID            string `json:"asset_id"`
	Visibility         string `json:"visibility"`
	PublishedVersionID string `json:"published_version_id,omitempty"`
}

// PublicationRequestPayload is shared by submitted/approved/rejected/cancelled.
type PublicationRequestPayload struct {
	RequestID      string `json:"request_id"`
	AssetID        string `json:"asset_id"`
	AssetVersionID string `json:"asset_version_id"`
	WorkspaceID    string `json:"workspace_id"`
	CancelReason   string `json:"cancel_reason,omitempty"`
}

// PublicationCommentedPayload carries the comment fact.
type PublicationCommentedPayload struct {
	RequestID   string `json:"request_id"`
	CommentID   string `json:"comment_id"`
	WorkspaceID string `json:"workspace_id"`
}

// TagCreatedPayload / TagArchivedPayload / TagRestoredPayload share the shape.
type TagLifecyclePayload struct {
	TagID       string `json:"tag_id"`
	WorkspaceID string `json:"workspace_id"`
}

// TagUpdatedPayload adds the changed field keys.
type TagUpdatedPayload struct {
	TagID         string   `json:"tag_id"`
	WorkspaceID   string   `json:"workspace_id"`
	ChangedFields []string `json:"changed_fields"`
}

// ResourceModelPolicyPublishedPayload marks a new immutable model version.
type ResourceModelPolicyPublishedPayload struct {
	ResourceModelID string `json:"resource_model_id"`
	VersionID       string `json:"version_id"`
	WorkspaceID     string `json:"workspace_id,omitempty"`
}

// WorkspaceMembershipChangedPayload expresses one membership operation.
type WorkspaceMembershipChangedPayload struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Role        string `json:"role,omitempty"`
	OldRole     string `json:"old_role,omitempty"`
	NewRole     string `json:"new_role,omitempty"`
	Operation   string `json:"operation"` // granted / role_changed / revoked / left
}

// AgentAccessPolicyChangedPayload marks an Agent policy change.
type AgentAccessPolicyChangedPayload struct {
	PolicyID    string `json:"policy_id"`
	AgentUserID string `json:"agent_user_id"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// SiteBindingChangedPayload marks a site binding change (phase 5 consumes).
type SiteBindingChangedPayload struct {
	SiteID    string `json:"site_id"`
	AssetID   string `json:"asset_id"`
	Operation string `json:"operation"`
}

// KnownEvents maps every catalog event to its payload version. The registry
// refuses to dispatch events absent from this table.
func KnownEvents() map[string]int {
	return map[string]int{
		EventAssetVersionCreated:          PayloadVersionV1,
		EventAssetPublished:               PayloadVersionV1,
		EventAssetArchived:                PayloadVersionV1,
		EventAssetRestored:                PayloadVersionV1,
		EventAssetVisibilityChanged:       PayloadVersionV1,
		EventPublicationSubmitted:         PayloadVersionV1,
		EventPublicationApproved:          PayloadVersionV1,
		EventPublicationRejected:          PayloadVersionV1,
		EventPublicationCancelled:         PayloadVersionV1,
		EventPublicationCommented:         PayloadVersionV1,
		EventTagCreated:                   PayloadVersionV1,
		EventTagUpdated:                   PayloadVersionV1,
		EventTagArchived:                  PayloadVersionV1,
		EventTagRestored:                  PayloadVersionV1,
		EventResourceModelPolicyPublished: PayloadVersionV1,
		EventWorkspaceMembershipChanged:   PayloadVersionV1,
		EventAgentAccessPolicyChanged:     PayloadVersionV1,
		EventSiteBindingChanged:           PayloadVersionV1,
	}
}
