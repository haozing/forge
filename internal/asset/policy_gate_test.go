package asset

import (
	"errors"
	"testing"
)

// TestDecodePublishingPolicyOmittedBlockIsDirect pins the intentional
// fail-open default: a custom model version without a publishing section
// decodes as direct mode with both gates and required fields disabled.
func TestDecodePublishingPolicyOmittedBlockIsDirect(t *testing.T) {
	for name, raw := range map[string][]byte{
		"nil policy":       nil,
		"empty object":     {},
		"empty policy":     []byte(`{}`),
		"other sections":   []byte(`{"visibility":{"default":"workspace","allowed":["workspace"]}}`),
		"empty publishing": []byte(`{"publishing":{}}`),
	} {
		policy, err := decodePublishingPolicy(raw)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", name, err)
		}
		if policy.Mode != PublishingModeDirect {
			t.Fatalf("%s: mode = %q, want direct", name, policy.Mode)
		}
		if policy.RequireCleanAttachments || policy.RequireHumanConfirmation || len(policy.RequiredFields) > 0 {
			t.Fatalf("%s: gates must default to disabled, got %+v", name, policy)
		}
	}
	policy, err := decodePublishingPolicy([]byte(`{"publishing":{"mode":"approval","required_fields":["sku"],"require_clean_attachments":true,"require_human_confirmation":true}}`))
	if err != nil {
		t.Fatalf("decode approval policy: %v", err)
	}
	if policy.Mode != PublishingModeApproval || !policy.RequireCleanAttachments || !policy.RequireHumanConfirmation {
		t.Fatalf("approval policy decoded wrong: %+v", policy)
	}
	if len(policy.RequiredFields) != 1 || policy.RequiredFields[0] != "sku" {
		t.Fatalf("required fields decoded wrong: %+v", policy.RequiredFields)
	}
}

// TestPublishingPolicyGateRequiredFields covers the required_fields gate:
// every listed field must exist with a non-empty value (trimmed strings,
// non-zero-length arrays and objects; any non-nil scalar counts).
func TestPublishingPolicyGateRequiredFields(t *testing.T) {
	policy := PublishingPolicy{Mode: PublishingModeDirect, RequiredFields: []string{"sku", "region"}}
	satisfied := publishGateFacts{Fields: map[string]any{
		"sku":    "ABC-1",
		"region": map[string]any{"code": "cn"},
	}}
	if err := policy.gate(satisfied); err != nil {
		t.Fatalf("present required fields must pass, got %v", err)
	}
	scalars := publishGateFacts{Fields: map[string]any{"sku": 42, "region": false}}
	if err := policy.gate(scalars); err != nil {
		t.Fatalf("non-nil scalars count as present, got %v", err)
	}
	for name, fields := range map[string]map[string]any{
		"missing field": {"region": "cn"},
		"null value":    {"sku": nil, "region": "cn"},
		"blank string":  {"sku": "  ", "region": "cn"},
		"empty array":   {"sku": []any{}, "region": "cn"},
		"empty object":  {"sku": map[string]any{}, "region": "cn"},
		"nil fields":    nil,
	} {
		if err := policy.gate(publishGateFacts{Fields: fields}); !errors.Is(err, ErrRequiredFieldMissing) {
			t.Fatalf("%s: want ErrRequiredFieldMissing, got %v", name, err)
		}
	}
	// Without required fields the gate stays silent even on empty content.
	empty := PublishingPolicy{Mode: PublishingModeDirect}
	if err := empty.gate(publishGateFacts{Fields: map[string]any{}}); err != nil {
		t.Fatalf("no required fields must not gate, got %v", err)
	}
	// The sentinel stays distinct from the other publish gates.
	if errors.Is(ErrRequiredFieldMissing, ErrConfirmationRequired) || errors.Is(ErrRequiredFieldMissing, ErrAttachmentNotClean) {
		t.Fatal("required-field sentinel must stay distinct from the other gates")
	}
}

// TestMemberAssetVisibilityHonorsPolicyDefault covers the empty-request
// resolution: the model policy's visibility.default wins when present,
// otherwise the contract default (workspace).
func TestMemberAssetVisibilityHonorsPolicyDefault(t *testing.T) {
	cases := []struct {
		name     string
		policy   []byte
		want     string
		wantFail bool
	}{
		{"no policy", nil, "workspace", false},
		{"policy without default", []byte(`{"visibility":{"allowed":["workspace","organization"]}}`), "workspace", false},
		{"organization default", []byte(`{"visibility":{"default":"organization","allowed":["workspace","organization"]}}`), "organization", false},
		{"public default", []byte(`{"visibility":{"default":"public","allowed":["public"]}}`), "public", false},
		{"invalid default falls back", []byte(`{"visibility":{"default":"login"}}`), "workspace", false},
	}
	for _, tc := range cases {
		got, err := memberAssetVisibility(tc.policy, "")
		if tc.wantFail {
			if err == nil {
				t.Fatalf("%s: expected failure", tc.name)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %q err=%v, want %q", tc.name, got, err, tc.want)
		}
	}
	// Explicit requests keep the allow-list narrowing from the previous tests.
	narrow := []byte(`{"visibility":{"default":"workspace","allowed":["workspace"]}}`)
	if _, err := memberAssetVisibility(narrow, "public"); !errors.Is(err, ErrInvalidVisibility) {
		t.Fatalf("explicit request outside allowed set must fail, got %v", err)
	}
	if got, err := memberAssetVisibility(narrow, "workspace"); err != nil || got != "workspace" {
		t.Fatalf("explicit allowed request must pass, got %q err=%v", got, err)
	}
}

// TestIfMatchAnyRevisionWildcard covers the If-Match "*" parsing used by
// LoadDraftTx: the wildcard (bare or ETag-quoted) skips the revision equality
// check; every other token keeps it.
func TestIfMatchAnyRevisionWildcard(t *testing.T) {
	for _, value := range []string{"*", `"*"`} {
		if !ifMatchAnyRevision(value) {
			t.Fatalf("If-Match %q must be treated as the existence-only wildcard", value)
		}
	}
	for _, value := range []string{"", "3", `"3"`, "**", " * "} {
		if ifMatchAnyRevision(value) {
			t.Fatalf("If-Match %q must not be treated as the wildcard", value)
		}
	}
}
