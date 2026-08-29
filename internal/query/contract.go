package query

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Query mode names (doc §6.1). No lexical/vector/keyword aliases exist.
const (
	ModeStructured = "structured"
	ModeFulltext   = "fulltext"
	ModeSemantic   = "semantic"
	ModeHybrid     = "hybrid"
)

// Ranking methods reported through the response metadata.
const (
	RankingStructured = "structured"
	RankingFulltext   = "fulltext"
	RankingSemantic   = "semantic"
	RankingRRF        = "rrf"
	RankingRerank     = "rerank"
)

// Degradation reasons (doc §10.6) — fixed enum, never raw provider errors.
const (
	ReasonSemanticProviderUnavailable = "semantic_provider_unavailable"
	ReasonSemanticProjectionPartial   = "semantic_projection_incomplete"
	ReasonFulltextUnavailable         = "fulltext_unavailable"
	ReasonRerankerUnavailable         = "reranker_unavailable"
)

// ValidMode reports whether the value is one of the four contract modes.
func ValidMode(mode string) bool {
	switch mode {
	case ModeStructured, ModeFulltext, ModeSemantic, ModeHybrid:
		return true
	}
	return false
}

// FulltextClass reports whether the mode requires the projection index.
func FulltextClass(mode string) bool {
	return mode == ModeFulltext || mode == ModeSemantic || mode == ModeHybrid
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

// ValidUUID reports whether the value is a canonical UUID.
func ValidUUID(value string) bool {
	return uuidPattern.MatchString(value)
}

// FieldFilter is one typed field condition (doc §6.3).
type FieldFilter struct {
	ResourceModelID string `json:"resource_model_id"`
	Field           string `json:"field"`
	Operator        string `json:"operator"`
	Value           any    `json:"value"`
}

// Request is the unified query contract (doc §6.2). JSON decoding uses
// DisallowUnknownFields at the HTTP boundary.
type Request struct {
	Query                string        `json:"query"`
	Mode                 string        `json:"mode"`
	ResourceModelIDs     []string      `json:"resource_model_ids"`
	Visibility           []string      `json:"visibility"`
	TagsAny              []string      `json:"tags_any"`
	TagsAll              []string      `json:"tags_all"`
	TagsNone             []string      `json:"tags_none"`
	FieldFilters         []FieldFilter `json:"field_filters"`
	Origins              []string      `json:"origins"`
	ConfirmationStatuses []string      `json:"confirmation_statuses"`
	PublishedAfter       *time.Time    `json:"published_after"`
	PublishedBefore      *time.Time    `json:"published_before"`
	TopK                 int           `json:"top_k"`
	Cursor               string        `json:"cursor"`
}

// TagSummary mirrors the phase 2 tag domain shape (doc §6.5).
type TagSummary struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Slug        string `json:"slug"`
}

// Citation is the primary reference of a full-text-class item (doc §6.5).
type Citation struct {
	CitationID          string          `json:"citation_id"`
	SourceType          string          `json:"source_type"`
	SourceLocator       json.RawMessage `json:"source_locator"`
	CharStart           int             `json:"char_start"`
	CharEnd             int             `json:"char_end"`
	Excerpt             string          `json:"excerpt"`
	SourceChecksum      string          `json:"source_checksum"`
	ChunkChecksum       string          `json:"chunk_checksum"`
	CanonicalizerVersion string         `json:"canonicalizer_version"`
}

// Item is one asset-level result. The same AssetVersion appears at most once.
type Item struct {
	AssetID            string       `json:"asset_id"`
	AssetVersionID     string       `json:"asset_version_id"`
	WorkspaceID        string       `json:"workspace_id"`
	ResourceModelID    string       `json:"resource_model_id"`
	Title              string       `json:"title"`
	Summary            string       `json:"summary"`
	Visibility         string       `json:"visibility"`
	Origin             string       `json:"origin"`
	ConfirmationStatus string       `json:"confirmation_status"`
	PublishedAt        *time.Time   `json:"published_at"`
	Tags               []TagSummary `json:"tags"`
	Score              *float64     `json:"score"`
	Citation           *Citation    `json:"citation"`
}

