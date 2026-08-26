package agentruntime

import (
	"testing"

	"github.com/cloudwego/eino-ext/components/model/openai"
)

func TestStructuredResponseFormatModes(t *testing.T) {
	object, err := structuredResponseFormat("json_object", nil)
	if err != nil || object.Type != openai.ChatCompletionResponseFormatTypeJSONObject {
		t.Fatalf("json_object response format = %#v, err=%v", object, err)
	}
	schema, err := structuredResponseFormat("json_schema", []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`))
	if err != nil || schema.Type != openai.ChatCompletionResponseFormatTypeJSONSchema || schema.JSONSchema == nil {
		t.Fatalf("json_schema response format = %#v, err=%v", schema, err)
	}
	if _, err := structuredResponseFormat("json_schema", []byte(`not-json`)); err == nil {
		t.Fatal("invalid JSON schema should be rejected")
	}
	if _, err := structuredResponseFormat("disabled", nil); err == nil {
		t.Fatal("disabled structured output should be rejected")
	}
}
