package organization

import "errors"

var (
	ErrNotFound            = errors.New("organization resource not found")
	ErrInvalidInput        = errors.New("invalid organization input")
	ErrForbidden           = errors.New("organization action forbidden")
	ErrConflict            = errors.New("organization state conflict")
	ErrLastOrgAdmin        = errors.New("last organization admin required")
	ErrLastWorkspaceAdmin  = errors.New("last workspace admin required")
	ErrEmailUnavailable    = errors.New("email already registered")
	ErrInvitationExists    = errors.New("pending invitation already exists")
	ErrInvitationInvalid   = errors.New("invitation invalid")
	ErrInvitationExpired   = errors.New("invitation expired")
	ErrInvitationConsumed  = errors.New("invitation already accepted")
	ErrWorkspaceArchived   = errors.New("workspace is archived")
	ErrMembershipExists    = errors.New("membership already exists")
	ErrSelfMembershipLeave = errors.New("use the leave command for your own membership")
)
