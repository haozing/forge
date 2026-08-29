package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Chunking budget constants (doc §7.4): the embedding context caps at 512
// tokens, chunks target 384 tokens and consecutive chunks overlap 64 tokens.
const (
	ChunkerV1          = "chunk-v1"
	MaxChunkTokens     = 512
	TargetChunkTokens  = 384
	OverlapChunkTokens = 64
)

// Chunk is one persisted retrieval.chunks row in memory.
type Chunk struct {
	Ordinal        int
	SourceType     string
	SourceLocator  json.RawMessage
	Label          string
	CharStart      int
	CharEnd        int
	Text           string
	SourceChecksum string
	ChunkChecksum  string
}

// ChunkSegments splits every canonical segment into chunks that never cross
// segment boundaries. char_start/char_end are Unicode code point offsets
// inside the segment text; the segment label (title/field label/heading path)
// is prefixed to the embedding text and counted against the token budget.
func ChunkSegments(segments []CanonicalSegment, tokenizer Tokenizer) []Chunk {
	chunks := make([]Chunk, 0, len(segments)*2)
	for _, segment := range segments {
		for _, chunk := range chunkSegment(segment, tokenizer) {
			chunk.Ordinal = len(chunks)
			chunks = append(chunks, chunk)
		}
	}
	return chunks
}

func chunkSegment(segment CanonicalSegment, tokenizer Tokenizer) []Chunk {
	runes := []rune(segment.Text)
	prefix := ""
	if segment.Label != "" {
		prefix = segment.Label + "\n"
	}
	budget := MaxChunkTokens - tokenizer.Count(prefix)
	if budget < 32 {
		// Degenerate labels still leave a workable window.
		budget = 32
	}

	sourceChecksum := SourceChecksum(segment.Text)
	out := make([]Chunk, 0, 2)
	for _, window := range chunkWindows(segment.Text, tokenizer, budget) {
		if window.Start < 0 || window.End > len(runes) || window.Start >= window.End {
			continue
		}
		sub := runes[window.Start:window.End]
		first, last := 0, len(sub)-1
		for first <= last && isChunkSpace(sub[first]) {
			first++
		}
		for last >= first && isChunkSpace(sub[last]) {
			last--
		}
		if first > last {
			continue
		}
		text := string(sub[first : last+1])
		start := window.Start + first
		end := window.Start + last + 1
		locator := CanonicalLocatorJSON(segment.SourceLocator)
		out = append(out, Chunk{
			SourceType:     segment.SourceType,
			SourceLocator:  locator,
			Label:          segment.Label,
			CharStart:      start,
			CharEnd:        end,
			Text:           text,
			SourceChecksum: sourceChecksum,
			ChunkChecksum:  ComputeChunkChecksum(CanonicalizerV1, ChunkerV1, locator, start, end, text),
		})
	}
	return out
}

func isChunkSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }

type tokenWindow struct {
	Start int
	End   int
}

// chunkWindows produces code point windows over the segment text. Text is
// first grouped into sentence-sized token units; units are packed up to the
// token budget, oversized units are hard-split with overlap, and consecutive
// windows share roughly OverlapChunkTokens tokens.
func chunkWindows(text string, tokenizer Tokenizer, budget int) []tokenWindow {
	spans := TokenSpans(text)
	if len(spans) == 0 {
		return nil
	}
	sentences := sentenceGroups(text, spans)
	windows := make([]tokenWindow, 0, len(sentences))
	start := 0
	for start < len(sentences) {
		end := start
		tokens := 0
		for end < len(sentences) && tokens+sentences[end].TokenCount <= budget {
			tokens += sentences[end].TokenCount
			end++
		}
		if end == start {
			// One sentence exceeds the budget: hard-split by tokens.
			sentence := sentences[start]
			step := budget - OverlapChunkTokens
			if step < 1 {
				step = 1
			}
			for begin := 0; begin < sentence.TokenCount; begin += step {
				stop := begin + budget
				if stop > sentence.TokenCount {
					stop = sentence.TokenCount
				}
				windows = append(windows, tokenWindow{
					Start: runeAtSpan(spans, sentence.FirstToken+begin),
					End:   runeAtSpan(spans, sentence.FirstToken+stop),
				})
				if stop >= sentence.TokenCount {
					break
				}
			}
			start++
			continue
		}
		last := end - 1
		windows = append(windows, tokenWindow{
			Start: runeAtSpan(spans, sentences[start].FirstToken),
			End:   runeAtSpan(spans, sentences[last].FirstToken+sentences[last].TokenCount),
		})
		if end >= len(sentences) {
			break
		}
		// Rewind so the next window re-includes about OverlapChunkTokens
		// tokens, but always keep forward progress.
		next := last
		overlap := OverlapChunkTokens
		for next > start && overlap-sentences[next].TokenCount > 0 {
			overlap -= sentences[next].TokenCount
			next--
		}
		if next <= start {
			next = start + 1
		}
		start = next
	}
	return windows
}

func runeAtSpan(spans []tokenSpan, tokenIndex int) int {
	if tokenIndex <= 0 {
		return spans[0].Start
	}
	if tokenIndex >= len(spans) {
		return spans[len(spans)-1].End
	}
	return spans[tokenIndex].Start
}

type sentenceInfo struct {
	FirstToken int // index into the token span list
	TokenCount int
}

// sentenceGroups groups token spans into sentence-sized units: a unit ends
// after a terminal punctuation rune or at the end of the segment.
func sentenceGroups(text string, spans []tokenSpan) []sentenceInfo {
	runes := []rune(text)
	groups := make([]sentenceInfo, 0, 8)
	current := sentenceInfo{}
	for index, span := range spans {
		current.TokenCount++
		terminal := span.End >= len(runes) || sentenceTerminal(runes[span.End])
		if terminal || index == len(spans)-1 {
			groups = append(groups, current)
			current = sentenceInfo{FirstToken: index + 1}
		}
	}
	return groups
}

// ComputeChunkChecksum derives chunk_checksum from the canonicalizer and
// chunker identity, the source locator, the char range and the text.
func ComputeChunkChecksum(canonicalizerVersion, chunkerVersion string, locator json.RawMessage, charStart, charEnd int, text string) string {
	digest := sha256.New()
	fmt.Fprintf(digest, "%s\n%s\n%s\n%d\n%d\n", canonicalizerVersion, chunkerVersion, string(locator), charStart, charEnd)
	digest.Write([]byte(text))
	return hex.EncodeToString(digest.Sum(nil))
}

// ChunkEmbeddingText renders the provider input for one chunk: the label is
// a metadata prefix already accounted for by the token budget.
func ChunkEmbeddingText(chunk Chunk) string {
	if chunk.Label == "" {
		return chunk.Text
	}
	return chunk.Label + "\n" + chunk.Text
}
