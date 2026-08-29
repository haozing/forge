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
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

const (
	ProjectionConsumer     = retrieval.ProjectionConsumer
	TranscriptionConsumer  = "conversation.transcription"
	AttachmentScanConsumer = "attachment.scan"
)

type ProjectionProcessor interface {
	Rebuild(context.Context, string) error
	Delete(context.Context, string) error
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
	Projection       ProjectionProcessor
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
		// Phase 0 fact consumer: the projector reacts to asset domain facts
		// instead of receiving downstream commands from the asset service.
		switch delivery.EventType {
		case eventing.EventAssetPublished:
			var payload eventing.AssetPublishedPayload
			if err := json.Unmarshal(delivery.Payload, &payload); err != nil || payload.VersionID == "" {
				return false, d.finish(ctx, delivery, true, "invalid_payload", "asset.published payload is invalid")
			}
			if d.Projection == nil {
				return d.fail(ctx, delivery, delivery.AttemptNo >= maxAttempts, "processor_unavailable", "retrieval projector is not configured")
			}
			if payload.PreviousVersionID != "" && payload.PreviousVersionID != payload.VersionID {
				if err := d.Projection.Delete(ctx, payload.PreviousVersionID); err != nil {
					return d.fail(ctx, delivery, delivery.AttemptNo >= maxAttempts, "processor_error", err.Error())
				}
			}
			if err := d.Projection.Rebuild(ctx, payload.VersionID); err != nil {
				return d.fail(ctx, delivery, delivery.AttemptNo >= maxAttempts, "processor_error", err.Error())
			}
			return false, d.finish(ctx, delivery, false, "", "")
		case eventing.EventAssetArchived:
			var payload eventing.AssetArchivedPayload
			if err := json.Unmarshal(delivery.Payload, &payload); err != nil {
				return false, d.finish(ctx, delivery, true, "invalid_payload", "asset.archived payload is invalid")
			}
			if d.Projection == nil {
				return d.fail(ctx, delivery, delivery.AttemptNo >= maxAttempts, "processor_unavailable", "retrieval projector is not configured")
			}
			if payload.PreviousVersionID != "" {
				if err := d.Projection.Delete(ctx, payload.PreviousVersionID); err != nil {
					return d.fail(ctx, delivery, delivery.AttemptNo >= maxAttempts, "processor_error", err.Error())
				}
			}
			return false, d.finish(ctx, delivery, false, "", "")
		case eventing.EventAssetVisibilityChanged, eventing.EventTagUpdated, eventing.EventResourceModelPolicyPublished:
			// Visibility is re-checked against primary data at query time and
			// tag/policy-driven canonical rebuilds arrive with phase 3; the
			// fact itself is consumed successfully here.
			return false, d.finish(ctx, delivery, false, "", "")
		default:
			return false, d.finish(ctx, delivery, true, "unsupported_event", fmt.Sprintf("projection consumer does not handle %q", delivery.EventType))
		}
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
	WorkerID   string
	Limit      int
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
