package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"agentchunzhi/internal/automation"
)

// TestWriteAutomationErrorMapsPreciseSentinels pins the P1-11 contract: every
// composite job-creation constraint surfaces its own status/code instead of
// collapsing into a bare 403 workspace_access_denied.
func TestWriteAutomationErrorMapsPreciseSentinels(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"legacy invalid input", automation.ErrInvalidInput, 422, "validation_failed"},
		{"legacy workspace denial stays stable", automation.ErrForbidden, 403, "workspace_access_denied"},
		{"application not bound to workspace", automation.ErrAppNotBound, 403, "application_not_bound_to_workspace"},
		{"wrapped application not bound keeps code", fmt.Errorf("inspect: %w", automation.ErrAppNotBound), 403, "application_not_bound_to_workspace"},
		{"application disabled", automation.ErrAppDisabled, 403, "agent_application_disabled"},
		{"workflow mismatch is semantic validation", automation.ErrWorkflowMismatch, 422, "workflow_mismatch"},
		{"endpoint unavailable", automation.ErrEndpointUnavailable, 403, "model_endpoint_unavailable"},
		{"revision revoked", automation.ErrRevokedRevision, 403, "model_endpoint_revision_revoked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeAutomationError(recorder, tc.err, "automation_job_create_failed")
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body %q: %v", recorder.Body.String(), err)
			}
			if body.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", body.Code, tc.wantCode)
			}
		})
	}
}

func TestWriteAutomationErrorUnknownErrorKeepsFallback(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeAutomationError(recorder, errors.New("connection reset"), "automation_job_create_failed")
	if recorder.Code != 500 {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if body.Code != "automation_job_create_failed" {
		t.Fatalf("fallback code = %q", body.Code)
	}
}
