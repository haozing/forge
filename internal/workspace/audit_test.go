package workspace

import (
	"context"
	"errors"
	"testing"

	"agentchunzhi/internal/store"
)

const (
	testWorkspaceID  = "1a6454f7-18ea-4e1a-963a-cf918ebe0d73"
	testOtherWSID    = "55014f34-a5dc-4555-9d80-b5f61533357c"
	testRowID        = "00000000-0000-4000-8000-000000000001"
	testRowID2       = "00000000-0000-4000-8000-000000000002"
	testUserID       = "11111111-2222-4333-8444-555555555555"
	testActorID      = "99999999-8888-4777-8666-777777777777"
	testInvitationID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
)

type memoryAuditSink struct {
	entries []store.AuditEntry
	err     error
}

func (s *memoryAuditSink) RecordAudit(ctx context.Context, entry store.AuditEntry) error {
	if s.err != nil {
		return s.err
	}
	s.entries = append(s.entries, entry)
	return nil
}

func TestValidEmail(t *testing.T) {
	cases := []struct {
		name  string
		email string
		want  bool
	}{
		{"plain address", "user@example.com", true},
		{"subdomain", "first.last+tag@sub.example.co.uk", true},
		{"digits and hyphen", "user-1_2@mail-room.example.io", true},
		{"uppercase kept by caller", "User@Example.COM", true},
		{"empty after trim is handled upstream", "", false},
		{"no at sign", "not-an-email", false},
		{"two at signs", "a@b@c.example.com", false},
		{"missing domain", "user@", false},
		{"missing local part", "@example.com", false},
		{"missing tld dot", "user@example", false},
		{"tld too short", "user@example.c", false},
		{"spaces inside", "us er@example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidEmail(tc.email); got != tc.want {
				t.Fatalf("ValidEmail(%q) = %v, want %v", tc.email, got, tc.want)
			}
		})
	}
}

func TestSelectMemberRecordAddressing(t *testing.T) {
	rowByRowID := memberRecord{RowID: testRowID, WorkspaceID: testWorkspaceID, UserID: testUserID, Role: "editor"}
	cases := []struct {
		name          string
		rows          []memberRecord
		workspaceHint string
		wantRecord    memberRecord
		wantErr       error
	}{
		{
			name:       "row id addressing resolves directly",
			rows:       []memberRecord{rowByRowID},
			wantRecord: rowByRowID,
		},
		{
			name:       "single membership via user id addressing",
			rows:       []memberRecord{{RowID: testRowID2, WorkspaceID: testWorkspaceID, UserID: testUserID, Role: "viewer"}},
			wantRecord: memberRecord{RowID: testRowID2, WorkspaceID: testWorkspaceID, UserID: testUserID, Role: "viewer"},
		},
		{
			name:    "unknown reference stays not found",
			rows:    nil,
			wantErr: ErrNotFound,
		},
		{
			name: "multi-workspace membership without hint is ambiguous",
			rows: []memberRecord{
				{RowID: testRowID, WorkspaceID: testWorkspaceID, UserID: testUserID, Role: "editor"},
				{RowID: testRowID2, WorkspaceID: testOtherWSID, UserID: testUserID, Role: "viewer"},
			},
			wantErr: ErrAmbiguousMember,
		},
		{
			name: "hint disambiguates multi-workspace membership",
			rows: []memberRecord{
				{RowID: testRowID, WorkspaceID: testWorkspaceID, UserID: testUserID, Role: "editor"},
				{RowID: testRowID2, WorkspaceID: testOtherWSID, UserID: testUserID, Role: "viewer"},
			},
			workspaceHint: testOtherWSID,
			wantRecord:    memberRecord{RowID: testRowID2, WorkspaceID: testOtherWSID, UserID: testUserID, Role: "viewer"},
		},
		{
			name:          "hint matching nothing falls back to not found",
			rows:          []memberRecord{{RowID: testRowID, WorkspaceID: testWorkspaceID, UserID: testUserID, Role: "editor"}},
			workspaceHint: testOtherWSID,
			wantErr:       ErrNotFound,
		},
		{
			name:          "malformed hint is rejected",
			rows:          []memberRecord{{RowID: testRowID, WorkspaceID: testWorkspaceID, UserID: testUserID, Role: "editor"}},
			workspaceHint: "abc",
			wantErr:       ErrInvalidInput,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record, err := selectMemberRecord(tc.rows, tc.workspaceHint)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("selectMemberRecord error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && record != tc.wantRecord {
				t.Fatalf("selectMemberRecord = %+v, want %+v", record, tc.wantRecord)
			}
		})
	}
}

func TestNewWorkspaceAuditEntryCarriesWorkspaceScope(t *testing.T) {
	entry := NewAuditEntry(AuditMemberRoleChange, "", "org", testActorID, auditResourceWorkspaceMember, testRowID, map[string]any{
		"workspace_id": testWorkspaceID,
	})
	if entry.Action != AuditMemberRoleChange {
		t.Fatalf("action = %q", entry.Action)
	}
	if entry.Result != "allowed" {
		t.Fatalf("default result = %q, want allowed", entry.Result)
	}
	if entry.Metadata["workspace_id"] != testWorkspaceID {
		t.Fatalf("metadata.workspace_id = %v, want %s", entry.Metadata["workspace_id"], testWorkspaceID)
	}

	denied := NewAuditEntry(AuditInvitationCreate, "denied", "org", testActorID, auditResourceWorkspaceInvitation, testInvitationID, nil)
	if denied.Result != "denied" {
		t.Fatalf("result override ignored, got %q", denied.Result)
	}
	if denied.Metadata == nil {
		t.Fatal("nil metadata must be replaced with an empty map")
	}
}

func TestWriteAuditEventWithMemorySink(t *testing.T) {
	sink := &memoryAuditSink{}
	entry := NewAuditEntry(AuditInvitationAccept, "", "org", testUserID, auditResourceWorkspaceInvitation, testInvitationID, map[string]any{
		"workspace_id":  testWorkspaceID,
		"invitation_id": testInvitationID,
		"role":          "editor",
	})
	if err := WriteAuditEvent(context.Background(), sink, entry); err != nil {
		t.Fatalf("WriteAuditEvent returned error: %v", err)
	}
	if len(sink.entries) != 1 {
		t.Fatalf("sink captured %d entries, want 1", len(sink.entries))
	}
	got := sink.entries[0]
	if got.Action != AuditInvitationAccept || got.ResourceType != auditResourceWorkspaceInvitation {
		t.Fatalf("unexpected entry: %+v", got)
	}
	if got.InitiatorUserID != testUserID {
		t.Fatalf("initiator should default to the actor, got %q", got.InitiatorUserID)
	}

	sink.err = errors.New("boom")
	if err := WriteAuditEvent(context.Background(), sink, entry); err == nil {
		t.Fatal("expected sink error to propagate so callers can log it")
	}

	// A disabled sink (nil) must be a no-op rather than a panic.
	if err := WriteAuditEvent(context.Background(), nil, entry); err != nil {
		t.Fatalf("nil sink returned error: %v", err)
	}
}

func TestValidMemberRole(t *testing.T) {
	valid := []string{"admin", "editor", "reviewer", "viewer", "member", "owner"}
	for _, role := range valid {
		if !validMemberRole(role) {
			t.Fatalf("%q should be valid", role)
		}
	}
	for _, role := range []string{"", " owner", "owner ", "superadmin", "Owner"} {
		if validMemberRole(role) {
			t.Fatalf("%q should be invalid before trimming", role)
		}
	}
}
