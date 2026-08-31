package site

import "errors"

// Sentinel errors of the site domain. Handlers map them onto the HTTP status
// contract; the service never returns HTTP concepts.
var (
	// ErrSiteNotFound reports a site id that does not exist inside the
	// (organization, workspace) scope of the caller.
	ErrSiteNotFound = errors.New("site not found")
	// ErrBindingNotFound reports a binding id that does not exist under the
	// addressed site.
	ErrBindingNotFound = errors.New("site binding not found")
	// ErrSlugInvalid reports a site slug outside ^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$.
	ErrSlugInvalid = errors.New("site slug invalid")
	// ErrPathInvalid reports a binding display_path outside
	// ^[a-z0-9][a-z0-9/-]{0,120}[a-z0-9]$.
	ErrPathInvalid = errors.New("binding display path invalid")
	// ErrBindingTargetInvalid reports an asset that fails the write-time
	// binding gate: not published, visibility above the site scope ceiling,
	// public_site channel disabled on its model policy, or inactive model.
	ErrBindingTargetInvalid = errors.New("binding target asset is not eligible")
	// ErrSiteDisabled reports a mutation attempted against a disabled site.
	ErrSiteDisabled = errors.New("site is disabled")
	// ErrConflict reports a unique-constraint loss (slug or display_path).
	ErrConflict = errors.New("site resource conflict")
	// ErrInvalidInput reports malformed enum/config input (template, scope,
	// content type, JSON configs).
	ErrInvalidInput = errors.New("site input invalid")
	// ErrForbidden collapses policy denials (unknown workspace and missing
	// action) so existence is never leaked past the handler guard.
	ErrForbidden = errors.New("site action forbidden")
)
