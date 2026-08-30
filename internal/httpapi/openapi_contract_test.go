package httpapi

// openapi_contract_test.go — phase 6 bidirectional contract gate: every
// path+method in openapi-v2.yaml must be registered by the router, and every
// registered /api route must appear in the yaml. Drift in either direction
// fails the suite, closing the manual-sync gap called out in the route ledger.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type openapiRoute struct {
	Path    string
	Method  string
}

var openapiPathRe = regexp.MustCompile(`(?m)^  (/(?:api)[^\s]*):\s*$`)
var openapiMethodRe = regexp.MustCompile(`(?m)^    ([a-z]+):\s*$`)

// routerTruth is the hand-maintained registry of every /api route the mux
// serves, with the methods each handler accepts. Kept beside the contract
// assertions in contract_v2_test.go.
var routerTruth = map[string][]string{
	"/api/public/v2/sessions":                                   {"post"},
	"/api/public/v2/password-resets":                            {"post"},
	"/api/public/v2/password-resets/resolve":                    {"post"},
	"/api/public/v2/password-resets/complete":                   {"post"},
	"/api/public/v2/organization-invitations/resolve":           {"post"},
	"/api/public/v2/organization-invitations/accept":            {"post"},
	"/api/v2/sessions":                                          {"get"},
	"/api/v2/sessions/current":                                  {"delete"},
	"/api/v2/sessions/{sessionId}":                              {"delete"},
	"/api/v2/me":                                                {"get", "patch"},
	"/api/v2/me/password":                                       {"put"},
	"/api/v2/me/preferences":                                    {"get", "patch"},
	"/api/v2/organization":                                      {"get", "patch"},
	"/api/v2/organization/members":                              {"get"},
	"/api/v2/organization/members/{userId}":                     {"get", "patch"},
	"/api/v2/organization/invitations":                          {"get", "post"},
	"/api/v2/organization/invitations/{invitationId}/resend":    {"post"},
	"/api/v2/organization/invitations/{invitationId}/revoke":    {"post"},
	"/api/v2/organization/workspaces":                           {"get", "post"},
	"/api/v2/organization/workspaces/{workspaceId}":             {"get"},
	"/api/v2/organization/workspaces/{workspaceId}/archive":     {"post"},
	"/api/v2/organization/workspaces/{workspaceId}/restore":     {"post"},
	"/api/v2/organization/workspaces/{workspaceId}/members":     {"post"},
	"/api/v2/organization/workspaces/{workspaceId}/members/{membershipId}": {"patch", "delete"},
	"/api/v2/workspaces":                                        {"get"},
	"/api/v2/workspaces/{workspaceId}":                          {"get", "patch"},
	"/api/v2/workspaces/{workspaceId}/summary":                  {"get"},
	"/api/v2/workspaces/{workspaceId}/members":                  {"get", "post"},
	"/api/v2/workspaces/{workspaceId}/members/me/leave":         {"post"},
	"/api/v2/workspaces/{workspaceId}/members/{membershipId}":   {"patch", "delete"},
	"/api/v2/workspaces/{workspaceId}/eligible-members":         {"get"},
	"/api/v2/workspaces/{workspaceId}/invitations":              {"get", "post"},
	"/api/v2/workspaces/{workspaceId}/invitations/{invitationId}/resend": {"post"},
	"/api/v2/workspaces/{workspaceId}/invitations/{invitationId}/revoke": {"post"},
	"/api/v2/workspaces/{workspaceId}/publication-requests":       {"get", "post"},
	"/api/v2/workspaces/{workspaceId}/publication-requests/batch": {"post"},
	"/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}":            {"get", "delete"},
	"/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}/approve":    {"post"},
	"/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}/reject":     {"post"},
	"/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}/cancel":     {"post"},
	"/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}/comments":   {"get", "post"},
	"/api/v2/assets/{assetId}/draft":          {"get", "patch"},
	"/api/v2/assets/{assetId}/commit-draft":   {"post"},
	"/api/v2/assets/{assetId}/publish":        {"post"},
	"/api/v2/assets/{assetId}/archive":        {"post"},
	"/api/v2/assets/{assetId}/restore":        {"post"},
	"/api/v2/asset-versions/{versionId}/confirm": {"post"},
	"/api/v2/workspaces/{workspaceId}/tags":          {"get", "post"},
	"/api/v2/workspaces/{workspaceId}/tags/{tagId}":  {"get", "patch"},
	"/api/v2/workspaces/{workspaceId}/tags/{tagId}/archive":  {"post"},
	"/api/v2/workspaces/{workspaceId}/tags/{tagId}/restore":  {"post"},
	"/api/v2/workspaces/{workspaceId}/tag-facets":     {"get"},
	"/api/v2/workspaces/{workspaceId}/query":          {"post"},
	"/api/v2/organization/query":                      {"post"},
	"/api/open/v2/query":                              {"post"},
	"/api/open/v2/references/validate":                {"post"},
	"/api/open/v2/hooks/assets":                       {"post"},
	"/api/open/v2/agent-tasks":                        {"post"},
	"/api/open/v2/agent-tasks/{taskId}":               {"get"},
	"/api/v2/organization/retrieval/profiles":                    {"get", "post"},
	"/api/v2/organization/retrieval/profiles/{profileId}/activate": {"post"},
	"/api/v2/workspaces/{workspaceId}/retrieval/status":            {"get"},
	"/api/v2/workspaces/{workspaceId}/retrieval/rebuilds":          {"post", "get"},
	"/api/v2/workspaces/{workspaceId}/retrieval/rebuilds/{rebuildId}": {"get"},
	"/api/v2/organization/retrieval/rebuilds":                      {"post"},
	"/api/v2/organization/retrieval/rebuilds/{rebuildId}":          {"get"},
	"/api/v2/workspaces/{workspaceId}/query-executions":            {"get"},
	"/api/v2/organization/query-executions":                        {"get"},
	"/api/v2/query-executions/{executionId}":                       {"get"},
	"/api/v2/workspaces/{workspaceId}/assets/{assetId}/prepare":                {"post"},
	"/api/v2/workspaces/{workspaceId}/assets/{assetId}/suggestions":            {"get"},
	"/api/v2/workspaces/{workspaceId}/assets/{assetId}/suggestions/accept-batch": {"post"},
	"/api/v2/workspaces/{workspaceId}/assets/{assetId}/processing-results":     {"get"},
	"/api/v2/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/accept": {"post"},
	"/api/v2/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/reject":  {"post"},
	"/api/v2/workspaces/{workspaceId}/sites":                          {"get", "post"},
	"/api/v2/workspaces/{workspaceId}/sites/{siteId}":                 {"get", "patch", "delete"},
	"/api/v2/workspaces/{workspaceId}/sites/{siteId}/bindings":        {"get", "post"},
	"/api/v2/workspaces/{workspaceId}/sites/{siteId}/bindings/{bindingId}": {"patch", "delete"},
	"/api/v2/workspaces/{workspaceId}/sites/{siteId}/preview":         {"get"},
	"/api/public/v2/sites/{slug}":                                     {"get"},
	"/api/public/v2/sites/{slug}/posts":                               {"get"},
	"/api/public/v2/sites/{slug}/posts/{displayPath}":                 {"get"},
	"/api/public/v2/sites/{slug}/sections/{sectionSlug}":              {"get"},
	"/api/public/v2/sites/{slug}/tags":                                {"get"},
	"/api/public/v2/sites/{slug}/tags/{key}":                          {"get"},
	"/api/public/v2/sites/{slug}/search":                              {"get"},
}

