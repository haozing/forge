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
	Path   string
	Method string
}

var openapiPathRe = regexp.MustCompile(`(?m)^  (/(?:api)[^\s]*):\s*$`)
var openapiMethodRe = regexp.MustCompile(`(?m)^    ([a-z]+):\s*$`)

// routerTruth is the hand-maintained registry of every /api route the mux
// serves, with the methods each handler accepts. Kept beside the contract
// assertions in contract_test.go.
var routerTruth = map[string][]string{
	"/api/public/sessions":                                                    {"post"},
	"/api/public/password-resets":                                             {"post"},
	"/api/public/password-resets/resolve":                                     {"post"},
	"/api/public/password-resets/complete":                                    {"post"},
	"/api/public/organization-invitations/resolve":                            {"post"},
	"/api/public/organization-invitations/accept":                             {"post"},
	"/api/sessions":                                                           {"get"},
	"/api/sessions/current":                                                   {"delete"},
	"/api/sessions/{sessionId}":                                               {"delete"},
	"/api/me":                                                                 {"get", "patch"},
	"/api/me/password":                                                        {"put"},
	"/api/me/preferences":                                                     {"get", "patch"},
	"/api/organization":                                                       {"get", "patch"},
	"/api/organization/members":                                               {"get"},
	"/api/organization/members/{userId}":                                      {"get", "patch"},
	"/api/organization/invitations":                                           {"get", "post"},
	"/api/organization/invitations/{invitationId}/resend":                     {"post"},
	"/api/organization/invitations/{invitationId}/revoke":                     {"post"},
	"/api/organization/workspaces":                                            {"get", "post"},
	"/api/organization/workspaces/{workspaceId}":                              {"get"},
	"/api/organization/workspaces/{workspaceId}/archive":                      {"post"},
	"/api/organization/workspaces/{workspaceId}/restore":                      {"post"},
	"/api/organization/workspaces/{workspaceId}/members":                      {"post"},
	"/api/organization/workspaces/{workspaceId}/members/{membershipId}":       {"patch", "delete"},
	"/api/workspaces":                                                         {"get"},
	"/api/workspaces/{workspaceId}":                                           {"get", "patch"},
	"/api/workspaces/{workspaceId}/summary":                                   {"get"},
	"/api/workspaces/{workspaceId}/members":                                   {"get", "post"},
	"/api/workspaces/{workspaceId}/members/me/leave":                          {"post"},
	"/api/workspaces/{workspaceId}/members/{membershipId}":                    {"patch", "delete"},
	"/api/workspaces/{workspaceId}/eligible-members":                          {"get"},
	"/api/workspaces/{workspaceId}/invitations":                               {"get", "post"},
	"/api/workspaces/{workspaceId}/invitations/{invitationId}/resend":         {"post"},
	"/api/workspaces/{workspaceId}/invitations/{invitationId}/revoke":         {"post"},
	"/api/workspaces/{workspaceId}/publication-requests":                      {"get", "post"},
	"/api/workspaces/{workspaceId}/publication-requests/batch":                {"post"},
	"/api/workspaces/{workspaceId}/publication-requests/{requestId}":          {"get", "delete"},
	"/api/workspaces/{workspaceId}/publication-requests/{requestId}/approve":  {"post"},
	"/api/workspaces/{workspaceId}/publication-requests/{requestId}/reject":   {"post"},
	"/api/workspaces/{workspaceId}/publication-requests/{requestId}/cancel":   {"post"},
	"/api/workspaces/{workspaceId}/publication-requests/{requestId}/comments": {"get", "post"},
	"/api/assets/{assetId}/draft":                                             {"get", "patch"},
	"/api/assets/{assetId}/commit-draft":                                      {"post"},
	"/api/assets/{assetId}/publish":                                           {"post"},
	"/api/assets/{assetId}/archive":                                           {"post"},
	"/api/assets/{assetId}/restore":                                           {"post"},
	"/api/asset-versions/{versionId}/confirm":                                 {"post"},
	"/api/workspaces/{workspaceId}/tags":                                      {"get", "post"},
	"/api/workspaces/{workspaceId}/tags/{tagId}":                              {"get", "patch"},
	"/api/workspaces/{workspaceId}/tags/{tagId}/archive":                      {"post"},
	"/api/workspaces/{workspaceId}/tags/{tagId}/restore":                      {"post"},
	"/api/workspaces/{workspaceId}/tag-facets":                                {"get"},
	"/api/workspaces/{workspaceId}/query":                                     {"post"},
	"/api/organization/style-presets":                                         {"get", "post"},
	"/api/organization/style-presets/{presetId}":                              {"delete"},
	"/api/organization/query":                                                 {"post"},
	"/api/open/query":                                                         {"post"},
	"/api/open/references/validate":                                           {"post"},
	"/api/open/hooks/assets":                                                  {"post"},
	"/api/open/agent-tasks":                                                   {"post"},
	"/api/open/agent-tasks/{taskId}":                                          {"get"},
	"/api/organization/retrieval/profiles":                                    {"get", "post"},
	"/api/organization/retrieval/profiles/{profileId}/activate":               {"post"},
	"/api/workspaces/{workspaceId}/retrieval/status":                          {"get"},
	"/api/workspaces/{workspaceId}/retrieval/rebuilds":                        {"post", "get"},
	"/api/workspaces/{workspaceId}/retrieval/rebuilds/{rebuildId}":            {"get"},
	"/api/organization/retrieval/rebuilds":                                    {"post"},
	"/api/organization/retrieval/rebuilds/{rebuildId}":                        {"get"},
	"/api/workspaces/{workspaceId}/query-executions":                          {"get"},
	"/api/organization/query-executions":                                      {"get"},
	"/api/query-executions/{executionId}":                                     {"get"},
	"/api/workspaces/{workspaceId}/assets/{assetId}/prepare":                  {"post"},
	"/api/workspaces/{workspaceId}/assets/{assetId}/suggestions":              {"get"},
	"/api/workspaces/{workspaceId}/assets/{assetId}/suggestions/accept-batch": {"post"},
	"/api/workspaces/{workspaceId}/assets/{assetId}/processing-results":       {"get"},
	"/api/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/accept":  {"post"},
	"/api/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/reject":  {"post"},
	"/api/workspaces/{workspaceId}/conversations":                             {"get", "post"},
	"/api/conversations/{conversationId}":                                     {"get", "patch", "delete"},
	"/api/conversations/{conversationId}/children":                            {"get"},
	"/api/conversations/{conversationId}/archive":                             {"post"},
	"/api/conversations/{conversationId}/messages":                            {"get", "post"},
	"/api/conversations/{conversationId}/blocks":                              {"get"},
	"/api/conversations/{conversationId}/chat":                                {"post"},
	"/api/conversations/{conversationId}/chat/stream":                         {"post"},
	"/api/conversations/{conversationId}/note":                                {"get"},
	"/api/conversations/{conversationId}/note/blocks":                         {"get", "post"},
	"/api/conversations/{conversationId}/note/blocks/{blockId}":               {"patch", "delete"},
	"/api/conversations/{conversationId}/derivations":                         {"post"},
	"/api/conversations/{conversationId}/media":                               {"post"},
	"/api/derivations/{derivationId}":                                         {"get"},
	"/api/derivations/{derivationId}/finalize":                                {"post"},
	"/api/conversation-media/{mediaId}":                                       {"get"},
	"/api/conversation-media/{mediaId}/transcribe":                            {"post"},
	"/api/conversation-media/{mediaId}/transcript":                            {"get"},
	"/api/workspaces/{workspaceId}/sites":                                     {"get", "post"},
	"/api/workspaces/{workspaceId}/sites/{siteId}":                            {"get", "patch", "delete"},
	"/api/workspaces/{workspaceId}/sites/{siteId}/bindings":                   {"get", "post"},
	"/api/workspaces/{workspaceId}/sites/{siteId}/bindings/{bindingId}":       {"patch", "delete"},
	"/api/workspaces/{workspaceId}/sites/{siteId}/preview":                    {"get", "post"},
	"/api/workspaces/{workspaceId}/sites/{siteId}/releases":                   {"get", "post"},
	"/api/workspaces/{workspaceId}/sites/{siteId}/comments":                   {"get"},
	"/api/workspaces/{workspaceId}/sites/{siteId}/comments/{commentId}":       {"patch", "delete"},
	"/api/public/sites/{slug}":                                                {"get"},
	"/api/public/sites/{slug}/posts":                                          {"get"},
	"/api/public/sites/{slug}/comments":                                       {"post"},
	"/api/public/sites/{slug}/posts/{displayPath}":                            {"get"},
	"/api/public/sites/{slug}/sections/{sectionSlug}":                         {"get"},
	"/api/public/sites/{slug}/tags":                                           {"get"},
	"/api/public/sites/{slug}/tags/{key}":                                     {"get"},
	"/api/public/sites/{slug}/search":                                         {"get"},
	"/api/admin/agent-applications":                                           {"get"},
	"/api/admin/agent-applications/{applicationId}":                           {"get"},
	"/api/admin/agent-applications/{applicationId}/status":                    {"patch"},
	"/api/admin/agent-users":                                                  {"post"},
	"/api/admin/agent-users/{agentUserId}/access-policy":                      {"put"},
	"/api/admin/agent-users/{agentUserId}/api-keys/revoke-all":                {"post"},
	"/api/admin/agent-users/{agentUserId}/api-keys/rotate":                    {"post"},
	"/api/admin/agent-users/{agentUserId}/onboarding":                         {"get"},
	"/api/agent-applications":                                                 {"get"},
	"/api/agent-applications/{applicationId}":                                 {"get", "patch"},
	"/api/agent-applications/{applicationId}/disable":                         {"post"},
	"/api/agent-applications/{applicationId}/enable":                          {"post"},
	"/api/agent-applications/{applicationId}/sessions":                        {"post"},
	"/api/agent-runs/{runId}":                                                 {"get"},
	"/api/agent-runs/{runId}/cancel":                                          {"post"},
	"/api/agent-runs/{runId}/events":                                          {"get"},
	"/api/agent-runs/{runId}/resume":                                          {"post"},
	"/api/agent-sessions/{sessionId}":                                         {"get"},
	"/api/agent-sessions/{sessionId}/cancel":                                  {"post"},
	"/api/agent-sessions/{sessionId}/chat":                                    {"post"},
	"/api/agent-sessions/{sessionId}/chat/stream":                             {"post"},
	"/api/agent-sessions/{sessionId}/references/validate":                     {"post"},
	"/api/agent-sessions/{sessionId}/runs":                                    {"post"},
	"/api/asset-versions/{versionId}":                                         {"get"},
	"/api/asset-versions/{versionId}/attachments":                             {"get"},
	"/api/asset-versions/{versionId}/processing":                              {"get"},
	"/api/assets/{assetId}":                                                   {"get", "patch", "delete"},
	"/api/assets/{assetId}/duplicate":                                         {"post"},
	"/api/assets/{assetId}/lineage":                                           {"get"},
	"/api/assets/{assetId}/relations":                                         {"get"},
	"/api/assets/{assetId}/source-conversation":                               {"get"},
	"/api/assets/{assetId}/versions":                                          {"get"},
	"/api/attachments/{attachmentId}":                                         {"get", "patch", "delete"},
	"/api/attachments/{attachmentId}/download":                                {"get"},
	"/api/attachments/{attachmentId}/link":                                    {"post"},
	"/api/attachments/{attachmentId}/presigned-download":                      {"post"},
	"/api/deletion-jobs/{jobId}":                                              {"get"},
	"/api/export-jobs/{jobId}":                                                {"get"},
	"/api/export-jobs/{jobId}/download":                                       {"get"},
	"/api/import-jobs/{jobId}":                                                {"get"},
	"/api/import-jobs/{jobId}/errors.csv":                                     {"get"},
	"/api/import-jobs/{jobId}/rows":                                           {"get"},
	"/api/model-endpoints":                                                    {"get", "post"},
	"/api/model-endpoints/{endpointId}":                                       {"get", "put"},
	"/api/model-endpoints/{endpointId}/disable":                               {"post"},
	"/api/model-endpoints/{endpointId}/enable":                                {"post"},
	"/api/model-endpoints/{endpointId}/test":                                  {"post"},
	"/api/notifications/{notificationId}/read":                                {"post"},
	"/api/open/assets":                                                        {"post"},
	"/api/open/assets/{assetId}":                                              {"patch"},
	"/api/open/assets/{assetId}/archive":                                      {"post"},
	"/api/open/assets/{assetId}/publish":                                      {"post"},
	"/api/open/assets/{assetId}/references":                                   {"get"},
	"/api/open/attachments/{attachmentId}/download":                           {"get"},
	"/api/open/workspaces/{workspaceId}/publication-requests":                 {"post"},
	"/api/resource-model-versions/{versionId}":                                {"get", "patch"},
	"/api/resource-model-versions/{versionId}/publish":                        {"post"},
	"/api/resource-model-versions/{versionId}/retire":                         {"post"},
	"/api/resource-model-versions/{versionId}/validate":                       {"post"},
	"/api/resource-models/{resourceModelId}":                                  {"get", "patch"},
	"/api/resource-models/{resourceModelId}/versions":                         {"get", "post"},
	"/api/task-runs/{runId}":                                                  {"get"},
	"/api/task-runs/{runId}/attempts":                                         {"get"},
	"/api/task-runs/{runId}/cancel":                                           {"post"},
	"/api/task-runs/{runId}/events":                                           {"get"},
	"/api/workspaces/{workspaceId}/agent-applications":                        {"get", "post"},
	"/api/workspaces/{workspaceId}/assets":                                    {"get", "post"},
	"/api/workspaces/{workspaceId}/assets/exports":                            {"post"},
	"/api/workspaces/{workspaceId}/assets/imports":                            {"post"},
	"/api/workspaces/{workspaceId}/attachments":                               {"post"},
	"/api/workspaces/{workspaceId}/notifications":                             {"get"},
	"/api/workspaces/{workspaceId}/notifications/read-all":                    {"post"},
	"/api/workspaces/{workspaceId}/notifications/stream":                      {"get"},
	"/api/workspaces/{workspaceId}/notifications/unread-count":                {"get"},
	"/api/workspaces/{workspaceId}/resource-models":                           {"get", "post"},
}

