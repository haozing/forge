package retrieval

import (
	"strings"
	"unicode"
)

// TokenizerV1 is the manifest-bound tokenizer identity. The chunker refuses
// to run against an unbound tokenizer so token counts always match the
// embedding budget assumptions recorded on the profile.
const TokenizerV1 = "tok-v1"

// Tokenizer approximates model token counts for the chunker. Implementations
// must be deterministic across processes: identical input yields identical
// counts because chunk boundaries and checksums depend on them.
type Tokenizer interface {
	Name() string
	Count(text string) int
}

// WordTokenizer implements Tokenizer with a word/punctuation boundary model:
// contiguous latin/digit runs count as one token, each CJK rune counts as a
// single token, and whitespace/punctuation only terminate tokens. It is a
// deliberate approximation for budgeting, never a fixed rune count.
type WordTokenizer struct{}

// NewWordTokenizer returns the v1 tokenizer.
func NewWordTokenizer() WordTokenizer { return WordTokenizer{} }

// Name implements Tokenizer.
func (WordTokenizer) Name() string { return TokenizerV1 }

// Count implements Tokenizer.
func (t WordTokenizer) Count(text string) int { return len(TokenSpans(text)) }

// tokenSpan records one token as a half-open rune index range.
type tokenSpan struct {
	Start int
	End   int
}

// TokenSpans splits text into approximate model tokens. CJK ideographs,
// kana and hangul are single-token units; latin/digit/underscore runs form
// one token; every other rune is a separator.
func TokenSpans(text string) []tokenSpan {
	runes := []rune(text)
	spans := make([]tokenSpan, 0, len(runes)/3+1)
	start := -1
	flush := func(end int) {
		if start >= 0 {
			spans = append(spans, tokenSpan{Start: start, End: end})
			start = -1
		}
	}
	for i, r := range runes {
		switch {
		case isCJKUnit(r):
			flush(i)
			spans = append(spans, tokenSpan{Start: i, End: i + 1})
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			if start < 0 {
				start = i
			}
		default:
			flush(i)
		}
	}
	flush(len(runes))
	return spans
}

func isCJKUnit(r rune) bool {
	// CJK punctuation (。、！？) is not part of any script below and therefore
	// terminates tokens instead of becoming a unit on its own.
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// sentenceTerminal reports whether the rune ends a sentence for chunk packing.
func sentenceTerminal(r rune) bool {
	return strings.ContainsRune("。！？；!?;.", r)
}
