package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/compose"
)

type assetPrepareState struct {
	Input     Input
	Candidate map[string]any
}

type assetPrepareGraph struct {
	runnable compose.Runnable[Input, Output]
}

type fixedWorkflowGraph struct {
	key      string
	runnable compose.Runnable[Input, Output]
}

func NewFixedWorkflowGraph(key string) (Runnable, error) {
	key = strings.TrimSpace(key)
	steps, ok := fixedWorkflowSteps()[key]
	if !ok {
		return nil, errors.New("workflow key is required")
	}
	graph := compose.NewGraph[Input, Output]()
	for index, step := range steps {
		if index == len(steps)-1 {
			if err := graph.AddLambdaNode(step, compose.InvokableLambda(func(_ context.Context, input Input) (Output, error) {
				if strings.TrimSpace(input.RunID) == "" {
					return Output{}, errors.New("workflow run ID is required")
				}
				return Output{WorkflowKey: key, CodeVersion: 1, Values: cloneValues(input.Values)}, nil
			})); err != nil {
				return nil, err
			}
			continue
		}
		if err := graph.AddLambdaNode(step, compose.InvokableLambda(func(_ context.Context, input Input) (Input, error) {
			if strings.TrimSpace(input.RunID) == "" {
				return Input{}, errors.New("workflow run ID is required")
			}
			input.Values = cloneValues(input.Values)
			return input, nil
		})); err != nil {
			return nil, err
		}
	}
	previous := compose.START
	for _, step := range steps {
		if err := graph.AddEdge(previous, step); err != nil {
			return nil, err
		}
		previous = step
	}
	if err := graph.AddEdge(previous, compose.END); err != nil {
		return nil, err
	}
	runnable, err := graph.Compile(context.Background(), compose.WithGraphName(key))
	if err != nil {
		return nil, err
	}
	return fixedWorkflowGraph{key: key, runnable: runnable}, nil
}

func fixedWorkflowSteps() map[string][]string {
	return map[string][]string{
		"asset_publish":    {"load_reviewed_version", "authz", "publish_transaction", "enqueue_projection"},
		"asset_archive":    {"load_published_version", "authz", "archive_transaction", "delete_projection"},
		"asset_import":     {"parse_input", "validate_schema", "write_raw", "enqueue_prepare"},
		"asset_reindex":    {"resolve_scope", "enqueue_projection", "collect_results"},
		"asset_transcribe": {"load_media", "request_asr", "write_content", "optional_prepare"},
		"note_sync":        {"load_conversation", "build_note_version", "idempotent_write"},
	}
}

func (g fixedWorkflowGraph) Invoke(ctx context.Context, input Input) (Output, error) {
	return g.runnable.Invoke(ctx, input)
}

func NewAssetPrepareGraph(extractors ...CandidateExtractor) (Runnable, error) {
	var extractor CandidateExtractor
	if len(extractors) > 0 {
		extractor = extractors[0]
	}
	graph := compose.NewGraph[Input, Output](compose.WithGenLocalState(func(context.Context) *assetPrepareState {
		return &assetPrepareState{Candidate: map[string]any{}}
	}))
	if err := graph.AddLambdaNode("load_source", compose.InvokableLambda(func(_ context.Context, input Input) (Input, error) {
		if strings.TrimSpace(input.RunID) == "" {
			return Input{}, errors.New("workflow scope is required")
		}
		if len(input.AssetIDs) == 0 {
			return Input{}, errors.New("asset_prepare requires asset IDs")
		}
		return input, nil
	})); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode("extract_fields", compose.InvokableLambda(func(ctx context.Context, input Input) (Input, error) {
		if extractor != nil {
			extracted, err := extractor.ExtractCandidate(ctx, input)
			if err != nil {
				return Input{}, err
			}
			input.Values = cloneValues(extracted.Fields)
			input.Values["_model_input_tokens"] = extracted.InputTokens
			input.Values["_model_output_tokens"] = extracted.OutputTokens
			input.extraction = &extracted
		}
		return input, nil
	})); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode("normalize", compose.InvokableLambda(func(_ context.Context, input Input) (Input, error) {
		input.Values = cloneValues(input.Values)
		for key, value := range input.Values {
			if text, ok := value.(string); ok {
				input.Values[key] = strings.TrimSpace(text)
			}
		}
		return input, nil
	})); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode("find_duplicates", compose.InvokableLambda(func(_ context.Context, input Input) (Input, error) {
		return input, nil
	})); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode("suggest_relations", compose.InvokableLambda(func(_ context.Context, input Input) (Input, error) {
		return input, nil
	})); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode("validate_candidate", compose.InvokableLambda(func(_ context.Context, input Input) (Output, error) {
		candidate := cloneValues(input.Values)
		inputTokens, _ := candidate["_model_input_tokens"].(int)
		outputTokens, _ := candidate["_model_output_tokens"].(int)
		delete(candidate, "_model_input_tokens")
		delete(candidate, "_model_output_tokens")
		candidate["asset_ids"] = append([]string(nil), input.AssetIDs...)
		output := Output{WorkflowKey: "asset_prepare", CodeVersion: 1, Candidate: candidate, Values: input.Values, InputTokens: inputTokens, OutputTokens: outputTokens}
		// The suggestion slots ride the extraction side-channel; they are
		// already whitelist-validated by the extractor.
		if input.extraction != nil {
			output.Summary = input.extraction.Summary
			output.FieldConfidence = input.extraction.FieldConfidence
			output.Tags = input.extraction.Tags
			output.Relations = input.extraction.Relations
		}
		return output, nil
	})); err != nil {
		return nil, err
	}
	for _, edge := range [][2]string{{compose.START, "load_source"}, {"load_source", "extract_fields"}, {"extract_fields", "normalize"}, {"normalize", "find_duplicates"}, {"find_duplicates", "suggest_relations"}, {"suggest_relations", "validate_candidate"}, {"validate_candidate", compose.END}} {
		if err := graph.AddEdge(edge[0], edge[1]); err != nil {
			return nil, fmt.Errorf("asset_prepare graph edge %s -> %s: %w", edge[0], edge[1], err)
		}
	}
	runnable, err := graph.Compile(context.Background(), compose.WithGraphName("asset_prepare"))
	if err != nil {
		return nil, fmt.Errorf("compile asset_prepare graph: %w", err)
	}
	return assetPrepareGraph{runnable: runnable}, nil
}

func (g assetPrepareGraph) Invoke(ctx context.Context, input Input) (Output, error) {
	return g.runnable.Invoke(ctx, input)
}

func cloneValues(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
