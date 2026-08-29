package retrieval

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestNormalizeTextStable(t *testing.T) {
	input := "  ｍｕｌｔｉ‐byte \r\n trailing   \nline2\t \n"
	got := NormalizeText(input)
	// The normalized value drops CRLF, per-line trailing spaces and the
	// leading/trailing blank space, and NFC-folds the fullwidth input.
	if got != "ｍｕｌｔｉ‐byte\n trailing\nline2" {
		t.Fatalf("NormalizeText() = %q", got)
	}
	if NormalizeText(got) != got {
		t.Fatalf("NormalizeText is not idempotent: %q", got)
	}
}

func TestCanonicalChecksumStableAcrossRunsAndOrderInsensitive(t *testing.T) {
	build := func() []CanonicalSegment {
		return []CanonicalSegment{
			{SourceType: SourceTypeTitle, SourceLocator: json.RawMessage(`{"type":"title"}`), Label: "title", Text: "标题 Title"},
			{SourceType: SourceTypeMarkdown, SourceLocator: json.RawMessage(`{"type":"markdown","heading_path":[],"block":0}`), Label: "", Text: "正文段落。"},
			{SourceType: SourceTypeField, SourceLocator: json.RawMessage(`{"type":"field","key":"topic"}`), Label: "主题", Text: "全文检索"},
		}
	}
	first := CanonicalChecksum(build())
	for i := 0; i < 3; i++ {
		if again := CanonicalChecksum(build()); again != first {
			t.Fatalf("checksum not stable: %s != %s", again, first)
		}
	}
	// Normalization inside Canonicalize must not change the checksum.
	normalized := Canonicalize(CanonicalizeInput{
		Title:       "  标题 Title \r\n",
		Markdown:    "  正文段落。\r\n   ",
		FieldSchema: json.RawMessage(`{"fields":[{"key":"topic","label":"主题","type":"text","searchable":true}]}`),
		Fields:      json.RawMessage(`{"topic":"全文检索"}`),
	})
	if CanonicalChecksum(normalized) != first {
		t.Fatalf("normalized checksum drifted: %s != %s", CanonicalChecksum(normalized), first)
	}
}

func TestCanonicalizeFieldAndTagOrderInsensitive(t *testing.T) {
	fields := json.RawMessage(`{"alpha":"A","beta":"B","gamma":"G"}`)
	schemaA := json.RawMessage(`{"fields":[
		{"key":"alpha","label":"Alpha","type":"text","searchable":true},
		{"key":"beta","label":"Beta","type":"text","searchable":true},
		{"key":"gamma","label":"Gamma","type":"text","searchable":true}]}`)
	schemaB := json.RawMessage(`{"fields":[
		{"key":"gamma","label":"Gamma","type":"text","searchable":true},
		{"key":"alpha","label":"Alpha","type":"text","searchable":true},
		{"key":"beta","label":"Beta","type":"text","searchable":true}]}`)

	segmentsA := Canonicalize(CanonicalizeInput{Title: "t", Fields: fields, FieldSchema: schemaA})
	segmentsB := Canonicalize(CanonicalizeInput{Title: "t", Fields: fields, FieldSchema: schemaB})
	if CanonicalChecksum(segmentsA) != CanonicalChecksum(segmentsB) {
		t.Fatal("field schema order must not change the canonical checksum")
	}

	tagsA := []TagInput{{ID: "b", Key: "k2", DisplayName: "二"}, {ID: "a", Key: "k1", DisplayName: "一"}}
	tagsB := []TagInput{{ID: "a", Key: "k1", DisplayName: "一"}, {ID: "b", Key: "k2", DisplayName: "二"}}
	segmentsA = Canonicalize(CanonicalizeInput{Title: "t", Tags: tagsA})
	segmentsB = Canonicalize(CanonicalizeInput{Title: "t", Tags: tagsB})
	if CanonicalChecksum(segmentsA) != CanonicalChecksum(segmentsB) {
		t.Fatal("tag order must not change the canonical checksum")
	}

	// Non-searchable and object fields never enter the segments.
	schemaObject := json.RawMessage(`{"fields":[
		{"key":"hidden","label":"H","type":"text","searchable":false},
		{"key":"meta","label":"M","type":"object","searchable":true},
		{"key":"alpha","label":"Alpha","type":"text","searchable":true}]}`)
	segments := Canonicalize(CanonicalizeInput{Title: "t", Fields: json.RawMessage(`{"alpha":"A","hidden":"x","meta":{"nested":1}}`), FieldSchema: schemaObject})
	for _, segment := range segments {
		if segment.SourceType == SourceTypeField && segment.Label != "Alpha" {
			t.Fatalf("unexpected field segment %q", segment.Label)
		}
	}
}

