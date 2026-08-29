package tag

import (
	"errors"
	"sort"
)

var (
	ErrNotFound             = errors.New("tag not found")
	ErrKeyExists            = errors.New("tag key already exists")
	ErrSlugConflict         = errors.New("tag slug conflict")
	ErrArchived             = errors.New("tag is archived")
	ErrAlreadyArchived      = errors.New("tag already archived")
	ErrAlreadyActive        = errors.New("tag already active")
	ErrRevisionMismatch     = errors.New("tag revision mismatch")
	ErrScopeMismatch        = errors.New("tag belongs to another workspace")
	ErrTooManyTags          = errors.New("too many tags")
	ErrUnknownTag           = errors.New("unknown tag key")
	ErrContradictoryFilter  = errors.New("contradictory tag filter")
	ErrForbidden            = errors.New("tag action forbidden")
	ErrInvalidInput         = errors.New("invalid tag input")
	ErrCreatePermission     = errors.New("tag creation requires tag.manage")
)

// Filter is the typed relational tag filter: any/all/none over workspace
// tag identities. Empty groups are no-ops.
type Filter struct {
	Any  []string `json:"tags_any,omitempty"`
	All  []string `json:"tags_all,omitempty"`
	None []string `json:"tags_none,omitempty"`
}

// Empty reports whether the filter carries no condition.
func (f Filter) Empty() bool {
	return len(f.Any) == 0 && len(f.All) == 0 && len(f.None) == 0
}

// KeyFilter is the wire form: normalized keys resolved inside one workspace.
type KeyFilter struct {
	Any  []string
	All  []string
	None []string
}

// NormalizeFilter validates group sizes, normalizes every key and checks the
// any/all/none contradiction rule before resolution.
func NormalizeFilter(raw KeyFilter) (KeyFilter, error) {
	total := 0
	result := KeyFilter{}
	for group, keys := range map[string][]string{
		"any":  raw.Any,
		"all":  raw.All,
		"none": raw.None,
	} {
		if len(keys) > MaxFilterKeysPerGroup {
			return KeyFilter{}, ErrTooManyTags
		}
		normalized := make([]string, 0, len(keys))
		for _, key := range keys {
			value, err := NormalizeKey(key)
			if err != nil {
				return KeyFilter{}, ErrUnknownTag
			}
			normalized = append(normalized, value)
		}
		normalized = dedupeSorted(normalized)
		total += len(normalized)
		switch group {
		case "any":
			result.Any = normalized
		case "all":
			result.All = normalized
		case "none":
			result.None = normalized
		}
	}
	if total > MaxFilterKeysTotal {
		return KeyFilter{}, ErrTooManyTags
	}
	if contradicts(result) {
		return KeyFilter{}, ErrContradictoryFilter
	}
	return result, nil
}

func contradicts(filter KeyFilter) bool {
	positive := map[string]bool{}
	for _, key := range filter.Any {
		positive[key] = true
	}
	for _, key := range filter.All {
		positive[key] = true
	}
	for _, key := range filter.None {
		if positive[key] {
			return true
		}
	}
	return false
}

func dedupeSorted(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// ResolvedFilter carries the workspace-scoped tag identities a repository
// needs to build the fixed SQL fragments.
type ResolvedFilter struct {
	Any  []string
	All  []string
	None []string
}

// Resolve turns normalized keys into tag IDs for one workspace. Unknown keys
// fail loudly (ErrUnknownTag) — misspellings are never silently ignored.
// Resolution covers archived tags too: filtering historical versions must
// keep working.
func Resolve(filter KeyFilter, lookup func(key string) (id string, ok bool)) (ResolvedFilter, error) {
	resolve := func(keys []string) ([]string, error) {
		ids := make([]string, 0, len(keys))
		for _, key := range keys {
			id, ok := lookup(key)
			if !ok {
				return nil, ErrUnknownTag
			}
			ids = append(ids, id)
		}
		return dedupeSorted(ids), nil
	}
	any, err := resolve(filter.Any)
	if err != nil {
		return ResolvedFilter{}, err
	}
	all, err := resolve(filter.All)
	if err != nil {
		return ResolvedFilter{}, err
	}
	none, err := resolve(filter.None)
	if err != nil {
		return ResolvedFilter{}, err
	}
	return ResolvedFilter{Any: any, All: all, None: none}, nil
}
