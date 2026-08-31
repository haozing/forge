package httpapi

// sites_test.go — the error mapping and guard behavior of the phase 5
// site management surface at the pure-function level (no database).

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentchunzhi/internal/site"
)

func TestSiteErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{site.ErrSiteNotFound, "site_not_found"},
		{site.ErrBindingNotFound, "binding_not_found"},
		{site.ErrSlugInvalid, "slug_invalid"},
		{site.ErrPathInvalid, "path_invalid"},
		{site.ErrBindingTargetInvalid, "binding_target_invalid"},
		{site.ErrSiteDisabled, "site_disabled"},
		{site.ErrInvalidInput, "validation_failed"},
		{site.ErrForbidden, "action_not_allowed"},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		SiteError(recorder, tc.err, "slug_conflict")
		if recorder.Code == http.StatusInternalServerError {
			t.Fatalf("error %v must map to a domain status, got 500", tc.err)
		}
		if !strings.Contains(recorder.Body.String(), fmt.Sprintf(`"code":%q`, tc.code)) {
			t.Fatalf("error %v: body = %s, want code %q", tc.err, recorder.Body.String(), tc.code)
		}
	}
	// ErrConflict takes the caller-supplied code: slug_conflict on site
	// surfaces, path_conflict on binding surfaces.
	for _, conflictCode := range []string{"slug_conflict", "path_conflict"} {
		recorder := httptest.NewRecorder()
		SiteError(recorder, site.ErrConflict, conflictCode)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("conflict status = %d, want 409", recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), fmt.Sprintf(`"code":%q`, conflictCode)) {
			t.Fatalf("body = %s, want code %q", recorder.Body.String(), conflictCode)
		}
	}
	// Nil and unknown errors keep the contract: nil is a no-op, unknown is 500.
	recorder := httptest.NewRecorder()
	SiteError(recorder, nil, "slug_conflict")
	if recorder.Code != http.StatusOK {
		t.Fatal("nil error must not write anything")
	}
	recorder = httptest.NewRecorder()
	SiteError(recorder, errors.New("boom"), "slug_conflict")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatal("unknown error must map to 500")
	}
}

func TestSiteRoutesRegistered(t *testing.T) {
	raw := newRouter(Dependencies{})
	for _, path := range []string{
		"/api/workspaces/11111111-1111-1111-1111-111111111111/sites",
		"/api/workspaces/11111111-1111-1111-1111-111111111111/sites/22222222-2222-2222-2222-222222222222",
		"/api/workspaces/11111111-1111-1111-1111-111111111111/sites/22222222-2222-2222-2222-222222222222/bindings",
		"/api/workspaces/11111111-1111-1111-1111-111111111111/sites/22222222-2222-2222-2222-222222222222/bindings/33333333-3333-3333-3333-333333333333",
		"/api/workspaces/11111111-1111-1111-1111-111111111111/sites/22222222-2222-2222-2222-222222222222/preview",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		raw.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("route %s must be registered", path)
		}
	}
}
