// Package tag implements the v2 tag domain: workspace-scoped definitions with
// stable identity (id, normalized key, slug), lifecycle (active/archived),
// draft relations and immutable version relations. Tag definitions never
// leak into ResourceModel dynamic fields and never mutate historical versions.
package tag

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Limits fixed by the phase 2 contract.
const (
	MaxKeyRunes        = 100
	MaxKeyBytes        = 400
	MaxDisplayNameRunes = 100
	MaxTagsPerAsset    = 100
	MaxFilterKeysPerGroup = 50
	MaxFilterKeysTotal = 100
)

// Lifecycle statuses.
const (
	StatusActive   = "active"
	StatusArchived = "archived"
)

// Relation sources.
const (
	SourceManual  = "manual"
	SourceAPI     = "api"
	SourceWebhook = "webhook"
	SourceImport  = "import"
	SourceAI      = "ai"
)

// ValidSource mirrors the relation CHECK constraint.
func ValidSource(value string) bool {
	switch value {
	case SourceManual, SourceAPI, SourceWebhook, SourceImport, SourceAI:
		return true
	default:
		return false
	}
}

// NormalizeKey derives the canonical normalized key: NFKC, whitespace folded
// to single ASCII spaces, Unicode case folding, then NFKC again. Control,
// format and line/paragraph separator characters are rejected.
func NormalizeKey(input string) (string, error) {
	normalized := norm.NFKC.String(input)
	normalized = foldSpaces(normalized)
	normalized = norm.NFKC.String(cases.Fold().String(normalized))
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return "", fmt.Errorf("normalized key is empty")
	}
	for _, ch := range normalized {
		if unicode.In(ch, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return "", fmt.Errorf("normalized key contains forbidden control characters")
		}
	}
	if len([]rune(normalized)) > MaxKeyRunes {
		return "", fmt.Errorf("normalized key exceeds %d characters", MaxKeyRunes)
	}
	if len(normalized) > MaxKeyBytes {
		return "", fmt.Errorf("normalized key exceeds %d bytes", MaxKeyBytes)
	}
	return normalized, nil
}

// ValidateDisplayName checks the display name bounds.
func ValidateDisplayName(input string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", fmt.Errorf("display name is empty")
	}
	if len([]rune(value)) > MaxDisplayNameRunes {
		return "", fmt.Errorf("display name exceeds %d characters", MaxDisplayNameRunes)
	}
	if len(value) > MaxKeyBytes {
		return "", fmt.Errorf("display name exceeds %d bytes", MaxDisplayNameBytes)
	}
	return value, nil
}

const MaxDisplayNameBytes = 400

// foldSpaces collapses every Unicode whitespace run into one ASCII space.
func foldSpaces(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	prevSpace := false
	for _, ch := range value {
		if unicode.IsSpace(ch) {
			if !prevSpace {
				builder.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		builder.WriteRune(ch)
	}
	return builder.String()
}

// DeriveSlug builds the immutable slug from the normalized key: keep Unicode
// letters/numbers and ASCII '-', fold other runs to '-', trim '-', cap at
// 120 code points. Empty results fall back to tag-<short uuid>.
func DeriveSlug(normalizedKey, idHint string) string {
	var builder strings.Builder
	prevDash := false
	for _, ch := range normalizedKey {
		if ch == '-' {
			if !prevDash && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			prevDash = true
			continue
		}
		if unicode.IsLetter(ch) || unicode.IsNumber(ch) {
			builder.WriteRune(ch)
			prevDash = false
			continue
		}
		if !prevDash && builder.Len() > 0 {
			builder.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	runes := []rune(slug)
	if len(runes) > 120 {
		slug = string(runes[:120])
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		hint := idHint
		if len(hint) > 12 {
			hint = hint[:12]
		}
		slug = "tag-" + hint
	}
	return slug
}
