package automation

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// usableFacts describes an application that passes every legacy composite
// constraint; individual cases flip one field at a time.
func usableFacts() appBindingFacts {
	return appBindingFacts{
		Bound:           true,
		BindingEnabled:  true,
		AppExists:       true,
		AppStatus:       "active",
		RuntimeMode:     "workflow",
		ApplicationKey:  "asset_prepare",
		EndpointExists:  true,
		EndpointStatus:  "active",
		RevisionPresent: true,
	}
}

func TestClassifyAppBindingBranches(t *testing.T) {
	revokedAt := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		mutate      func(*appBindingFacts)
		workflowKey string
		wantErr     error
	}{
		{name: "matching workflow application passes", mutate: func(f *appBindingFacts) {}, workflowKey: "asset_prepare", wantErr: nil},
		{name: "unbound application", mutate: func(f *appBindingFacts) { f.Bound = false }, workflowKey: "asset_prepare", wantErr: ErrAppNotBound},
		{name: "workspace binding disabled", mutate: func(f *appBindingFacts) { f.BindingEnabled = false }, workflowKey: "asset_prepare", wantErr: ErrAppNotBound},
		{name: "application missing from organization", mutate: func(f *appBindingFacts) { f.AppExists = false }, workflowKey: "asset_prepare", wantErr: ErrAppNotBound},
		{name: "application disabled", mutate: func(f *appBindingFacts) { f.AppStatus = "paused" }, workflowKey: "asset_prepare", wantErr: ErrAppDisabled},
		{name: "rag mode cannot run workflow operation", mutate: func(f *appBindingFacts) { f.RuntimeMode = "rag" }, workflowKey: "asset_prepare", wantErr: ErrWorkflowMismatch},
		{name: "workflow key mismatch", mutate: func(f *appBindingFacts) { f.ApplicationKey = "note_sync" }, workflowKey: "asset_prepare", wantErr: ErrWorkflowMismatch},
		{name: "endpoint unavailable", mutate: func(f *appBindingFacts) { f.EndpointStatus = "unavailable" }, workflowKey: "asset_prepare", wantErr: ErrEndpointUnavailable},
		{name: "endpoint row absent", mutate: func(f *appBindingFacts) { f.EndpointExists = false }, workflowKey: "asset_prepare", wantErr: ErrEndpointUnavailable},
		{name: "current revision row absent", mutate: func(f *appBindingFacts) { f.RevisionPresent = false }, workflowKey: "asset_prepare", wantErr: ErrEndpointUnavailable},
		{name: "revision revoked", mutate: func(f *appBindingFacts) { f.RevokedAt = &revokedAt }, workflowKey: "asset_prepare", wantErr: ErrRevokedRevision},
		// Operations without a mapped workflow key keep the legacy bypass of
		// the runtime_mode/workflow_key gate ("export" on a rag application).
		{name: "no-workflow operation bypasses mode gate", mutate: func(f *appBindingFacts) { f.RuntimeMode = "rag"; f.ApplicationKey = "" }, workflowKey: "", wantErr: nil},
		{name: "bypassed gate still requires active endpoint", mutate: func(f *appBindingFacts) {
			f.RuntimeMode = "rag"
			f.ApplicationKey = ""
			f.EndpointStatus = "disabled"
		}, workflowKey: "", wantErr: ErrEndpointUnavailable},
		// Precedence checks for deterministic messages.
		{name: "binding failure wins over everything else", mutate: func(f *appBindingFacts) {
			f.Bound = false
			f.AppExists = false
			f.EndpointExists = false
			f.RevokedAt = &revokedAt
		}, workflowKey: "asset_prepare", wantErr: ErrAppNotBound},
		{name: "mode mismatch reported before endpoint problems", mutate: func(f *appBindingFacts) {
			f.RuntimeMode = "rag"
			f.EndpointStatus = "unavailable"
		}, workflowKey: "asset_prepare", wantErr: ErrWorkflowMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := usableFacts()
			tc.mutate(&facts)
			err := classifyAppBinding(facts, tc.workflowKey)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
			// The old blanket denial must never leak back through this path.
			if errors.Is(err, ErrForbidden) {
				t.Fatalf("classification must not fold into workspace_access_denied: %v", err)
			}
		})
	}
}

func TestClassifyAppBindingWrapsSentinelWithDetail(t *testing.T) {
	facts := usableFacts()
	facts.AppStatus = "disabled"
	err := classifyAppBinding(facts, "asset_prepare")
	if !errors.Is(err, ErrAppDisabled) {
		t.Fatalf("sentinel lost through wrapping: %v", err)
	}
	for _, fragment := range []string{"agent_application_disabled", `status "disabled"`} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error %q should mention %q", err, fragment)
		}
	}
}
