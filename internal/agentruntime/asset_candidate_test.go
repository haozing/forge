package agentruntime

import (
	"encoding/json"
	"testing"
)

func TestDecodeCandidateObject(t *testing.T) {
	fields, err := decodeCandidateObject("```json\n{\"fields\":{\"name\":\" value \"}}\n```")
	if err != nil {
		t.Fatalf("decode candidate: %v", err)
	}
	if fields["name"] != " value " {
		t.Fatalf("candidate fields = %#v", fields)
	}
	fields, err = decodeCandidateObject(`{"organization_id":"forged","score":2}`)
	if err != nil {
		t.Fatalf("decode candidate with forbidden fields: %v", err)
	}
	if _, exists := fields["organization_id"]; exists {
		t.Fatal("organization_id must never be accepted from a model")
	}
	if _, err := decodeCandidateObject("not json"); err == nil {
		t.Fatal("invalid model output should be rejected")
	}
	if _, err := decodeCandidateObject(`{"first":1} {"second":2}`); err == nil {
		t.Fatal("multiple JSON values should be rejected")
	}
}

func TestAssetCandidateResponseSchemaConvertsResourceFields(t *testing.T) {
	result, err := assetCandidateResponseSchema([]byte(`{"fields":[{"key":"name","type":"string","required":true,"validation":{"min_length":2}},{"key":"tags","type":"multiselect","options":["a","b"]}],"additional_properties":false}`))
	if err != nil {
		t.Fatalf("convert candidate schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(result, &schema); err != nil {
		t.Fatalf("decode converted schema: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	name, _ := properties["name"].(map[string]any)
	tags, _ := properties["tags"].(map[string]any)
	if name["minLength"] != float64(2) || tags["type"] != "array" || schema["additionalProperties"] != false {
		t.Fatalf("unexpected converted schema: %#v", schema)
	}
}
