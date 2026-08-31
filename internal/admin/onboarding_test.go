package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeriveOnboardingOperationsDeniesEverythingWithoutPolicy(t *testing.T) {
	operations := deriveOnboardingOperations([]string{"query.read", "asset.create", "asset.edit", "asset.publish", "asset.archive", "reference.read", "agent.run"}, nil)
	if len(operations) != 9 {
		t.Fatalf("operation catalog should annotate 9 operations, got %d", len(operations))
	}
	for _, operation := range operations {
		if operation.Allowed {
			t.Fatalf("empty policy must deny %q", operation.Operation)
		}
	}
}

func TestDeriveOnboardingOperationsAnnotatesCapabilityPlusPolicyMatrix(t *testing.T) {
	capabilities := []string{"query.read", "asset.create", "asset.edit", "asset.publish", "reference.read"}
	models := []AgentOnboardingResourceModel{
		{ID: "m-1", Name: "Docs", Actions: []string{"read"}},
	}
	got := map[string]bool{}
	for _, operation := range deriveOnboardingOperations(capabilities, models) {
		got[operation.Operation] = operation.Allowed
	}
	expected := map[string]bool{
		"query":                true,
		"assets.create":        false, // capability present, policy lacks create
		"assets.update":        false, // policy lacks edit
		"assets.publish":       false, // policy lacks publish
		"assets.archive":       false, // no asset.archive capability
		"references":           true,
		"attachments.download": true,
		"tasks":                false, // agent.run capability missing
		"automation.callback":  true,
	}
	for operation, want := range expected {
		if got[operation] != want {
			t.Fatalf("operation %q allowed=%v, want %v", operation, got[operation], want)
		}
	}
}

func TestDeriveOnboardingOperationsAllowsFullGrant(t *testing.T) {
	capabilities := []string{"query.read", "asset.create", "asset.edit", "asset.publish", "asset.archive", "reference.read", "agent.run"}
	models := []AgentOnboardingResourceModel{{ID: "m-1", Name: "Docs", Actions: []string{"read", "create", "edit", "publish", "archive"}}}
	for _, operation := range deriveOnboardingOperations(capabilities, models) {
		if !operation.Allowed {
			t.Fatalf("fully granted agent should allow %q", operation.Operation)
		}
	}
}

func TestMergeOnboardingPolicyRowsUnionsDuplicateGrants(t *testing.T) {
	rows := []onboardingPolicyRow{
		{ID: "model-b", Name: "Beta", Actions: []string{"read"}},
		{ID: "model-a", Name: "Alpha", Actions: []string{"publish", "edit"}},
		{ID: "model-a", Name: "Alpha", Actions: []string{"edit", "read", ""}},
	}
	models := mergeOnboardingPolicyRows(rows)
	if len(models) != 2 {
		t.Fatalf("duplicate rows must fold into one entry per model, got %d", len(models))
	}
	alpha := models[0]
	if alpha.ID != "model-a" || strings.Join(alpha.Actions, ",") != "edit,publish,read" {
		t.Fatalf("alpha union sorted wrong: %#v", alpha)
	}
	beta := models[1]
	if beta.ID != "model-b" || strings.Join(beta.Actions, ",") != "read" {
		t.Fatalf("beta row wrong: %#v", beta)
	}
	if merged := mergeOnboardingPolicyRows(nil); merged == nil || len(merged) != 0 {
		t.Fatal("empty policy rows must still return an initialized slice")
	}
}

func TestBuildOnboardingSampleCurlTrimsBaseURLOnly(t *testing.T) {
	curl := buildOnboardingSampleCurl("https://kb.example.com/")
	for _, fragment := range []string{
		"https://kb.example.com/api/open/query",
		`-H "Authorization: Bearer <agent-api-key>"`,
		`"mode":"hybrid"`,
	} {
		if !strings.Contains(curl, fragment) {
			t.Fatalf("sample curl missing %s in %q", fragment, curl)
		}
	}
}

func TestAgentOnboardingJSONContractShape(t *testing.T) {
	prefix := "ak_CANJasK5f"
	pack := AgentOnboarding{
		BaseURL:           "https://kb.example.com",
		AgentUserID:       "agent-1",
		ApiKeyPrefix:      &prefix,
		Auth:              AgentOnboardingAuth{Type: "Bearer", Header: "Authorization", Format: onboardingAuthFormatTmpl},
		OpenAPIURL:        onboardingOpenAPIPath,
		RuntimeMode:       "workflow",
		WorkflowKey:       "asset_prepare",
		Capabilities:      []string{"query.read"},
		ResourceModels:    []AgentOnboardingResourceModel{},
		AllowedOperations: []AgentAllowedOperation{{Operation: "query", Allowed: true}},
		SampleCurl:        buildOnboardingSampleCurl("https://kb.example.com"),
	}
	payload, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, fragment := range []string{
		`"api_key_prefix":"ak_CANJasK5f"`,
		`"openapi_url":"/openapi.yaml"`,
		`"workflow_key":"asset_prepare"`,
		`"auth":{"type":"Bearer","header":"Authorization"`,
		`"allowed_operations":[{"operation":"query","allowed":true}]`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("onboarding payload missing %s in %s", fragment, text)
		}
	}
	var decoded struct {
		Auth AgentOnboardingAuth `json:"auth"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Auth.Format != "Authorization: Bearer <agent-api-key>" {
		t.Fatalf("auth format template wrong: %q", decoded.Auth.Format)
	}
	pack.ApiKeyPrefix = nil
	pack.WorkflowKey = ""
	payload, err = json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	text = string(payload)
	if strings.Contains(text, "api_key_prefix") || strings.Contains(text, "workflow_key") {
		t.Fatalf("absent prefix/workflow key must stay omitted, got %s", text)
	}
}
