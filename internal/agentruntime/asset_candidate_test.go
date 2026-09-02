package agentruntime

import (
	"encoding/json"
	"strings"
	"testing"

	"agentchunzhi/internal/workflows"
)

var decodeTagCandidates = []workflows.TagCandidate{
	{Key: "go", DisplayName: "Go"},
	{Key: "retrieval", DisplayName: "Retrieval"},
}

var decodeRelationCandidates = []workflows.RelationCandidate{
	{AssetID: "11111111-1111-1111-1111-111111111111", Title: "Target One", Snippet: "hit one"},
	{AssetID: "22222222-2222-2222-2222-222222222222", Title: "Target Two", Snippet: "hit two"},
}

func TestDecodeCandidateResponseStructured(t *testing.T) {
	raw := "```json\n" + `{
		"fields": {"name": " value ", "score": 2},
		"field_confidence": {"name": 0.9, "name_absent": 0.8, "score": 1.7},
		"summary": "  一段中文摘要。  ",
		"tags": [
			{"key": "go", "display_name": "ignored", "is_new": false, "confidence": 0.8},
			{"key": "Ghost", "display_name": "Ghost", "is_new": false, "confidence": 0.9},
			{"key": "  vector ", "display_name": "", "is_new": true, "confidence": 1.4},
			{"key": "", "display_name": "x", "is_new": true, "confidence": 0.5},
			{"key": "bad\u0000key", "display_name": "z", "is_new": true, "confidence": 0.5},
			{"key": "notes", "display_name": "", "is_new": true, "confidence": -3}
		],
		"relations": [
			{"target_asset_id": "11111111-1111-1111-1111-111111111111", "relation_type": "references", "confidence": 0.7},
			{"target_asset_id": "99999999-9999-9999-9999-999999999999", "relation_type": "cites", "confidence": 0.9},
			{"target_asset_id": "22222222-2222-2222-2222-222222222222", "relation_type": "part_of", "confidence": 0.9}
		]
	}` + "\n```"
	extraction, err := decodeCandidateResponse(raw, decodeTagCandidates, decodeRelationCandidates)
	if err != nil {
		t.Fatalf("decode structured response: %v", err)
	}
	if extraction.Fields["name"] != " value " || extraction.Fields["score"] == nil {
		t.Fatalf("candidate fields = %#v", extraction.Fields)
	}
	if _, exists := extraction.Fields["organization_id"]; exists {
		t.Fatal("organization_id must never be accepted from a model")
	}
	if extraction.FieldConfidence["name"] != 0.9 {
		t.Fatalf("field confidence = %#v", extraction.FieldConfidence)
	}
	if _, exists := extraction.FieldConfidence["name_absent"]; exists {
		t.Fatal("confidence for keys the model never output must be dropped")
	}
	if extraction.FieldConfidence["score"] != 1 {
		t.Fatal("out-of-range confidence must clamp to 1")
	}
	if extraction.Summary == nil || *extraction.Summary != "一段中文摘要。" {
		t.Fatalf("summary = %#v", extraction.Summary)
	}
	if len(extraction.Tags) != 3 {
		t.Fatalf("tags = %#v", extraction.Tags)
	}
	if extraction.Tags[0].Key != "go" || extraction.Tags[0].DisplayName != "Go" || extraction.Tags[0].IsNew {
		t.Fatalf("vocabulary hit must keep the workspace display name: %#v", extraction.Tags[0])
	}
	if extraction.Tags[1].Key != "vector" || !extraction.Tags[1].IsNew || extraction.Tags[1].Confidence != 1 || extraction.Tags[1].DisplayName != "vector" {
		t.Fatalf("new tag must normalize its key, default its name and clamp: %#v", extraction.Tags[1])
	}
	if extraction.Tags[2].Key != "notes" || extraction.Tags[2].Confidence != 0 {
		t.Fatalf("negative confidence must clamp to 0 instead of dropping the tag: %#v", extraction.Tags[2])
	}
	if len(extraction.Relations) != 1 {
		t.Fatalf("relations = %#v", extraction.Relations)
	}
	if extraction.Relations[0].TargetAssetID != "11111111-1111-1111-1111-111111111111" ||
		extraction.Relations[0].RelationType != "references" || extraction.Relations[0].Confidence != 0.7 {
		t.Fatalf("relation = %#v", extraction.Relations[0])
	}
}