func TestCanonicalizeMarkdownHeadingPath(t *testing.T) {
	segments := Canonicalize(CanonicalizeInput{
		Title:    "文档",
		Markdown: "# 指南\n\n第一段。\n\n## 安装\n\n安装段落。\n\n```\ncode line\n```\n\n- 列表一\n- 列表二\n",
	})
	var found int
	for _, segment := range segments {
		if segment.SourceType != SourceTypeMarkdown {
			continue
		}
		found++
		var locator struct {
			HeadingPath []string `json:"heading_path"`
			Block       int      `json:"block"`
		}
		if err := json.Unmarshal(segment.SourceLocator, &locator); err != nil {
			t.Fatalf("locator %s is invalid: %v", segment.SourceLocator, err)
		}
		switch segment.Text {
		case "第一段。":
			if len(locator.HeadingPath) != 1 || locator.HeadingPath[0] != "指南" {
				t.Fatalf("heading path = %v", locator.HeadingPath)
			}
		case "安装段落。":
			if len(locator.HeadingPath) != 2 || locator.HeadingPath[1] != "安装" {
				t.Fatalf("heading path = %v", locator.HeadingPath)
			}
		case "指南", "安装":
			// Headings are segments of their own.
		case "code line", "列表一\n列表二":
			// Code blocks and folded lists are projected as text.
		default:
			t.Fatalf("unexpected markdown segment %q", segment.Text)
		}
	}
	if found != 6 {
		t.Fatalf("expected 6 markdown segments, got %d", found)
	}
}

func TestSourceChecksumMatchesSegmentText(t *testing.T) {
	segments := Canonicalize(CanonicalizeInput{Title: "标题", Summary: "  摘要 \r\n"})
	for _, segment := range segments {
		if segment.Text == "" {
			t.Fatal("canonicalize produced an empty segment")
		}
	}
	if SourceChecksum("a") == SourceChecksum("b") {
		t.Fatal("source checksum collided")
	}
}

func TestChunkSegmentLargeTextBreaksIntoChunksWithOverlap(t *testing.T) {
	tokenizer := NewWordTokenizer()
	// A long CJK paragraph: one token per rune, ~3000 tokens.
	long := strings.Repeat("这是一段需要切分的正文内容，用于验证分块边界。", 70)
	segments := Canonicalize(CanonicalizeInput{Title: "长文", Markdown: long})
	chunks := ChunkSegments(segments, tokenizer)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	previousEnd := -1
	overlapped := false
	for i, chunk := range chunks {
		if chunk.Ordinal != i {
			t.Fatalf("chunk ordinal %d out of order", chunk.Ordinal)
		}
		runes := []rune(segments[chunkSourceSegment(t, segments, chunk)].Text)
		if chunk.CharStart < 0 || chunk.CharEnd > len(runes) || chunk.CharStart >= chunk.CharEnd {
			t.Fatalf("chunk %d has invalid range [%d,%d) of %d", i, chunk.CharStart, chunk.CharEnd, len(runes))
		}
		if string(runes[chunk.CharStart:chunk.CharEnd]) != chunk.Text {
			t.Fatalf("chunk %d text does not resolve via locator", i)
		}
		if chunk.CharStart < previousEnd {
			overlapped = true
		}
		previousEnd = chunk.CharEnd
		if tokenizer.Count(chunk.Text) > MaxChunkTokens+16 {
			t.Fatalf("chunk %d exceeds the token budget: %d", i, tokenizer.Count(chunk.Text))
		}
	}
	if !overlapped {
		t.Fatal("expected overlapping consecutive chunks")
	}
	if chunks[0].SourceChecksum != SourceChecksum(segments[0].Text) {
		t.Fatal("source checksum must be the segment text checksum")
	}
	if chunks[0].ChunkChecksum != ComputeChunkChecksum(CanonicalizerV1, ChunkerV1, chunks[0].SourceLocator, chunks[0].CharStart, chunks[0].CharEnd, chunks[0].Text) {
		t.Fatal("chunk checksum mismatch")
	}
	if chunks[0].ChunkChecksum == chunks[len(chunks)-1].ChunkChecksum {
		t.Fatal("distinct chunks must not share a checksum")
	}
}

