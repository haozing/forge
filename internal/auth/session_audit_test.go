package auth

import (
	"encoding/json"
	"testing"
)

func TestBuildSessionAuditEntry(t *testing.T) {
	cases := []struct {
		name       string
		event      SessionAuditEvent
		wantAction string
		wantResult string
		wantMeta   map[string]any
	}{
		{
			name:       "successful login",
			event:      SessionAuditEvent{Action: SessionLogin, Result: "allowed", OrganizationID: "org-1", UserID: "user-1", LoginName: "alice"},
			wantAction: SessionLogin,
			wantResult: "allowed",
			wantMeta:   map[string]any{"login_name": "alice"},
		},
		{
			name:       "failed login carries reason and attempted name only",
			event:      SessionAuditEvent{Action: SessionLogin, Result: "denied", LoginName: "ghost", Reason: "unknown_login_name"},
			wantAction: SessionLogin,
			wantResult: "denied",
			wantMeta:   map[string]any{"login_name": "ghost", "reason": "unknown_login_name"},
		},
		{
			name:       "logout defaults to allowed",
			event:      SessionAuditEvent{Action: SessionLogout, OrganizationID: "org-1", UserID: "user-1"},
			wantAction: SessionLogout,
			wantResult: "allowed",
			wantMeta:   map[string]any{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := BuildSessionAuditEntry(tc.event)
			if entry.Action != tc.wantAction {
				t.Fatalf("action = %q, want %q", entry.Action, tc.wantAction)
			}
			if entry.Result != tc.wantResult {
				t.Fatalf("result = %q, want %q", entry.Result, tc.wantResult)
			}
			if len(entry.Metadata) != len(tc.wantMeta) {
				t.Fatalf("metadata = %v, want %v", entry.Metadata, tc.wantMeta)
			}
			for key, value := range tc.wantMeta {
				if entry.Metadata[key] != value {
					t.Fatalf("metadata[%q] = %v, want %v", key, entry.Metadata[key], value)
				}
			}
		})
	}
}

func TestSessionAuditEntriesNeverCarryPasswords(t *testing.T) {
	// The compiler already enforces that SessionAuditEvent has no password
	// field; here we pin down that its serialized form only ever exposes the
	// whitelisted forensic keys with values from the fixed reason vocabulary.
	events := []SessionAuditEvent{
		{Action: SessionLogin, Result: "denied", LoginName: "alice", Reason: ReasonInvalidCredentials},
		{Action: SessionLogin, Result: "denied", LoginName: "ghost", Reason: ReasonUnknownLoginName},
		{Action: SessionLogin, Result: "allowed", OrganizationID: "org-1", UserID: "user-1", LoginName: "alice"},
		{Action: SessionLogout, OrganizationID: "org-1", UserID: "user-1"},
	}
	allowedKeys := map[string]bool{"login_name": true, "reason": true}
	allowedReasons := map[string]bool{ReasonUnknownLoginName: true, ReasonInvalidCredentials: true, ReasonSessionCreateFailed: true}
	for _, event := range events {
		raw, err := json.Marshal(BuildSessionAuditEntry(event).Metadata)
		if err != nil {
			t.Fatalf("marshal metadata: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal metadata: %v", err)
		}
		for key, value := range decoded {
			if !allowedKeys[key] {
				t.Fatalf("unexpected audit metadata key %q in %s", key, raw)
			}
			if key == "reason" && !allowedReasons[value.(string)] {
				t.Fatalf("unexpected audit reason %q", value)
			}
		}
	}
}

func TestReportSessionEventUsesHook(t *testing.T) {
	captured := make(chan SessionAuditEvent, 4)
	service := SessionService{AuditHook: func(event SessionAuditEvent) { captured <- event }}

	service.reportSessionEvent(SessionAuditEvent{Action: SessionLogout, OrganizationID: "org-1", UserID: "user-1"})
	logout := <-captured
	if logout.Action != SessionLogout || logout.OrganizationID != "org-1" {
		t.Fatalf("hook captured unexpected event: %+v", logout)
	}

	service.reportSessionEvent(SessionAuditEvent{Action: SessionLogin, Result: "denied", LoginName: "bob"})
	denied := <-captured
	if denied.Result != "denied" || denied.LoginName != "bob" {
		t.Fatalf("hook captured unexpected event: %+v", denied)
	}
}

func TestReportSessionEventHookPanicIsContained(t *testing.T) {
	service := SessionService{AuditHook: func(SessionAuditEvent) { panic("auditor exploded") }}
	service.reportSessionEvent(SessionAuditEvent{Action: SessionLogin, Result: "allowed"})
	// No panic escaping means the request path stays healthy.
}
