package httpapi

// router_groups.go — the only route registry. Groups follow the v2 contract:
// legacy routes stay only as ledger-tracked pending retirements; every new
// capability registers under /api/v2, /api/open/v2 or /api/public/v2.

import "net/http"

func newRouter(deps Dependencies) *http.ServeMux {
	mux := http.NewServeMux()
	registerSystemRoutes(deps, mux)
	registerSessionRoutes(deps, mux)
	registerV2IdentityRoutes(deps, mux)
	registerV2OrganizationRoutes(deps, mux)
	registerV2AssetRoutes(deps, mux)
	registerV2SiteRoutes(deps, mux)
	registerV2TagRoutes(deps, mux)
	registerV2ReviewRoutes(deps, mux)
	registerV2QueryRoutes(deps, mux)
	registerOpenV2Routes(deps, mux)
	registerPublicV2Routes(deps, mux)
	registerLegacyModelRoutes(deps, mux)        // ledger: retire in phase 4-6
	registerLegacyAssetRoutes(deps, mux)       // ledger: retire in phase 2
	registerLegacyRetrievalRoutes(deps, mux)   // empty: retired in phase 3
	registerLegacyTransferRoutes(deps, mux)    // ledger: retire in phase 2
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

// registerSessionRoutes keeps only the v2 anonymous login; the legacy
// /api/sessions route retired with phase 1 (see the route ledger).
func registerSessionRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/public/v2/sessions", v2CreateSession(deps))
}

// registerV2IdentityRoutes holds the member identity surface: profile,
// preferences, session management and the anonymous password reset and
// invitation resolve/accept endpoints.
func registerV2IdentityRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/v2/sessions/current", v2DeleteCurrentSession(deps))
	mux.HandleFunc("/api/v2/sessions", v2ListSessions(deps))
	mux.HandleFunc("/api/v2/sessions/{sessionId}", v2DeleteSession(deps))
	mux.HandleFunc("/api/v2/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			v2GetMe(deps)(w, r)
			return
		}
		v2PatchMe(deps)(w, r)
	})
	mux.HandleFunc("/api/v2/me/password", v2ChangePassword(deps))
	mux.HandleFunc("/api/v2/me/preferences", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			v2GetPreferences(deps)(w, r)
			return
		}
		v2PatchPreferences(deps)(w, r)
	})
	mux.HandleFunc("/api/public/v2/password-resets", v2RequestPasswordReset(deps))
	mux.HandleFunc("/api/public/v2/password-resets/resolve", v2ResolvePasswordReset(deps))
	mux.HandleFunc("/api/public/v2/password-resets/complete", v2CompletePasswordReset(deps))
	mux.HandleFunc("/api/public/v2/organization-invitations/resolve", v2ResolveInvitation(deps))
	mux.HandleFunc("/api/public/v2/organization-invitations/accept", v2AcceptInvitation(deps))
}

