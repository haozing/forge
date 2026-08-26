package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrNoPendingDelivery = errors.New("no pending delivery")
var ErrDeliveryLeaseLost = errors.New("delivery lease lost")

type Delivery struct {
	ID             string
	EventID        string
	OrganizationID string
	ConsumerKey    string
	EventType      string
	AggregateID    string
	Payload        json.RawMessage
	AttemptNo      int
	LeaseToken     string
	StartedAt      time.Time
}

// ClaimDelivery atomically claims one due delivery for an outbox event and
// records its attempt. Expired processing leases are eligible after a crash.
func (s *Store) ClaimDelivery(ctx context.Context, eventID string, lease time.Duration, processorVersion string) (Delivery, error) {
	if s == nil || s.Pool == nil {
		return Delivery{}, errors.New("database store is not initialized")
	}
	if strings.TrimSpace(eventID) == "" {
		return Delivery{}, errors.New("event id is required")
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Delivery{}, fmt.Errorf("begin delivery claim: %w", err)
	}
	defer tx.Rollback(ctx)

	var delivery Delivery
	var payload []byte
	err = tx.QueryRow(ctx, `
		SELECT d.id::text, d.event_id::text, e.organization_id::text, d.consumer_key, e.event_type,
		       e.aggregate_id::text, e.payload, d.attempt_count + 1
		FROM audit.event_deliveries d
		JOIN audit.outbox_events e ON e.id = d.event_id
		WHERE d.event_id = $1::uuid
		  AND (
			(d.status IN ('pending', 'retry_wait') AND d.next_attempt_at <= now())
			OR (d.status = 'processing' AND d.lease_until < now())
		  )
		ORDER BY d.next_attempt_at, d.id
		LIMIT 1
		FOR UPDATE OF d SKIP LOCKED
	`, eventID).Scan(
		&delivery.ID,
		&delivery.EventID,
		&delivery.OrganizationID,
		&delivery.ConsumerKey,
		&delivery.EventType,
		&delivery.AggregateID,
		&payload,
		&delivery.AttemptNo,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrNoPendingDelivery
	}
	if err != nil {
		return Delivery{}, fmt.Errorf("select delivery: %w", err)
	}
	delivery.Payload = json.RawMessage(payload)
	delivery.StartedAt = time.Now().UTC()

	if err := tx.QueryRow(ctx, `
		UPDATE audit.event_deliveries
		SET status = 'processing',
		    attempt_count = attempt_count + 1,
		    lease_token = gen_random_uuid()::text,
		    lease_until = now() + $2::interval
		WHERE id = $1::uuid
		RETURNING lease_token, attempt_count
	`, delivery.ID, lease.String()).Scan(&delivery.LeaseToken, &delivery.AttemptNo); err != nil {
		return Delivery{}, fmt.Errorf("mark delivery processing: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.event_delivery_attempts
			(event_delivery_id, attempt_no, status, processor_version, started_at)
		VALUES ($1::uuid, $2, 'started', $3, now())
	`, delivery.ID, delivery.AttemptNo, processorVersion); err != nil {
		return Delivery{}, fmt.Errorf("record delivery attempt: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Delivery{}, fmt.Errorf("commit delivery claim: %w", err)
	}
	return delivery, nil
}

// DueEventIDs returns event IDs whose deliveries must be dispatched now. It is
// used by the periodic recovery job to repair a River job discarded or lost
// after an otherwise committed business transaction.
func (s *Store) DueEventIDs(ctx context.Context, limit int) ([]string, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("database store is not initialized")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT d.event_id::text
		FROM audit.event_deliveries d
		WHERE (d.status IN ('pending', 'retry_wait') AND d.next_attempt_at <= now())
		   OR (d.status = 'processing' AND d.lease_until < now())
		ORDER BY d.event_id::text
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list due event deliveries: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var eventID string
		if err := rows.Scan(&eventID); err != nil {
			return nil, fmt.Errorf("scan due event delivery: %w", err)
		}
		ids = append(ids, eventID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due event deliveries: %w", err)
	}
	return ids, nil
}

// FinishDelivery updates a delivery only while its lease token is still held.
// Retry and dead statuses retain a concise error summary for operations.
func (s *Store) FinishDelivery(ctx context.Context, delivery Delivery, success bool, dead bool, retryAt time.Time, errorCode, errorSummary string) error {
	if s == nil || s.Pool == nil {
		return errors.New("database store is not initialized")
	}
	status := "retry_wait"
	if success {
		status = "succeeded"
	} else if dead {
		status = "dead"
	}
	if retryAt.IsZero() {
		retryAt = time.Now().UTC()
	}
	errorSummary = strings.TrimSpace(errorSummary)
	if len(errorSummary) > 1024 {
		errorSummary = errorSummary[:1024]
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delivery finish: %w", err)
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `
		UPDATE audit.event_deliveries
		SET status = $3,
		    next_attempt_at = CASE WHEN $3 = 'retry_wait' THEN $4::timestamptz ELSE next_attempt_at END,
		    lease_token = NULL,
		    lease_until = NULL,
		    last_error_code = CASE WHEN $3 = 'succeeded' THEN NULL ELSE $5 END,
		    last_error_summary = CASE WHEN $3 = 'succeeded' THEN NULL ELSE $6 END,
		    completed_at = CASE WHEN $3 IN ('succeeded', 'dead') THEN now() ELSE NULL END
		WHERE id = $1::uuid
		  AND lease_token = $2
	`, delivery.ID, delivery.LeaseToken, status, retryAt, errorCode, errorSummary)
	if err != nil {
		return fmt.Errorf("finish delivery: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrDeliveryLeaseLost
	}
	attemptStatus := "failed"
	if success {
		attemptStatus = "succeeded"
	}
	durationMS := time.Since(delivery.StartedAt).Milliseconds()
	if durationMS < 0 {
		durationMS = 0
	}
	if _, err := tx.Exec(ctx, `
		UPDATE audit.event_delivery_attempts
		SET status = $3,
		    error_code = CASE WHEN $3 = 'succeeded' THEN NULL ELSE $4 END,
		    duration_ms = $5,
		    completed_at = now()
		WHERE event_delivery_id = $1::uuid
		  AND attempt_no = $2
	`, delivery.ID, delivery.AttemptNo, attemptStatus, errorCode, durationMS); err != nil {
		return fmt.Errorf("finish delivery attempt: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delivery finish: %w", err)
	}
	return nil
}
