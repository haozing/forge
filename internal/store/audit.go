package store

import (
	"context"
	"encoding/json"
	"fmt"
)

func (s *Store) RecordQueryLog(ctx context.Context, organizationID, actorUserID, endpoint, queryHash string, resultCount, latencyMS int, outcome string) error {
	if s == nil || s.Pool == nil {
		return fmt.Errorf("database store is not initialized")
	}
	// The original schema uses allowed/denied/error; accept the newer handler
	// vocabulary at the boundary so migrations remain backward compatible.
	if outcome == "succeeded" {
		outcome = "allowed"
	} else if outcome == "failed" {
		outcome = "error"
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO retrieval.query_logs
			(organization_id, actor_user_id, endpoint, query_hash, result_count, outcome, latency_ms)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)
	`, organizationID, actorUserID, endpoint, queryHash, resultCount, outcome, latencyMS)
	return err
}

func (s *Store) RecordAgentChatAudit(ctx context.Context, organizationID, actorUserID, initiatorUserID, applicationID, sessionID, result string, metadata map[string]any) error {
	if s == nil || s.Pool == nil {
		return fmt.Errorf("database store is not initialized")
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode agent chat audit metadata: %w", err)
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, initiator_user_id, agent_application_id,
			 action, resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			 'agent.chat', 'agent_session', $5::uuid, $6, $7::jsonb)
	`, organizationID, actorUserID, initiatorUserID, applicationID, sessionID, result, string(metadataJSON))
	return err
}
