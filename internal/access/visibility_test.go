package access

import "testing"

func TestVisibilityContractIsClosed(t *testing.T) {
	for _, value := range AllVisibilities {
		if !Valid(value) {
			t.Fatalf("visibility %s must be valid", value)
		}
	}
	for _, legacy := range []string{"login", "private", "internal", "public_read", ""} {
		if Valid(legacy) {
			t.Fatalf("legacy visibility %q must be rejected", legacy)
		}
	}
}

func TestAllowedRequiresExplicitGrant(t *testing.T) {
	if Allowed(nil, VisibilityWorkspace) {
		t.Fatal("an empty allowed set must deny everything")
	}
	if !Allowed([]string{VisibilityOrganization, VisibilityPublic}, VisibilityPublic) {
		t.Fatal("public must be allowed when listed")
	}
	if Allowed([]string{VisibilityOrganization}, VisibilityWorkspace) {
		t.Fatal("workspace must not leak into an organization-only scope")
	}
}
