package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMemberContractRoutesRequireSession(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
	}{
		// "workspaces" and "workspace" collection/resource routes retired in
		// phase 1 (route ledger): they must now answer 404 and are covered by
		// TestPhase1LegacyRoutesAreRetired.
		{"models", http.MethodGet, "/api/workspaces/00000000-0000-4000-8000-000000000001/resource-models"},
		{"model", http.MethodGet, "/api/resource-models/00000000-0000-4000-8000-000000000001"},
		{"model versions", http.MethodGet, "/api/resource-models/00000000-0000-4000-8000-000000000001/versions"},
		{"model migration", http.MethodGet, "/api/resource-model-migrations/00000000-0000-4000-8000-000000000001"},
		{"assets", http.MethodGet, "/api/workspaces/00000000-0000-4000-8000-000000000001/assets"},
		{"member query", http.MethodPost, "/api/workspaces/00000000-0000-4000-8000-000000000001/query"},
		{"import job", http.MethodGet, "/api/import-jobs/00000000-0000-4000-8000-000000000001"},
		{"export job", http.MethodGet, "/api/export-jobs/00000000-0000-4000-8000-000000000001"},
		{"asset", http.MethodGet, "/api/assets/00000000-0000-4000-8000-000000000001"},
		{"asset versions attachments", http.MethodGet, "/api/asset-versions/00000000-0000-4000-8000-000000000001/attachments"},
		{"attachment", http.MethodGet, "/api/attachments/00000000-0000-4000-8000-000000000001"},
		{"patch attachment", http.MethodPatch, "/api/attachments/00000000-0000-4000-8000-000000000001"},
		{"container tree", http.MethodGet, "/api/workspaces/00000000-0000-4000-8000-000000000001/containers/tree"},
		{"container", http.MethodGet, "/api/containers/00000000-0000-4000-8000-000000000001"},
		{"automation jobs", http.MethodGet, "/api/workspaces/00000000-0000-4000-8000-000000000001/automation-jobs"},
		{"automation job", http.MethodGet, "/api/automation-jobs/00000000-0000-4000-8000-000000000001"},
		{"task run", http.MethodGet, "/api/task-runs/00000000-0000-4000-8000-000000000001"},
		{"task attempts", http.MethodGet, "/api/task-runs/00000000-0000-4000-8000-000000000001/attempts"},
		{"conversations", http.MethodGet, "/api/workspaces/00000000-0000-4000-8000-000000000001/conversations"},
		{"conversation chat", http.MethodPost, "/api/conversations/00000000-0000-4000-8000-000000000001/chat"},
		{"model endpoints", http.MethodGet, "/api/model-endpoints"},
		{"model endpoint", http.MethodGet, "/api/model-endpoints/00000000-0000-4000-8000-000000000001"},
		{"test model endpoint", http.MethodPost, "/api/model-endpoints/00000000-0000-4000-8000-000000000001/test"},
	}

	handler := NewHandler()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
			}
		})
	}
}

func TestAttachmentResourceRejectsUnsupportedMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/attachments/00000000-0000-4000-8000-000000000001", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rec.Code)
	}
}
