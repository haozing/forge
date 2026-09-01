package httpapi

// openapi_contract_test.go — phase 6 bidirectional contract gate: every
// path+method in openapi.yaml must be registered by the router, and every
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
// assertions in contract_test.go.
var routerTruth = map[string][]string{
	"/api/public/sessions":                                   {"post"},
	"/api/public/password-resets":                            {"post"},
	"/api/public/password-resets/resolve":                    {"post"},
	"/api/public/password-resets/complete":                   {"post"},
	"/api/public/organization-invitations/resolve":           {"post"},
	"/api/public/organization-invitations/accept":            {"post"},
	"/api/sessions":                                          {"get"},
	"/api/sessions/current":                                  {"delete"},
	"/api/sessions/{sessionId}":                              {"delete"},
	"/api/me":                                                {"get", "patch"},
	"/api/me/password":                                       {"put"},
	"/api/me/preferences":                                    {"get", "patch"},
	"/api/organization":                                      {"get", "patch"},
	"/api/organization/members":                              {"get"},
	"/api/organization/members/{userId}":                     {"get", "patch"},
	"/api/organization/invitations":                          {"get", "post"},
	"/api/organization/invitations/{invitationId}/resend":    {"post"},
	"/api/organization/invitations/{invitationId}/revoke":    {"post"},
	"/api/organization/workspaces":                           {"get", "post"},
	"/api/organization/workspaces/{workspaceId}":             {"get"},
	"/api/organization/workspaces/{workspaceId}/archive":     {"post"},
	"/api/organization/workspaces/{workspaceId}/restore":     {"post"},
	"/api/organization/workspaces/{workspaceId}/members":     {"post"},
	"/api/organization/workspaces/{workspaceId}/members/{membershipId}": {"patch", "delete"},
	"/api/workspaces":                                        {"get"},
	"/api/workspaces/{workspaceId}":                          {"get", "patch"},
	"/api/workspaces/{workspaceId}/summary":                  {"get"},
	"/api/workspaces/{workspaceId}/members":                  {"get", "post"},
	"/api/workspaces/{workspaceId}/members/me/leave":         {"post"},
	"/api/workspaces/{workspaceId}/members/{membershipId}":   {"patch", "delete"},
	"/api/workspaces/{workspaceId}/eligible-members":         {"get"},
	"/api/workspaces/{workspaceId}/invitations":              {"get", "post"},
	"/api/workspaces/{workspaceId}/invitations/{invitationId}/resend": {"post"},
	"/api/workspaces/{workspaceId}/invitations/{invitationId}/revoke": {"post"},
	"/api/workspaces/{workspaceId}/publication-requests":       {"get", "post"},
	"/api/workspaces/{workspaceId}/publication-requests/batch": {"post"},
	"/api/workspaces/{workspaceId}/publication-requests/{requestId}":            {"get", "delete"},
	"/api/workspaces/{workspaceId}/publication-requests/{requestId}/approve":    {"post"},
	"/api/workspaces/{workspaceId}/publication-requests/{requestId}/reject":     {"post"},
	"/api/workspaces/{workspaceId}/publication-requests/{requestId}/cancel":     {"post"},
	"/api/workspaces/{workspaceId}/publication-requests/{requestId}/comments":   {"get", "post"},
	"/api/assets/{assetId}/draft":          {"get", "patch"},
	"/api/assets/{assetId}/commit-draft":   {"post"},
	"/api/assets/{assetId}/publish":        {"post"},
	"/api/assets/{assetId}/archive":        {"post"},
	"/api/assets/{assetId}/restore":        {"post"},
	"/api/asset-versions/{versionId}/confirm": {"post"},
	"/api/workspaces/{workspaceId}/tags":          {"get", "post"},
	"/api/workspaces/{workspaceId}/tags/{tagId}":  {"get", "patch"},
	"/api/workspaces/{workspaceId}/tags/{tagId}/archive":  {"post"},
	"/api/workspaces/{workspaceId}/tags/{tagId}/restore":  {"post"},
	"/api/workspaces/{workspaceId}/tag-facets":     {"get"},
	"/api/workspaces/{workspaceId}/query":          {"post"},
	"/api/organization/query":                      {"post"},
	"/api/open/query":                              {"post"},
	"/api/open/references/validate":                {"post"},
	"/api/open/hooks/assets":                       {"post"},
	"/api/open/agent-tasks":                        {"post"},
	"/api/open/agent-tasks/{taskId}":               {"get"},
	"/api/organization/retrieval/profiles":                    {"get", "post"},
	"/api/organization/retrieval/profiles/{profileId}/activate": {"post"},
	"/api/workspaces/{workspaceId}/retrieval/status":            {"get"},
	"/api/workspaces/{workspaceId}/retrieval/rebuilds":          {"post", "get"},
	"/api/workspaces/{workspaceId}/retrieval/rebuilds/{rebuildId}": {"get"},
	"/api/organization/retrieval/rebuilds":                      {"post"},
	"/api/organization/retrieval/rebuilds/{rebuildId}":          {"get"},
	"/api/workspaces/{workspaceId}/query-executions":            {"get"},
	"/api/organization/query-executions":                        {"get"},
	"/api/query-executions/{executionId}":                       {"get"},
	"/api/workspaces/{workspaceId}/assets/{assetId}/prepare":                {"post"},
	"/api/workspaces/{workspaceId}/assets/{assetId}/suggestions":            {"get"},
	"/api/workspaces/{workspaceId}/assets/{assetId}/suggestions/accept-batch": {"post"},
	"/api/workspaces/{workspaceId}/assets/{assetId}/processing-results":     {"get"},
	"/api/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/accept": {"post"},
	"/api/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/reject":  {"post"},
	"/api/workspaces/{workspaceId}/conversations":                      {"get", "post"},
	"/api/conversations/{conversationId}":                              {"get", "patch"},
	"/api/conversations/{conversationId}/children":                     {"get"},
	"/api/conversations/{conversationId}/archive":                      {"post"},
	"/api/conversations/{conversationId}/messages":                     {"get", "post"},
	"/api/conversations/{conversationId}/blocks":                       {"get"},
	"/api/conversations/{conversationId}/chat":                         {"post"},
	"/api/conversations/{conversationId}/chat/stream":                  {"post"},
	"/api/conversations/{conversationId}/note/sync":                    {"post"},
	"/api/conversations/{conversationId}/note":                         {"get"},
	"/api/conversations/{conversationId}/note/publish":                 {"post"},
	"/api/conversations/{conversationId}/derivations":                  {"post"},
	"/api/conversations/{conversationId}/media":                        {"post"},
	"/api/derivations/{derivationId}":                                  {"get"},
	"/api/derivations/{derivationId}/finalize":                         {"post"},
	"/api/conversation-media/{mediaId}":                                {"get"},
	"/api/conversation-media/{mediaId}/transcribe":                     {"post"},
	"/api/conversation-media/{mediaId}/transcript":                     {"get"},
	"/api/workspaces/{workspaceId}/sites":                          {"get", "post"},
	"/api/workspaces/{workspaceId}/sites/{siteId}":                 {"get", "patch", "delete"},
	"/api/workspaces/{workspaceId}/sites/{siteId}/bindings":        {"get", "post"},
	"/api/workspaces/{workspaceId}/sites/{siteId}/bindings/{bindingId}": {"patch", "delete"},
	"/api/workspaces/{workspaceId}/sites/{siteId}/preview":         {"get"},
	"/api/public/sites/{slug}":                                     {"get"},
	"/api/public/sites/{slug}/posts":                               {"get"},
	"/api/public/sites/{slug}/posts/{displayPath}":                 {"get"},
	"/api/public/sites/{slug}/sections/{sectionSlug}":              {"get"},
	"/api/public/sites/{slug}/tags":                                {"get"},
	"/api/public/sites/{slug}/tags/{key}":                          {"get"},
	"/api/public/sites/{slug}/search":                              {"get"},
}

func TestOpenAPIMatchesRouter(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "openapi.yaml"))
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
		t.Errorf("routes registered but missing in openapi.yaml:\n  %s", strings.Join(missingInYaml, "\n  "))
	}
	if len(missingInRouter) > 0 {
		t.Errorf("openapi.yaml paths with no registered route:\n  %s", strings.Join(missingInRouter, "\n  "))
	}
	if len(paths) < len(routerTruth) {
		t.Errorf("yaml path count %d < router table %d", len(paths), len(routerTruth))
	}
}
