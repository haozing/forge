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
