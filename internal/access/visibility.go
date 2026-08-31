// Package access holds cross-domain read-boundary semantics: content
// visibility. It must stay free of domain imports so asset, query, site and
// authorization code can share one definition.
package access

// Visibility values fixed by the contract. The legacy values
// login/private/internal are retired: login is an authentication method,
// internal overlapped workspace, and private has no shared ownership model.
const (
	VisibilityWorkspace    = "workspace"
	VisibilityOrganization = "organization"
	VisibilityPublic       = "public"
)

// AllVisibilities is the closed set; the database CHECK mirrors it.
var AllVisibilities = []string{
	VisibilityWorkspace,
	VisibilityOrganization,
	VisibilityPublic,
}

// Valid reports whether value is one of the three visibilities.
func Valid(value string) bool {
	switch value {
	case VisibilityWorkspace, VisibilityOrganization, VisibilityPublic:
		return true
	default:
		return false
	}
}

// Allowed evaluates whether a visibility is inside an allowed set. An empty
// allowed set denies everything: the caller must not fall back to legacy
// defaults.
func Allowed(allowed []string, value string) bool {
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}
