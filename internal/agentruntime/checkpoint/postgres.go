package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/modelendpoint"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var ErrInvalidCheckpointKey = errors.New("invalid checkpoint key")

// PostgresStore is an Eino compose.CheckPointStore backed by encrypted,
// append-versioned checkpoints. One instance is scoped to one organization
// and run so a graph cannot read another tenant's execution state.
type PostgresStore struct {
	Store          *store.Store
	Cipher         *modelendpoint.CredentialCipher
	OrganizationID string
	RunID          string
}

func (s PostgresStore) Set(ctx context.Context, key string, value []byte) error {
	key = strings.TrimSpace(key)
	if !s.valid(key) || s.Cipher == nil {
		return ErrInvalidCheckpointKey
	}
	if s.Store == nil || s.Store.Pool == nil {
		return errors.New("checkpoint store is not initialized")
	}
	checksum := sha256.Sum256(value)
	additionalData := checkpointAdditionalData(s.OrganizationID, s.RunID, key)
	ciphertext, err := s.Cipher.Encrypt(string(value), additionalData)
	if err != nil {
		return fmt.Errorf("encrypt Eino checkpoint: %w", err)
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin checkpoint write: %w", err)
	}
	defer tx.Rollback(ctx)
	var runExists bool
	if err := tx.QueryRow(ctx, `
		SELECT true FROM automation.runs
		WHERE id = $1::uuid AND organization_id = $2::uuid
		FOR UPDATE
	`, s.RunID, s.OrganizationID).Scan(&runExists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("checkpoint run not found")
		}
		return fmt.Errorf("lock checkpoint run: %w", err)
	}
	var checkpointID string
	var sequence int64
	err = tx.QueryRow(ctx, `
		INSERT INTO automation.checkpoints
			(organization_id, run_id, sequence, checkpoint_key, payload_ciphertext, payload_checksum)
		SELECT $1::uuid, $2::uuid, COALESCE(max(sequence), 0) + 1, $3, $4, $5
		FROM automation.checkpoints
		WHERE organization_id = $1::uuid AND run_id = $2::uuid
		RETURNING id::text, sequence
	`, s.OrganizationID, s.RunID, key, ciphertext, hex.EncodeToString(checksum[:])).Scan(&checkpointID, &sequence)
	if err != nil {
		return fmt.Errorf("write Eino checkpoint: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE automation.runs
		SET eino_checkpoint_id = $2::uuid, checkpoint_sequence = $3
		WHERE id = $1::uuid AND organization_id = $4::uuid
	`, s.RunID, checkpointID, sequence, s.OrganizationID); err != nil {
		return fmt.Errorf("link Eino checkpoint to run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit Eino checkpoint: %w", err)
	}
	return nil
}

func (s PostgresStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	key = strings.TrimSpace(key)
	if !s.valid(key) || s.Cipher == nil {
		return nil, false, ErrInvalidCheckpointKey
	}
	if s.Store == nil || s.Store.Pool == nil {
		return nil, false, errors.New("checkpoint store is not initialized")
	}
	var ciphertext []byte
	var checksum string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT payload_ciphertext, payload_checksum
		FROM automation.checkpoints
		WHERE organization_id = $1::uuid AND run_id = $2::uuid AND checkpoint_key = $3
		ORDER BY sequence DESC LIMIT 1
	`, s.OrganizationID, s.RunID, key).Scan(&ciphertext, &checksum)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load Eino checkpoint: %w", err)
	}
	plaintext, err := s.Cipher.Decrypt(ciphertext, checkpointAdditionalData(s.OrganizationID, s.RunID, key))
	if err != nil {
		return nil, false, fmt.Errorf("decrypt Eino checkpoint: %w", err)
	}
	digest := sha256.Sum256([]byte(plaintext))
	if checksum != hex.EncodeToString(digest[:]) {
		return nil, false, errors.New("Eino checkpoint checksum mismatch")
	}
	return []byte(plaintext), true, nil
}

func (s PostgresStore) valid(key string) bool {
	return s.Store != nil && len(key) > 0 && len(key) <= 500 && strings.TrimSpace(s.OrganizationID) != "" && strings.TrimSpace(s.RunID) != ""
}

func checkpointAdditionalData(organizationID, runID, key string) []byte {
	return []byte("agentchunzhi:eino-checkpoint:" + organizationID + ":" + runID + ":" + key)
}
