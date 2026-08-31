package httpapi

// router_groups.go — the only route registry. The API is a single unversioned
// tree: member surface under /api, anonymous flows and the public-site read
// face under /api/public, the technical OpenAPI surface under /api/open and
// the operator surface under /api/admin. No /api/v2, /api/frontend or
// /api/open/v1 prefixes exist anymore (dev-stage cleanup, no redirects).

import "net/http"

func newRouter(deps Dependencies) *http.ServeMux {
	mux := http.NewServeMux()
	registerSystemRoutes(deps, mux)
	registerPublicRoutes(deps, mux)
	registerIdentityRoutes(deps, mux)
	registerOrganizationRoutes(deps, mux)
	registerWorkspaceRoutes(deps, mux)
	registerAssetRoutes(deps, mux)
	registerSuggestionRoutes(deps, mux)
	registerReviewRoutes(deps, mux)
	registerTagRoutes(deps, mux)
	registerSiteRoutes(deps, mux)
	registerQueryRoutes(deps, mux)
	registerAttachmentRoutes(deps, mux)
	registerContainerRoutes(deps, mux)
	registerConversationRoutes(deps, mux)
	registerAgentRoutes(deps, mux)
	registerAutomationRoutes(deps, mux)
	registerTransferRoutes(deps, mux)
	registerNotificationRoutes(deps, mux)
	registerModelRoutes(deps, mux)
	registerOpenRoutes(deps, mux)
	registerAdminRoutes(deps, mux)
	return mux
}

func registerSystemRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/readyz", readyRoute(deps))
}

// registerPublicRoutes holds the anonymous surface: email login, password
// reset and invitation resolve/accept plus the public-site read face
// (home/posts/detail/sections/tags/search with D4 ETags and the shared
// public_site_ip budget). Safe reads and one-time tokens only: no
// idempotency contract applies (requiresHTTPIdempotency excludes /api/public).
func registerPublicRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/public/sessions", CreateSession(deps))
	mux.HandleFunc("/api/public/password-resets", RequestPasswordReset(deps))
	mux.HandleFunc("/api/public/password-resets/resolve", ResolvePasswordReset(deps))
	mux.HandleFunc("/api/public/password-resets/complete", CompletePasswordReset(deps))
	mux.HandleFunc("/api/public/organization-invitations/resolve", ResolveInvitation(deps))
	mux.HandleFunc("/api/public/organization-invitations/accept", AcceptInvitation(deps))
	mux.HandleFunc("/api/public/sites/{slug}", publicSiteView(deps))
	mux.HandleFunc("/api/public/sites/{slug}/posts", publicSitePosts(deps))
	mux.HandleFunc("/api/public/sites/{slug}/posts/{displayPath...}", publicSitePost(deps))
	mux.HandleFunc("/api/public/sites/{slug}/sections/{sectionSlug}", publicSiteSection(deps))
	mux.HandleFunc("/api/public/sites/{slug}/tags", publicSiteTags(deps))
	mux.HandleFunc("/api/public/sites/{slug}/tags/{key}", publicSiteTagPage(deps))
	mux.HandleFunc("/api/public/sites/{slug}/search", publicSiteSearch(deps))
}

// registerIdentityRoutes holds the member identity surface: profile,
// preferences and session management.
func registerIdentityRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/sessions/current", DeleteCurrentSession(deps))
	mux.HandleFunc("/api/sessions", ListSessions(deps))
	mux.HandleFunc("/api/sessions/{sessionId}", DeleteSession(deps))
	mux.HandleFunc("/api/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			GetMe(deps)(w, r)
			return
		}
		PatchMe(deps)(w, r)
	})
	mux.HandleFunc("/api/me/password", ChangePassword(deps))
	mux.HandleFunc("/api/me/preferences", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			GetPreferences(deps)(w, r)
			return
		}
		PatchPreferences(deps)(w, r)
	})
}