func TestDecodeCandidateResponseFailsClosedOnRelations(t *testing.T) {
	extraction, err := decodeCandidateResponse(`{"fields":{},"tags":[],"relations":[
		{"target_asset_id":"11111111-1111-1111-1111-111111111111","relation_type":"references","confidence":0.7}
	]}`, decodeTagCandidates, nil)
	if err != nil {
		t.Fatalf("decode without relation candidates: %v", err)
	}
	if len(extraction.Relations) != 0 {
		t.Fatalf("relation whitelist must be empty when no candidates are supplied: %#v", extraction.Relations)
	}
}

func TestDecodeCandidateResponseEmptyVocabularyForcesNewTags(t *testing.T) {
	extraction, err := decodeCandidateResponse(`{"fields":{},"tags":[
		{"key":"Legacy","display_name":"","is_new":false,"confidence":0.9}
	]}`, nil, decodeRelationCandidates)
	if err != nil {
		t.Fatalf("decode without tag vocabulary: %v", err)
	}
	if len(extraction.Tags) != 0 {
		t.Fatalf("tags outside an empty vocabulary must be dropped: %#v", extraction.Tags)
	}
}

func TestDecodeCandidateResponseRejectsInvalidJSON(t *testing.T) {
	if _, err := decodeCandidateResponse("not json", nil, nil); err == nil {
		t.Fatal("invalid model output should be rejected")
	}
	if _, err := decodeCandidateResponse(`{"fields":{}} {"second":2}`, nil, nil); err == nil {
		t.Fatal("multiple JSON values should be rejected")
	}
	if _, err := decodeCandidateResponse(`[]`, nil, nil); err == nil {
		t.Fatal("non-object output should be rejected")
	}
}

func TestDecodeCandidateResponseTruncatesLongSummary(t *testing.T) {
	long := strings.Repeat("长", 600)
	extraction, err := decodeCandidateResponse(`{"fields":{},"summary":"`+long+`"}`, nil, nil)
	if err != nil {
		t.Fatalf("decode long summary: %v", err)
	}
	if extraction.Summary == nil || len([]rune(*extraction.Summary)) != 500 {
		t.Fatalf("summary must truncate to 500 runes: %#v", extraction.Summary)
	}
	if extraction.Tags != nil {
		t.Fatalf("a summary-only reply without array keys is not treated as legacy fields here")
	}
}

func TestDecodeCandidateResponseDeduplicatesSuggestions(t *testing.T) {
	extraction, err := decodeCandidateResponse(`{"fields":{},"tags":[
		{"key":"go","display_name":"","is_new":false,"confidence":0.4},
		{"key":"go","display_name":"","is_new":false,"confidence":0.8}
	],"relations":[
		{"target_asset_id":"11111111-1111-1111-1111-111111111111","relation_type":"cites","confidence":0.2},
		{"target_asset_id":"11111111-1111-1111-1111-111111111111","relation_type":"cites","confidence":0.6}
	]}`, decodeTagCandidates, decodeRelationCandidates)
	if err != nil {
		t.Fatalf("decode duplicates: %v", err)
	}
	if len(extraction.Tags) != 1 || extraction.Tags[0].Confidence != 0.8 {
		t.Fatalf("tag duplicates must keep the higher confidence: %#v", extraction.Tags)
	}
	if len(extraction.Relations) != 1 || extraction.Relations[0].Confidence != 0.6 {
		t.Fatalf("relation duplicates must keep the higher confidence: %#v", extraction.Relations)
	}
}

