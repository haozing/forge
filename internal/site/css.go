package site

// css.go — the L2 custom-CSS whitelist sanitizer (design doc 二期 §4.3).
// The security contract: the model (or a console user) authors CSS, code
// decides what survives. Everything is tokenized with gorilla/css; only
// whitelisted properties, values without url()/expression(), bounded
// selectors and whitelisted @media conditions pass. The stored document is
// the sanitized canonical output; the render side sanitizes again (defense
// in depth, idempotent).

import (
	"fmt"
	"strings"

	css "github.com/gorilla/css/scanner"
)

// CSS input/output bounds.
const (
	CSSMaxInput          = 64 * 1024
	CSSMaxRules          = 512
	CSSMaxSelectorLength = 256
	CSSMaxValueLength    = 512
)

// cssAllowedProperties is the property whitelist. Prefix entries accept any
// suffix that keeps the property in the same family (border-*, margin-*...).
var cssAllowedProperties = map[string]bool{
	"color": true, "background": true, "background-color": true,
	"background-image": true, "background-size": true, "background-position": true,
	"background-repeat": true, "background-clip": true, "background-origin": true,
	"opacity": true, "visibility": true, "cursor": true, "box-sizing": true,
	"width": true, "height": true, "min-width": true, "min-height": true,
	"max-width": true, "max-height": true, "aspect-ratio": true,
	"overflow": true, "object-fit": true, "object-position": true,
	"vertical-align": true, "white-space": true, "word-break": true,
	"overflow-wrap": true, "text-overflow": true,
	"position": true, "z-index": true,
	"top": true, "left": true, "right": true, "bottom": true,
}

var cssAllowedPrefixes = []string{
	"border", "margin", "padding", "font", "text", "letter-", "line-height",
	"flex", "grid", "gap", "align-", "justify-", "order", "transition",
	"box-shadow", "outline", "list-style", "column", "row-", "transform",
	"display", "place-",
}

// cssValueForbidden matches value fragments that never survive.
var cssValueForbidden = []string{"url(", "expression(", "javascript:", "behavior", "\\u0075rl", "&quot;"}

// cssAllowedPositionValues restricts position (clickjacking containment).
var cssAllowedPositionValues = map[string]bool{"static": true, "relative": true}

// cssAllowedMediaConditions are the only @media query features accepted.
var cssAllowedMediaConditions = []string{
	"prefers-color-scheme", "prefers-reduced-motion", "min-width", "max-width",
}

// SanitizeCSS strips everything outside the whitelist and returns the
// canonical output plus notes describing what was removed (the agent tool
// feeds the notes back to the model for its self-healing round).
func SanitizeCSS(input string) (string, []string) {
	if len(input) > CSSMaxInput {
		input = input[:CSSMaxInput]
	}
	scanner := css.New(input)
	sanitizer := &cssSanitizer{scanner: scanner, stripped: []string{}}
	output := sanitizer.parseStylesheet()
	if len(sanitizer.stripped) == 0 {
		return output, nil
	}
	return output, sanitizer.stripped
}

type cssSanitizer struct {
	scanner      *css.Scanner
	stripped     []string
	rules        int
	pendingClose bool
}

func (s *cssSanitizer) next() *css.Token {
	for {
		token := s.scanner.Next()
		// Comments and stray whitespace fold away; the canonical output is
		// rebuilt with regular spacing.
		if token.Type == css.TokenComment {
			continue
		}
		return token
	}
}

func (s *cssSanitizer) strip(reason string) {
	s.stripped = append(s.stripped, reason)
}

// parseStylesheet walks the top level: selectors, at-rules, declarations.
func (s *cssSanitizer) parseStylesheet() string {
	var out strings.Builder
	for {
		token := s.next()
		if token.Type == css.TokenEOF || token.Type == css.TokenError {
			break
		}
		switch {
		case token.Type == css.TokenAtKeyword:
			// The token value carries its leading '@'.
			atName := strings.TrimPrefix(strings.ToLower(token.Value), "@")
			condition, hasBlock := s.readAtRulePrelude()
			if !hasBlock || atName != "media" || !mediaConditionAllowed(condition) {
				s.skipBlockIfNeeded(hasBlock)
				s.strip("@" + atName + " 块被移除（仅允许 @media 且条件受限）")
				continue
			}
			inner := s.parseStylesheet()
			out.WriteString("@media " + condition + " {" + inner + "}")
		case token.Type == css.TokenS:
			continue
		case token.Type == css.TokenChar && token.Value == "}":
			// Closing a nested block; the media caller resumes.
			return out.String()
		case token.Type == css.TokenChar && (token.Value == ";" || token.Value == "{"):
			// Stray punctuation outside any rule.
			continue
		default:
			selector := s.readSelector(token)
			if selector == "" {
				continue
			}
			body, ok := s.parseDeclarations()
			if !ok || body == "" {
				continue
			}
			s.rules++
			if s.rules > CSSMaxRules {
				s.strip("规则数超出上限，已截断")
				return out.String()
			}
			out.WriteString(selector + "{" + body + "}")
		}
	}
	return out.String()
}

