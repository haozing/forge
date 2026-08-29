package httpapi

// v2_tag.go — the phase 2 tag domain surface: workspace tag catalog CRUD,
// lifecycle (archive/restore) and the facet counts endpoint. Handlers only
// authenticate, enforce the workspace policy, call the tag services and map
// domain errors; all SQL lives inside internal/tag.

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	agentquery "agentchunzhi/internal/query"
	"agentchunzhi/internal/tag"
)

// v2TagItem is one catalog entry; include_usage=true attaches the usage block.
type v2TagItem struct {
	tag.Tag
	Usage *tag.Usage `json:"usage,omitempty"`
}

// requireWorkspaceActionV2 gates a workspace-scoped v2 command through the
// workspace policy. Permission denials answer 403; unknown workspaces stay
// 404 so probing is distinguishable from denial.
func requireWorkspaceActionV2(w http.ResponseWriter, r *http.Request, deps Dependencies, principal auth.Principal, workspaceID, action string) bool {
	if deps.WorkspacePolicy == nil {
		writeError(w, http.StatusInternalServerError, "authorization_unconfigured")
		return false
	}
	if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", action); err != nil {
		if errors.Is(err, authz.ErrWorkspaceNotFound) {
			writeError(w, http.StatusNotFound, "resource_not_found")
			return false
		}
		writeError(w, http.StatusForbidden, "action_not_allowed")
		return false
	}
	return true
}

// v2TagError maps tag domain errors onto the v2 status/code contract.
func v2TagError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, tag.ErrNotFound):
		writeError(w, http.StatusNotFound, "tag_not_found")
	case errors.Is(err, tag.ErrKeyExists):
		writeError(w, http.StatusConflict, "tag_key_exists")
	case errors.Is(err, tag.ErrArchived):
		writeError(w, http.StatusConflict, "tag_archived")
	case errors.Is(err, tag.ErrAlreadyArchived):
		writeError(w, http.StatusConflict, "tag_already_archived")
	case errors.Is(err, tag.ErrAlreadyActive):
		writeError(w, http.StatusConflict, "tag_already_active")
	case errors.Is(err, tag.ErrRevisionMismatch):
		writeError(w, http.StatusPreconditionFailed, "tag_revision_mismatch")
	case errors.Is(err, tag.ErrUnknownTag):
		writeError(w, http.StatusUnprocessableEntity, "unknown_tag")
	case errors.Is(err, tag.ErrContradictoryFilter):
		writeError(w, http.StatusUnprocessableEntity, "contradictory_tag_filter")
	case errors.Is(err, tag.ErrTooManyTags):
		writeError(w, http.StatusUnprocessableEntity, "too_many_tags")
	case errors.Is(err, tag.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, tag.ErrCreatePermission), errors.Is(err, authz.ErrWorkspaceForbidden):
		writeError(w, http.StatusForbidden, "action_not_allowed")
	case errors.Is(err, authz.ErrWorkspaceNotFound):
		writeError(w, http.StatusNotFound, "resource_not_found")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error")
	}
}

type v2CreateTagRequest struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
}