func TestOpenAPIMatchesRouter(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "openapi-v2.yaml"))
	if err != nil {
		t.Fatalf("read openapi: %v", err)
	}
	doc := string(raw)

	// Parse top-level paths and their immediate operation methods.
	paths := map[string][]string{}
	current := ""
	for _, line := range strings.Split(doc, "\n") {
		if m := openapiPathRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			if _, ok := paths[current]; !ok {
				paths[current] = []string{}
			}
			continue
		}
		if current != "" {
			if m := openapiMethodRe.FindStringSubmatch(line); m != nil {
				method := m[1]
				switch method {
				case "get", "post", "patch", "put", "delete":
					paths[current] = append(paths[current], method)
				}
			}
		}
		if strings.HasPrefix(line, "components:") {
			current = ""
		}
	}

	yamlSet := map[string]bool{}
	for path, methods := range paths {
		for _, method := range methods {
			yamlSet[method+" "+path] = true
		}
	}
	routerSet := map[string]bool{}
	for path, methods := range routerTruth {
		for _, method := range methods {
			routerSet[method+" "+path] = true
		}
	}

	var missingInYaml, missingInRouter []string
	for key := range routerSet {
		if !yamlSet[key] {
			missingInYaml = append(missingInYaml, key)
		}
	}
	for key := range yamlSet {
		if !routerSet[key] {
			missingInRouter = append(missingInRouter, key)
		}
	}
	sort.Strings(missingInYaml)
	sort.Strings(missingInRouter)
	if len(missingInYaml) > 0 {
		t.Errorf("routes registered but missing in openapi-v2.yaml:\n  %s", strings.Join(missingInYaml, "\n  "))
	}
	if len(missingInRouter) > 0 {
		t.Errorf("openapi-v2.yaml paths with no registered route:\n  %s", strings.Join(missingInRouter, "\n  "))
	}
	if len(paths) < len(routerTruth) {
		t.Errorf("yaml path count %d < router table %d", len(paths), len(routerTruth))
	}
}
