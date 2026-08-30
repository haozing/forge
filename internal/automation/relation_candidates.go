package automation

// relation_candidates.go — the retrieval-backed relation candidate source of
// the asset_prepare workflow (doc §11.1). Agents never touch the database or
// the retrieval projections directly: the relatable-asset whitelist comes from
// the unified query engine, so every hit already passed the scope, visibility
// and policy gates of the caller's principal.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/query"
	"agentchunzhi/internal/workflows"
)

// relationQueryService is the slice of query.Service the candidate source
// needs; the concrete query.Service value satisfies it directly.
type relationQueryService interface {
	// AgentQuery compiles the ForAgent scope (AgentAccessPolicy-intersected).
	AgentQuery(ctx context.Context, principal auth.Principal, req query.Request) (query.Response, error)
	// WorkspaceQuery compiles the member workspace scope.
	WorkspaceQuery(ctx context.Context, principal auth.Principal, workspaceID string, req query.Request) (query.Response, error)
}

// RelationCandidateQuery implements asset.RelationCandidateSource on top of
// the unified query service. Agent principals ride the ForAgent scope; member
// principals compile their workspace membership scope. A retrieval failure is
// returned as an error so the preparation side can fail closed (doc §11.1).
type RelationCandidateQuery struct {
	Query relationQueryService
}

// Candidates runs one fulltext query over the workspace's published assets and
// maps the hits to relatable-asset candidates. Excluding the source asset is
// not this type's job — the preparation service owns that filter.
// orQuery turns the source title/summary into an OR-of-terms query: the
// lexical search ANDs plain multi-term queries, so the source-specific words
// would zero out every hit on related-but-differently-worded assets. Each
// term is escaped with the shared PGroonga escaper; only the literal OR
// separators are ours, so no operators can be injected.
func orQuery(text string) string {
	terms := strings.Fields(text)
	if len(terms) > 10 {
		terms = terms[:10]
	}
	escaped := make([]string, 0, len(terms))
	for _, term := range terms {
		if len([]rune(term)) < 2 {
			continue
		}
		escaped = append(escaped, query.EscapePGroongaQuery(term))
	}
	if len(escaped) == 0 {
		return query.EscapePGroongaQuery(text)
	}
	return strings.Join(escaped, " OR ")
}

func (r RelationCandidateQuery) Candidates(ctx context.Context, principal auth.Principal, workspaceID, queryText string, limit int) ([]workflows.RelationCandidate, error) {
	if r.Query == nil {
		return nil, errors.New("relation candidate query service is not configured")
	}
	if limit <= 0 {
		limit = query.DefaultTopK
	}
	if limit > query.MaxTopK {
		limit = query.MaxTopK
	}
	req := query.Request{
		Mode:  query.ModeFulltext,
		Query: orQuery(queryText),
		TopK:  limit,
	}
	var response query.Response
	var err error
	if principal.UserType == auth.UserTypeAgent {
		response, err = r.Query.AgentQuery(ctx, principal, req)
	} else {
		response, err = r.Query.WorkspaceQuery(ctx, principal, workspaceID, req)
	}
	if err != nil {
		return nil, fmt.Errorf("query relation candidates: %w", err)
	}
	candidates := make([]workflows.RelationCandidate, 0, len(response.Items))
	for _, item := range response.Items {
		// The retrieval excerpt is the citation snippet; items without one fall
		// back to the hit's summary.
		snippet := item.Summary
		if item.Citation != nil && item.Citation.Excerpt != "" {
			snippet = item.Citation.Excerpt
		}
		candidates = append(candidates, workflows.RelationCandidate{
			AssetID: item.AssetID,
			Title:   item.Title,
			Snippet: snippet,
		})
	}
	return candidates, nil
}