// registerOrganizationRoutes holds organization governance (profile, members,
// invitations, workspace lifecycle) and the organization-scoped retrieval and
// query-audit surfaces.
func registerOrganizationRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/organization", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			GetOrganization(deps)(w, r)
			return
		}
		PatchOrganization(deps)(w, r)
	})
	mux.HandleFunc("/api/organization/members", ListOrganizationMembers(deps))
	mux.HandleFunc("/api/organization/members/{userId}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			GetOrganizationMember(deps)(w, r)
			return
		}
		PatchOrganizationMember(deps)(w, r)
	})
	mux.HandleFunc("/api/organization/invitations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			ListOrganizationInvitations(deps)(w, r)
			return
		}
		CreateOrganizationInvitation(deps)(w, r)
	})
	mux.HandleFunc("/api/organization/invitations/{invitationId}/resend", ResendOrganizationInvitation(deps))
	mux.HandleFunc("/api/organization/invitations/{invitationId}/revoke", RevokeOrganizationInvitation(deps))
	mux.HandleFunc("/api/organization/workspaces", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			ListOrganizationWorkspaces(deps)(w, r)
			return
		}
		CreateOrganizationWorkspace(deps)(w, r)
	})
	mux.HandleFunc("/api/organization/workspaces/{workspaceId}", GetOrganizationWorkspace(deps))
	mux.HandleFunc("/api/organization/workspaces/{workspaceId}/archive", ArchiveOrganizationWorkspace(deps))
	mux.HandleFunc("/api/organization/workspaces/{workspaceId}/restore", RestoreOrganizationWorkspace(deps))
	mux.HandleFunc("/api/organization/workspaces/{workspaceId}/members", GrantOrganizationWorkspaceMember(deps))
	mux.HandleFunc("/api/organization/workspaces/{workspaceId}/members/{membershipId}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			PatchOrganizationWorkspaceMember(deps)(w, r)
			return
		}
		RevokeOrganizationWorkspaceMember(deps)(w, r)
	})
	mux.HandleFunc("/api/organization/query", OrganizationQuery(deps))
	mux.HandleFunc("/api/organization/retrieval/profiles", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			ListRetrievalProfiles(deps)(w, r)
			return
		}
		CreateRetrievalProfile(deps)(w, r)
	})
	mux.HandleFunc("/api/organization/retrieval/profiles/{profileId}/activate", ActivateRetrievalProfile(deps))
	mux.HandleFunc("/api/organization/retrieval/rebuilds", OrganizationRetrievalRebuilds(deps))
	mux.HandleFunc("/api/organization/retrieval/rebuilds/{rebuildId}", OrganizationRebuildGet(deps))
	mux.HandleFunc("/api/organization/query-executions", OrganizationQueryExecutions(deps))
}

