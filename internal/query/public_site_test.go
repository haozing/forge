package query

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"agentchunzhi/internal/access"
	"agentchunzhi/internal/store"
)

// TestNullableSubjectBindsAnonymousAsNull pins the anonymous public-site
// binding rule (migration 0009): subject_kind='public_site' visitors without a
// users row persist a NULL subject_id, member/agent subjects keep theirs.
func TestNullableSubjectBindsAnonymousAsNull(t *testing.T) {
	if nullableSubject("") != nil {
		t.Fatal("an anonymous public-site subject must bind SQL NULL")
	}
	member := "00000000-0000-4000-8000-00000000000b"
	if nullableSubject(member) != any(member) {
		t.Fatalf("member subject binding = %v, want the users id unchanged", nullableSubject(member))
	}
}

// TestSubjectKindsFitDatabaseCheck guards the subject_kind length CHECK
// (1..30) shared by search_sessions and query_executions: a new kind that
// overflows the CHECK would fail every audit insert at runtime.
func TestSubjectKindsFitDatabaseCheck(t *testing.T) {
	for _, kind := range []string{SubjectMember, SubjectAgent, SubjectPublicSite} {
		if len(kind) < 1 || len(kind) > 30 {
			t.Fatalf("subject kind %q violates the 1..30 length CHECK", kind)
		}
	}
}

// TestPublicSiteVisibilitiesTieringMatrix pins the D5' tiering: the site's
// default_content_scope is the ceiling, the verified visitor membership picks
// the tier, and the public band is always included.
func TestPublicSiteVisibilitiesTieringMatrix(t *testing.T) {
	publicOnly := []string{access.VisibilityPublic}
	organizationBand := []string{access.VisibilityOrganization, access.VisibilityPublic}
	workspaceBand := []string{access.VisibilityWorkspace, access.VisibilityOrganization, access.VisibilityPublic}

	cases := []struct {
		name        string
		ceiling     string
		orgMember   bool
		wsMember    bool
		want        []string
	}{
		{"public ceiling anonymous", access.VisibilityPublic, false, false, publicOnly},
		{"public ceiling org member", access.VisibilityPublic, true, false, publicOnly},
		{"public ceiling ws member", access.VisibilityPublic, true, true, publicOnly},

		{"organization ceiling anonymous", access.VisibilityOrganization, false, false, publicOnly},
		{"organization ceiling org member", access.VisibilityOrganization, true, false, organizationBand},
		{"organization ceiling ws member", access.VisibilityOrganization, true, true, organizationBand},

		{"workspace ceiling anonymous", access.VisibilityWorkspace, false, false, publicOnly},
		{"workspace ceiling org member", access.VisibilityWorkspace, true, false, organizationBand},
		{"workspace ceiling ws member", access.VisibilityWorkspace, true, true, workspaceBand},

		// Unknown ceiling values degrade to the public-only band (fail closed).
		{"unknown ceiling anonymous", "legacy", false, false, publicOnly},
		{"unknown ceiling ws member", "legacy", true, true, publicOnly},
	}
	for _, tc := range cases {
		got := publicSiteVisibilities(tc.ceiling, tc.orgMember, tc.wsMember)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: band = %#v, want %#v", tc.name, got, tc.want)
		}
		for index := range got {
			if got[index] != tc.want[index] {
				t.Fatalf("%s: band = %#v, want %#v", tc.name, got, tc.want)
			}
		}
		containsPublic := false
		for _, visibility := range got {
			if visibility == access.VisibilityPublic {
				containsPublic = true
			}
		}
		if !containsPublic {
			t.Fatalf("%s: band %#v must always contain the public band", tc.name, got)
		}
	}
}

// TestForPublicSiteFailsClosedWithoutStore checks the guard rails that run
// before any SQL: an uninitialized store is an error, never an open scope.
func TestForPublicSiteFailsClosedWithoutStore(t *testing.T) {
	compiler := ScopeCompiler{Store: &store.Store{}, HashSecret: "secret"}
	_, err := compiler.ForPublicSite(context.Background(), PublicSiteRef{
		OrganizationID: "00000000-0000-4000-8000-00000000000a",
		WorkspaceID:    "00000000-0000-4000-8000-000000000001",
		DefaultScope:   access.VisibilityPublic,
	}, VisitorIdentity{})
	if err == nil {
		t.Fatal("a nil pool must fail closed")
	}
}

// TestPublicSiteContentUnavailableSentinel pins the wire contract of the
// fail-closed empty-model-set sentinel: 503 site_content_unavailable.
func TestPublicSiteContentUnavailableSentinel(t *testing.T) {
	status, code := HTTPStatus(ErrPublicSiteContentUnavailable)
	if status != 503 || code != "site_content_unavailable" {
		t.Fatalf("sentinel maps to %d/%s, want 503/site_content_unavailable", status, code)
	}
}

// TestPublicSiteScopeFingerprintDiscriminatesVisitors: anonymous and
// member-identified visitors of the same site must fingerprint differently —
// subject identity is part of the canonical scope, so a cursor minted for an
// anonymous visitor cannot be replayed by a member (and vice versa).
func TestPublicSiteScopeFingerprintDiscriminatesVisitors(t *testing.T) {
	base := QueryAccessScope{
		OrganizationID:      "00000000-0000-4000-8000-00000000000a",
		SubjectKind:         SubjectPublicSite,
		Channel:             ChannelPublicSite,
		WorkspaceIDs:        []string{"00000000-0000-4000-8000-000000000001"},
		ResourceModelIDs:    []string{"00000000-0000-4000-8000-000000000002"},
		AllowedVisibilities: []string{access.VisibilityPublic},
		VersionScope:        VersionScopePublished,
	}
	anonymous := base
	member := base
	member.SubjectID = "00000000-0000-4000-8000-00000000000b"
	if computeScopeFingerprint(anonymous, "secret") == computeScopeFingerprint(member, "secret") {
		t.Fatal("anonymous and member visitor scopes must fingerprint differently")
	}
}

// TestExecuteAuditBindsExactlyScopeWorkspaces guards the audit contract the
// real-database acceptance reads: retrieval.query_execution_workspaces rows
// must mirror the compiled scope exactly — for the public-site channel that
// is precisely the site's one workspace (doc phase 5 §3.2), never an
// organization-wide expansion. Execute has exactly one workspace-binding call
// site and it passes scope.WorkspaceIDs verbatim; the audit begin (which runs
// before the scope compile) must not pre-bind any workspace list.
func TestExecuteAuditBindsExactlyScopeWorkspaces(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".", "service.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	bindCalls := regexp.MustCompile(`BindExecutionWorkspaces\((?s:[^)]*)\)`)
	matches := bindCalls.FindAllString(source, -1)
	if len(matches) != 1 {
		t.Fatalf("BindExecutionWorkspaces call sites = %d, want exactly 1", len(matches))
	}
	if !strings.Contains(matches[0], "scope.WorkspaceIDs") {
		t.Fatalf("audit binding must pass scope.WorkspaceIDs verbatim, got: %s", matches[0])
	}
	begin := regexp.MustCompile(`BeginQueryExecution\((?s:[^)]*)\)`)
	beginMatches := begin.FindAllString(source, -1)
	if len(beginMatches) != 1 {
		t.Fatalf("BeginQueryExecution call sites = %d, want exactly 1", len(beginMatches))
	}
	// The begin call tail binds no workspace list (nested parens defeat the
	// naive match above, so anchor on the literal tail).
	if !strings.Contains(source, "requestHash, normalized.Mode, nil)") {
		t.Fatal("audit begin runs before the scope compile and must not bind workspaces")
	}
}
