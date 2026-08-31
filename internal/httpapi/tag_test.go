package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/tag"
)

// TestTagErrorMapping pins the tag domain error → status/code contract.
func TestTagErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not found", tag.ErrNotFound, http.StatusNotFound, "tag_not_found"},
		{"key exists", tag.ErrKeyExists, http.StatusConflict, "tag_key_exists"},
		{"archived reference", tag.ErrArchived, http.StatusConflict, "tag_archived"},
		{"already archived", tag.ErrAlreadyArchived, http.StatusConflict, "tag_already_archived"},
		{"already active", tag.ErrAlreadyActive, http.StatusConflict, "tag_already_active"},
		{"revision mismatch", tag.ErrRevisionMismatch, http.StatusPreconditionFailed, "tag_revision_mismatch"},
		{"unknown tag", tag.ErrUnknownTag, http.StatusUnprocessableEntity, "unknown_tag"},
		{"contradictory filter", tag.ErrContradictoryFilter, http.StatusUnprocessableEntity, "contradictory_tag_filter"},
		{"too many tags", tag.ErrTooManyTags, http.StatusUnprocessableEntity, "too_many_tags"},
		{"invalid input", tag.ErrInvalidInput, http.StatusUnprocessableEntity, "validation_failed"},
		{"workspace forbidden", authz.ErrWorkspaceForbidden, http.StatusForbidden, "action_not_allowed"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			TagError(recorder, fmt.Errorf("wrapped: %w", testCase.err))
			if recorder.Code != testCase.status {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.status)
			}
			if !strings.Contains(recorder.Body.String(), `"code":"`+testCase.code+`"`) {
				t.Fatalf("body missing %q: %s", testCase.code, recorder.Body.String())
			}
		})
	}
	recorder := httptest.NewRecorder()
	TagError(recorder, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("nil error must not write, status = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	TagError(recorder, errors.New("boom"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unknown error must map to 500, status = %d", recorder.Code)
	}
}

// TestTagRoutesRegistered keeps the phase 2 tag surface wired in the single
// route registry.
func TestTagRoutesRegistered(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".", "router_groups.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		`"/api/workspaces/{workspaceId}/tags"`,
		`"/api/workspaces/{workspaceId}/tags/{tagId}"`,
		`"/api/workspaces/{workspaceId}/tags/{tagId}/archive"`,
		`"/api/workspaces/{workspaceId}/tags/{tagId}/restore"`,
		`"/api/workspaces/{workspaceId}/tag-facets"`,
		`"/api/open/hooks/assets"`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("route %s must be registered", required)
		}
	}
}