// registerWorkspaceRoutes holds the member workspace surface: the membership
// list, invitations and the personal workspace list.
func registerWorkspaceRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces", ListMyWorkspaces(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			GetWorkspace(deps)(w, r)
			return
		}
		PatchWorkspace(deps)(w, r)
	})
	mux.HandleFunc("/api/workspaces/{workspaceId}/summary", GetWorkspaceSummary(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/members", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			ListWorkspaceMembers(deps)(w, r)
			return
		}
		AddWorkspaceMember(deps)(w, r)
	})
	mux.HandleFunc("/api/workspaces/{workspaceId}/members/{membershipId}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			PatchWorkspaceMember(deps)(w, r)
			return
		}
		RemoveWorkspaceMember(deps)(w, r)
	})
	mux.HandleFunc("/api/workspaces/{workspaceId}/members/me/leave", LeaveWorkspace(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/eligible-members", ListEligibleWorkspaceMembers(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/invitations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			ListWorkspaceInvitations(deps)(w, r)
			return
		}
		CreateWorkspaceInvitation(deps)(w, r)
	})
	mux.HandleFunc("/api/workspaces/{workspaceId}/invitations/{invitationId}/resend", ResendWorkspaceInvitation(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/invitations/{invitationId}/revoke", RevokeWorkspaceInvitation(deps))
}

// registerAssetRoutes holds the member asset surface: workspace collection
// list/create, resource detail, versions, lineage, document tree links and
// the draft/commit/publish lifecycle. Publish, archive and restore use the
// contract handlers (mandatory Idempotency-Key, data envelope) — the retired member
// variants were dropped with the /api/frontend prefix.
func registerAssetRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces/{workspaceId}/assets", memberAssetsCollection(deps))
	mux.HandleFunc("/api/assets/{assetId}", assetResource(deps))
	mux.HandleFunc("/api/assets/{assetId}/lineage", assetLineage(deps))
	mux.HandleFunc("/api/assets/{assetId}/versions", assetVersionCollection(deps))
	mux.HandleFunc("/api/assets/{assetId}/draft", AssetDraft(deps))
	mux.HandleFunc("/api/assets/{assetId}/commit-draft", CommitDraft(deps))
	mux.HandleFunc("/api/assets/{assetId}/publish", memberPublishAsset(deps))
	mux.HandleFunc("/api/assets/{assetId}/archive", memberArchiveAsset(deps))
	mux.HandleFunc("/api/assets/{assetId}/restore", memberRestoreAsset(deps))
	mux.HandleFunc("/api/assets/{assetId}/duplicate", duplicateAsset(deps))
	mux.HandleFunc("/api/assets/{assetId}/move", moveAssetToContainer(deps))
	mux.HandleFunc("/api/assets/{assetId}/document-parent", documentParentResource(deps))
	mux.HandleFunc("/api/assets/{assetId}/document-children", documentChildren(deps))
	mux.HandleFunc("/api/assets/{assetId}/containers", assetContainers(deps))
	mux.HandleFunc("/api/asset-versions/{versionId}", assetVersionResource(deps))
	mux.HandleFunc("/api/asset-versions/{versionId}/processing", assetVersionProcessing(deps))
	mux.HandleFunc("/api/asset-versions/{versionId}/confirm", ConfirmVersion(deps))
}

// registerSuggestionRoutes holds the member review surface: the unified
// suggestion queue, single and batch decisions, agent processing results and
// member-initiated asset preparation runs.
func registerSuggestionRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces/{workspaceId}/assets/{assetId}/suggestions", AssetSuggestions(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/assets/{assetId}/suggestions/accept-batch", AssetSuggestionsAcceptBatch(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/assets/{assetId}/processing-results", AssetProcessingResults(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/assets/{assetId}/prepare", AssetPrepare(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/accept", SuggestionAccept(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/reject", SuggestionReject(deps))
}

