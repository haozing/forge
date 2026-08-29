package query

import (
	"context"
	"fmt"

	"agentchunzhi/internal/store"
)

// modelPolicy is the retrieval-relevant slice of the published ResourceModel
// version policy. The scope channel and both retrieval modes gate the plan
// (doc §6.1); there is no retrieval.hybrid switch.
type modelPolicy struct {
	ModelID         string
	ChannelEnabled  bool
	FulltextEnabled bool
	SemanticEnabled bool
}

// loadModelPolicies reads the current published version policy of every scope
// model, including the enablement flag of the scope channel.
func loadModelPolicies(ctx context.Context, store *store.Store, organizationID string, channel QueryChannel, modelIDs []string) (map[string]modelPolicy, error) {
	policies := make(map[string]modelPolicy, len(modelIDs))
	if len(modelIDs) == 0 {
		return policies, nil
	}
	rows, err := store.Pool.Query(ctx, `
		SELECT rm.id::text,
		       COALESCE(NULLIF(v.policy #>> ARRAY['channels', $2, 'enabled'], '')::boolean, false),
		       COALESCE(NULLIF(v.policy #>> '{retrieval,fulltext,enabled}'::text[], '')::boolean, false),
		       COALESCE(NULLIF(v.policy #>> '{retrieval,semantic,enabled}'::text[], '')::boolean, false)
		FROM model.resource_models rm
		JOIN model.resource_model_versions v
		  ON v.organization_id = rm.organization_id AND v.id = rm.current_version_id
		WHERE rm.organization_id = $1::uuid AND rm.status = 'active'
		  AND rm.id = ANY($3::uuid[])
	`, organizationID, string(channel), modelIDs)
	if err != nil {
		return nil, fmt.Errorf("load model retrieval policies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var policy modelPolicy
		if err := rows.Scan(&policy.ModelID, &policy.ChannelEnabled, &policy.FulltextEnabled, &policy.SemanticEnabled); err != nil {
			return nil, err
		}
		policies[policy.ModelID] = policy
	}
	return policies, rows.Err()
}

// plan is the policy-gated execution plan. Empty model lists mean "recall
// nothing on this branch", which is distinct from an authorization failure.
type plan struct {
	RequestedMode  string
	ExecutedMode   string
	ChannelModels  []string
	FulltextModels []string
	SemanticModels []string
	// DegradationReasons accumulates the fixed degradation enum values of the
	// executed request (doc §10.6).
	DegradationReasons []string
}

// buildPlan resolves the executed mode and per-branch models. Policy
// disallowance is a controlled 422 (query_mode_not_enabled) when the caller
// explicitly requested one model; scope-wide queries with no eligible model
// simply return no candidates (doc §6.1).
func buildPlan(mode string, scope QueryAccessScope, requestedModels []string, policies map[string]modelPolicy) (plan, error) {
	effective := make([]string, 0, len(scope.ResourceModelIDs))
	for _, modelID := range scope.ResourceModelIDs {
		policy, ok := policies[modelID]
		if !ok || !policy.ChannelEnabled {
			continue
		}
		effective = append(effective, modelID)
	}
	if len(requestedModels) > 0 {
		effective = intersectStrings(effective, requestedModels)
	}
	fulltext := make([]string, 0, len(effective))
	semantic := make([]string, 0, len(effective))
	for _, modelID := range effective {
		policy := policies[modelID]
		if policy.FulltextEnabled {
			fulltext = append(fulltext, modelID)
		}
		if policy.SemanticEnabled {
			semantic = append(semantic, modelID)
		}
	}
	result := plan{
		RequestedMode:  mode,
		ExecutedMode:   mode,
		ChannelModels:  effective,
		FulltextModels: fulltext,
		SemanticModels: semantic,
	}
	// A request that only names models outside the raw scope gets a silent
	// empty result: it must not reveal policy state of models the caller
	// cannot see. Explicitly requested in-scope models with a disabled mode
	// answer query_mode_not_enabled (doc §6.1/§11.5).
	rawScopeIntersection := intersectStrings(scope.ResourceModelIDs, requestedModels)
	explicitRequest := len(requestedModels) > 0 && len(rawScopeIntersection) > 0
	switch mode {
	case ModeStructured:
		return result, nil
	case ModeFulltext:
		if len(fulltext) == 0 {
			if explicitRequest {
				return plan{}, ErrQueryModeNotEnabled
			}
		}
		return result, nil
	case ModeSemantic:
		if len(semantic) == 0 {
			if explicitRequest {
				return plan{}, ErrQueryModeNotEnabled
			}
		}
		return result, nil
	case ModeHybrid:
		switch {
		case len(fulltext) > 0 && len(semantic) > 0:
			return result, nil
		case len(fulltext) > 0:
			// One policy-sanctioned branch: executed mode reflects reality
			// without counting as a technical degradation (doc §6.1).
			result.ExecutedMode = ModeFulltext
			return result, nil
		case len(semantic) > 0:
			result.ExecutedMode = ModeSemantic
			return result, nil
		default:
			if explicitRequest {
				return plan{}, ErrQueryModeNotEnabled
			}
			return result, nil
		}
	}
	return plan{}, ErrInvalidQueryMode
}
