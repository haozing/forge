package workspace

import "regexp"

// invitationEmailPattern is the deliberately simple RFC5322-style shape used
// during development: one @, a non-empty local part and a dotted domain with a
// two-or-more letter TLD. Invitations are identifiers for humans, so we only
// need to reject obvious garbage such as "not-an-email".
var invitationEmailPattern = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}$`)

// ValidEmail reports whether value looks like an address an invitation can be
// delivered to. Input is expected to be lowercased and trimmed by callers.
func ValidEmail(value string) bool {
	if len(value) < 3 || len(value) > 254 {
		return false
	}
	return invitationEmailPattern.MatchString(value)
}
