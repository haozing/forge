package admin

import (
	"context"
	"errors"
	"testing"

	"agentchunzhi/internal/auth"
)

func TestListAgentApplicationsRejectsInvalidInput(t *testing.T) {
	principal := auth.Principal{UserType: "agent"}
	if _, err := (Service{}).ListAgentApplications(context.Background(), principal, 100); !errors.Is(err, ErrApplicationListInvalidInput) {
		t.Fatalf("expected invalid principal error, got %v", err)
	}
	principal.UserType = "member"
	if _, err := (Service{}).ListAgentApplications(context.Background(), principal, 101); !errors.Is(err, ErrApplicationListInvalidInput) {
		t.Fatalf("expected invalid limit error, got %v", err)
	}
}

func TestGetAgentApplicationRejectsInvalidID(t *testing.T) {
	_, err := (Service{}).GetAgentApplication(context.Background(), auth.Principal{UserType: "member"}, "not-a-uuid")
	if !errors.Is(err, ErrApplicationListInvalidInput) {
		t.Fatalf("expected invalid id error, got %v", err)
	}
}

func TestSetAgentApplicationStatusRejectsInvalidInput(t *testing.T) {
	_, err := (Service{}).SetAgentApplicationStatus(context.Background(), auth.Principal{UserType: "member"}, SetApplicationStatusInput{
		ApplicationID:  "00000000-0000-4000-8000-000000000001",
		Status:         "paused",
		IdempotencyKey: "application-status-key",
	})
	if !errors.Is(err, ErrApplicationStatusInvalidInput) {
		t.Fatalf("expected invalid status error, got %v", err)
	}
}

func TestUpdateAgentApplicationRejectsEmptyPatch(t *testing.T) {
	_, err := (Service{}).UpdateAgentApplication(context.Background(), auth.Principal{UserType: "member"}, UpdateAgentApplicationInput{
		ApplicationID:  "00000000-0000-4000-8000-000000000001",
		IdempotencyKey: "application-update-key",
	})
	if !errors.Is(err, ErrApplicationUpdateInvalidInput) {
		t.Fatalf("expected empty patch error, got %v", err)
	}
}

func TestValidRuntimeModeRequiresWorkflowKeyOnlyForWorkflow(t *testing.T) {
	if !validRuntimeMode("rag", "") || !validRuntimeMode("react", "") {
		t.Fatal("rag and react modes should be accepted without a workflow key")
	}
	if validRuntimeMode("react", "unexpected") {
		t.Fatal("react mode must reject a workflow key")
	}
	if !validRuntimeMode("workflow", "asset-organizer") || validRuntimeMode("workflow", "") {
		t.Fatal("workflow mode must require a workflow key")
	}
}
