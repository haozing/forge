package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agentchunzhi/internal/asset"
	"agentchunzhi/internal/attachment"
	"agentchunzhi/internal/automation"
	"agentchunzhi/internal/deletion"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/resourcemodel"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const (
	// ProjectionConsumer is the retrieval fact consumer key registered in
	// the eventing registry.
	ProjectionConsumer     = "retrieval.projection"
	TranscriptionConsumer  = "conversation.transcription"
	AttachmentScanConsumer = "attachment.scan"
)

// EventProcessor is the retrieval fact consumer surface: the coordinator
// reacts to domain facts and ensures projection runs/jobs in short
// transactions.
type EventProcessor interface {
	ProcessFact(ctx context.Context, eventType string, payload json.RawMessage) error
}

type TranscriptionProcessor interface {
	Process(context.Context, string, string) error
	Fail(context.Context, string, string, string) error
}

type AttachmentScanProcessor interface {
	Process(context.Context, string, string) error
	Fail(context.Context, string, string) error
}

// Dispatcher keeps consumer-specific behavior, while River owns scheduling,
// wakeups, retries, and worker process lifecycle.
type Dispatcher struct {
	Store            *store.Store
	Retrieval        EventProcessor
	Transcription    TranscriptionProcessor
	AttachmentScan   AttachmentScanProcessor
	Lease            time.Duration
	RetryDelay       time.Duration
	ProcessorVersion string
	Logf             func(string, ...any)
}

type transcriptionEvent struct {
	JobID   string `json:"job_id"`
	MediaID string `json:"media_id"`
}

type attachmentScanEvent struct {
	AttachmentID string `json:"attachment_id"`
}

// ProcessEvent drains all deliveries that are currently due for one event.
// A retryable consumer failure is returned to River only after its own
// delivery attempt has been committed as retry_wait.
func (d Dispatcher) ProcessEvent(ctx context.Context, eventID string, maxAttempts int) error {
	if d.Store == nil || d.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	if maxAttempts <= 0 {
		maxAttempts = eventing.DefaultMaxDeliveryAttempts
	}
	for {
		delivery, err := d.Store.ClaimDelivery(ctx, eventID, d.Lease, d.ProcessorVersion)
		if errors.Is(err, store.ErrNoPendingDelivery) {
			return nil
		}
		if err != nil {
			return err
		}
		retry, err := d.processDelivery(ctx, delivery, maxAttempts)
		if err != nil {
			return err
		}
		if retry {
			return fmt.Errorf("event delivery %s requires retry", delivery.ID)
		}
	}
}

