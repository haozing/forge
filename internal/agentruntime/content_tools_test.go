package agentruntime

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeDisplayPath(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"Cloud Native Guide", "cloud-native-guide"},
		{"  Hello--World!!  ", "hello-world"},
		{"UPPER CASE Title", "upper-case-title"},
		{"k8s/best practices", "k8s-best-practices"},
		{"///leading", "leading"},
		{"trailing---", "trailing"},
		{"", ""},
		{"!!!", ""},
		{"a", "a"},
	}
	for _, c := range cases {
		if got := normalizeDisplayPath(c.raw); got != c.want {
			t.Errorf("normalizeDisplayPath(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
	long := strings.Repeat("a", 200)
	if got := normalizeDisplayPath(long); len(got) != 120 {
		t.Errorf("long input should cap at 120, got %d", len(got))
	}
}

// TestLinkCheckerBlocksPrivateEgress is the SSRF red line of the checklist
// tool (plan §12.3): loopback, link-local (cloud metadata) and private
// ranges must never be dialed, regardless of what the article body says.
func TestLinkCheckerBlocksPrivateEgress(t *testing.T) {
	for _, target := range []string{
		"http://127.0.0.1:8080/healthz",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.1.2.3/admin",
		"http://192.168.1.1/",
	} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodHead, target, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		response, err := linkCheckerClient.Do(req)
		if err == nil {
			response.Body.Close()
			t.Errorf("%s must be refused by the SSRF dialer, got status %d", target, response.StatusCode)
		}
	}
}
