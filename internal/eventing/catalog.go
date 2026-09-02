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
)

// PublicSite aggregate facts (phase 5 extends the catalog with site changes;
// this is the only eventing change of P5-2).
const (
	EventSiteChanged = "site.site_changed"
)

// PublicSite binding facts.
const (
	EventSiteBindingChanged = "site.binding_changed"
)

// PublicSite comment facts (二期 §8): one landed comment; consumed by the
// delivery cache invalidator.
const (
	EventSiteCommentCreated = "site.comment_created"
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
	EventWorkspaceArchived               = "workspace.archived"
	EventWorkspaceRestored               = "workspace.restored"
	EventIdentityPasswordChanged         = "identity.password_changed"
)

// Agent processing facts (phase 4 suggestion stream).
const (
	EventAgentProcessingCompleted = "agent.processing_completed"
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

// SiteChangedPayload marks a public site configuration or lifecycle change.
// Action carries the management verb (created / updated / disabled /
// binding_created / binding_updated / binding_deleted); binding details ride
// on site.binding_changed.
type SiteChangedPayload struct {
	SiteID      string `json:"site_id"`
	WorkspaceID string `json:"workspace_id"`
	Action      string `json:"action"`
}

// SiteBindingChangedPayload marks a site binding change (phase 5 consumes).
type SiteBindingChangedPayload struct {
	SiteID    string `json:"site_id"`
	AssetID   string `json:"asset_id"`
	Operation string `json:"operation"`
}

// AgentProcessingCompletedPayload marks an agent prepare run landing its
// pending suggestion set; counts are keyed by field/summary/tag/relation.
type AgentProcessingCompletedPayload struct {
	AssetID            string         `json:"asset_id"`
	RunID              string         `json:"run_id"`
	InputVersionID     string         `json:"input_version_id"`
	ProcessingResultID string         `json:"processing_result_id"`
	Counts             map[string]int `json:"counts"`
}

// SiteCommentCreatedPayload marks one comment landing on a site asset.
type SiteCommentCreatedPayload struct {
	SiteID  string `json:"site_id"`
	AssetID string `json:"asset_id"`
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
		EventTagCreated:                   PayloadVersionV1,
		EventTagUpdated:                   PayloadVersionV1,
		EventTagArchived:                  PayloadVersionV1,
		EventTagRestored:                  PayloadVersionV1,
		EventResourceModelPolicyPublished: PayloadVersionV1,
		EventWorkspaceMembershipChanged:   PayloadVersionV1,
		EventSiteChanged:                  PayloadVersionV1,
		EventSiteBindingChanged:           PayloadVersionV1,
		EventSiteCommentCreated:           PayloadVersionV1,
		EventAgentProcessingCompleted:     PayloadVersionV1,
	}
}
