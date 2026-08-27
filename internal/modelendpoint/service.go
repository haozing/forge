package modelendpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type HealthChecker interface {
	Check(ctx context.Context, endpointID string, revision int64) (Capabilities, error)
}

type Service struct {
	Store          *store.Store
	Cipher         *CredentialCipher
	AllowedHosts   []string
	DefaultTimeout int
	Health         HealthChecker
}

func (s Service) Create(ctx context.Context, principal auth.Principal, input CreateInput) (Endpoint, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.ProviderType = strings.TrimSpace(input.ProviderType)
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.ModelName = strings.TrimSpace(input.ModelName)
	input.APIKey = strings.TrimSpace(input.APIKey)
	input.SecretRef = strings.TrimSpace(input.SecretRef)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.Options = input.Options.WithDefaults(s.DefaultTimeout)
	if principal.UserType != "member" || !s.validConfiguration(input.Name, input.ProviderType, input.BaseURL, input.ModelName, input.APIKey, input.SecretRef, input.Options) || !validIdempotencyKey(input.IdempotencyKey) {
		return Endpoint{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil || s.Cipher == nil {
		return Endpoint{}, errors.New("model endpoint service is not initialized")
	}

	endpointID := uuid.NewString()
	revisionID := uuid.NewString()
	credentialMode, ciphertext, keyID, err := s.encodeCredential(principal.OrganizationID, endpointID, input.APIKey, input.SecretRef)
	if err != nil {
		return Endpoint{}, err
	}
	optionsJSON, _ := json.Marshal(input.Options)
	capabilitiesJSON := []byte(`{}`)
	checksum := configurationChecksum(input.ProviderType, input.BaseURL, input.ModelName, credentialMode, ciphertext, input.SecretRef, optionsJSON)

	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Endpoint{}, fmt.Errorf("begin model endpoint creation: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := reserveIdempotency(ctx, tx, principal, "model_endpoint.create", input.IdempotencyKey, checksum); err != nil {
		return Endpoint{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO integration.model_endpoints
			(id, organization_id, name, current_revision, status, created_by)
		VALUES ($1::uuid, $2::uuid, $3, 1, 'unavailable', $4::uuid)
	`, endpointID, principal.OrganizationID, input.Name, principal.UserID); err != nil {
		return Endpoint{}, translateWriteError("create model endpoint", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO integration.model_endpoint_revisions
			(id, model_endpoint_id, revision, provider_type, base_url, model_name,
			 credential_mode, credential_ciphertext, credential_key_id, secret_ref,
			 options, capabilities, config_checksum, created_by)
		VALUES
			($1::uuid, $2::uuid, 1, $3, $4, $5, $6, $7, $8, $9,
			 $10::jsonb, $11::jsonb, $12, $13::uuid)
	`, revisionID, endpointID, input.ProviderType, input.BaseURL, input.ModelName,
		credentialMode, ciphertext, nullableString(keyID), nullableString(input.SecretRef),
		string(optionsJSON), string(capabilitiesJSON), checksum, principal.UserID); err != nil {
		return Endpoint{}, translateWriteError("create model endpoint revision", err)
	}
	if err := recordAudit(ctx, tx, principal, endpointID, "model_endpoint.create", map[string]any{
		"revision": 1, "provider_type": input.ProviderType, "model_name": input.ModelName,
		"credential_mode": credentialMode, "config_checksum": checksum,
	}); err != nil {
		return Endpoint{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Endpoint{}, fmt.Errorf("commit model endpoint creation: %w", err)
	}
	return s.Get(ctx, principal, endpointID)
}

func (s Service) Replace(ctx context.Context, principal auth.Principal, input ReplaceInput) (Endpoint, error) {
	input.EndpointID = strings.TrimSpace(input.EndpointID)
	input.Name = strings.TrimSpace(input.Name)
	input.ProviderType = strings.TrimSpace(input.ProviderType)
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.ModelName = strings.TrimSpace(input.ModelName)
	input.APIKey = strings.TrimSpace(input.APIKey)
	input.SecretRef = strings.TrimSpace(input.SecretRef)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.Options = input.Options.WithDefaults(s.DefaultTimeout)
	if principal.UserType != "member" || uuid.Validate(input.EndpointID) != nil || !validText(input.Name, 1, 200) || !validProvider(input.ProviderType) || ValidateBaseURL(input.BaseURL, s.AllowedHosts) != nil || !validText(input.ModelName, 1, 200) || !validateOptions(input.Options) || (input.APIKey != "" && input.SecretRef != "") || !validIdempotencyKey(input.IdempotencyKey) {
		return Endpoint{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil || s.Cipher == nil {
		return Endpoint{}, errors.New("model endpoint service is not initialized")
	}

	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Endpoint{}, fmt.Errorf("begin model endpoint replacement: %w", err)
	}
	defer tx.Rollback(ctx)
	var currentRevision int64
	var currentMode string
	var currentCiphertext []byte
	var currentKeyID, currentSecretRef *string
	err = tx.QueryRow(ctx, `
		SELECT e.current_revision, r.credential_mode, r.credential_ciphertext,
		       r.credential_key_id, r.secret_ref
		FROM integration.model_endpoints e
		JOIN integration.model_endpoint_revisions r
		  ON r.model_endpoint_id = e.id AND r.revision = e.current_revision
		WHERE e.id = $1::uuid AND e.organization_id = $2::uuid
		FOR UPDATE OF e
	`, input.EndpointID, principal.OrganizationID).Scan(&currentRevision, &currentMode, &currentCiphertext, &currentKeyID, &currentSecretRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return Endpoint{}, ErrNotFound
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("load model endpoint for replacement: %w", err)
	}

	credentialMode := currentMode
	ciphertext := currentCiphertext
	keyID := stringValue(currentKeyID)
	secretRef := stringValue(currentSecretRef)
	if input.APIKey != "" || input.SecretRef != "" {
		credentialMode, ciphertext, keyID, err = s.encodeCredential(principal.OrganizationID, input.EndpointID, input.APIKey, input.SecretRef)
		if err != nil {
			return Endpoint{}, err
		}
		secretRef = input.SecretRef
	}
	optionsJSON, _ := json.Marshal(input.Options)
	checksum := configurationChecksum(input.ProviderType, input.BaseURL, input.ModelName, credentialMode, ciphertext, secretRef, optionsJSON)
	if err := reserveIdempotency(ctx, tx, principal, "model_endpoint.replace", input.IdempotencyKey, checksum); err != nil {
		return Endpoint{}, err
	}
	newRevision := currentRevision + 1
	if _, err := tx.Exec(ctx, `
		INSERT INTO integration.model_endpoint_revisions
			(model_endpoint_id, revision, provider_type, base_url, model_name,
			 credential_mode, credential_ciphertext, credential_key_id, secret_ref,
			 options, capabilities, config_checksum, created_by)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, '{}'::jsonb, $11, $12::uuid)
	`, input.EndpointID, newRevision, input.ProviderType, input.BaseURL, input.ModelName,
		credentialMode, ciphertext, nullableString(keyID), nullableString(secretRef), string(optionsJSON), checksum, principal.UserID); err != nil {
		return Endpoint{}, translateWriteError("create replacement model endpoint revision", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE integration.model_endpoints
		SET name = $3, current_revision = $4, status = 'unavailable',
		    last_verified_at = NULL, last_health_error_code = NULL, updated_at = now()
		WHERE id = $1::uuid AND organization_id = $2::uuid
	`, input.EndpointID, principal.OrganizationID, input.Name, newRevision); err != nil {
		return Endpoint{}, translateWriteError("activate replacement model endpoint revision", err)
	}
	if err := recordAudit(ctx, tx, principal, input.EndpointID, "model_endpoint.replace", map[string]any{
		"revision": newRevision, "provider_type": input.ProviderType, "model_name": input.ModelName,
		"credential_mode": credentialMode, "config_checksum": checksum,
	}); err != nil {
		return Endpoint{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Endpoint{}, fmt.Errorf("commit model endpoint replacement: %w", err)
	}
	return s.Get(ctx, principal, input.EndpointID)
}

func (s Service) List(ctx context.Context, principal auth.Principal, limit int) ([]Endpoint, error) {
	if principal.UserType != "member" || limit < 1 || limit > 200 || s.Store == nil || s.Store.Pool == nil {
		return nil, ErrInvalidInput
	}
	rows, err := s.Store.Pool.Query(ctx, endpointSelect+`
		WHERE e.organization_id = $1::uuid
		ORDER BY e.updated_at DESC, e.id
		LIMIT $2
	`, principal.OrganizationID, limit)
	if err != nil {
		return nil, fmt.Errorf("list model endpoints: %w", err)
	}
	defer rows.Close()
	result := make([]Endpoint, 0)
	for rows.Next() {
		item, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model endpoints: %w", err)
	}
	return result, nil
}

func (s Service) Get(ctx context.Context, principal auth.Principal, endpointID string) (Endpoint, error) {
	if principal.UserType != "member" || uuid.Validate(endpointID) != nil || s.Store == nil || s.Store.Pool == nil {
		return Endpoint{}, ErrInvalidInput
	}
	item, err := scanEndpoint(s.Store.Pool.QueryRow(ctx, endpointSelect+`
		WHERE e.organization_id = $1::uuid AND e.id = $2::uuid
	`, principal.OrganizationID, endpointID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Endpoint{}, ErrNotFound
	}
	return item, err
}

// StatusResult carries the updated endpoint plus an optional machine-readable
// warning. The warning is set when a previously unverified or unhealthy
// endpoint is enabled; enabling stays an explicit administrator decision, so a
// missing external connectivity no longer blocks the flip.
type StatusResult struct {
	Endpoint
	Warning string `json:"warning,omitempty"`
}

const WarningEnableUnverified = "model_endpoint_enabled_without_verified_health"

func (s Service) SetStatus(ctx context.Context, principal auth.Principal, endpointID, status string) (StatusResult, error) {
	status = strings.TrimSpace(status)
	if principal.UserType != "member" || uuid.Validate(endpointID) != nil || (status != "active" && status != "disabled") || s.Store == nil || s.Store.Pool == nil {
		return StatusResult{}, ErrInvalidInput
	}
	warning := ""
	if status == "active" {
		var verified *time.Time
		var healthError *string
		if err := s.Store.Pool.QueryRow(ctx, `SELECT last_verified_at, last_health_error_code FROM integration.model_endpoints WHERE id = $1::uuid AND organization_id = $2::uuid`, endpointID, principal.OrganizationID).Scan(&verified, &healthError); errors.Is(err, pgx.ErrNoRows) {
			return StatusResult{}, ErrNotFound
		} else if err != nil {
			return StatusResult{}, fmt.Errorf("load model endpoint health: %w", err)
		}
		// Enable is an intent operation, not a probe result: warn the caller
		// when health telemetry is stale or failed instead of blocking.
		warning = enableWarning(verified, stringValue(healthError))
	}
	result, err := s.Store.Pool.Exec(ctx, `UPDATE integration.model_endpoints SET status = $3, updated_at = now() WHERE id = $1::uuid AND organization_id = $2::uuid`, endpointID, principal.OrganizationID, status)
	if err != nil {
		return StatusResult{}, fmt.Errorf("update model endpoint status: %w", err)
	}
	if result.RowsAffected() == 0 {
		return StatusResult{}, ErrNotFound
	}
	item, err := s.Get(ctx, principal, endpointID)
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{Endpoint: item, Warning: warning}, nil
}

// enableWarning maps current health telemetry to the warning code emitted on
// enable. An empty result means the endpoint was verified and healthy.
func enableWarning(verified *time.Time, healthErrorCode string) string {
	if verified == nil || healthErrorCode != "" {
		return WarningEnableUnverified
	}
	return ""
}

const HealthCheckFailedCode = "model_endpoint_check_failed"

// ProbeResult reports a connectivity probe outcome. The endpoint status is
// deliberately absent from the mutation: a failed probe must never flip
// endpoint.status (it only refreshes health telemetry), so administrators keep
// full control over enable/disable.
type ProbeResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Endpoint
}

// probeOutcome classifies one health-check attempt. It never decides the
// endpoint status; the health error code is telemetry only.
func probeOutcome(err error) (bool, string, string) {
	if err == nil {
		return true, "", ""
	}
	return false, err.Error(), HealthCheckFailedCode
}

func (s Service) Test(ctx context.Context, principal auth.Principal, endpointID string) (ProbeResult, error) {
	if principal.UserType != "member" || uuid.Validate(endpointID) != nil || s.Store == nil || s.Store.Pool == nil || s.Health == nil {
		return ProbeResult{}, ErrInvalidInput
	}
	item, err := s.Get(ctx, principal, endpointID)
	if err != nil {
		return ProbeResult{}, err
	}
	capabilities, checkErr := s.Health.Check(ctx, endpointID, item.CurrentRevision)
	ok, detail, healthErrorCode := probeOutcome(checkErr)
	capabilitiesJSON, _ := json.Marshal(capabilities)
	if checkErr != nil {
		// Failed probe: record telemetry only; endpoint.status stays as-is.
		if _, updateErr := s.Store.Pool.Exec(ctx, `
			UPDATE integration.model_endpoints
			SET last_verified_at = now(), last_health_error_code = $3, updated_at = now()
			WHERE id = $1::uuid AND organization_id = $2::uuid
		`, endpointID, principal.OrganizationID, healthErrorCode); updateErr != nil {
			return ProbeResult{}, fmt.Errorf("save failed endpoint health: %w", updateErr)
		}
	} else {
		tx, err := s.Store.Pool.Begin(ctx)
		if err != nil {
			return ProbeResult{}, fmt.Errorf("begin endpoint health update: %w", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `
			UPDATE integration.model_endpoint_revisions
			SET capabilities = $3::jsonb
			WHERE model_endpoint_id = $1::uuid AND revision = $2
		`, endpointID, item.CurrentRevision, string(capabilitiesJSON)); err != nil {
			return ProbeResult{}, fmt.Errorf("save model capabilities: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE integration.model_endpoints
			SET last_verified_at = now(), last_health_error_code = NULL, updated_at = now()
			WHERE id = $1::uuid AND organization_id = $2::uuid
		`, endpointID, principal.OrganizationID); err != nil {
			return ProbeResult{}, fmt.Errorf("save endpoint health: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return ProbeResult{}, fmt.Errorf("commit endpoint health: %w", err)
		}
	}
	item, err = s.Get(ctx, principal, endpointID)
	if err != nil {
		return ProbeResult{}, err
	}
	return ProbeResult{OK: ok, Detail: detail, Endpoint: item}, nil
}

func (s Service) encodeCredential(organizationID, endpointID, apiKey, secretRef string) (string, []byte, string, error) {
	if apiKey != "" && secretRef == "" {
		ciphertext, err := s.Cipher.Encrypt(apiKey, CredentialAdditionalData(organizationID, endpointID))
		if err != nil {
			return "", nil, "", fmt.Errorf("encrypt model credential: %w", err)
		}
		return "encrypted", ciphertext, s.Cipher.KeyID(), nil
	}
	if apiKey == "" && validSecretRef(secretRef) {
		return "secret_ref", nil, "", nil
	}
	return "", nil, "", ErrInvalidInput
}

func (s Service) validConfiguration(name, providerType, baseURL, modelName, apiKey, secretRef string, options Options) bool {
	return validText(name, 1, 200) && validProvider(providerType) && ValidateBaseURL(baseURL, s.AllowedHosts) == nil && validText(modelName, 1, 200) && validateOptions(options) && ((apiKey != "" && secretRef == "") || (apiKey == "" && validSecretRef(secretRef)))
}

func validProvider(value string) bool {
	return value == ProviderOpenAI || value == ProviderOpenAICompatible
}

func validSecretRef(value string) bool {
	return strings.HasPrefix(value, "env://") && validText(strings.TrimPrefix(value, "env://"), 1, 200)
}

func validText(value string, min, max int) bool {
	value = strings.TrimSpace(value)
	return len([]rune(value)) >= min && len([]rune(value)) <= max && !strings.ContainsRune(value, '\x00')
}

func validIdempotencyKey(value string) bool {
	return len(value) >= 16 && len(value) <= 200 && !strings.ContainsRune(value, '\x00')
}

func configurationChecksum(providerType, baseURL, modelName, credentialMode string, ciphertext []byte, secretRef string, options []byte) string {
	digest := sha256.New()
	for _, value := range [][]byte{[]byte(providerType), []byte(baseURL), []byte(modelName), []byte(credentialMode), ciphertext, []byte(secretRef), options} {
		_, _ = digest.Write(value)
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

const endpointSelect = `
	SELECT e.id::text, e.organization_id::text, e.name, e.current_revision, e.status,
	       r.provider_type, r.base_url, r.model_name, r.credential_mode,
	       r.credential_ciphertext IS NOT NULL, COALESCE(r.credential_key_id, ''),
	       COALESCE(r.secret_ref, ''), r.options, r.capabilities, r.config_checksum,
	       e.last_verified_at, COALESCE(e.last_health_error_code, ''), e.created_at, e.updated_at
	FROM integration.model_endpoints e
	JOIN integration.model_endpoint_revisions r
	  ON r.model_endpoint_id = e.id AND r.revision = e.current_revision
`

type endpointScanner interface {
	Scan(dest ...any) error
}

func scanEndpoint(row endpointScanner) (Endpoint, error) {
	var item Endpoint
	var optionsJSON, capabilitiesJSON []byte
	err := row.Scan(&item.ID, &item.OrganizationID, &item.Name, &item.CurrentRevision, &item.Status,
		&item.ProviderType, &item.BaseURL, &item.ModelName, &item.CredentialMode,
		&item.HasCredential, &item.CredentialKeyID, &item.SecretRef, &optionsJSON,
		&capabilitiesJSON, &item.ConfigChecksum, &item.LastVerifiedAt,
		&item.LastHealthErrorCode, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return Endpoint{}, err
	}
	if err := json.Unmarshal(optionsJSON, &item.Options); err != nil {
		return Endpoint{}, fmt.Errorf("decode model endpoint options: %w", err)
	}
	if err := json.Unmarshal(capabilitiesJSON, &item.Capabilities); err != nil {
		return Endpoint{}, fmt.Errorf("decode model endpoint capabilities: %w", err)
	}
	return item, nil
}

func reserveIdempotency(ctx context.Context, tx pgx.Tx, principal auth.Principal, operation, key, requestHash string) error {
	result, err := tx.Exec(ctx, `
		INSERT INTO system.idempotency_keys
			(organization_id, subject_id, operation, idempotency_key, request_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, now() + interval '24 hours')
		ON CONFLICT (organization_id, subject_id, operation, idempotency_key) DO NOTHING
	`, principal.OrganizationID, principal.UserID, operation, key, requestHash)
	if err != nil {
		return fmt.Errorf("reserve model endpoint idempotency: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func recordAudit(ctx context.Context, tx pgx.Tx, principal auth.Principal, endpointID, action string, metadata map[string]any) error {
	metadataJSON, _ := json.Marshal(metadata)
	_, err := tx.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, initiator_user_id, action,
			 resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, $2::uuid, $3, 'model_endpoint', $4::uuid, 'allowed', $5::jsonb)
	`, principal.OrganizationID, principal.UserID, action, endpointID, string(metadataJSON))
	if err != nil {
		return fmt.Errorf("record model endpoint audit: %w", err)
	}
	return nil
}

func translateWriteError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23503") {
		return ErrConflict
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
