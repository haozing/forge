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

// Subject kinds stored in search_sessions / query_executions.
const (
	SubjectMember = "member"
	SubjectAgent  = "agent"
)

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
