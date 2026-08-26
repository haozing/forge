package workflows

import (
	"context"
	"encoding/json"
	"strings"
)

// Executor is the worker-facing adapter. PostgreSQL stores only the lookup
// key; graph topology and node code remain compiled into this process.
type Executor struct {
	Registry Registry
}

func (e Executor) RegistryReady() bool {
	return len(e.Registry.definitions) > 0
}

func (e Executor) Execute(ctx context.Context, workflowKey, runID string, payload map[string]any) (Output, error) {
	definition, err := e.Registry.Resolve(workflowKey)
	if err != nil {
		return Output{}, err
	}
	input := Input{RunID: strings.TrimSpace(runID), Values: payload, AssetIDs: assetIDs(payload["asset_ids"])}
	return definition.Run.Invoke(ctx, input)
}

func assetIDs(value any) []string {
	var result []string
	switch values := value.(type) {
	case []string:
		result = append(result, values...)
	case []any:
		for _, item := range values {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, strings.TrimSpace(text))
			}
		}
	}
	return result
}

func DecodePayload(raw []byte) map[string]any {
	result := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return result
}