// registerV2OrganizationRoutes holds the phase 1 organization governance and
// workspace membership surfaces.
func registerV2OrganizationRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/v2/organization", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			v2GetOrganization(deps)(w, r)
			return
		}
		v2PatchOrganization(deps)(w, r)
	})
	mux.HandleFunc("/api/v2/organization/members", v2ListOrganizationMembers(deps))
	mux.HandleFunc("/api/v2/organization/members/{userId}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			v2GetOrganizationMember(deps)(w, r)
			return
		}
		v2PatchOrganizationMember(deps)(w, r)
	})
	mux.HandleFunc("/api/v2/organization/invitations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			v2ListOrganizationInvitations(deps)(w, r)
			return
		}
		v2CreateOrganizationInvitation(deps)(w, r)
	})
	mux.HandleFunc("/api/v2/organization/invitations/{invitationId}/resend", v2ResendOrganizationInvitation(deps))
	mux.HandleFunc("/api/v2/organization/invitations/{invitationId}/revoke", v2RevokeOrganizationInvitation(deps))
	mux.HandleFunc("/api/v2/organization/workspaces", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			v2ListOrganizationWorkspaces(deps)(w, r)
			return
		}
		v2CreateOrganizationWorkspace(deps)(w, r)
	})
	mux.HandleFunc("/api/v2/organization/workspaces/{workspaceId}", v2GetOrganizationWorkspace(deps))
	mux.HandleFunc("/api/v2/organization/workspaces/{workspaceId}/archive", v2ArchiveOrganizationWorkspace(deps))
	mux.HandleFunc("/api/v2/organization/workspaces/{workspaceId}/restore", v2RestoreOrganizationWorkspace(deps))
	mux.HandleFunc("/api/v2/organization/workspaces/{workspaceId}/members", v2GrantOrganizationWorkspaceMember(deps))
	mux.HandleFunc("/api/v2/organization/workspaces/{workspaceId}/members/{membershipId}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			v2PatchOrganizationWorkspaceMember(deps)(w, r)
			return
		}
		v2RevokeOrganizationWorkspaceMember(deps)(w, r)
	})
	mux.HandleFunc("/api/v2/workspaces", v2ListMyWorkspaces(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			v2GetWorkspace(deps)(w, r)
			return
		}
		v2PatchWorkspace(deps)(w, r)
	})
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/summary", v2GetWorkspaceSummary(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/members", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			v2ListWorkspaceMembers(deps)(w, r)
			return
		}
		v2AddWorkspaceMember(deps)(w, r)
	})
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/members/{membershipId}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			v2PatchWorkspaceMember(deps)(w, r)
			return
		}
		v2RemoveWorkspaceMember(deps)(w, r)
	})
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/members/me/leave", v2LeaveWorkspace(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/eligible-members", v2ListEligibleWorkspaceMembers(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/invitations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			v2ListWorkspaceInvitations(deps)(w, r)
			return
		}
		v2CreateWorkspaceInvitation(deps)(w, r)
	})
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/invitations/{invitationId}/resend", v2ResendWorkspaceInvitation(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/invitations/{invitationId}/revoke", v2RevokeWorkspaceInvitation(deps))
}

func registerV2AssetRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/v2/assets/{assetId}/draft", v2AssetDraft(deps))
	mux.HandleFunc("/api/v2/assets/{assetId}/commit-draft", v2CommitDraft(deps))
	mux.HandleFunc("/api/v2/assets/{assetId}/publish", v2PublishAsset(deps))
	mux.HandleFunc("/api/v2/assets/{assetId}/archive", v2ArchiveAsset(deps))
	mux.HandleFunc("/api/v2/assets/{assetId}/restore", v2RestoreAsset(deps))
	mux.HandleFunc("/api/v2/asset-versions/{versionId}/confirm", v2ConfirmVersion(deps))
	registerV2SuggestionRoutes(deps, mux)
}

// registerV2SuggestionRoutes holds the phase 4 member review surface: the
// unified suggestion queue, single and batch decisions, agent processing
// results and member-initiated asset preparation runs.
func registerV2SuggestionRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/assets/{assetId}/suggestions", v2AssetSuggestions(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/assets/{assetId}/suggestions/accept-batch", v2AssetSuggestionsAcceptBatch(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/assets/{assetId}/processing-results", v2AssetProcessingResults(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/assets/{assetId}/prepare", v2AssetPrepare(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/accept", v2SuggestionAccept(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/reject", v2SuggestionReject(deps))
}

