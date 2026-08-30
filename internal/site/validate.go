package site

// validate.go — the pure rules of the site domain: slug/display-path/domain
// formats, the D5' scope ceiling, config JSON shape and If-Match revision
// semantics. Everything here is dependency-free so the stage 5 contract can
// be unit tested without a database.

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agentchunzhi/internal/access"
)

// slugPattern is the stage 5 plan rule: 3..64 chars, lowercase alphanumerics
// with interior dashes only. The database stores the UNIQUE(slug) constraint
// only; this service-layer check is the single format authority (the rule is
// documented in db/migrations/0010_site.sql).
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

// displayPathPattern mirrors the display_path CHECK of
// site.site_content_bindings: 2..122 chars, interior slashes and dashes
// allowed for nested public URLs.
var displayPathPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9/-]{0,120}[a-z0-9]$`)

// domainPattern mirrors the domain CHECK of site.public_sites: deployment
// layer owns routing, the service only rejects malformed values.
var domainPattern = regexp.MustCompile(`^[a-z0-9.-]+$`)

// Template values of the presentation layer (0010 CHECK blog|pro).
const (
	TemplateBlog = "blog"
	TemplatePro  = "pro"
)

// DefaultContentScope values (0010 CHECK, identical to the D5' tiers).
const (
	ScopePublic       = "public"
	ScopeOrganization = "organization"
	ScopeWorkspace    = "workspace"
)

// Binding content types (0010 CHECK article|featured|about). The site domain
// deliberately does not reuse the asset content_kind enum: these values
// describe presentation slots, not content shapes.
const (
	ContentTypeArticle  = "article"
	ContentTypeFeatured = "featured"
	ContentTypeAbout    = "about"
)

// Site status values (0010 CHECK active|disabled).
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// ValidSlug reports whether the value satisfies the slug rule.
func ValidSlug(value string) bool {
	return slugPattern.MatchString(value)
}

// ValidDisplayPath reports whether the value satisfies the display path rule.
func ValidDisplayPath(value string) bool {
	return displayPathPattern.MatchString(value)
}

// ValidDomain reports whether the value satisfies the stored-domain rule.
func ValidDomain(value string) bool {
	return domainPattern.MatchString(value)
}

// ValidTemplate reports whether the value is a known template.
func ValidTemplate(value string) bool {
	return value == TemplateBlog || value == TemplatePro
}

// ValidScope reports whether the value is a known content scope tier.
func ValidScope(value string) bool {
	switch value {
	case ScopePublic, ScopeOrganization, ScopeWorkspace:
		return true
	default:
		return false
	}
}

// ValidContentType reports whether the value is a known binding slot type.
func ValidContentType(value string) bool {
	switch value {
	case ContentTypeArticle, ContentTypeFeatured, ContentTypeAbout:
		return true
	default:
		return false
	}
}

// ScopeCeiling computes the D5' visibility upper set of a site scope: an
// asset is bindable only when its visibility sits inside this set. The
// ordering is fixed (public < organization < workspace) and unknown scopes
// yield an empty set so callers fail closed.
func ScopeCeiling(scope string) []string {
	switch scope {
	case ScopePublic:
		return []string{ScopePublic}
	case ScopeOrganization:
		return []string{ScopePublic, ScopeOrganization}
	case ScopeWorkspace:
		return []string{ScopePublic, ScopeOrganization, ScopeWorkspace}
	default:
		return []string{}
	}
}

// BindingTargetFacts carries the facts the binding gate judges. They are
// fetched with one JOIN over asset.assets, the published version, its bound
// resource model version (policy) and the model head.
type BindingTargetFacts struct {
	Visibility          string
	PublicationStatus   string
	HasPublishedVersion bool
	ModelActive         bool
	PublicSiteChannel   bool
}

// BindingTargetEligible judges the fetched facts against the site scope
// ceiling. This is the single write-time gate of plan section 3.1: later
// archive/visibility downgrades are handled by the read layer (404/zero
// hits), never by re-checks here.
func BindingTargetEligible(scope string, facts BindingTargetFacts) bool {
	if !ValidScope(scope) {
		return false
	}
	if facts.PublicationStatus != "published" || !facts.HasPublishedVersion {
		return false
	}
	if !facts.ModelActive || !facts.PublicSiteChannel {
		return false
	}
	return access.Allowed(ScopeCeiling(scope), facts.Visibility)
}

// validConfigObject reports whether raw is a JSON object (or empty: the
// caller defaults it to {}). Configs are free-form extension points of the
// presentation layer, but a bare array or scalar would break every consumer.
func validConfigObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return true
	}
	if !json.Valid(raw) {
		return false
	}
	var object map[string]any
	return json.Unmarshal(raw, &object) == nil
}

// revisionMatches implements the If-Match semantics of the site surface: an
// empty expected revision (header absent) and the "*" wildcard skip the
// check; any concrete revision must equal the current one exactly (quotes
// already stripped by the handler helper).
func revisionMatches(current int64, expected string) bool {
	expected = strings.TrimSpace(expected)
	if expected == "" || expected == "*" {
		return true
	}
	return expected == strconv.FormatInt(current, 10)
}

// ResolveDisplayPublishedAt implements the double-source semantics of the
// plan (section 8.4): the binding mirror wins, the asset published_at is the
// fallback. Exposed for the P5-3 public surface.
func ResolveDisplayPublishedAt(bindingPublishedAt, assetPublishedAt *time.Time) *time.Time {
	if bindingPublishedAt != nil {
		return bindingPublishedAt
	}
	return assetPublishedAt
}
