package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFrontendContractRoutesRequireSession(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
	}{
		// "workspaces" and "workspace" collection/resource routes retired in
		// phase 1 (route ledger): they must now answer 404 and are covered by
		// TestPhase1LegacyRoutesAreRetired.
		{"models", http.MethodGet, "/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/resource-models"},
		{"model", http.MethodGet, "/api/frontend/resource-models/00000000-0000-4000-8000-000000000001"},
		{"model versions", http.MethodGet, "/api/frontend/resource-models/00000000-0000-4000-8000-000000000001/versions"},
		{"model migration", http.MethodGet, "/api/frontend/resource-model-migrations/00000000-0000-4000-8000-000000000001"},
		{"assets", http.MethodGet, "/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/assets"},
		{"member query", http.MethodPost, "/api/v2/workspaces/00000000-0000-4000-8000-000000000001/query"},
		{"import job", http.MethodGet, "/api/frontend/import-jobs/00000000-0000-4000-8000-000000000001"},
		{"export job", http.MethodGet, "/api/frontend/export-jobs/00000000-0000-4000-8000-000000000001"},
		{"asset", http.MethodGet, "/api/frontend/assets/00000000-0000-4000-8000-000000000001"},
		{"asset versions attachments", http.MethodGet, "/api/frontend/asset-versions/00000000-0000-4000-8000-000000000001/attachments"},
		{"attachment", http.MethodGet, "/api/frontend/attachments/00000000-0000-4000-8000-000000000001"},
		{"patch attachment", http.MethodPatch, "/api/frontend/attachments/00000000-0000-4000-8000-000000000001"},
		{"container tree", http.MethodGet, "/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/containers/tree"},
		{"container", http.MethodGet, "/api/frontend/containers/00000000-0000-4000-8000-000000000001"},
		{"automation jobs", http.MethodGet, "/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/automation-jobs"},
		{"automation job", http.MethodGet, "/api/frontend/automation-jobs/00000000-0000-4000-8000-000000000001"},
		{"task run", http.MethodGet, "/api/frontend/task-runs/00000000-0000-4000-8000-000000000001"},
		{"task attempts", http.MethodGet, "/api/frontend/task-runs/00000000-0000-4000-8000-000000000001/attempts"},
		{"conversations", http.MethodGet, "/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/conversations"},
		{"conversation chat", http.MethodPost, "/api/frontend/conversations/00000000-0000-4000-8000-000000000001/chat"},
		{"model endpoints", http.MethodGet, "/api/frontend/model-endpoints"},
		{"model endpoint", http.MethodGet, "/api/frontend/model-endpoints/00000000-0000-4000-8000-000000000001"},
		{"test model endpoint", http.MethodPost, "/api/frontend/model-endpoints/00000000-0000-4000-8000-000000000001/test"},
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

func TestFrontendAttachmentResourceRejectsUnsupportedMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/frontend/attachments/00000000-0000-4000-8000-000000000001", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rec.Code)
	}
}
