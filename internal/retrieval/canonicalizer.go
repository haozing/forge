package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// CanonicalizerV1 is the canonicalization algorithm identity recorded on
// profiles, runs and chunks. Any normalization change must bump the version
// because canonical_checksum values are persisted.
const CanonicalizerV1 = "canon-v1"

// CanonicalSegment is one ordered, normalized piece of retrievable text.
// Char ranges in citations always resolve against a single segment, so
// segments added elsewhere never shift existing offsets.
type CanonicalSegment struct {
	SourceType    string
	SourceLocator json.RawMessage
	Label         string
	Text          string
}

// TagInput describes one immutable tag relation row.
type TagInput struct {
	ID          string
	Key         string
	DisplayName string
}

// AttachmentInput is one attachment text of the version's materialized
// images: the vision extractor's description plus OCR.
type AttachmentInput struct {
	ID   string
	Alt  string
	Text string
}

// CanonicalizeInput carries every immutable input of one asset version.
type CanonicalizeInput struct {
	Title       string
	Summary     string
	Markdown    string
	Fields      json.RawMessage
	FieldSchema json.RawMessage
	Tags        []TagInput
	Attachments []AttachmentInput
}

// FieldDefinition is one entry of field_schema.fields.
type FieldDefinition struct {
	Key        string
	Label      string
	Type       string
	Searchable bool
}

// NormalizeText applies the canonical text normalization: CRLF to LF, NFC,
// trailing whitespace removed per line and the value trimmed.
func NormalizeText(value string) string {
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = norm.NFC.String(value)
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\u00a0\u3000")
	}
	value = strings.Join(lines, "\n")
	value = strings.Trim(value, "\n")
	value = strings.TrimLeft(value, " \t\u00a0\u3000")
	return strings.TrimRight(value, " \t\u00a0\u3000")
}

// Canonicalize builds the ordered segment set for one asset version:
// title, summary, markdown blocks, searchable non-object fields (schema
// order, stable key tie-break) and tag segments ordered by tag ID.
func Canonicalize(input CanonicalizeInput) []CanonicalSegment {
	segments := make([]CanonicalSegment, 0, 16)
	appendSegment := func(sourceType string, locator map[string]any, label, text string) {
		text = NormalizeText(text)
		if text == "" {
			return
		}
		raw, err := json.Marshal(locator)
		if err != nil {
			// Locators are plain maps of strings/numbers; marshalling cannot
			// fail in practice. Fall back to a minimal object.
			raw = json.RawMessage(`{}`)
		}
		segments = append(segments, CanonicalSegment{
			SourceType:    sourceType,
			SourceLocator: json.RawMessage(raw),
			Label:         label,
			Text:          text,
		})
	}

	appendSegment(SourceTypeTitle, map[string]any{"type": SourceTypeTitle}, "title", input.Title)
	appendSegment(SourceTypeSummary, map[string]any{"type": SourceTypeSummary}, "summary", input.Summary)

	blockOrdinal := 0
	for _, segment := range MarkdownSegments(input.Markdown) {
		appendSegment(SourceTypeMarkdown, segment.Locator, segment.Label, segment.Text)
		blockOrdinal++
	}

	for _, field := range SearchableFields(input.FieldSchema) {
		value := fieldValueText(input.Fields, field.Key)
		if value == "" {
			continue
		}
		label := field.Label
		if label == "" {
			label = field.Key
		}
		appendSegment(SourceTypeField, map[string]any{"type": SourceTypeField, "key": field.Key}, label, value)
	}

	tags := make([]TagInput, len(input.Tags))
	copy(tags, input.Tags)
	sort.Slice(tags, func(i, j int) bool { return tags[i].ID < tags[j].ID })
	for _, tag := range tags {
		appendSegment(SourceTypeTag,
			map[string]any{"type": SourceTypeTag, "tag_id": tag.ID, "key": tag.Key},
			tag.DisplayName,
			tag.DisplayName+"\n"+tag.Key)
	}

	// Attachment texts (vision OCR + description) are retrievable asset
	// metadata; the repository orders them by attachment ID already.
	for _, attachment := range input.Attachments {
		label := attachment.Alt
		if label == "" {
			label = "图片"
		}
		appendSegment(SourceTypeAttachment,
			map[string]any{"type": SourceTypeAttachment, "attachment_id": attachment.ID},
			label, attachment.Text)
	}
	return segments
}

