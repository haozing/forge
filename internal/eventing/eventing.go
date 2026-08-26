package eventing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
)

const (
	QueueEvents      = "events"
	QueueMaintenance = "maintenance"

	DispatchEventKind             = "event.dispatch"
	RecoverPendingDeliveriesKind  = "event.recover_pending"
	RecoverAutomationAttemptsKind = "automation.recover_attempts"
	ProcessBackgroundJobsKind     = "background.process_jobs"
	DefaultMaxDeliveryAttempts    = 8
)

// DispatchEventArgs is the durable River trigger for all deliveries created
// for one outbox event. The event itself stays in audit.outbox_events.
type DispatchEventArgs struct {
	EventID string `json:"event_id" river:"unique"`
}

func (DispatchEventArgs) Kind() string { return DispatchEventKind }

func (DispatchEventArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: DefaultMaxDeliveryAttempts,
		Queue:       QueueEvents,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
		},
	}
}

type RecoverPendingDeliveriesArgs struct{}

func (RecoverPendingDeliveriesArgs) Kind() string { return RecoverPendingDeliveriesKind }

func (RecoverPendingDeliveriesArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, Queue: QueueMaintenance}
}

type RecoverAutomationAttemptsArgs struct{}

func (RecoverAutomationAttemptsArgs) Kind() string { return RecoverAutomationAttemptsKind }

func (RecoverAutomationAttemptsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, Queue: QueueMaintenance}
}

type ProcessBackgroundJobsArgs struct{}

func (ProcessBackgroundJobsArgs) Kind() string { return ProcessBackgroundJobsKind }

func (ProcessBackgroundJobsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, Queue: QueueMaintenance}
}

type ConsumerManifest struct {
	Key             string
	EventVersions   map[string]int
	IdempotencyNote string
	FailureMode     string
}

type Registry struct {
	byEvent map[string][]ConsumerManifest
}

func NewRegistry(manifests []ConsumerManifest) (Registry, error) {
	registry := Registry{byEvent: make(map[string][]ConsumerManifest)}
	keys := make(map[string]struct{}, len(manifests))
	for _, manifest := range manifests {
		manifest.Key = strings.TrimSpace(manifest.Key)
		if manifest.Key == "" {
			return Registry{}, errors.New("event consumer key is required")
		}
		if _, exists := keys[manifest.Key]; exists {
			return Registry{}, fmt.Errorf("duplicate event consumer key %q", manifest.Key)
		}
		keys[manifest.Key] = struct{}{}
		if len(manifest.EventVersions) == 0 {
			return Registry{}, fmt.Errorf("event consumer %q has no accepted event types", manifest.Key)
		}
		for eventType, version := range manifest.EventVersions {
			eventType = strings.TrimSpace(eventType)
			if eventType == "" || version <= 0 {
				return Registry{}, fmt.Errorf("event consumer %q has invalid event contract", manifest.Key)
			}
			registry.byEvent[eventKey(eventType, version)] = append(registry.byEvent[eventKey(eventType, version)], manifest)
		}
	}
	for key := range registry.byEvent {
		sort.Slice(registry.byEvent[key], func(i, j int) bool {
			return registry.byEvent[key][i].Key < registry.byEvent[key][j].Key
		})
	}
	return registry, nil
}

func (r Registry) ConsumersFor(eventType string, payloadVersion int) []ConsumerManifest {
	consumers := r.byEvent[eventKey(eventType, payloadVersion)]
	return append([]ConsumerManifest(nil), consumers...)
}

func eventKey(eventType string, payloadVersion int) string {
	return strings.TrimSpace(eventType) + "@" + fmt.Sprint(payloadVersion)
}

func DefaultRegistry() (Registry, error) {
	return NewRegistry([]ConsumerManifest{
		{
			Key:             "retrieval.projection",
			EventVersions:   map[string]int{"asset.retrieval_projection_requested": 1},
			IdempotencyNote: "asset version chunk and embedding projection is idempotent",
			FailureMode:     "retry_then_dead",
		},
		{
			Key:             "conversation.transcription",
			EventVersions:   map[string]int{"conversation.media.transcription_requested": 1},
			IdempotencyNote: "processing job and transcription block are idempotent",
			FailureMode:     "retry_then_dead",
		},
		{
			Key:             "attachment.scan",
			EventVersions:   map[string]int{"attachment.created": 1},
			IdempotencyNote: "terminal attachment scan states are idempotent",
			FailureMode:     "retry_then_failed",
		},
	})
}

type QueueClient interface {
	InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

type EventStore struct {
	Queue    QueueClient
	Registry Registry
}

type Event struct {
	OrganizationID   string
	EventType        string
	AggregateType    string
	AggregateID      string
	AggregateVersion int
	PayloadVersion   int
	Payload          any
}

// AppendTx writes the immutable event, its declared consumer deliveries, and
// the River dispatch job in the caller's business transaction.
func (s EventStore) AppendTx(ctx context.Context, tx pgx.Tx, event Event) (string, error) {
	if tx == nil {
		return "", errors.New("event transaction is required")
	}
	if strings.TrimSpace(event.OrganizationID) == "" || strings.TrimSpace(event.EventType) == "" ||
		strings.TrimSpace(event.AggregateType) == "" || strings.TrimSpace(event.AggregateID) == "" ||
		event.AggregateVersion <= 0 || event.PayloadVersion <= 0 {
		return "", errors.New("event metadata is invalid")
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return "", fmt.Errorf("encode event payload: %w", err)
	}
	checksum := sha256.Sum256(payload)
	var eventID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO audit.outbox_events
			(organization_id, event_type, aggregate_type, aggregate_id, aggregate_version, payload_version, payload, payload_checksum)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6, $7::jsonb, $8)
		RETURNING id::text
	`, event.OrganizationID, event.EventType, event.AggregateType, event.AggregateID,
		event.AggregateVersion, event.PayloadVersion, string(payload), hex.EncodeToString(checksum[:])).Scan(&eventID); err != nil {
		return "", fmt.Errorf("record outbox event: %w", err)
	}
	consumers := s.Registry.ConsumersFor(event.EventType, event.PayloadVersion)
	for _, consumer := range consumers {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit.event_deliveries (event_id, consumer_key)
			VALUES ($1::uuid, $2)
		`, eventID, consumer.Key); err != nil {
			return "", fmt.Errorf("record event delivery %s: %w", consumer.Key, err)
		}
	}
	if len(consumers) > 0 {
		if s.Queue == nil {
			return "", errors.New("river event queue is not initialized")
		}
		if _, err := s.Queue.InsertTx(ctx, tx, DispatchEventArgs{EventID: eventID}, nil); err != nil {
			return "", fmt.Errorf("enqueue River event dispatch: %w", err)
		}
	}
	return eventID, nil
}

func NewInsertOnlyClient(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	return river.NewClient(riverpgxv5.New(pool), &river.Config{SkipUnknownJobCheck: true})
}

func NewEventStore(pool *pgxpool.Pool) (EventStore, error) {
	registry, err := DefaultRegistry()
	if err != nil {
		return EventStore{}, err
	}
	queue, err := NewInsertOnlyClient(pool)
	if err != nil {
		return EventStore{}, err
	}
	return EventStore{Queue: queue, Registry: registry}, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("database pool is required")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire River migration lock connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('agentchunzhi:river-migrations'))`); err != nil {
		return fmt.Errorf("lock River migration: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('agentchunzhi:river-migrations'))`)
	}()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("initialize River migration: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("apply River migration: %w", err)
	}
	return nil
}