func TestAssetCandidateResponseSchemaConstrainsSuggestions(t *testing.T) {
	result, err := assetCandidateResponseSchema([]byte(`{"fields":[{"key":"name","type":"string","required":true,"validation":{"min_length":2}},{"key":"tags","type":"multiselect","options":["a","b"]}],"additional_properties":false}`),
		decodeTagCandidates, decodeRelationCandidates)
	if err != nil {
		t.Fatalf("convert candidate schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(result, &schema); err != nil {
		t.Fatalf("decode converted schema: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	if schema["additionalProperties"] != false {
		t.Fatalf("top level must close additional properties: %#v", schema)
	}
	fields, _ := properties["fields"].(map[string]any)
	fieldProperties, _ := fields["properties"].(map[string]any)
	name, _ := fieldProperties["name"].(map[string]any)
	multiselect, _ := fieldProperties["tags"].(map[string]any)
	if name["minLength"] != float64(2) || multiselect["type"] != "array" || fields["additionalProperties"] != false {
		t.Fatalf("resource fields must keep their converted schema: %#v", fields)
	}
	tags, _ := properties["tags"].(map[string]any)
	tagItems, _ := tags["items"].(map[string]any)
	tagItemProperties, _ := tagItems["properties"].(map[string]any)
	tagKey, _ := tagItemProperties["key"].(map[string]any)
	enum, _ := tagKey["enum"].([]any)
	if len(enum) != 2 || enum[0] != "go" || enum[1] != "retrieval" {
		t.Fatalf("tag key enum must mirror the vocabulary: %#v", tagKey)
	}
	relations, _ := properties["relations"].(map[string]any)
	relationItems, _ := relations["items"].(map[string]any)
	relationProperties, _ := relationItems["properties"].(map[string]any)
	target, _ := relationProperties["target_asset_id"].(map[string]any)
	targetEnum, _ := target["enum"].([]any)
	relationType, _ := relationProperties["relation_type"].(map[string]any)
	typeEnum, _ := relationType["enum"].([]any)
	if len(targetEnum) != 2 || targetEnum[0] != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("relation target enum must mirror the candidate list: %#v", target)
	}
	if len(typeEnum) != 5 {
		t.Fatalf("relation type enum must be the closed five-value vocabulary: %#v", relationType)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 5 {
		t.Fatalf("relations must join the required slots: %#v", required)
	}
}

func TestAssetCandidateResponseSchemaFailClosedWithoutCandidates(t *testing.T) {
	result, err := assetCandidateResponseSchema([]byte(`{}`), nil, nil)
	if err != nil {
		t.Fatalf("convert candidate schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(result, &schema); err != nil {
		t.Fatalf("decode converted schema: %v", err)
	}
	properties := schema["properties"].(map[string]any)
	if _, exists := properties["relations"]; exists {
		t.Fatal("the relations slot must be omitted when no candidates are supplied")
	}
	tags := properties["tags"].(map[string]any)
	tagItems := tags["items"].(map[string]any)
	tagItemProperties := tagItems["properties"].(map[string]any)
	tagKey := tagItemProperties["key"].(map[string]any)
	if _, hasEnum := tagKey["enum"]; hasEnum {
		t.Fatal("the tag key enum must be omitted when the vocabulary is empty")
	}
}

func TestAssetCandidateRequestInjectsUntrustedBlocks(t *testing.T) {
	request := assetCandidateRequest(workflows.Input{
		ExistingSummary: "现有摘要",
		SourceTags:      []workflows.TagCandidate{{Key: "go", DisplayName: "Go"}},
		TagCandidates:   decodeTagCandidates,
		RelationCandidates: []workflows.RelationCandidate{
			{AssetID: "11111111-1111-1111-1111-111111111111", Title: "Target One", Snippet: "hit one"},
		},
	}, `{"fields":[]}`, `{"name":"x"}`, "body")
	for _, block := range []string{
		"<schema>", "<fields>", "<summary>", "<source_tags>", "<title>", "<content>",
		"<tag_vocabulary>", "<relation_candidates>", "- key: go | display_name: Go",
	} {
		if !strings.Contains(request, block) {
			t.Fatalf("request must inject %q, got:\n%s", block, request)
		}
	}
	empty := assetCandidateRequest(workflows.Input{}, `{"fields":[]}`, `{}`, "body")
	if !strings.Contains(empty, "none supplied; every suggested tag must set is_new to true") ||
		!strings.Contains(empty, "none supplied; return no relations") {
		t.Fatalf("empty candidates must state the fail-closed rules, got:\n%s", empty)
	}
}
