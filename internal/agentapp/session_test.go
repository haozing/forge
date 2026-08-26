package agentapp

import (
	"context"
	"errors"
	"testing"

	"agentchunzhi/internal/auth"
)

func TestStartRejectsNonMemberAndInvalidKey(t *testing.T) {
	principal := auth.Principal{UserType: "agent"}
	_, err := (Service{}).Start(context.Background(), principal, []string{"00000000-0000-4000-8000-000000000001"}, "00000000-0000-4000-8000-000000000002", "valid-idempotency-key")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected non-member validation, got %v", err)
	}
	principal.UserType = "member"
	_, err = (Service{}).Start(context.Background(), principal, []string{"00000000-0000-4000-8000-000000000001"}, "00000000-0000-4000-8000-000000000002", "short")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected idempotency validation, got %v", err)
	}
}

func TestResolveActiveAgentPrincipalRejectsInvalidSession(t *testing.T) {
	_, err := (Service{}).ResolveActiveAgentPrincipal(context.Background(), auth.Principal{UserType: "member"}, "not-a-session")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected invalid session input, got %v", err)
	}
}