// registerV2SiteRoutes holds the phase 5 public-site management surface:
// workspace site CRUD (GET/POST collection, GET/PATCH/DELETE resource where
// DELETE is the soft disable), binding CRUD and the no-store JSON preview
// snapshot. Binding surfaces sit behind site.manage per the stage 5 matrix.
func registerV2SiteRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/sites", v2SitesCollection(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/sites/{siteId}", v2SiteResource(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/sites/{siteId}/bindings", v2SiteBindingsCollection(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/sites/{siteId}/bindings/{bindingId}", v2SiteBindingResource(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/sites/{siteId}/preview", v2SitePreview(deps))
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

// registerV2TagRoutes holds the phase 2 tag domain surface: the workspace
// catalog, lifecycle commands and facet counts. The legacy frontend tag
// parameters stay only as ledger-tracked pending retirements.
func registerV2TagRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/tags", v2TagCollection(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/tags/{tagId}", v2TagResource(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/tags/{tagId}/archive", v2TagArchive(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/tags/{tagId}/restore", v2TagRestore(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/tag-facets", v2TagFacets(deps))
}

func registerOpenV2Routes(deps Dependencies, mux *http.ServeMux) {
	// Phase 2 moved webhook intake off the retired /api/open/v1 path; query
	// follows in phase 3.
	mux.HandleFunc("/api/open/v2/hooks/assets", webhookCreateAsset(deps))
}

// registerPublicV2Routes holds the phase 5 public read face: anonymous or
// optional-member visitors read one site slug, throttled per address prefix
// through the shared public_site_ip bucket. Safe reads only: no idempotency
// contract applies (requiresHTTPIdempotency excludes /api/public/v2).
func registerPublicV2Routes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/public/v2/sites/{slug}", publicSiteView(deps))
	mux.HandleFunc("/api/public/v2/sites/{slug}/posts", publicSitePosts(deps))
	mux.HandleFunc("/api/public/v2/sites/{slug}/posts/{displayPath...}", publicSitePost(deps))
	mux.HandleFunc("/api/public/v2/sites/{slug}/sections/{sectionSlug}", publicSiteSection(deps))
	mux.HandleFunc("/api/public/v2/sites/{slug}/tags", publicSiteTags(deps))
	mux.HandleFunc("/api/public/v2/sites/{slug}/tags/{key}", publicSiteTagPage(deps))
	mux.HandleFunc("/api/public/v2/sites/{slug}/search", publicSiteSearch(deps))
}

// The phase 1 legacy identity/workspace registrations (current-user profile,
// legacy login, member preferences and the frontend workspace governance
// family) are removed; see docs/route-retirement-ledger.md. Retired paths now
// answer 404 from the default mux without compatibility redirects.

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

// registerLegacyRetrievalRoutes is empty: the phase 3 retrieval surface
// (/api/frontend/.../query|index|query-audit|search/suggestions and
// /api/open/v1/query) retired once the unified v2 query routes passed their
// contract; see docs/route-retirement-ledger.md.
func registerLegacyRetrievalRoutes(deps Dependencies, mux *http.ServeMux) {
	_ = deps
	_ = mux
}

// registerV2QueryRoutes holds the phase 3 unified query surface: member and
// OpenAPI query, projection profile/rebuild operations and the query
// execution audit (doc §11).
func registerV2QueryRoutes(deps Dependencies, mux *http.ServeMux) {
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/query", v2WorkspaceQuery(deps))
	mux.HandleFunc("/api/v2/organization/query", v2OrganizationQuery(deps))
	mux.HandleFunc("/api/open/v2/query", v2OpenQuery(deps))
	mux.HandleFunc("/api/open/v2/references/validate", v2OpenReferenceValidate(deps))
	// Phase 4 agent tasks: same guard chain as the v1 surface (API-key
	// principal + capability + idempotency); the v1 routes retire per ledger.
	mux.HandleFunc("/api/open/v2/agent-tasks", createAgentTask(deps))
	mux.HandleFunc("/api/open/v2/agent-tasks/{taskId}", getAgentTask(deps))

	mux.HandleFunc("/api/v2/organization/retrieval/profiles", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			v2ListRetrievalProfiles(deps)(w, r)
			return
		}
		v2CreateRetrievalProfile(deps)(w, r)
	})
	mux.HandleFunc("/api/v2/organization/retrieval/profiles/{profileId}/activate", v2ActivateRetrievalProfile(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/retrieval/status", v2WorkspaceRetrievalStatus(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/retrieval/rebuilds", v2WorkspaceRetrievalRebuild(deps))
	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/retrieval/rebuilds/{rebuildId}", v2WorkspaceRebuildGet(deps))
	mux.HandleFunc("/api/v2/organization/retrieval/rebuilds", v2OrganizationRetrievalRebuilds(deps))
	mux.HandleFunc("/api/v2/organization/retrieval/rebuilds/{rebuildId}", v2OrganizationRebuildGet(deps))

	mux.HandleFunc("/api/v2/workspaces/{workspaceId}/query-executions", v2WorkspaceQueryExecutions(deps))
	mux.HandleFunc("/api/v2/organization/query-executions", v2OrganizationQueryExecutions(deps))
	mux.HandleFunc("/api/v2/query-executions/{executionId}", v2QueryExecution(deps))
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
	// /api/public/workspaces/{workspaceId}/assets and /api/public/assets/{assetId}
	// retired in phase 5: the public read face lives at
	// /api/public/v2/sites/{slug}/... (see docs/route-retirement-ledger.md).
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
	// /api/open/v1/hooks/assets retired in phase 2: webhook intake now lives at
	// /api/open/v2/hooks/assets (see docs/route-retirement-ledger.md).
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