// Page is the snapshot pagination block.
type Page struct {
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

// IndexInfo is only returned for full-text-class modes (doc §6.5).
type IndexInfo struct {
	ProfileGeneration int64      `json:"profile_generation"`
	IndexedThrough    *time.Time `json:"indexed_through"`
	LagSeconds        float64    `json:"lag_seconds"`
}

// Response is the unified query response (doc §6.5).
type Response struct {
	RequestedMode      string    `json:"requested_mode"`
	ExecutedMode       string    `json:"executed_mode"`
	RankingMethod      string    `json:"ranking_method"`
	Degraded           bool      `json:"degraded"`
	DegradationReasons []string  `json:"degradation_reasons"`
	SessionID          string    `json:"session_id"`
	Items              []Item    `json:"items"`
	Page               Page      `json:"page"`
	Index              *IndexInfo `json:"index,omitempty"`
}

// ValidatedReference is the outcome of POST /api/open/v2/references/validate.
type ValidatedReference struct {
	CitationRef    string `json:"citation_ref"`
	AssetID        string `json:"asset_id"`
	AssetVersionID string `json:"asset_version_id"`
}

// canonicalRequestValue is the canonicalization input of the request hash:
// every page of one logical query must carry the same normalized request.
type canonicalRequestValue struct {
	Mode                 string        `json:"mode"`
	Query                string        `json:"query"`
	ResourceModelIDs     []string      `json:"resource_model_ids"`
	Visibility           []string      `json:"visibility"`
	TagsAny              []string      `json:"tags_any"`
	TagsAll              []string      `json:"tags_all"`
	TagsNone             []string      `json:"tags_none"`
	FieldFilters         []FieldFilter `json:"field_filters"`
	Origins              []string      `json:"origins"`
	ConfirmationStatuses []string      `json:"confirmation_statuses"`
	PublishedAfter       *string       `json:"published_after"`
	PublishedBefore      *string       `json:"published_before"`
}

// NormalizedRequest renders the canonical form: trimmed query, sorted
// de-duplicated ID lists and stable field filter ordering. Cursor pages are
// verified against this hash, not the raw body.
func NormalizedRequest(req Request) Request {
	normalized := req
	normalized.Query = strings.TrimSpace(req.Query)
	normalized.ResourceModelIDs = normalizeIDList(req.ResourceModelIDs)
	normalized.Visibility = normalizeStringList(req.Visibility)
	normalized.Origins = normalizeStringList(req.Origins)
	normalized.ConfirmationStatuses = normalizeStringList(req.ConfirmationStatuses)
	filters := make([]FieldFilter, len(req.FieldFilters))
	copy(filters, req.FieldFilters)
	sort.SliceStable(filters, func(i, j int) bool {
		if filters[i].ResourceModelID != filters[j].ResourceModelID {
			return filters[i].ResourceModelID < filters[j].ResourceModelID
		}
		if filters[i].Field != filters[j].Field {
			return filters[i].Field < filters[j].Field
		}
		return filters[i].Operator < filters[j].Operator
	})
	normalized.FieldFilters = filters
	return normalized
}

// RequestHash computes the HMAC-SHA256 of the canonical request. The raw
// request text is never persisted — only this hash (doc §10.9).
func RequestHash(req Request, secret string) string {
	if secret == "" {
		secret = "agentchunzhi-query-hash"
	}
	canonical := canonicalRequestValue{
		Mode:                 req.Mode,
		Query:                req.Query,
		ResourceModelIDs:     req.ResourceModelIDs,
		Visibility:           req.Visibility,
		TagsAny:              req.TagsAny,
		TagsAll:              req.TagsAll,
		TagsNone:             req.TagsNone,
		FieldFilters:         req.FieldFilters,
		Origins:              req.Origins,
		ConfirmationStatuses: req.ConfirmationStatuses,
	}
	if req.PublishedAfter != nil {
		value := req.PublishedAfter.UTC().Format(time.RFC3339Nano)
		canonical.PublishedAfter = &value
	}
	if req.PublishedBefore != nil {
		value := req.PublishedBefore.UTC().Format(time.RFC3339Nano)
		canonical.PublishedBefore = &value
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		payload = []byte(canonical.Mode)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func normalizeIDList(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.ToLower(strings.TrimSpace(value))
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

// truncateRunes returns the first max code points of value.
func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func formatTimePtr(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func mustJSONRaw(value any) json.RawMessage {
	payload, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return payload
}

// compactJSON collapses whitespace in a locator document for storage.
func compactJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return json.RawMessage("{}")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("{}")
	}
	return payload
}

func errInvalidFieldFilterf(index int, field, format string, args ...any) error {
	return fmt.Errorf("%w: field_filters[%d] %q %s", ErrInvalidFieldFilter, index, field, fmt.Sprintf(format, args...))
}
