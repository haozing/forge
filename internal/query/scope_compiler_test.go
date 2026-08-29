package query

import (
	"testing"
)

func TestEmptyScopeIsRejected(t *testing.T) {
	// An empty workspace/model/visibility list means "no permission" — never
	// "unrestricted" (doc §5.1).
	cases := []QueryAccessScope{
		{},
		{WorkspaceIDs: []string{"00000000-0000-4000-8000-000000000001"}},
		{WorkspaceIDs: []string{"00000000-0000-4000-8000-000000000001"}, ResourceModelIDs: []string{"00000000-0000-4000-8000-000000000002"}},
		{WorkspaceIDs: []string{"00000000-0000-4000-8000-000000000001"}, AllowedVisibilities: []string{"public"}},
	}
	for index, scope := range cases {
		if !scope.Empty() {
			t.Fatalf("scope %d must be empty", index)
		}
	}
	full := QueryAccessScope{
		WorkspaceIDs:        []string{"00000000-0000-4000-8000-000000000001"},
		ResourceModelIDs:    []string{"00000000-0000-4000-8000-000000000002"},
		AllowedVisibilities: []string{"organization", "public"},
	}
	if full.Empty() {
		t.Fatal("populated scope must not be empty")
	}
}

func TestScopeFingerprintIsDeterministicAndDiscriminating(t *testing.T) {
	base := QueryAccessScope{
		OrganizationID:      "00000000-0000-4000-8000-00000000000a",
		SubjectKind:         SubjectMember,
		SubjectID:           "00000000-0000-4000-8000-00000000000b",
		Channel:             ChannelWorkspace,
		WorkspaceIDs:        []string{"00000000-0000-4000-8000-000000000001"},
		ResourceModelIDs:    []string{"00000000-0000-4000-8000-000000000002"},
		AllowedVisibilities: organizationVisibilities,
		VersionScope:        VersionScopePublished,
	}
	first := computeScopeFingerprint(base, "secret")
	second := computeScopeFingerprint(base, "secret")
	if first != second {
		t.Fatal("the same canonical scope must fingerprint identically")
	}
	wider := base
	wider.WorkspaceIDs = []string{
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000003",
	}
	if computeScopeFingerprint(wider, "secret") == first {
		t.Fatal("a different workspace set must change the fingerprint")
	}
	if computeScopeFingerprint(base, "other-secret") == first {
		t.Fatal("the fingerprint must be secret-keyed")
	}
	// Subject identity is part of the canonical scope: a cursor stolen by
	// another subject would fail downstream request binding.
	other := base
	other.SubjectID = "00000000-0000-4000-8000-00000000000c"
	if computeScopeFingerprint(other, "secret") == first {
		t.Fatal("subject identity must change the fingerprint")
	}
}

func TestBuildPlanQueryModeNotEnabled(t *testing.T) {
	scope := QueryAccessScope{
		Channel:          ChannelWorkspace,
		WorkspaceIDs:     []string{"00000000-0000-4000-8000-000000000001"},
		ResourceModelIDs: []string{"00000000-0000-4000-8000-000000000002"},
	}
	requested := []string{"00000000-0000-4000-8000-000000000002"}
	policies := map[string]modelPolicy{
		"00000000-0000-4000-8000-000000000002": {
			ModelID: "00000000-0000-4000-8000-000000000002",
			ChannelEnabled: true,
		},
	}
	// Explicitly requested model with both retrieval modes disabled.
	if _, err := buildPlan(ModeFulltext, scope, requested, policies); err != ErrQueryModeNotEnabled {
		t.Fatalf("fulltext: got %v, want ErrQueryModeNotEnabled", err)
	}
	if _, err := buildPlan(ModeSemantic, scope, requested, policies); err != ErrQueryModeNotEnabled {
		t.Fatalf("semantic: got %v, want ErrQueryModeNotEnabled", err)
	}
	if _, err := buildPlan(ModeHybrid, scope, requested, policies); err != ErrQueryModeNotEnabled {
		t.Fatalf("hybrid: got %v, want ErrQueryModeNotEnabled", err)
	}
	// Structured is unaffected by retrieval-mode switches.
	plan, err := buildPlan(ModeStructured, scope, requested, policies)
	if err != nil || plan.ExecutedMode != ModeStructured {
		t.Fatalf("structured: plan=%#v err=%v", plan, err)
	}
}