// registerReviewRoutes holds the publication-request workflow: submit/list/
// get/comment/approve/reject/cancel and the batch endpoint.
func registerReviewRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces/{workspaceId}/publication-requests", PublicationRequests(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/publication-requests/batch", PublicationBatch(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/publication-requests/{requestId}", PublicationRequest(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/publication-requests/{requestId}/approve", PublicationDecide(deps, "approve"))
	mux.HandleFunc("/api/workspaces/{workspaceId}/publication-requests/{requestId}/reject", PublicationDecide(deps, "reject"))
	mux.HandleFunc("/api/workspaces/{workspaceId}/publication-requests/{requestId}/cancel", PublicationCancel(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/publication-requests/{requestId}/comments", PublicationComments(deps))
}

// registerTagRoutes holds the tag domain surface: the workspace catalog,
// lifecycle commands and facet counts.
func registerTagRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces/{workspaceId}/tags", TagCollection(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/tags/{tagId}", TagResource(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/tags/{tagId}/archive", TagArchive(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/tags/{tagId}/restore", TagRestore(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/tag-facets", TagFacets(deps))
}

// registerSiteRoutes holds the public-site management surface: workspace site
// CRUD (DELETE is the soft disable), binding CRUD and the no-store JSON
// preview snapshot. Binding surfaces sit behind site.manage.
func registerSiteRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces/{workspaceId}/sites", SitesCollection(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/sites/{siteId}", SiteResource(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/sites/{siteId}/bindings", SiteBindingsCollection(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/sites/{siteId}/bindings/{bindingId}", SiteBindingResource(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/sites/{siteId}/preview", SitePreview(deps))
}

// registerQueryRoutes holds the unified query surface (workspace and member
// audit), the retrieval operations (status, rebuilds) and the query
// execution audit.
func registerQueryRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces/{workspaceId}/query", WorkspaceQuery(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/retrieval/status", WorkspaceRetrievalStatus(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/retrieval/rebuilds", WorkspaceRetrievalRebuild(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/retrieval/rebuilds/{rebuildId}", WorkspaceRebuildGet(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/query-executions", WorkspaceQueryExecutions(deps))
	mux.HandleFunc("/api/query-executions/{executionId}", QueryExecution(deps))
}

// registerAttachmentRoutes holds the member attachment surface: per-version
// attachment registration, resource detail, linking and downloads.
func registerAttachmentRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/asset-versions/{versionId}/attachments", assetVersionAttachments(deps))
	mux.HandleFunc("/api/attachments/{attachmentId}", attachmentResource(deps))
	mux.HandleFunc("/api/attachments/{attachmentId}/link", linkAttachment(deps))
	mux.HandleFunc("/api/attachments/{attachmentId}/download", memberDownloadAttachment(deps))
	mux.HandleFunc("/api/attachments/{attachmentId}/presigned-download", presignedAttachmentDownload(deps))
}

// registerContainerRoutes holds the workspace container tree: read the tree,
// create/rename/move containers and list children/assets.
func registerContainerRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces/{workspaceId}/containers/tree", containersCollection(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/containers", createContainer(deps))
	mux.HandleFunc("/api/containers/{containerId}", containerResource(deps))
	mux.HandleFunc("/api/containers/{containerId}/move", moveContainer(deps))
	mux.HandleFunc("/api/containers/{containerId}/children", containerChildren(deps))
	mux.HandleFunc("/api/containers/{containerId}/assets", listContainerAssets(deps))
}

// registerConversationRoutes holds the note/conversation surface: collections,
// messages, blocks, chat (sync and SSE stream), note sync/publish,
// derivations and media transcription.
func registerConversationRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces/{workspaceId}/conversations", conversationsCollection(deps))
	mux.HandleFunc("/api/conversations/{conversationId}", conversationResource(deps))
	mux.HandleFunc("/api/conversations/{conversationId}/archive", archiveConversation(deps))
	mux.HandleFunc("/api/conversations/{conversationId}/messages", appendMessage(deps))
	mux.HandleFunc("/api/conversations/{conversationId}/blocks", conversationBlocks(deps))
	mux.HandleFunc("/api/conversations/{conversationId}/chat", conversationChat(deps, false))
	mux.HandleFunc("/api/conversations/{conversationId}/chat/stream", conversationChat(deps, true))
	mux.HandleFunc("/api/conversations/{conversationId}/note/sync", syncConversationNote(deps))
	mux.HandleFunc("/api/conversations/{conversationId}/note", conversationNote(deps))
	mux.HandleFunc("/api/conversations/{conversationId}/note/publish", publishConversationNote(deps))
	mux.HandleFunc("/api/conversations/{conversationId}/derivations", createDerivation(deps))
	mux.HandleFunc("/api/conversations/{conversationId}/media", registerConversationMedia(deps))
	mux.HandleFunc("/api/derivations/{derivationId}", getDerivation(deps))
	mux.HandleFunc("/api/derivations/{derivationId}/finalize", finalizeDerivation(deps))
	mux.HandleFunc("/api/conversation-media/{mediaId}", getConversationMedia(deps))
	mux.HandleFunc("/api/conversation-media/{mediaId}/transcribe", transcribeConversationMedia(deps))
	mux.HandleFunc("/api/conversation-media/{mediaId}/transcript", conversationTranscript(deps))
}