func (d Dispatcher) processDelivery(ctx context.Context, delivery store.Delivery, maxAttempts int) (bool, error) {
	if delivery.ConsumerKey == ProjectionConsumer {
		// Phase 3 retrieval consumer: the coordinator reacts to the domain
		// facts declared in the event registry and ensures projection runs
		// and River jobs in short transactions.
		if d.Retrieval == nil {
			return d.fail(ctx, delivery, delivery.AttemptNo >= maxAttempts, "processor_unavailable", "retrieval coordinator is not configured")
		}
		if err := d.Retrieval.ProcessFact(ctx, delivery.EventType, delivery.Payload); err != nil {
			return d.fail(ctx, delivery, delivery.AttemptNo >= maxAttempts, "processor_error", err.Error())
		}
		return false, d.finish(ctx, delivery, false, "", "")
	}

	if delivery.ConsumerKey == TranscriptionConsumer {
		var event transcriptionEvent
		if err := json.Unmarshal(delivery.Payload, &event); err != nil || event.JobID == "" || event.MediaID == "" {
			return false, d.finish(ctx, delivery, true, "invalid_payload", "transcription payload is invalid")
		}
		if d.Transcription == nil {
			return d.fail(ctx, delivery, delivery.AttemptNo >= maxAttempts, "processor_unavailable", "transcription processor is not configured")
		}
		if err := d.Transcription.Process(ctx, event.JobID, event.MediaID); err == nil {
			return false, d.finish(ctx, delivery, false, "", "")
		} else {
			dead := delivery.AttemptNo >= maxAttempts
			if dead {
				_ = d.Transcription.Fail(ctx, event.JobID, event.MediaID, err.Error())
			}
			return d.fail(ctx, delivery, dead, "processor_error", err.Error())
		}
	}

	if delivery.ConsumerKey == AttachmentScanConsumer {
		var event attachmentScanEvent
		if err := json.Unmarshal(delivery.Payload, &event); err != nil || event.AttachmentID == "" {
			return false, d.finish(ctx, delivery, true, "invalid_payload", "attachment scan payload is invalid")
		}
		if d.AttachmentScan == nil {
			dead := delivery.AttemptNo >= maxAttempts
			if dead {
				_ = (&attachment.ScanProcessor{Store: d.Store}).Fail(ctx, delivery.OrganizationID, event.AttachmentID)
			}
			return d.fail(ctx, delivery, dead, "processor_unavailable", "attachment scanner is not configured")
		}
		if err := d.AttachmentScan.Process(ctx, delivery.OrganizationID, event.AttachmentID); err == nil || errors.Is(err, attachment.ErrNotFound) {
			return false, d.finish(ctx, delivery, false, "", "")
		} else {
			dead := delivery.AttemptNo >= maxAttempts
			if dead {
				_ = d.AttachmentScan.Fail(ctx, delivery.OrganizationID, event.AttachmentID)
			}
			return d.fail(ctx, delivery, dead, "scan_failed", err.Error())
		}
	}

	return false, d.finish(ctx, delivery, true, "unsupported_consumer", fmt.Sprintf("unsupported consumer %q", delivery.ConsumerKey))
}

func (d Dispatcher) fail(ctx context.Context, delivery store.Delivery, dead bool, code, summary string) (bool, error) {
	if err := d.finish(ctx, delivery, dead, code, summary); err != nil {
		return false, err
	}
	return !dead, nil
}

// finish stores the per-consumer result. A terminal failure is represented as
// a successful River execution because this event has no runnable delivery.
func (d Dispatcher) finish(ctx context.Context, delivery store.Delivery, dead bool, code, summary string) error {
	success := code == ""
	retryAt := time.Now().UTC().Add(d.retryDelay(delivery.AttemptNo))
	if success || dead {
		retryAt = time.Now().UTC()
	}
	if err := d.Store.FinishDelivery(ctx, delivery, success, dead, retryAt, code, summary); err != nil {
		return err
	}
	if d.Logf != nil {
		if success {
			d.Logf("delivery succeeded consumer=%s delivery=%s attempt=%d", delivery.ConsumerKey, delivery.ID, delivery.AttemptNo)
		} else if dead {
			d.Logf("delivery dead consumer=%s delivery=%s attempt=%d code=%s", delivery.ConsumerKey, delivery.ID, delivery.AttemptNo, code)
		} else {
			d.Logf("delivery retry scheduled consumer=%s delivery=%s attempt=%d code=%s", delivery.ConsumerKey, delivery.ID, delivery.AttemptNo, code)
		}
	}
	return nil
}