func TestBuildPlanSingleSanctionedBranchIsNotDegraded(t *testing.T) {
	scope := QueryAccessScope{
		Channel:          ChannelWorkspace,
		ResourceModelIDs: []string{"00000000-0000-4000-8000-000000000002"},
	}
	model := "00000000-0000-4000-8000-000000000002"
	policies := map[string]modelPolicy{
		model: {ModelID: model, ChannelEnabled: true, SemanticEnabled: true},
	}
	plan, err := buildPlan(ModeHybrid, scope, nil, policies)
	if err != nil {
		t.Fatalf("hybrid: %v", err)
	}
	if plan.ExecutedMode != ModeSemantic {
		t.Fatalf("executed = %s, want semantic (the only sanctioned branch)", plan.ExecutedMode)
	}
	if len(plan.DegradationReasons) != 0 {
		t.Fatalf("policy-sanctioned branch must not degrade: %v", plan.DegradationReasons)
	}
}

func TestBuildPlanExcludesDisabledModelsForMultiModelQueries(t *testing.T) {
	scope := QueryAccessScope{
		Channel: ChannelWorkspace,
		ResourceModelIDs: []string{
			"00000000-0000-4000-8000-000000000002",
			"00000000-0000-4000-8000-000000000003",
		},
	}
	policies := map[string]modelPolicy{
		"00000000-0000-4000-8000-000000000002": {ModelID: "00000000-0000-4000-8000-000000000002", ChannelEnabled: true, FulltextEnabled: true},
		"00000000-0000-4000-8000-000000000003": {ModelID: "00000000-0000-4000-8000-000000000003", ChannelEnabled: true},
	}
	plan, err := buildPlan(ModeFulltext, scope, nil, policies)
	if err != nil {
		t.Fatalf("fulltext: %v", err)
	}
	if len(plan.FulltextModels) != 1 || plan.FulltextModels[0] != "00000000-0000-4000-8000-000000000002" {
		t.Fatalf("fulltext models = %#v", plan.FulltextModels)
	}
}

func TestBuildPlanChannelDisabledModelsAreExcluded(t *testing.T) {
	scope := QueryAccessScope{
		Channel: ChannelWorkspace,
		ResourceModelIDs: []string{
			"00000000-0000-4000-8000-000000000002",
			"00000000-0000-4000-8000-000000000003",
		},
	}
	policies := map[string]modelPolicy{
		"00000000-0000-4000-8000-000000000002": {ModelID: "00000000-0000-4000-8000-000000000002", ChannelEnabled: false, FulltextEnabled: true},
		"00000000-0000-4000-8000-000000000003": {ModelID: "00000000-0000-4000-8000-000000000003", ChannelEnabled: true, FulltextEnabled: true},
	}
	plan, err := buildPlan(ModeFulltext, scope, nil, policies)
	if err != nil {
		t.Fatalf("fulltext: %v", err)
	}
	if len(plan.ChannelModels) != 1 || plan.ChannelModels[0] != "00000000-0000-4000-8000-000000000003" {
		t.Fatalf("channel models = %#v", plan.ChannelModels)
	}
}

func TestBuildPlanRequestedModelsOutsideScopeStaySilent(t *testing.T) {
	scope := QueryAccessScope{
		Channel:          ChannelWorkspace,
		ResourceModelIDs: []string{"00000000-0000-4000-8000-000000000002"},
	}
	// A model the caller cannot even see must not leak policy state through a
	// 422 — the plan simply recalls nothing.
	policies := map[string]modelPolicy{}
	plan, err := buildPlan(ModeFulltext, scope, []string{"00000000-0000-4000-8000-000000000099"}, policies)
	if err != nil {
		t.Fatalf("out-of-scope request must stay silent, got %v", err)
	}
	if len(plan.FulltextModels) != 0 {
		t.Fatalf("expected no recall models, got %#v", plan.FulltextModels)
	}
}

func TestEscapePGroongaQueryNeutralizesOperators(t *testing.T) {
	escaped := EscapePGroongaQuery(`hello (OR) "phrase" +boost -not*`)
	if !escapedContains(escaped, `\(`) || !escapedContains(escaped, `\"`) || !escapedContains(escaped, `\*`) {
		t.Fatalf("operator characters not escaped: %q", escaped)
	}
	// The plain text survives escaping.
	if !escapedContains(escaped, "hello") || !escapedContains(escaped, "phrase") {
		t.Fatalf("escaped query lost the text: %q", escaped)
	}
}

func escapedContains(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}