// registerAgentRoutes holds the member agent surface: application registry,
// per-workspace applications, chat sessions (sync, stream, runs) and the
// run/event polling endpoints.
func registerAgentRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/agent-applications", agentApplications(deps))
	mux.HandleFunc("/api/agent-applications/{applicationId}", agentApplicationResource(deps))
	mux.HandleFunc("/api/agent-applications/{applicationId}/enable", agentApplicationStatus(deps, "active"))
	mux.HandleFunc("/api/agent-applications/{applicationId}/disable", agentApplicationStatus(deps, "disabled"))
	mux.HandleFunc("/api/agent-applications/{applicationId}/sessions", startAgentApplicationSession(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/agent-applications", workspaceAgentApplications(deps))
	mux.HandleFunc("/api/agent-sessions/{sessionId}", agentSessionResource(deps))
	mux.HandleFunc("/api/agent-sessions/{sessionId}/references/validate", validateAgentSessionReferences(deps))
	mux.HandleFunc("/api/agent-sessions/{sessionId}/chat", chatAgentSession(deps))
	mux.HandleFunc("/api/agent-sessions/{sessionId}/chat/stream", streamAgentSession(deps))
	mux.HandleFunc("/api/agent-sessions/{sessionId}/runs", agentSessionRuns(deps))
	mux.HandleFunc("/api/agent-sessions/{sessionId}/cancel", cancelAgentSession(deps))
	mux.HandleFunc("/api/agent-runs/{runId}", agentRunResource(deps))
	mux.HandleFunc("/api/agent-runs/{runId}/events", agentRunEvents(deps))
	mux.HandleFunc("/api/agent-runs/{runId}/resume", resumeAgentRun(deps))
	mux.HandleFunc("/api/agent-runs/{runId}/cancel", cancelAgentRun(deps))
}

// registerAutomationRoutes holds the scheduled job surface: job CRUD, pause/
// resume, run-now and the run/attempt inspection endpoints.
func registerAutomationRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces/{workspaceId}/automation-jobs", automationJobsCollection(deps))
	mux.HandleFunc("/api/automation-jobs/{jobId}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteAutomationJob(deps)(w, r)
			return
		}
		automationJobResource(deps)(w, r)
	})
	mux.HandleFunc("/api/automation-jobs/{jobId}/pause", pauseAutomationJob(deps, false))
	mux.HandleFunc("/api/automation-jobs/{jobId}/resume", pauseAutomationJob(deps, true))
	mux.HandleFunc("/api/automation-jobs/{jobId}/run-now", createAutomationRun(deps))
	mux.HandleFunc("/api/automation-jobs/{jobId}/runs", listAutomationRuns(deps))
	mux.HandleFunc("/api/task-runs/{runId}", getAutomationRun(deps))
	mux.HandleFunc("/api/task-runs/{runId}/attempts", listAutomationAttempts(deps))
	mux.HandleFunc("/api/task-runs/{runId}/retry", retryAutomationRun(deps))
	mux.HandleFunc("/api/task-runs/{runId}/cancel", cancelAutomationRun(deps))
	mux.HandleFunc("/api/task-runs/{runId}/events", taskRunEvents(deps))
}