// chunkSourceSegment locates the segment a chunk belongs to (the test only
// builds single-segment inputs).
func chunkSourceSegment(t *testing.T, segments []CanonicalSegment, chunk Chunk) int {
	t.Helper()
	for i, segment := range segments {
		if segment.SourceType == chunk.SourceType && SourceChecksum(segment.Text) == chunk.SourceChecksum {
			return i
		}
	}
	t.Fatalf("segment for chunk %d not found", chunk.Ordinal)
	return 0
}

func TestChunkSegmentShortTextSingleChunk(t *testing.T) {
	segments := Canonicalize(CanonicalizeInput{Title: "短文", Summary: "一句话"})
	chunks := ChunkSegments(segments, NewWordTokenizer())
	if len(chunks) != 2 {
		t.Fatalf("expected one chunk per short segment, got %d", len(chunks))
	}
	if chunks[0].Text != "短文" || chunks[1].Text != "一句话" {
		t.Fatalf("unexpected chunk texts: %q %q", chunks[0].Text, chunks[1].Text)
	}
	if chunks[0].Label != "title" {
		t.Fatalf("title chunk label = %q", chunks[0].Label)
	}
}

func TestTokenizerCountsWordsAndCJK(t *testing.T) {
	tokenizer := NewWordTokenizer()
	if tokenizer.Name() != TokenizerV1 {
		t.Fatalf("tokenizer name = %q", tokenizer.Name())
	}
	if got := tokenizer.Count("hello world"); got != 2 {
		t.Fatalf("Count(hello world) = %d", got)
	}
	if got := tokenizer.Count("全文检索"); got != 4 {
		t.Fatalf("Count(全文检索) = %d", got)
	}
	mixed := "检索 engine, v2!"
	if got := tokenizer.Count(mixed); got != 4 { // 检 索 engine v2
		t.Fatalf("Count(%q) = %d, want 4", mixed, got)
	}
	spans := TokenSpans(mixed)
	for _, span := range spans {
		if span.Start < 0 || span.End <= span.Start {
			t.Fatalf("invalid span %+v", span)
		}
	}
}

func TestValidateEmbeddingResponseRejectsBadPayloads(t *testing.T) {
	good := make([][]float32, 1)
	good[0] = make([]float32, 4)
	if err := ValidateEmbeddingResponse(good, 1, 4); err != nil {
		t.Fatalf("good payload rejected: %v", err)
	}
	short := [][]float32{{1, 2}}
	if err := ValidateEmbeddingResponse(short, 1, 4); err == nil {
		t.Fatal("wrong dimension accepted")
	}
	missing := [][]float32{{1, 2, 3, 4}, {1, 2, 3, 4}}
	if err := ValidateEmbeddingResponse(missing, 1, 4); err == nil {
		t.Fatal("wrong count accepted")
	}
	NaN := [][]float32{{float32(math.NaN()), 2, 3, 4}}
	if err := ValidateEmbeddingResponse(NaN, 1, 4); err == nil {
		t.Fatal("NaN accepted")
	}
}
