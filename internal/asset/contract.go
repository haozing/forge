// Package asset contract values fixed by phase 0. The publication status
// lives only on Asset; AssetVersion is an immutable snapshot with origin and
// confirmation as orthogonal facts. These values are mirrored by the database
// CHECK constraints and openapi.yaml — change all three together.
package asset

import "errors"

// Publication statuses stored on asset.assets.publication_status.
const (
	PublicationDraft     = "draft"
	PublicationPublished = "published"
	PublicationArchived  = "archived"
)

// Version origins: how a snapshot came into existence.
const (
	OriginHuman       = "human"
	OriginImported    = "imported"
	OriginAIGenerated = "ai_generated"
	OriginAIAssisted  = "ai_assisted"
)

// Confirmation statuses: independent from origin; only a human confirm
// command produces human_confirmed, and the fact lives on the version
// snapshot with confirmed_by/confirmed_at.
const (
	ConfirmationUnconfirmed    = "unconfirmed"
	ConfirmationHumanConfirmed = "human_confirmed"
)

// Relation types on asset.asset_relations.relation_type and the phase 4
// suggestion tables; mirrored by the database CHECK constraints.
const (
	RelationRelatedTo     = "related_to"
	RelationReferences    = "references"
	RelationDerivedFrom   = "derived_from"
	RelationCites         = "cites"
	RelationContinuesFrom = "continues_from"
)

// ValidRelationType mirrors the relation_type CHECK constraint.
func ValidRelationType(value string) bool {
	switch value {
	case RelationRelatedTo, RelationReferences, RelationDerivedFrom, RelationCites, RelationContinuesFrom:
		return true
	default:
		return false
	}
}

// Suggestion kinds of the phase 4 review surface: field and summary share one
// table (asset.asset_field_suggestions), tag and relation have their own.
const (
	SuggestionKindField    = "field"
	SuggestionKindSummary  = "summary"
	SuggestionKindTag      = "tag"
	SuggestionKindRelation = "relation"
)

// Suggestion statuses shared by the three suggestion tables.
const (
	SuggestionStatusPending  = "pending"
	SuggestionStatusAccepted = "accepted"
	SuggestionStatusRejected = "rejected"
)

// ValidSuggestionKind reports whether kind addresses one of the three tables.
func ValidSuggestionKind(kind string) bool {
	switch kind {
	case SuggestionKindField, SuggestionKindSummary, SuggestionKindTag, SuggestionKindRelation:
		return true
	default:
		return false
	}
}

// Attachment scan statuses on asset.attachments.status.
const (
	AttachmentUploading = "uploading"
	AttachmentScanning  = "scanning"
	AttachmentClean     = "clean"
	AttachmentRejected  = "rejected"
	AttachmentFailed    = "failed"
)

// Publication policy modes on the ResourceModelVersion.
const (
	PublishingModeDirect   = "direct"
	PublishingModeApproval = "approval"
)

// Processing job statuses on content.processing_jobs.status.
const (
	ProcessingQueued    = "queued"
	ProcessingRunning   = "running"
	ProcessingSucceeded = "succeeded"
	ProcessingFailed    = "failed"
	ProcessingCancelled = "cancelled"
)

var (
	ErrInvalidTransition   = errors.New("invalid publication status transition")
	ErrInvalidVisibility   = errors.New("invalid visibility")
	ErrInvalidOrigin       = errors.New("invalid origin")
	ErrInvalidConfirmation = errors.New("invalid confirmation status")
)

// ValidPublicationStatus mirrors the assets CHECK constraint.
func ValidPublicationStatus(value string) bool {
	switch value {
	case PublicationDraft, PublicationPublished, PublicationArchived:
		return true
	default:
		return false
	}
}

// ValidOrigin mirrors the asset_versions CHECK constraint.
func ValidOrigin(value string) bool {
	switch value {
	case OriginHuman, OriginImported, OriginAIGenerated, OriginAIAssisted:
		return true
	default:
		return false
	}
}

// ValidConfirmation mirrors the asset_versions CHECK constraint.
func ValidConfirmation(value string) bool {
	switch value {
	case ConfirmationUnconfirmed, ConfirmationHumanConfirmed:
		return true
	default:
		return false
	}
}

// CanTransition encodes the publication state machine:
//
//	create -> draft; draft --publish--> published; draft/published --archive--> archived;
//	archived --restore--> draft. There is no archived -> published jump and no
//	transition into draft other than create/restore.
func CanTransition(from, command string) bool {
	switch command {
	case "create":
		return from == ""
	case "publish":
		return from == PublicationDraft || from == PublicationPublished
	case "archive":
		return from == PublicationDraft || from == PublicationPublished
	case "restore":
		return from == PublicationArchived
	default:
		return false
	}
}

// TransitionTarget returns the status after a legal command. Illegal
// combinations return ErrInvalidTransition.
func TransitionTarget(from, command string) (string, error) {
	if !CanTransition(from, command) {
		return "", ErrInvalidTransition
	}
	switch command {
	case "create":
		return PublicationDraft, nil
	case "publish":
		return PublicationPublished, nil
	case "archive":
		return PublicationArchived, nil
	case "restore":
		return PublicationDraft, nil
	}
	return "", ErrInvalidTransition
}