// parseOpenAPIPathMethods extracts the top-level paths block and each path's
// immediate operation methods from the raw yaml document.
func parseOpenAPIPathMethods(doc string) map[string][]string {
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
	return paths
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
	paths := parseOpenAPIPathMethods(string(raw))

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

// routerSourcePathRe extracts every registered mux pattern straight from the
// router source; wildcardTailRe normalizes the {name...} subtree syntax to a
// plain {name} placeholder so patterns align with OpenAPI path templates.
var (
	routerSourcePathRe = regexp.MustCompile(`mux\.HandleFunc\("([^"]+)"`)
	wildcardTailRe     = regexp.MustCompile(`\{([^}/]+)\.\.\.\}`)
)

// TestOpenAPIPathCoverageMatchesRouterSource is the path-level extraction gate:
// unlike routerTruth (method-level and hand-maintained), it derives the route
// table from router_groups.go itself and diff-checks the path keys of
// openapi.yaml bidirectionally. A route registered in the source but absent
// from the contract fails, and vice versa.
func TestOpenAPIPathCoverageMatchesRouterSource(t *testing.T) {
	rawSource, err := os.ReadFile(filepath.Join(".", "router_groups.go"))
	if err != nil {
		t.Fatalf("read router source: %v", err)
	}
	routerPaths := map[string]bool{}
	for _, m := range routerSourcePathRe.FindAllStringSubmatch(string(rawSource), -1) {
		pattern := m[1]
		if !strings.HasPrefix(pattern, "/api/") {
			continue
		}
		routerPaths[wildcardTailRe.ReplaceAllString(pattern, "{$1}")] = true
	}
	if len(routerPaths) == 0 {
		t.Fatal("no /api patterns extracted from router_groups.go")
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	rawDoc, err := os.ReadFile(filepath.Join(root, "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi: %v", err)
	}
	yamlPaths := map[string]bool{}
	for path := range parseOpenAPIPathMethods(string(rawDoc)) {
		yamlPaths[path] = true
	}

	var missingInYaml, missingInRouter []string
	for path := range routerPaths {
		if !yamlPaths[path] {
			missingInYaml = append(missingInYaml, path)
		}
	}
	for path := range yamlPaths {
		if !routerPaths[path] {
			missingInRouter = append(missingInRouter, path)
		}
	}
	sort.Strings(missingInYaml)
	sort.Strings(missingInRouter)
	if len(missingInYaml) > 0 {
		t.Errorf("paths registered in router_groups.go but missing in openapi.yaml:\n  %s", strings.Join(missingInYaml, "\n  "))
	}
	if len(missingInRouter) > 0 {
		t.Errorf("openapi.yaml paths with no registration in router_groups.go:\n  %s", strings.Join(missingInRouter, "\n  "))
	}
}