// readSelector consumes tokens until '{' and validates the selector text.
func (s *cssSanitizer) readSelector(first *css.Token) string {
	var parts []string
	token := first
	for {
		switch token.Type {
		case css.TokenEOF, css.TokenError:
			return ""
		case css.TokenChar:
			switch token.Value {
			case "{":
				text := strings.TrimSpace(strings.Join(parts, ""))
				if !selectorAllowed(text) {
					s.strip("选择器被移除: " + clip(text))
					return ""
				}
				return text
			case ";":
				return ""
			case "}", ")":
				return ""
			default:
				parts = append(parts, token.Value)
			}
		case css.TokenURI:
			s.strip("选择器中的 url() 被移除")
			return ""
		case css.TokenS:
			parts = append(parts, " ")
		default:
			parts = append(parts, token.Value)
		}
		token = s.next()
	}
}

// parseDeclarations consumes "prop: value;" pairs until '}'.
func (s *cssSanitizer) parseDeclarations() (string, bool) {
	var out strings.Builder
	for {
		token := s.next()
		switch {
		case token.Type == css.TokenEOF || token.Type == css.TokenError:
			return out.String(), false
		case token.Type == css.TokenS:
			continue
		case token.Type == css.TokenChar && token.Value == "}":
			return out.String(), true
		case token.Type == css.TokenChar && token.Value == ";":
			continue
		case token.Type == css.TokenChar:
			s.skipDeclaration()
		default:
			prop, value, ok := s.readDeclaration(token)
			if !ok {
				continue
			}
			if prop == "" {
				continue
			}
			clean, ok := sanitizeDeclaration(prop, value)
			if !ok {
				s.strip("属性被移除: " + prop)
				continue
			}
			out.WriteString(clean + ";")
			if s.pendingClose {
				s.pendingClose = false
				return out.String(), true
			}
		}
	}
}

// readDeclaration reads one "prop: value" pair. A '}' terminator sets
// pendingClose so the declaration loop stops; URI tokens reject the pair.
// The loop advances at the bottom — cases must not `continue` (it would
// skip the advance and reprocess the same token).
func (s *cssSanitizer) readDeclaration(first *css.Token) (string, string, bool) {
	var prop []string
	var value []cssValueToken
	seenColon := false
	token := first
	for {
		switch token.Type {
		case css.TokenEOF, css.TokenError:
			return "", "", false
		case css.TokenChar:
			switch token.Value {
			case ":":
				if !seenColon {
					seenColon = true
				} else {
					value = append(value, cssValueToken{word: false, text: ":"})
				}
			case ";":
				return joinProp(prop), joinValue(value), true
			case "}":
				s.pendingClose = true
				return joinProp(prop), joinValue(value), true
			default:
				if !seenColon {
					return "", "", false
				}
				value = append(value, cssValueToken{word: false, text: token.Value})
			}
		case css.TokenURI:
			s.strip("属性值中的 url() 被移除: " + joinProp(prop))
			return "", "", false
		case css.TokenS:
			if seenColon {
				value = append(value, cssValueToken{word: false, text: " "})
			}
		default:
			if seenColon {
				value = append(value, cssValueToken{word: true, text: token.Value})
			} else {
				prop = append(prop, token.Value)
			}
		}
		token = s.next()
	}
}

// cssValueToken is one value token with its join class: word tokens take a
// space between each other, punctuation tokens glue to their neighbours
// (a space between '-' and '2px' would break negative lengths).
type cssValueToken struct {
	word bool
	text string
}

func joinProp(parts []string) string { return strings.Join(parts, "") }

func joinValue(tokens []cssValueToken) string {
	var builder strings.Builder
	pendingSpace := false
	previousWord := false
	afterSeparator := false
	for _, token := range tokens {
		if token.text == " " {
			pendingSpace = true
			continue
		}
		needsSpace := (token.word && previousWord && pendingSpace) ||
			(token.word && afterSeparator)
		if needsSpace {
			builder.WriteString(" ")
		}
		builder.WriteString(token.text)
		previousWord = token.word
		pendingSpace = false
		afterSeparator = !token.word && (token.text == ":" || token.text == "," || token.text == ")")
	}
	return strings.TrimSpace(builder.String())
}