// registerTransferRoutes holds the bulk data surface: asset imports/exports,
// their job inspection and download endpoints, and deletion jobs.
func registerTransferRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces/{workspaceId}/assets/imports", startImport(deps))
	mux.HandleFunc("/api/import-jobs/{jobId}", getImport(deps))
	mux.HandleFunc("/api/import-jobs/{jobId}/rows", listImportJobRows(deps))
	mux.HandleFunc("/api/import-jobs/{jobId}/errors.csv", downloadImportJobErrorsCsv(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/assets/exports", startExport(deps))
	mux.HandleFunc("/api/export-jobs/{jobId}", getExport(deps))
	mux.HandleFunc("/api/export-jobs/{jobId}/download", exportDownload(deps))
	mux.HandleFunc("/api/deletion-jobs/{jobId}", getDeletionJob(deps))
}

// registerNotificationRoutes holds the member notification surface: list,
// unread count, read markers and the SSE stream.
func registerNotificationRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces/{workspaceId}/notifications", listNotifications(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/notifications/unread-count", unreadNotificationCount(deps))
	mux.HandleFunc("/api/notifications/{notificationId}/read", markNotificationRead(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/notifications/read-all", markAllNotificationsRead(deps))
	mux.HandleFunc("/api/workspaces/{workspaceId}/notifications/stream", notificationStream(deps))
}

// registerModelRoutes holds the resource-model registry (versions, validation,
// publish/retire, migrations) and the model-endpoint registry (test/enable/
// disable).
func registerModelRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/workspaces/{workspaceId}/resource-models", resourceModelsCollection(deps))
	mux.HandleFunc("/api/resource-models/{resourceModelId}", resourceModelResource(deps))
	mux.HandleFunc("/api/resource-models/{resourceModelId}/versions", resourceModelVersionsCollection(deps))
	mux.HandleFunc("/api/resource-model-versions/{versionId}", resourceModelVersionResource(deps))
	mux.HandleFunc("/api/resource-model-versions/{versionId}/validate", validateResourceModelVersion(deps))
	mux.HandleFunc("/api/resource-model-versions/{versionId}/publish", publishResourceModelVersion(deps))
	mux.HandleFunc("/api/resource-model-versions/{versionId}/retire", retireResourceModelVersion(deps))
	mux.HandleFunc("/api/resource-models/{resourceModelId}/migration-previews", previewResourceModelMigration(deps))
	mux.HandleFunc("/api/resource-models/{resourceModelId}/migrations", startResourceModelMigration(deps))
	mux.HandleFunc("/api/resource-model-migrations/{migrationId}", getResourceModelMigration(deps))
	mux.HandleFunc("/api/resource-model-migrations/{migrationId}/cancel", cancelResourceModelMigration(deps))
	mux.HandleFunc("/api/model-endpoints", modelEndpointCollection(deps))
	mux.HandleFunc("/api/model-endpoints/{endpointId}", modelEndpointResource(deps))
	mux.HandleFunc("/api/model-endpoints/{endpointId}/test", testModelEndpoint(deps))
	mux.HandleFunc("/api/model-endpoints/{endpointId}/enable", setModelEndpointStatus(deps, "active"))
	mux.HandleFunc("/api/model-endpoints/{endpointId}/disable", setModelEndpointStatus(deps, "disabled"))
}

// registerOpenRoutes holds the technical OpenAPI surface for external agent
// credentials: webhook intake, unified query, reference validation, multi-
// asset agent tasks, asset mutations and the automation run callback.
func registerOpenRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/open/hooks/assets", webhookCreateAsset(deps))
	mux.HandleFunc("/api/open/query", OpenQuery(deps))
	mux.HandleFunc("/api/open/references/validate", OpenReferenceValidate(deps))
	mux.HandleFunc("/api/open/agent-tasks", createAgentTask(deps))
	mux.HandleFunc("/api/open/agent-tasks/{taskId}", getAgentTask(deps))
	mux.HandleFunc("/api/open/assets", createAsset(deps))
	mux.HandleFunc("/api/open/assets/{assetId}", updateAsset(deps))
	mux.HandleFunc("/api/open/assets/{assetId}/publish", publishAsset(deps))
	mux.HandleFunc("/api/open/assets/{assetId}/archive", archiveAsset(deps))
	mux.HandleFunc("/api/open/assets/{assetId}/references", assetReferences(deps))
	mux.HandleFunc("/api/open/attachments/{attachmentId}/download", downloadAttachment(deps))
	mux.HandleFunc("/api/open/automation/runs/{runId}/callback", automationRunCallback(deps))
}

// registerAdminRoutes holds the operator surface: agent-user registration,
// access policies, API-key rotation and application oversight.
func registerAdminRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/agent-users", registerAgent(deps))
	mux.HandleFunc("/api/admin/agent-users/{agentUserId}/access-policy", replaceAgentModelPolicy(deps))
	mux.HandleFunc("/api/admin/agent-users/{agentUserId}/api-keys/rotate", rotateAgentAPIKey(deps))
	mux.HandleFunc("/api/admin/agent-users/{agentUserId}/api-keys/revoke-all", revokeAllAgentAPIKeys(deps))
	mux.HandleFunc("/api/admin/agent-users/{agentUserId}/onboarding", getAgentUserOnboarding(deps))
	mux.HandleFunc("/api/admin/agent-applications", listAgentApplications(deps))
	mux.HandleFunc("/api/admin/agent-applications/{applicationId}", getAgentApplication(deps))
	mux.HandleFunc("/api/admin/agent-applications/{applicationId}/status", updateAgentApplicationStatus(deps))
}
