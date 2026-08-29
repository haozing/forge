package httpapi

// router_groups.go — the only route registry. Groups follow the v2 contract:
// legacy routes stay only as ledger-tracked pending retirements; every new
// capability registers under /api/v2, /api/open/v2 or /api/public/v2.

import "net/http"

func newRouter(deps Dependencies) *http.ServeMux {
	mux := http.NewServeMux()
	registerSystemRoutes(deps, mux)
	registerSessionRoutes(deps, mux)
	registerV2WorkspaceRoutes(deps, mux)
	registerV2AssetRoutes(deps, mux)
	registerV2ReviewRoutes(deps, mux)
	registerOpenV2Routes(deps, mux)
	registerPublicV2Routes(deps, mux)
	registerLegacyIdentityRoutes(deps, mux)     // ledger: retire in phase 1
	registerLegacyWorkspaceRoutes(deps, mux)    // ledger: retire in phase 1
	registerLegacyModelRoutes(deps, mux)        // ledger: retire in phase 4-6
	registerLegacyAssetRoutes(deps, mux)        // ledger: retire in phase 2
	registerLegacyRetrievalRoutes(deps, mux)    // ledger: retire in phase 3
	registerLegacyTransferRoutes(deps, mux)     // ledger: retire in phase 2
	registerLegacyReviewRoutes(deps, mux)       // empty: retired in phase 0
	registerLegacyContainerRoutes(deps, mux)    // ledger: retire in phase 4-6
	registerLegacyAutomationRoutes(deps, mux)   // ledger: retire in phase 4-6
	registerLegacyConversationRoutes(deps, mux) // ledger: retire in phase 4-6
	registerLegacyAgentRoutes(deps, mux)        // ledger: retire in phase 4-6
	registerLegacyAdminRoutes(deps, mux)        // ledger: retire in phase 4-6
	return mux
}

func registerSystemRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/readyz", readyRoute(deps))
}

func registerSessionRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/sessions", sessionResource(deps.SessionService)) // ledger: replaced by /api/public/v2/sessions in phase 1
}

// registerV2WorkspaceRoutes holds /api/v2 workspace governance endpoints;
// they arrive with phase 1 and are intentionally absent in phase 0.
func registerV2WorkspaceRoutes(deps Dependencies, mux *http.ServeMux) {
	_ = deps
}

func registerV2AssetRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/v2/assets/{assetId}/draft", v2AssetDraft(deps))
	mux.HandleFunc("/api/v2/assets/{assetId}/commit-draft", v2CommitDraft(deps))
	mux.HandleFunc("/api/v2/assets/{assetId}/publish", v2PublishAsset(deps))
	mux.HandleFunc("/api/v2/assets/{assetId}/archive", v2ArchiveAsset(deps))
	mux.HandleFunc("/api/v2/assets/{assetId}/restore", v2RestoreAsset(deps))
	mux.HandleFunc("/api/v2/asset-versions/{versionId}/confirm", v2ConfirmVersion(deps))
}

func registerV2ReviewRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/publication-requests", v2PublicationRequests(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/publication-requests/batch", v2PublicationBatch(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}", v2PublicationRequest(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}/approve", v2PublicationDecide(deps, "approve"))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}/reject", v2PublicationDecide(deps, "reject"))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}/cancel", v2PublicationCancel(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}/comments", v2PublicationComments(deps))
}

func registerOpenV2Routes(deps Dependencies, mux *http.ServeMux) {
	_ = deps
	// /api/open/v2/query arrives with phase 3; webhook intake moves to
	// /api/open/v2 in phase 2.
}

func registerPublicV2Routes(deps Dependencies, mux *http.ServeMux) {
	_ = deps
	// /api/public/v2/sites/... arrives with phase 5.
}

func registerLegacyIdentityRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/me", currentUserFinal(deps))
	mux.HandleFunc("/api/me/profile", currentUserProfileFinal(deps))
	mux.HandleFunc("/api/frontend/me/preferences", memberPreferences(deps))
}

func registerLegacyWorkspaceRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/frontend/workspaces", workspaceCollectionFinal(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}", workspaceResourceFinal(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/members/me", getWorkspaceMember(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/members", listWorkspaceMembersFinal(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/member-invitations", workspaceInvitationsFinal(deps))
	mux.HandleFunc("/api/frontend/member-invitations/{invitationId}/accept", acceptWorkspaceInvitationFinal(deps))
	mux.HandleFunc("/api/frontend/member-invitations/{invitationId}/revoke", revokeWorkspaceInvitationFinal(deps))
	mux.HandleFunc("/api/frontend/workspace-members/{memberId}", workspaceMemberResourceFinal(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/counts", getWorkspaceCounts(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/stats", workspaceStats(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/activity", workspaceActivity(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/audit-logs", auditLogsFinal(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/settings", workspaceSettings(deps))
}

func registerLegacyModelRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/resource-models", resourceModelsCollection(deps))
	mux.HandleFunc("/api/frontend/resource-models/{resourceModelId}", resourceModelResource(deps))
	mux.HandleFunc("/api/frontend/resource-models/{resourceModelId}/versions", resourceModelVersionsCollection(deps))
	mux.HandleFunc("/api/frontend/resource-model-versions/{versionId}", resourceModelVersionResource(deps))
	mux.HandleFunc("/api/frontend/resource-model-versions/{versionId}/validate", validateResourceModelVersion(deps))
	mux.HandleFunc("/api/frontend/resource-model-versions/{versionId}/publish", publishResourceModelVersion(deps))
	mux.HandleFunc("/api/frontend/resource-model-versions/{versionId}/retire", retireResourceModelVersion(deps))
	mux.HandleFunc("/api/frontend/resource-models/{resourceModelId}/migration-previews", previewResourceModelMigration(deps))
	mux.HandleFunc("/api/frontend/resource-models/{resourceModelId}/migrations", startResourceModelMigration(deps))
	mux.HandleFunc("/api/frontend/resource-model-migrations/{migrationId}", getResourceModelMigration(deps))
	mux.HandleFunc("/api/frontend/resource-model-migrations/{migrationId}/cancel", cancelResourceModelMigration(deps))
	mux.HandleFunc("/api/frontend/model-endpoints", modelEndpointCollection(deps))
	mux.HandleFunc("/api/frontend/model-endpoints/{endpointId}", modelEndpointResource(deps))
	mux.HandleFunc("/api/frontend/model-endpoints/{endpointId}/test", testModelEndpoint(deps))
	mux.HandleFunc("/api/frontend/model-endpoints/{endpointId}/enable", setModelEndpointStatus(deps, "active"))
	mux.HandleFunc("/api/frontend/model-endpoints/{endpointId}/disable", setModelEndpointStatus(deps, "disabled"))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/agent-applications", workspaceAgentApplicationsFinal(deps))
}

func registerLegacyAssetRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/assets", memberAssetsCollection(deps))
	mux.HandleFunc("/api/frontend/assets/{assetId}", assetResourceFinal(deps))
	mux.HandleFunc("/api/frontend/assets/{assetId}/lineage", assetLineage(deps))
	mux.HandleFunc("/api/frontend/assets/{assetId}/versions", assetVersionCollectionFinal(deps))
	mux.HandleFunc("/api/frontend/asset-versions/{versionId}", assetVersionResourceFinal(deps))
	mux.HandleFunc("/api/frontend/asset-versions/{versionId}/processing", assetVersionProcessing(deps))
	mux.HandleFunc("/api/frontend/assets/{assetId}/publish", publishMemberAsset(deps))
	mux.HandleFunc("/api/frontend/assets/{assetId}/archive", archiveMemberAsset(deps))
	mux.HandleFunc("/api/frontend/assets/{assetId}/restore", restoreAssetFinal(deps))
	mux.HandleFunc("/api/frontend/assets/{assetId}/duplicate", duplicateAssetFinal(deps))
	mux.HandleFunc("/api/frontend/asset-versions/{versionId}/attachments", frontendAssetVersionAttachments(deps))
	mux.HandleFunc("/api/frontend/attachments/{attachmentId}", frontendAttachmentResource(deps))
	mux.HandleFunc("/api/frontend/attachments/{attachmentId}/link", linkFrontendAttachment(deps))
	mux.HandleFunc("/api/frontend/attachments/{attachmentId}/download", memberDownloadAttachment(deps))
	mux.HandleFunc("/api/frontend/attachments/{attachmentId}/presigned-download", presignedAttachmentDownloadFinal(deps))
	mux.HandleFunc("/api/frontend/assets/{assetId}/containers", assetContainersFinal(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/assets/{assetId}/move", moveAssetToContainer(deps))
	mux.HandleFunc("/api/frontend/assets/{assetId}/move", moveAssetToContainer(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/assets/{assetId}/document-parent", documentParentResource(deps))
	mux.HandleFunc("/api/frontend/assets/{assetId}/document-parent", documentParentResource(deps))
	mux.HandleFunc("/api/frontend/assets/{assetId}/document-children", documentChildrenFinal(deps))
	mux.HandleFunc("/api/attachments/{attachmentId}", attachmentStatus(deps))
	mux.HandleFunc("/api/attachments/{attachmentId}/download", memberDownloadAttachment(deps))
}

func registerLegacyRetrievalRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/query", memberQuery(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/index/status", retrievalIndexStatus(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/index/rebuild", rebuildRetrievalIndex(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/query-audit", queryAuditLogs(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/search/suggestions", searchSuggestions(deps))
	mux.HandleFunc("/api/frontend/assets/{assetId}/index/retry", retryAssetIndex(deps))
	mux.HandleFunc("/api/open/v1/query", unifiedQueryR3(deps))
}

func registerLegacyTransferRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/assets/imports", startImport(deps))
	mux.HandleFunc("/api/frontend/import-jobs/{jobId}", getImport(deps))
	mux.HandleFunc("/api/frontend/import-jobs/{jobId}/rows", listImportJobRows(deps))
	mux.HandleFunc("/api/frontend/import-jobs/{jobId}/errors.csv", downloadImportJobErrorsCsv(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/assets/exports", startExport(deps))
	mux.HandleFunc("/api/frontend/export-jobs/{jobId}", getExport(deps))
	mux.HandleFunc("/api/frontend/export-jobs/{jobId}/download", exportDownloadFinal(deps))
	mux.HandleFunc("/api/frontend/deletion-jobs/{jobId}", getDeletionJob(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/notifications", listNotificationsFinal(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/notifications/unread-count", unreadNotificationCountFinal(deps))
	mux.HandleFunc("/api/frontend/notifications/{notificationId}/read", markNotificationReadFinal(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/notifications/read-all", markAllNotificationsReadFinal(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/notifications/stream", notificationStreamFinal(deps))
}

// registerLegacyReviewRoutes is empty: the v1 review surface was retired in
// phase 0 once /api/v2/.../publication-requests passed its contract tests.
func registerLegacyReviewRoutes(deps Dependencies, mux *http.ServeMux) {
	_ = deps
	_ = mux
}

func registerLegacyContainerRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/containers/tree", containersCollection(deps))
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/containers", createContainer(deps))
	mux.HandleFunc("/api/frontend/containers/{containerId}", containerResource(deps))
	mux.HandleFunc("/api/frontend/containers/{containerId}/move", moveContainerFinal(deps))
	mux.HandleFunc("/api/frontend/containers/{containerId}/children", containerChildrenFinal(deps))
	mux.HandleFunc("/api/frontend/containers/{containerId}/assets", listContainerAssets(deps))
	mux.HandleFunc("/api/public/workspaces/{workspaceId}/assets", publicAssets(deps))
	mux.HandleFunc("/api/public/assets/{assetId}", publicAsset(deps))
}

func registerLegacyAutomationRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/automation-jobs", automationJobsCollection(deps))
	mux.HandleFunc("/api/frontend/automation-jobs/{jobId}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteAutomationJobFinal(deps)(w, r)
			return
		}
		automationJobResource(deps)(w, r)
	})
	mux.HandleFunc("/api/frontend/automation-jobs/{jobId}/pause", pauseAutomationJob(deps, false))
	mux.HandleFunc("/api/frontend/automation-jobs/{jobId}/resume", pauseAutomationJob(deps, true))
	mux.HandleFunc("/api/frontend/automation-jobs/{jobId}/run-now", createAutomationRun(deps))
	mux.HandleFunc("/api/frontend/automation-jobs/{jobId}/runs", listAutomationRuns(deps))
	mux.HandleFunc("/api/frontend/task-runs/{runId}", getAutomationRun(deps))
	mux.HandleFunc("/api/frontend/task-runs/{runId}/attempts", listAutomationAttempts(deps))
	mux.HandleFunc("/api/frontend/task-runs/{runId}/retry", retryAutomationRun(deps))
	mux.HandleFunc("/api/frontend/task-runs/{runId}/cancel", cancelAutomationRun(deps))
	mux.HandleFunc("/api/frontend/task-runs/{runId}/events", taskRunEventsFinal(deps))
	mux.HandleFunc("/api/open/v1/automation/runs/{runId}/callback", automationRunCallback(deps))
}

func registerLegacyConversationRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/frontend/workspaces/{workspaceId}/conversations", conversationsCollection(deps))
	mux.HandleFunc("/api/frontend/conversations/{conversationId}", conversationResourceFinal(deps))
	mux.HandleFunc("/api/frontend/conversations/{conversationId}/archive", archiveConversationFinal(deps))
	mux.HandleFunc("/api/frontend/conversations/{conversationId}/messages", appendMessage(deps))
	mux.HandleFunc("/api/frontend/conversations/{conversationId}/blocks", conversationBlocksFinal(deps))
	mux.HandleFunc("/api/frontend/conversations/{conversationId}/chat", conversationChat(deps, false))
	mux.HandleFunc("/api/frontend/conversations/{conversationId}/chat/stream", conversationChat(deps, true))
	mux.HandleFunc("/api/frontend/conversations/{conversationId}/note/sync", syncConversationNote(deps))
	mux.HandleFunc("/api/frontend/conversations/{conversationId}/note", conversationNoteFinal(deps))
	mux.HandleFunc("/api/frontend/conversations/{conversationId}/note/publish", publishConversationNote(deps))
	mux.HandleFunc("/api/frontend/conversations/{conversationId}/derivations", createDerivation(deps))
	mux.HandleFunc("/api/frontend/derivations/{derivationId}", getDerivation(deps))
	mux.HandleFunc("/api/frontend/derivations/{derivationId}/finalize", finalizeDerivation(deps))
	mux.HandleFunc("/api/frontend/conversations/{conversationId}/media", registerConversationMedia(deps))
	mux.HandleFunc("/api/frontend/conversation-media/{mediaId}", getConversationMedia(deps))
	mux.HandleFunc("/api/frontend/conversation-media/{mediaId}/transcribe", transcribeConversationMedia(deps))
	mux.HandleFunc("/api/frontend/conversation-media/{mediaId}/transcript", conversationTranscriptFinal(deps))
}

func registerLegacyAgentRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/frontend/agent-applications", frontendAgentApplications(deps))
	mux.HandleFunc("/api/frontend/agent-applications/{applicationId}", frontendAgentApplicationResource(deps))
	mux.HandleFunc("/api/frontend/agent-applications/{applicationId}/enable", frontendAgentApplicationStatus(deps, "active"))
	mux.HandleFunc("/api/frontend/agent-applications/{applicationId}/disable", frontendAgentApplicationStatus(deps, "disabled"))
	mux.HandleFunc("/api/frontend/agent-applications/{applicationId}/sessions", startAgentApplicationSession(deps))
	mux.HandleFunc("/api/frontend/agent-sessions/{sessionId}", agentSessionResourceFinal(deps))
	mux.HandleFunc("/api/frontend/agent-sessions/{sessionId}/references/validate", validateAgentSessionReferences(deps))
	mux.HandleFunc("/api/frontend/agent-sessions/{sessionId}/chat", chatAgentSession(deps))
	mux.HandleFunc("/api/frontend/agent-sessions/{sessionId}/chat/stream", streamAgentSession(deps))
	mux.HandleFunc("/api/frontend/agent-sessions/{sessionId}/runs", agentSessionRuns(deps))
	mux.HandleFunc("/api/frontend/agent-sessions/{sessionId}/cancel", cancelAgentSessionFinal(deps))
	mux.HandleFunc("/api/frontend/agent-runs/{runId}", agentRunResource(deps))
	mux.HandleFunc("/api/frontend/agent-runs/{runId}/events", agentRunEvents(deps))
	mux.HandleFunc("/api/frontend/agent-runs/{runId}/resume", resumeAgentRun(deps))
	mux.HandleFunc("/api/frontend/agent-runs/{runId}/cancel", cancelAgentRun(deps))
	mux.HandleFunc("/api/open/v1/assets", createAsset(deps))
	mux.HandleFunc("/api/open/v1/assets/{assetId}", updateAsset(deps))
	mux.HandleFunc("/api/open/v1/assets/{assetId}/publish", publishAsset(deps))
	mux.HandleFunc("/api/open/v1/assets/{assetId}/archive", archiveAsset(deps))
	mux.HandleFunc("/api/open/v1/assets/{assetId}/references", assetReferences(deps))
	mux.HandleFunc("/api/open/v1/agent/tasks", createAgentTask(deps))
	mux.HandleFunc("/api/open/v1/agent/tasks/{taskId}", getAgentTask(deps))
	mux.HandleFunc("/api/open/v1/hooks/assets", webhookCreateAsset(deps))
	mux.HandleFunc("/api/open/v1/attachments/{attachmentId}/download", downloadAttachment(deps))
}

func registerLegacyAdminRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/agent-users", registerAgent(deps))
	mux.HandleFunc("/api/admin/agent-users/{agentUserId}/access-policy", replaceAgentModelPolicy(deps))
	mux.HandleFunc("/api/admin/agent-users/{agentUserId}/api-keys/rotate", rotateAgentAPIKey(deps))
	mux.HandleFunc("/api/admin/agent-users/{agentUserId}/api-keys/revoke-all", revokeAllAgentAPIKeys(deps))
	mux.HandleFunc("/api/admin/agent-users/{agentUserId}/onboarding", getAgentUserOnboarding(deps))
	mux.HandleFunc("/api/admin/agent-applications", listAgentApplications(deps))
	mux.HandleFunc("/api/admin/agent-applications/{applicationId}", getAgentApplication(deps))
	mux.HandleFunc("/api/admin/agent-applications/{applicationId}/status", updateAgentApplicationStatus(deps))
}