// skipDeclaration consumes tokens until ';' or '}' (the block close stops
// the walk without swallowing context).
func (s *cssSanitizer) skipDeclaration() {
	for {
		token := s.next()
		if token.Type == css.TokenEOF || token.Type == css.TokenError {
			return
		}
		if token.Type == css.TokenChar && (token.Value == ";" || token.Value == "}") {
			return
		}
	}
}

// readAtRulePrelude reads the prelude of an at-rule up to '{' or ';'.
// Returns ("", false) for at-rules without a block.
func (s *cssSanitizer) readAtRulePrelude() (string, bool) {
	var parts []cssValueToken
	for {
		token := s.next()
		switch token.Type {
		case css.TokenEOF, css.TokenError:
			return "", false
		case css.TokenChar:
			switch token.Value {
			case "{":
				return joinValue(parts), true
			case ";":
				return "", false
			default:
				parts = append(parts, cssValueToken{word: false, text: token.Value})
			}
		case css.TokenURI:
			return "", false
		case css.TokenS:
			parts = append(parts, cssValueToken{word: false, text: " "})
		default:
			parts = append(parts, cssValueToken{word: true, text: token.Value})
		}
	}
}

// skipBlockIfNeeded drops the at-rule body when its prelude ended on '{'.
func (s *cssSanitizer) skipBlockIfNeeded(hasBlock bool) {
	if hasBlock {
		s.skipBlock()
	}
}

// skipBlock consumes one balanced {} block (its '{' already consumed).
func (s *cssSanitizer) skipBlock() {
	depth := 0
	for {
		token := s.next()
		if token.Type == css.TokenEOF || token.Type == css.TokenError {
			return
		}
		if token.Type == css.TokenChar {
			switch token.Value {
			case "{":
				depth++
			case "}":
				depth--
				if depth <= 0 {
					return
				}
			}
		}
	}
}

// selectorAllowed bounds selector shape and content.
func selectorAllowed(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || len(trimmed) > CSSMaxSelectorLength {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, forbidden := range cssValueForbidden {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	// No at-keywords or stray braces smuggled into selectors.
	if strings.ContainsAny(trimmed, "{}@") {
		return false
	}
	return true
}

// sanitizeDeclaration validates one prop/value pair and renders the
// canonical "prop: value" text.
func sanitizeDeclaration(prop, value string) (string, bool) {
	prop = strings.ToLower(strings.TrimSpace(prop))
	value = strings.TrimSpace(value)
	if prop == "" || value == "" || len(value) > CSSMaxValueLength {
		return "", false
	}
	if !cssPropertyAllowed(prop) {
		return "", false
	}
	lower := strings.ToLower(value)
	for _, forbidden := range cssValueForbidden {
		if strings.Contains(lower, forbidden) {
			return "", false
		}
	}
	if prop == "position" && !cssAllowedPositionValues[lower] {
		return "", false
	}
	if prop == "z-index" {
		var index int
		if _, err := fmt.Sscanf(lower, "%d", &index); err != nil || index < 0 || index > 10 {
			return "", false
		}
	}
	return prop + ": " + value, true
}

func cssPropertyAllowed(prop string) bool {
	if cssAllowedProperties[prop] {
		return true
	}
	for _, prefix := range cssAllowedPrefixes {
		if strings.HasPrefix(prop, prefix) {
			return true
		}
	}
	return false
}

// mediaConditionAllowed bounds @media prelude features.
func mediaConditionAllowed(condition string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(condition))
	if trimmed == "" || len(trimmed) > 200 {
		return false
	}
	for _, forbidden := range cssValueForbidden {
		if strings.Contains(trimmed, forbidden) {
			return false
		}
	}
	matched := false
	for _, feature := range cssAllowedMediaConditions {
		if strings.Contains(trimmed, feature) {
			matched = true
		}
	}
	if !matched {
		return false
	}
	// Only whitelisted features may appear: reject any other feature token.
	for _, feature := range strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ' ' || r == ',' || r == '(' || r == ')' || r == ':'
	}) {
		switch feature {
		case "prefers-color-scheme", "prefers-reduced-motion", "min-width", "max-width",
			"all", "screen", "and", "not", "only", "dark", "light", "no-preference", "reduce":
			continue
		default:
			if strings.HasPrefix(feature, "0") || strings.ContainsAny(feature, "0123456789") || strings.HasSuffix(feature, "px") || strings.HasSuffix(feature, "em") || strings.HasSuffix(feature, "rem") {
				continue // size literals of the whitelisted width queries
			}
			return false
		}
	}
	return true
}

func clip(text string) string {
	if len(text) > 80 {
		return text[:80] + "…"
	}
	return text
}