// v2TagCollection serves GET/POST /api/v2/workspaces/{workspaceId}/tags.
// Reads need tag.read; creating needs tag.manage plus an Idempotency-Key.
func v2TagCollection(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := v2Principal(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			if !requireWorkspaceActionV2(w, r, deps, principal, workspaceID, authz.ActionTagRead) {
				return
			}
			includeUsage := false
			if raw := r.URL.Query().Get("include_usage"); raw != "" {
				parsed, err := strconv.ParseBool(raw)
				if err != nil {
					writeError(w, http.StatusUnprocessableEntity, "validation_failed")
					return
				}
				includeUsage = parsed
			}
			page, err := deps.TagService.List(r.Context(), principal, workspaceID, tag.ListInput{
				Query:        r.URL.Query().Get("q"),
				Status:       r.URL.Query().Get("status"),
				Sort:         r.URL.Query().Get("sort"),
				IncludeUsage: includeUsage,
				Limit:        atoiDefault(r.URL.Query().Get("limit"), 20),
				Cursor:       r.URL.Query().Get("cursor"),
			})
			if err != nil {
				v2TagError(w, err)
				return
			}
			items := make([]v2TagItem, 0, len(page.Items))
			// Usage is a representation enrichment only: it never participates
			// in sort or cursor, so dynamic counts cannot shift the ordering.
			if includeUsage && len(page.Items) > 0 {
				ids := make([]string, 0, len(page.Items))
				for _, item := range page.Items {
					ids = append(ids, item.ID)
				}
				usage, usageErr := deps.TagService.Usage(r.Context(), principal, workspaceID, ids)
				if usageErr != nil {
					v2TagError(w, usageErr)
					return
				}
				for _, item := range page.Items {
					entry := v2TagItem{Tag: item}
					if value, ok := usage[item.ID]; ok {
						copied := value
						entry.Usage = &copied
					}
					items = append(items, entry)
				}
			} else {
				for _, item := range page.Items {
					items = append(items, v2TagItem{Tag: item})
				}
			}
			writeData(w, r, http.StatusOK, map[string]any{
				"items": items,
				"page":  pageFrom(len(items), atoiDefault(r.URL.Query().Get("limit"), 20), page.NextCursor),
			})
		case http.MethodPost:
			if !requireWorkspaceActionV2(w, r, deps, principal, workspaceID, authz.ActionTagManage) {
				return
			}
			if _, ok := requireIdempotencyKeyV2(w, r); !ok {
				return
			}
			var input v2CreateTagRequest
			if !decodeV2Body(w, r, &input, 16*1024) {
				return
			}
			item, err := deps.TagService.Create(r.Context(), principal, workspaceID, input.Key, input.DisplayName)
			if err != nil {
				v2TagError(w, err)
				return
			}
			writeETag(w, item.ETag)
			writeData(w, r, http.StatusCreated, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

type v2RenameTagRequest struct {
	DisplayName string `json:"display_name"`
}

// v2TagResource serves GET/PATCH /api/v2/workspaces/{workspaceId}/tags/{tagId}.
// PATCH accepts display_name only and demands the tag revision If-Match.
func v2TagResource(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := v2Principal(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		tagID := r.PathValue("tagId")
		if !requirePathUUID(w, workspaceID, tagID) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			if !requireWorkspaceActionV2(w, r, deps, principal, workspaceID, authz.ActionTagRead) {
				return
			}
			item, err := deps.TagService.Get(r.Context(), principal, workspaceID, tagID)
			if err != nil {
				v2TagError(w, err)
				return
			}
			writeETag(w, item.ETag)
			writeData(w, r, http.StatusOK, item)
		case http.MethodPatch:
			if !requireWorkspaceActionV2(w, r, deps, principal, workspaceID, authz.ActionTagManage) {
				return
			}
			expected, ok := requireIfMatchV2(w, r)
			if !ok {
				return
			}
			var input v2RenameTagRequest
			if !decodeV2Body(w, r, &input, 16*1024) {
				return
			}
			item, err := deps.TagService.Rename(r.Context(), principal, workspaceID, tagID, expected, input.DisplayName)
			if err != nil {
				v2TagError(w, err)
				return
			}
			writeETag(w, item.ETag)
			writeData(w, r, http.StatusOK, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// v2TagArchive serves POST .../tags/{tagId}/archive (tag.manage, If-Match +
// Idempotency-Key).
func v2TagArchive(deps Dependencies) http.HandlerFunc {
	return v2TagLifecycleCommand(deps, deps.TagService.Archive)
}

// v2TagRestore serves POST .../tags/{tagId}/restore with the same guards.
func v2TagRestore(deps Dependencies) http.HandlerFunc {
	return v2TagLifecycleCommand(deps, deps.TagService.Restore)
}

func v2TagLifecycleCommand(deps Dependencies, command func(ctx context.Context, principal auth.Principal, workspaceID, tagID, expectedRevision string) (tag.Tag, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := v2Principal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		workspaceID := r.PathValue("workspaceId")
		tagID := r.PathValue("tagId")
		if !requirePathUUID(w, workspaceID, tagID) {
			return
		}
		if !requireWorkspaceActionV2(w, r, deps, principal, workspaceID, authz.ActionTagManage) {
			return
		}
		expected, ok := requireIfMatchV2(w, r)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		item, err := command(r.Context(), principal, workspaceID, tagID, expected)
		if err != nil {
			v2TagError(w, err)
			return
		}
		writeETag(w, item.ETag)
		writeData(w, r, http.StatusOK, item)
	}
}

// v2TagFacets serves GET /api/v2/workspaces/{workspaceId}/tag-facets: per-tag
// distinct asset counts under the working or published scope.
func v2TagFacets(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := v2Principal(w, r, deps)
		if !ok {
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		// Facets read both the catalog and the assets it counts over.
		if !requireWorkspaceActionV2(w, r, deps, principal, workspaceID, authz.ActionTagRead) {
			return
		}
		if !requireWorkspaceActionV2(w, r, deps, principal, workspaceID, authz.ActionAssetRead) {
			return
		}
		query := r.URL.Query()
		scope := query.Get("scope")
		if scope == "" {
			scope = "working"
		}
		if scope != "working" && scope != "published" {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		resourceModelID := query.Get("resource_model_id")
		if resourceModelID != "" && !agentquery.ValidUUID(resourceModelID) {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		// The facet tag status is a closed whitelist like the scope parameter.
		tagStatus := query.Get("tag_status")
		if tagStatus != "" && tagStatus != tag.StatusActive && tagStatus != tag.StatusArchived {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		items, err := deps.FacetService.Counts(r.Context(), principal, workspaceID, tag.FacetScope{
			Scope:             scope,
			ResourceModelID:   resourceModelID,
			Visibility:        query.Get("visibility"),
			PublicationStatus: query.Get("publication_status"),
		}, tag.KeyFilter{
			Any:  query["tags_any"],
			All:  query["tags_all"],
			None: query["tags_none"],
		}, tagStatus, atoiDefault(query.Get("limit"), 50))
		if err != nil {
			v2TagError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "page": CursorPage{NextCursor: nil, HasMore: false}})
	}
}
