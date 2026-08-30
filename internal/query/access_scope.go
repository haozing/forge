package query

import (
	"crypto/hmac"
	"crypto/sha256"
	"sort"
	"strconv"
	"strings"

	"agentchunzhi/internal/access"
)

// QueryChannel is the transport that produced the scope (doc §5.1). The
// public_site compiler arrives with phase 5.
type QueryChannel string

const (
	ChannelWorkspace  QueryChannel = "workspace"
	ChannelAgent      QueryChannel = "agent"
	ChannelOpenAPI    QueryChannel = "open_api"
	ChannelPublicSite QueryChannel = "public_site"
)

// VersionScopePublished is the only phase 3 version scope.
const VersionScopePublished = "published"

// Subject kinds stored in search_sessions / query_executions. public_site
// marks the phase 5 public-site channel: its visitors are anonymous unless a
// member session identifies them, so audit rows may bind a NULL subject_id
// (migration 0009 made the column nullable for exactly this case).
const (
	SubjectMember     = "member"
	SubjectAgent      = "agent"
	SubjectPublicSite = "public_site"
)

// UserTypePublicSite is the auth.Principal.UserType marker of a public-site
// visitor principal built by PublicSiteQuery. It never reaches
// identity.users (whose CHECK only accepts member/agent); it only routes the
// audit subject_kind and the anonymous-subject binding of Task P5-1.
const UserTypePublicSite = "public_site"

// QueryAccessScope is the only authority a repository may consult. Empty ID
// slices mean "no permission", never "unrestricted" (doc §5.1).
type QueryAccessScope struct {
	OrganizationID      string
	SubjectKind         string
	SubjectID           string
	Channel             QueryChannel
	WorkspaceIDs        []string
	ResourceModelIDs    []string
	AllowedVisibilities []string
	VersionScope        string
	PolicyRevision      int64
	ScopeFingerprint    [32]byte
}

// Empty reports a scope that grants nothing: the caller must be rejected
// before any SQL runs.
func (s QueryAccessScope) Empty() bool {
	return len(s.WorkspaceIDs) == 0 || len(s.ResourceModelIDs) == 0 || len(s.AllowedVisibilities) == 0
}

// computeScopeFingerprint is the HMAC of the canonical scope under the query
// hash secret. It deliberately excludes role display names and credentials.
func computeScopeFingerprint(scope QueryAccessScope, secret string) [32]byte {
	if secret == "" {
		warnFallbackSecret("QUERY_HASH_SECRET", "scope fingerprint")
		secret = "agentchunzhi-query-hash"
	}
	canonical := strings.Join([]string{
		scope.OrganizationID,
		scope.SubjectKind,
		scope.SubjectID,
		string(scope.Channel),
		strings.Join(scope.WorkspaceIDs, ","),
		strings.Join(scope.ResourceModelIDs, ","),
		strings.Join(scope.AllowedVisibilities, ","),
		scope.VersionScope,
		strconv.FormatInt(scope.PolicyRevision, 10),
	}, "\x00")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical))
	var fingerprint [32]byte
	copy(fingerprint[:], mac.Sum(nil))
	return fingerprint
}

// normalizeScopeIDs sorts and de-duplicates every ID list (doc §5.1: empty
// slice means no permission).
func normalizeScopeIDs(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// publishedVisibilities is the visibility set of member scopes: workspace
// members read everything inside their workspace, organization members only
// the published organization/public band (doc §5.2/§5.3).
var publishedVisibilities = []string{
	access.VisibilityWorkspace,
	access.VisibilityOrganization,
	access.VisibilityPublic,
}

// organizationVisibilities excludes workspace-private content.
var organizationVisibilities = []string{
	access.VisibilityOrganization,
	access.VisibilityPublic,
}

// publicSiteVisibilities resolves the visibility band of a public-site visitor
// (doc phase 5 D5'). The site's default_content_scope is the exposure ceiling;
// the verified visitor membership picks the actual tier:
//
//	public ceiling       → [public] for everyone;
//	organization ceiling → [organization, public] for same-organization
//	                       active members, [public] otherwise;
//	workspace ceiling    → [workspace, organization, public] for active
//	                       members of the site workspace, the organization
//	                       tier for same-organization members, [public] for
//	                       anonymous visitors.
//
// The result always contains the public band. An unknown ceiling degrades to
// the public-only band (fail closed). Membership flags must be established by
// the compiler's membership SQL beforehand; this function is pure so the
// tiering matrix stays unit-testable.
func publicSiteVisibilities(defaultScope string, organizationMember, workspaceMember bool) []string {
	switch defaultScope {
	case access.VisibilityWorkspace:
		switch {
		case workspaceMember:
			return publishedVisibilities
		case organizationMember:
			return organizationVisibilities
		}
	case access.VisibilityOrganization:
		if organizationMember {
			return organizationVisibilities
		}
	}
	return []string{access.VisibilityPublic}
}
