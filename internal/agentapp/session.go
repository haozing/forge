package agentapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("agent application not found")
var ErrInvalidInput = errors.New("invalid agent application session input")

type Service struct {
	Store *store.Store
}

type SessionResult struct {
	SessionID          string    `json:"session_id"`
	AgentApplicationID string    `json:"agent_application_id"`
	InitiatorUserID    string    `json:"initiator_user_id"`
	ExpiresAt          time.Time `json:"expires_at"`
	Status             string    `json:"status"`
}

type SessionBinding struct {
	AgentPrincipal     auth.Principal
	AgentApplicationID string
	ModelEndpointID    string
	ModelRevision      int64
	RuntimeMode        string
	AnswerPosture      string
}

func (s Service) Start(ctx context.Context, principal auth.Principal, allowedApplicationIDs []string, applicationID, idempotencyKey string) (SessionResult, error) {
	if principal.UserType != "member" || len(allowedApplicationIDs) == 0 || !validID(applicationID) || !validIdempotencyKey(idempotencyKey) {
		return SessionResult{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return SessionResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return SessionResult{}, fmt.Errorf("begin agent application session: %w", err)
	}
	defer tx.Rollback(ctx)
	var boundAgentUserID string
	err = tx.QueryRow(ctx, `
		SELECT aa.bound_agent_user_id::text
		FROM integration.agent_applications aa
		JOIN identity.users au ON au.id = aa.bound_agent_user_id
		JOIN integration.model_endpoints me ON me.id = aa.model_endpoint_id
		JOIN integration.model_endpoint_revisions mer
		  ON mer.model_endpoint_id = me.id AND mer.revision = me.current_revision
		WHERE aa.id = $1::uuid
		  AND aa.organization_id = $2::uuid
		  AND aa.id::text = ANY($3::text[])
		  AND aa.status = 'active'
		  AND me.status = 'active'
		  AND mer.revoked_at IS NULL
		  AND au.user_type = 'agent'
		  AND au.status = 'active'
		FOR UPDATE OF aa
	`, applicationID, principal.OrganizationID, allowedApplicationIDs).Scan(&boundAgentUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionResult{}, ErrNotFound
	}
	if err != nil {
		return SessionResult{}, fmt.Errorf("load agent application: %w", err)
	}
	var result SessionResult
	result.AgentApplicationID = applicationID
	result.InitiatorUserID = principal.UserID
	err = tx.QueryRow(ctx, `
		INSERT INTO integration.agent_sessions
			(organization_id, agent_application_id, initiator_user_id, bound_agent_user_id, idempotency_key, expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, now() + interval '30 minutes')
		ON CONFLICT (organization_id, initiator_user_id, agent_application_id, idempotency_key) DO NOTHING
		RETURNING id::text, expires_at, status
	`, principal.OrganizationID, applicationID, principal.UserID, boundAgentUserID, idempotencyKey).Scan(&result.SessionID, &result.ExpiresAt, &result.Status)
	created := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			SELECT id::text, expires_at, status
			FROM integration.agent_sessions
			WHERE organization_id = $1::uuid
			  AND initiator_user_id = $2::uuid
			  AND agent_application_id = $3::uuid
			  AND idempotency_key = $4
		`, principal.OrganizationID, principal.UserID, applicationID, idempotencyKey).Scan(&result.SessionID, &result.ExpiresAt, &result.Status)
	}
	if err != nil {
		return SessionResult{}, fmt.Errorf("persist agent application session: %w", err)
	}
	if created {
		metadata, _ := json.Marshal(map[string]string{
			"session_id":           result.SessionID,
			"agent_application_id": applicationID,
			"bound_agent_user_id":  boundAgentUserID,
		})
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit.audit_log
				(organization_id, actor_user_id, initiator_user_id, agent_application_id, action, resource_type, resource_id, result, metadata)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'agent.session.start', 'agent_session', $5::uuid, 'allowed', $6::jsonb)
		`, principal.OrganizationID, boundAgentUserID, principal.UserID, applicationID, result.SessionID, string(metadata)); err != nil {
			return SessionResult{}, fmt.Errorf("record agent application session audit: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return SessionResult{}, fmt.Errorf("commit agent application session: %w", err)
	}
	return result, nil
}

// ResolveActiveAgentPrincipal returns the Agent identity bound to a member's
// still-active application session. The member is only used to scope the
// session lookup; all data authorization must use the returned Agent principal.
func (s Service) ResolveActiveAgentPrincipal(ctx context.Context, member auth.Principal, sessionID string) (auth.Principal, error) {
	binding, err := s.ResolveActiveSession(ctx, member, sessionID)
	if err != nil {
		return auth.Principal{}, err
	}
	return binding.AgentPrincipal, nil
}

func (s Service) ResolveActiveSession(ctx context.Context, member auth.Principal, sessionID string) (SessionBinding, error) {
	if member.UserType != "member" || !validID(sessionID) {
		return SessionBinding{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return SessionBinding{}, errors.New("database store is not initialized")
	}
	var agentUserID, applicationID, modelEndpointID, runtimeMode, answerPosture string
	var modelRevision int64
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT s.bound_agent_user_id::text, s.agent_application_id::text,
		       aa.model_endpoint_id::text, me.current_revision, aa.runtime_mode, aa.answer_posture
		FROM integration.agent_sessions s
		JOIN integration.agent_applications aa ON aa.id = s.agent_application_id
		JOIN identity.users au ON au.id = s.bound_agent_user_id
		JOIN integration.model_endpoints me ON me.id = aa.model_endpoint_id
		JOIN integration.model_endpoint_revisions mer
		  ON mer.model_endpoint_id = me.id AND mer.revision = me.current_revision
		WHERE s.id = $1::uuid
		  AND s.organization_id = $2::uuid
		  AND s.initiator_user_id = $3::uuid
		  AND aa.bound_agent_user_id = s.bound_agent_user_id
		  AND s.status = 'active'
		  AND s.expires_at > now()
		  AND aa.status = 'active'
		  AND me.status = 'active'
		  AND mer.revoked_at IS NULL
		  AND au.organization_id = s.organization_id
		  AND au.user_type = 'agent'
		  AND au.status = 'active'
	`, sessionID, member.OrganizationID, member.UserID).Scan(&agentUserID, &applicationID, &modelEndpointID, &modelRevision, &runtimeMode, &answerPosture)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionBinding{}, ErrNotFound
	}
	if err != nil {
		return SessionBinding{}, fmt.Errorf("resolve active agent session: %w", err)
	}
	return SessionBinding{
		AgentApplicationID: applicationID,
		ModelEndpointID:    modelEndpointID,
		ModelRevision:      modelRevision,
		RuntimeMode:        runtimeMode,
		AnswerPosture:      answerPosture,
		AgentPrincipal: auth.Principal{
			UserID:         agentUserID,
			OrganizationID: member.OrganizationID,
			UserType:       "agent",
		},
	}, nil
}

func validIdempotencyKey(value string) bool {
	return len(value) >= 16 && len(value) <= 200
}

func validID(value string) bool {
	return uuidPattern.MatchString(value)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
