package query

import (
	"strings"

	"agentchunzhi/internal/access"
	"agentchunzhi/internal/tag"
)

// validateRequest enforces the fixed limits of doc §6.2 before any SQL runs.
// The scope is already compiled: visibility may only narrow it.
func validateRequest(scope QueryAccessScope, req *Request) error {
	req.Query = strings.TrimSpace(req.Query)
	if len(req.Query) > 0 && strings.ContainsRune(req.Query, '\x00') {
		return ErrInvalidRequest
	}
	switch req.Mode {
	case ModeStructured:
		if req.Query != "" {
			return ErrStructuredQueryTextNotAllowed
		}
	case ModeFulltext, ModeSemantic, ModeHybrid:
		if req.Query == "" {
			return ErrQueryTextRequired
		}
		if len([]rune(req.Query)) > MaxQueryRunes {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidQueryMode
	}
	if len(req.ResourceModelIDs) > MaxResourceModelIDs {
		return ErrInvalidRequest
	}
	for _, modelID := range req.ResourceModelIDs {
		if !ValidUUID(modelID) {
			return ErrInvalidRequest
		}
	}
	if req.TopK < 0 || req.TopK > MaxTopK {
		return ErrInvalidRequest
	}
	if req.TopK == 0 {
		req.TopK = DefaultTopK
	}
	if len(req.FieldFilters) > MaxFieldFilters {
		return ErrInvalidRequest
	}
	for _, filter := range req.FieldFilters {
		if !ValidUUID(filter.ResourceModelID) || strings.TrimSpace(filter.Field) == "" || filter.Operator == "" {
			return ErrInvalidRequest
		}
	}
	if err := validateVisibility(scope, req.Visibility); err != nil {
		return err
	}
	if err := validateEnums(req); err != nil {
		return err
	}
	if err := validateTagGroups(req); err != nil {
		return err
	}
	return nil
}

// validateVisibility rejects filters that would widen the scope: only a
// subset of the scope's allowed visibilities is acceptable (doc §6.2).
func validateVisibility(scope QueryAccessScope, requested []string) error {
	if len(requested) == 0 {
		return nil
	}
	allowed := map[string]bool{}
	for _, value := range scope.AllowedVisibilities {
		allowed[value] = true
	}
	for _, value := range requested {
		if !access.Valid(value) || !allowed[value] {
			return ErrInvalidVisibility
		}
	}
	return nil
}

func validateEnums(req *Request) error {
	for _, origin := range req.Origins {
		switch origin {
		case "human", "imported", "ai_generated", "ai_assisted":
		default:
			return ErrInvalidRequest
		}
	}
	for _, status := range req.ConfirmationStatuses {
		switch status {
		case "unconfirmed", "human_confirmed":
		default:
			return ErrInvalidRequest
		}
	}
	if req.PublishedAfter != nil && req.PublishedBefore != nil &&
		req.PublishedAfter.After(*req.PublishedBefore) {
		return ErrInvalidRequest
	}
	return nil
}

// validateTagGroups normalizes the any/all/none key groups through the phase 2
// tag domain: per-group limit 50, total 100, no cross-group conflicts.
func validateTagGroups(req *Request) error {
	normalized, err := tag.NormalizeFilter(tag.KeyFilter{
		Any:  req.TagsAny,
		All:  req.TagsAll,
		None: req.TagsNone,
	})
	if err != nil {
		return ErrInvalidTagFilter
	}
	req.TagsAny = normalized.Any
	req.TagsAll = normalized.All
	req.TagsNone = normalized.None
	return nil
}

// containsString reports membership in a plain list.
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// intersectStrings returns the sorted intersection of two lists.
func intersectStrings(a, b []string) []string {
	set := make(map[string]bool, len(a))
	for _, value := range a {
		set[value] = true
	}
	out := make([]string, 0, len(b))
	for _, value := range b {
		if set[value] && !containsString(out, value) {
			out = append(out, value)
		}
	}
	return out
}