func (d Dispatcher) retryDelay(attempt int) time.Duration {
	base := d.RetryDelay
	if base <= 0 {
		base = 10 * time.Second
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for i := 1; i < attempt && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

type DispatchEventWorker struct {
	river.WorkerDefaults[eventing.DispatchEventArgs]
	Dispatcher Dispatcher
}

func (w *DispatchEventWorker) Work(ctx context.Context, job *river.Job[eventing.DispatchEventArgs]) error {
	return w.Dispatcher.ProcessEvent(ctx, job.Args.EventID, job.MaxAttempts)
}

type RecoverPendingDeliveriesWorker struct {
	river.WorkerDefaults[eventing.RecoverPendingDeliveriesArgs]
	Store *store.Store
	Limit int
}

type RecoverAutomationAttemptsWorker struct {
	river.WorkerDefaults[eventing.RecoverAutomationAttemptsArgs]
	Service automation.Service
	Limit   int
}

type BackgroundJobsWorker struct {
	river.WorkerDefaults[eventing.ProcessBackgroundJobsArgs]
	Migrations resourcemodel.MigrationProcessor
	Transfers  asset.TransferProcessor
	Deletions  deletion.Processor
	Automation automation.Service
	Operations automation.OperationProcessor
	// Attachments, when set, expires unreferenced attachments from the
	// background loop (see attachment.ScanProcessor.CleanupExpired).
	Attachments AttachmentCleanupProcessor
	WorkerID    string
	Limit       int
}

// AttachmentCleanupProcessor is the narrow surface of the attachment scanner
// the background loop needs.
type AttachmentCleanupProcessor interface {
	CleanupExpired(ctx context.Context) (int, error)
}

func (w *BackgroundJobsWorker) Work(ctx context.Context, _ *river.Job[eventing.ProcessBackgroundJobsArgs]) error {
	limit := w.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	workerID := w.WorkerID
	if workerID == "" {
		workerID = "background-worker"
	}
	for i := 0; i < limit; i++ {
		processed := false
		if err := w.Migrations.ProcessNext(ctx); err == nil {
			processed = true
		} else if !errors.Is(err, resourcemodel.ErrNoPendingMigration) {
			return err
		}
		if w.Attachments != nil {
			if expired, err := w.Attachments.CleanupExpired(ctx); err != nil {
				return err
			} else if expired > 0 {
				processed = true
			}
		}
		if err := w.Transfers.ProcessNextImport(ctx); err == nil {
			processed = true
		} else if !errors.Is(err, asset.ErrNoPendingImport) {
			return err
		}
		if err := w.Transfers.ProcessNextExport(ctx); err == nil {
			processed = true
		} else if !errors.Is(err, asset.ErrNoPendingExport) {
			return err
		}
		if err := w.Deletions.ProcessNext(ctx); err == nil {
			processed = true
		} else if !errors.Is(err, deletion.ErrNoPendingJob) {
			return err
		}
		claimed, err := w.Automation.ClaimNextRun(ctx, workerID, 10*time.Minute)
		if err == nil {
			processed = true
			operationErr := w.Operations.Process(ctx, claimed)
			if errors.Is(operationErr, automation.ErrRunWaiting) {
				continue
			}
			if _, finishErr := w.Automation.FinishAttempt(ctx, claimed.Attempt.ID, workerID, operationErr == nil, errorCode(operationErr), errorSummary(operationErr)); finishErr != nil {
				return finishErr
			}
		} else if !errors.Is(err, automation.ErrNoPendingRun) {
			return err
		}
		if !processed {
			return nil
		}
	}
	return nil
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	return "operation_failed"
}
func errorSummary(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 2000 {
		return value[:2000]
	}
	return value
}

func (w *RecoverAutomationAttemptsWorker) Work(ctx context.Context, _ *river.Job[eventing.RecoverAutomationAttemptsArgs]) error {
	if w.Service.Store == nil {
		return errors.New("database store is not initialized")
	}
	_, err := w.Service.RequeueExpiredAttempts(ctx, w.Limit)
	return err
}

func (w *RecoverPendingDeliveriesWorker) Work(ctx context.Context, _ *river.Job[eventing.RecoverPendingDeliveriesArgs]) error {
	if w.Store == nil {
		return errors.New("database store is not initialized")
	}
	ids, err := w.Store.DueEventIDs(ctx, w.Limit)
	if err != nil {
		return err
	}
	client := river.ClientFromContext[pgx.Tx](ctx)
	if client == nil {
		return errors.New("River client is unavailable in job context")
	}
	for _, eventID := range ids {
		if _, err := client.Insert(ctx, eventing.DispatchEventArgs{EventID: eventID}, nil); err != nil {
			return fmt.Errorf("requeue event %s: %w", eventID, err)
		}
	}
	return nil
}

// CleanupExpiredRowsArgs triggers the retention sweep for tables without an
// application-level expiry path: replay reservations, dead sessions and auth
// rate-limit buckets.
type CleanupExpiredRowsArgs struct{}

func (CleanupExpiredRowsArgs) Kind() string { return "maintenance.cleanup_expired_rows" }

func (CleanupExpiredRowsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, Queue: eventing.QueueMaintenance}
}

// retentionSweepBatchSize bounds each DELETE statement so the sweep holds
// short lock queues even against large backlogs.
const retentionSweepBatchSize = 500

// ExpiredRowsWorker is the periodic retention sweep. It deletes:
//   - system.idempotency_keys rows past expires_at (replay is impossible
//     after the 24h TTL),
//   - identity.sessions rows whose absolute lifetime ended more than 7 days
//     ago (retention window for revoked/expired sessions; a row this old can
//     never authenticate again),
//   - security.auth_rate_limits buckets from windows older than 1 day, unless
//     a block started in that window is still in force.
//
// Each table is drained in batches of at most retentionSweepBatchSize rows.
type ExpiredRowsWorker struct {
	river.WorkerDefaults[CleanupExpiredRowsArgs]
	Store *store.Store
	Logf  func(string, ...any)
}

func (w *ExpiredRowsWorker) Work(ctx context.Context, _ *river.Job[CleanupExpiredRowsArgs]) error {
	if w.Store == nil || w.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	swept, err := SweepExpiredRows(ctx, w.Store)
	if err != nil {
		return err
	}
	if w.Logf != nil && (swept[0] > 0 || swept[1] > 0 || swept[2] > 0) {
		w.Logf("retention sweep removed idempotency_keys=%d sessions=%d auth_rate_limits=%d", swept[0], swept[1], swept[2])
	}
	return nil
}

// SweepExpiredRows drains all expired retention tables and returns the total
// deleted rows per table (idempotency_keys, sessions, auth_rate_limits).
func SweepExpiredRows(ctx context.Context, database *store.Store) ([3]int, error) {
	if database == nil || database.Pool == nil {
		return [3]int{}, errors.New("database store is not initialized")
	}
	var swept [3]int
	var err error
	if swept[0], err = deleteInBatches(ctx, database.Pool, `
		DELETE FROM system.idempotency_keys
		WHERE id IN (SELECT id FROM system.idempotency_keys WHERE expires_at <= now() LIMIT $1)
	`); err != nil {
		return swept, fmt.Errorf("sweep idempotency keys: %w", err)
	}
	if swept[1], err = deleteInBatches(ctx, database.Pool, `
		DELETE FROM identity.sessions
		WHERE id IN (
			SELECT id FROM identity.sessions
			WHERE absolute_expires_at < now() - interval '7 days' LIMIT $1
		)
	`); err != nil {
		return swept, fmt.Errorf("sweep sessions: %w", err)
	}
	if swept[2], err = deleteInBatches(ctx, database.Pool, `
		DELETE FROM security.auth_rate_limits
		WHERE (bucket_type, key_hash) IN (
			SELECT bucket_type, key_hash FROM security.auth_rate_limits
			WHERE window_started_at < now() - interval '1 day'
			  AND (blocked_until IS NULL OR blocked_until <= now())
			LIMIT $1
		)
	`); err != nil {
		return swept, fmt.Errorf("sweep auth rate limits: %w", err)
	}
	return swept, nil
}

// deleteInBatches repeats the batched DELETE until fewer than the batch size
// rows remain to delete.
func deleteInBatches(ctx context.Context, pool *pgxpool.Pool, statement string) (int, error) {
	total := 0
	for {
		command, err := pool.Exec(ctx, statement, retentionSweepBatchSize)
		if err != nil {
			return total, err
		}
		deleted := int(command.RowsAffected())
		total += deleted
		if deleted < retentionSweepBatchSize {
			return total, nil
		}
	}
}
