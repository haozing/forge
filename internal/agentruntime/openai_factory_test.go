package agentruntime

import (
	"reflect"
	"testing"

	"agentchunzhi/internal/modelendpoint"
)

func TestModelExtraFields(t *testing.T) {
	if got := modelExtraFields(modelendpoint.Options{}); got != nil {
		t.Fatalf("empty thinking mode produced extra fields: %#v", got)
	}

	want := map[string]any{
		"thinking": map[string]any{"type": "disabled"},
	}
	got := modelExtraFields(modelendpoint.Options{ThinkingMode: "disabled"})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extra fields = %#v, want %#v", got, want)
	}
}
