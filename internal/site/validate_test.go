package site

// validate_test.go — the pure rules of the site domain without a database:
// slug/display-path formats, the D5' scope ceiling, the binding gate, config
// shape, If-Match revision semantics and the display_published_at fallback.

import (
	"encoding/json"
	"testing"
	"time"
)

func TestValidSlug(t *testing.T) {
	valid := []string{"abc", "my-blog", "a1-b2-c3", "news-2026"}
	for _, value := range valid {
		if !ValidSlug(value) {
			t.Fatalf("slug %q must be valid", value)
		}
	}
	invalid := []string{
		"", "ab", // too short (3..64)
		"-lead", "trail-", // dashes at the edges
		"UPPER", "has space", "under_score", "dot.name", "slash/x", "中文",
	}
	for _, value := range invalid {
		if ValidSlug(value) {
			t.Fatalf("slug %q must be invalid", value)
		}
	}
	sixtyFour := "a" + repeat("b", 62) + "c"
	if !ValidSlug(sixtyFour) {
		t.Fatal("64-char slug must be valid")
	}
	if ValidSlug(sixtyFour + "d") {
		t.Fatal("65-char slug must be invalid")
	}
}

func repeat(value string, count int) string {
	out := ""
	for i := 0; i < count; i++ {
		out += value
	}
	return out
}

func TestValidDisplayPath(t *testing.T) {
	valid := []string{"ab", "posts", "posts/hello-world", "a/b/c", "guides/2026/launch"}
	for _, value := range valid {
		if !ValidDisplayPath(value) {
			t.Fatalf("display path %q must be valid", value)
		}
	}
	invalid := []string{
		"", "a", // too short (2..122)
		"/lead", "trail/", "-lead", "trail-", // edges must be alphanumeric
		"UPPER", "has space", "bad?query", "...", "中文",
	}
	for _, value := range invalid {
		if ValidDisplayPath(value) {
			t.Fatalf("display path %q must be invalid", value)
		}
	}
}

func TestScopeCeiling(t *testing.T) {
	// The D5' upper sets: public < organization < workspace.
	cases := map[string][]string{
		ScopePublic:       {"public"},
		ScopeOrganization: {"public", "organization"},
		ScopeWorkspace:    {"public", "organization", "workspace"},
	}
	for scope, want := range cases {
		got := ScopeCeiling(scope)
		if len(got) != len(want) {
			t.Fatalf("scope %q ceiling = %v, want %v", scope, got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("scope %q ceiling = %v, want %v", scope, got, want)
			}
		}
	}
	if got := ScopeCeiling("unknown"); len(got) != 0 {
		t.Fatalf("unknown scope must fail closed, got %v", got)
	}
	// Each returned set is a fresh slice: mutating one result must not leak
	// into the next call.
	public := ScopeCeiling(ScopePublic)
	public[0] = "workspace"
	if ScopeCeiling(ScopePublic)[0] != "public" {
		t.Fatal("scope ceiling slices must not share state between calls")
	}
}

func TestBindingTargetEligible(t *testing.T) {
	published := func(visibility string) BindingTargetFacts {
		return BindingTargetFacts{
			Visibility:          visibility,
			PublicationStatus:   "published",
			HasPublishedVersion: true,
			ModelActive:         true,
			PublicSiteChannel:   true,
		}
	}
	// Visibility bands inside the ceiling are bindable.
	if !BindingTargetEligible(ScopeWorkspace, published("workspace")) {
		t.Fatal("workspace-band asset must bind to a workspace-scope site")
	}
	if !BindingTargetEligible(ScopeOrganization, published("organization")) {
		t.Fatal("organization-band asset must bind to an organization-scope site")
	}
	// The core leak-prevention rule: the band above the ceiling is rejected.
	if BindingTargetEligible(ScopePublic, published("organization")) {
		t.Fatal("organization asset must not bind to a public-scope site")
	}
	if BindingTargetEligible(ScopeOrganization, published("workspace")) {
		t.Fatal("workspace asset must not bind to an organization-scope site")
	}
	// Draft or archived assets never bind.
	draft := published("public")
	draft.PublicationStatus = "draft"
	if BindingTargetEligible(ScopeWorkspace, draft) {
		t.Fatal("draft asset must not bind")
	}
	// Channel disabled on the bound model policy is rejected.
	noChannel := published("public")
	noChannel.PublicSiteChannel = false
	if BindingTargetEligible(ScopeWorkspace, noChannel) {
		t.Fatal("asset with public_site channel disabled must not bind")
	}
	// Inactive model head is rejected.
	inactive := published("public")
	inactive.ModelActive = false
	if BindingTargetEligible(ScopeWorkspace, inactive) {
		t.Fatal("asset on an inactive model must not bind")
	}
	// Missing published pointer is rejected even with status published.
	noPointer := published("public")
	noPointer.HasPublishedVersion = false
	if BindingTargetEligible(ScopeWorkspace, noPointer) {
		t.Fatal("asset without a published pointer must not bind")
	}
	// Unknown site scope fails closed.
	if BindingTargetEligible("galactic", published("public")) {
		t.Fatal("unknown scope must fail closed")
	}
}

func TestValidConfigObject(t *testing.T) {
	if !validConfigObject(nil) {
		t.Fatal("nil config must default cleanly")
	}
	if !validConfigObject(json.RawMessage("")) {
		t.Fatal("empty config must default cleanly")
	}
	if !validConfigObject(json.RawMessage(`{"hero":{"title":"Hi"}}`)) {
		t.Fatal("JSON object config must be valid")
	}
	if validConfigObject(json.RawMessage(`[1,2,3]`)) {
		t.Fatal("JSON array config must be invalid")
	}
	if validConfigObject(json.RawMessage(`"scalar"`)) {
		t.Fatal("scalar config must be invalid")
	}
	if validConfigObject(json.RawMessage(`{"broken":`)) {
		t.Fatal("malformed JSON config must be invalid")
	}
}

func TestRevisionMatchesIfMatchSemantics(t *testing.T) {
	const current = int64(7)
	// Empty expected revision skips the check (header absent).
	if !revisionMatches(current, "") {
		t.Fatal("empty expected revision must skip")
	}
	// The "*" wildcard only demands existence.
	if !revisionMatches(current, "*") {
		t.Fatal("wildcard must skip")
	}
	if !revisionMatches(current, "7") {
		t.Fatal("matching revision must pass")
	}
	if revisionMatches(current, "6") {
		t.Fatal("stale revision must fail with a conflict")
	}
	if revisionMatches(current, "8") {
		t.Fatal("future revision must fail with a conflict")
	}
}

func TestResolveDisplayPublishedAtPrefersBindingMirror(t *testing.T) {
	bindingAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	assetAt := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	if got := ResolveDisplayPublishedAt(&bindingAt, &assetAt); got != &bindingAt {
		t.Fatal("binding mirror must win over the asset published_at")
	}
	if got := ResolveDisplayPublishedAt(nil, &assetAt); got != &assetAt {
		t.Fatal("asset published_at must be the fallback")
	}
	if got := ResolveDisplayPublishedAt(nil, nil); got != nil {
		t.Fatal("both nil must resolve to nil")
	}
}
