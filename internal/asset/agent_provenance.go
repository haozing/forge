package asset

// agent_provenance.go — doc §8.3 publish-record provenance. A publish whose
// version came out of the suggestion flow must record the agent user, the
// agent application, the rule version, the input and output versions and the
// processing result. The data lives on integration.agent_processing_results:
// its output_version_id is backfilled by the draft commit that materialized
// the accepted suggestions, so "the newest processing result of this output
// version" is exactly the six-element chain §8.3 demands.

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// agentProvenanceTx returns the §8.3 publish-record fields when a processing
// result produced this version: agent user, application, rule version,
// input/output version ids and the processing result id. No row (a purely
// human version) or a query failure yields nil — provenance is enrichment on
// the audit trail, never a gate on the publish itself.
func agentProvenanceTx(ctx context.Context, tx pgx.Tx, organizationID, versionID string) map[string]any {
	var agentUserID, agentApplicationID, ruleVersion, inputVersionID, processingResultID string
	err := tx.QueryRow(ctx, `
		SELECT pr.agent_user_id::text, pr.agent_application_id::text, COALESCE(pr.rule_version, ''),
		       pr.input_version_id::text, pr.id::text
		FROM integration.agent_processing_results pr
		WHERE pr.organization_id = $1::uuid AND pr.output_version_id = $2::uuid
		ORDER BY pr.completed_at DESC
		LIMIT 1
	`, organizationID, versionID).Scan(&agentUserID, &agentApplicationID, &ruleVersion,
		&inputVersionID, &processingResultID)
	if err != nil {
		// ErrNoRows is the ordinary human-publish case; anything else must not
		// fail the publish transaction, so it degrades to no provenance.
		return nil
	}
	return map[string]any{
		"agent_user_id":        agentUserID,
		"agent_application_id": agentApplicationID,
		"rule_version":         ruleVersion,
		"input_version_id":     inputVersionID,
		"output_version_id":    versionID,
		"processing_result_id": processingResultID,
	}
}

// AgentProvenanceTx is the exported form for surfaces outside this package
// (the review approve audit merges the same map; doc §4.5).
func AgentProvenanceTx(ctx context.Context, tx pgx.Tx, organizationID, versionID string) map[string]any {
	return agentProvenanceTx(ctx, tx, organizationID, versionID)
}

// MergeAgentProvenance shallow-copies metadata and appends the provenance
// keys; neither input map is mutated. An empty provenance returns metadata
// unchanged.
func MergeAgentProvenance(metadata, provenance map[string]any) map[string]any {
	if len(provenance) == 0 {
		return metadata
	}
	merged := make(map[string]any, len(metadata)+len(provenance))
	for key, value := range metadata {
		merged[key] = value
	}
	for key, value := range provenance {
		merged[key] = value
	}
	return merged
}