// SearchableFields extracts the searchable non-object fields from a
// field_schema document. The result is ordered by field key only, so the
// checksum is insensitive to the order rows arrive in.
func SearchableFields(fieldSchema json.RawMessage) []FieldDefinition {
	if len(fieldSchema) == 0 {
		return nil
	}
	var schema struct {
		Fields []struct {
			Key        string `json:"key"`
			Label      string `json:"label"`
			Type       string `json:"type"`
			Searchable bool   `json:"searchable"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(fieldSchema, &schema); err != nil {
		return nil
	}
	fields := make([]FieldDefinition, 0, len(schema.Fields))
	for _, field := range schema.Fields {
		if !field.Searchable || strings.EqualFold(field.Type, "object") {
			continue
		}
		fields = append(fields, FieldDefinition{
			Key: field.Key, Label: field.Label, Type: field.Type, Searchable: true,
		})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Key < fields[j].Key })
	return fields
}

// fieldValueText renders one asset field as canonical text. Object fields are
// never flattened; arrays join their textual elements.
func fieldValueText(fields json.RawMessage, key string) string {
	if len(fields) == 0 {
		return ""
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(fields, &parsed); err != nil {
		return ""
	}
	raw, ok := parsed[key]
	if !ok {
		return ""
	}
	return renderJSONValue(raw)
}

func renderJSONValue(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	switch {
	case strings.HasPrefix(trimmed, "\""):
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return ""
		}
		return value
	case strings.HasPrefix(trimmed, "["):
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return ""
		}
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if rendered := renderJSONValue(item); rendered != "" {
				parts = append(parts, rendered)
			}
		}
		return strings.Join(parts, "\n")
	case strings.HasPrefix(trimmed, "{"):
		// Object fields are not searchable; ignore defensively.
		return ""
	default:
		return trimmed
	}
}

// CanonicalChecksum derives the run canonical_checksum: the SHA-256 of the
// ordered segments rendered as canonical JSON (sorted object keys).
func CanonicalChecksum(segments []CanonicalSegment) string {
	digest := sha256.New()
	digest.Write([]byte(CanonicalizerV1))
	digest.Write([]byte{0})
	encoded, err := json.Marshal(encodeSegments(segments))
	if err != nil {
		// encodeSegments only emits strings and raw JSON; marshal cannot fail.
		encoded = []byte("[]")
	}
	digest.Write(encoded)
	return hex.EncodeToString(digest.Sum(nil))
}

// SourceChecksum is the per-segment checksum stored on each chunk.
func SourceChecksum(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func encodeSegments(segments []CanonicalSegment) []map[string]any {
	encoded := make([]map[string]any, 0, len(segments))
	for _, segment := range segments {
		encoded = append(encoded, map[string]any{
			"source_type":    segment.SourceType,
			"source_locator": canonicalJSONValue(segment.SourceLocator),
			"label":          segment.Label,
			"text":           segment.Text,
		})
	}
	return encoded
}

// canonicalJSONValue re-marshals raw JSON through maps/slices so object keys
// render in sorted order on every process.
func canonicalJSONValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return map[string]any{}
	}
	return value
}

// CanonicalLocatorJSON normalizes one locator for checksum input.
func CanonicalLocatorJSON(raw json.RawMessage) json.RawMessage {
	encoded, err := json.Marshal(canonicalJSONValue(raw))
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(encoded)
}
