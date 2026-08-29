package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequiresHTTPIdempotency(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodPost, "/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/assets", true},
		{http.MethodPatch, "/api/frontend/assets/00000000-0000-4000-8000-000000000001", true},
		{http.MethodPost, "/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/query", false},
		{http.MethodPost, "/api/frontend/agent-sessions/00000000-0000-4000-8000-000000000001/references/validate", false},
		{http.MethodPost, "/api/frontend/conversations/00000000-0000-4000-8000-000000000001/chat/stream", false},
		{http.MethodPatch, "/api/frontend/assets/00000000-0000-4000-8000-000000000002", true},
		{http.MethodPost, "/api/sessions", false},
		{http.MethodPost, "/api/public/v2/sessions", false},
	}
	for _, test := range cases {
		request := httptest.NewRequest(test.method, test.path, nil)
		if got := requiresHTTPIdempotency(request); got != test.want {
			t.Fatalf("%s %s: got %v want %v", test.method, test.path, got, test.want)
		}
	}
}

func TestSnapshotIdempotentRequestPreservesBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/frontend/test?b=2", strings.NewReader(`{"value":1}`))
	firstHash, cleanup, err := snapshotIdempotentRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"value":1}` || firstHash == "" {
		t.Fatalf("unexpected snapshot body=%q hash=%q", body, firstHash)
	}

	second := httptest.NewRequest(http.MethodPost, "/api/frontend/test?b=2", strings.NewReader(`{"value":1}`))
	secondHash, secondCleanup, err := snapshotIdempotentRequest(second)
	if err != nil {
		t.Fatal(err)
	}
	defer secondCleanup()
	if firstHash != secondHash {
		t.Fatalf("equal requests must hash equally: %s %s", firstHash, secondHash)
	}
}
