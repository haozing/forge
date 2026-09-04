package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"agentchunzhi/internal/agentruntime"
	"agentchunzhi/internal/asset"
	"agentchunzhi/internal/attachment"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/automation"
	"agentchunzhi/internal/config"
	"agentchunzhi/internal/content"
	"agentchunzhi/internal/deletion"
	"agentchunzhi/internal/delivery"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/modelendpoint"
	"agentchunzhi/internal/notification"
	"agentchunzhi/internal/objectstore"
	"agentchunzhi/internal/query"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/review"
	"agentchunzhi/internal/site"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"
	"agentchunzhi/internal/transcription"
	"agentchunzhi/internal/worker"
	"agentchunzhi/internal/workflows"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func main() {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("worker configuration invalid: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startupCtx, startupCancel := context.WithTimeout(ctx, 15*time.Second)
	db, err := store.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		startupCancel()
		log.Fatalf("worker database startup failed: %v", err)
	}
	if err := store.VerifySchemaContract(startupCtx, db, cfg.MigrationPath); err != nil {
		db.Close()
		startupCancel()
		log.Fatalf("worker schema contract verification failed: %v", err)
	}
	startupCancel()
	defer db.Close()
	modelCipher, err := modelendpoint.NewCredentialCipher(cfg.AgentModelSecretEncryptionKey)
	if err != nil {
		log.Fatalf("model credential encryption startup failed: %v", err)
	}
	checkpointCipher, err := modelendpoint.NewCredentialCipher(cfg.AgentCheckpointEncryptionKey)
	if err != nil {
		log.Fatalf("checkpoint encryption startup failed: %v", err)
	}
	modelRegistry := &agentruntime.ModelRegistry{
		Source: agentruntime.PostgresConfigSource{Store: db}, Cipher: modelCipher,
		Secrets: agentruntime.EnvironmentSecretResolver{},
		Factory: agentruntime.OpenAIModelFactory{
			AllowedHosts:       cfg.AgentModelAllowedHosts,
			AllowPrivateEgress: cfg.AgentModelAllowPrivateEgress,
			Limiter:            agentruntime.NewModelRequestLimiter(cfg.AgentModelMaxConcurrentRequests),
		},
		MaxEntries: cfg.AgentModelMaxCacheEntries,
	}

	events, err := eventing.NewEventStore(db.Pool)
	if err != nil {
		log.Fatalf("worker event store startup failed: %v", err)
	}
	// Phase 1 email delivery worker: claim, decrypt, render and send. Payload
	// encryption uses the same key ring as the API.
	deliveryKeys, currentKeyVersion, err := cfg.EmailKeyRing()
	if err != nil {
		log.Fatalf("worker email delivery key ring startup failed: %v", err)
	}
	deliveryCipher, err := notification.NewCipher(notification.KeyRing{Keys: deliveryKeys, Current: currentKeyVersion})
	if err != nil {
		log.Fatalf("worker email delivery cipher startup failed: %v", err)
	}
	var mailer notification.Mailer
	switch cfg.MailerProvider {
	case config.MailerProviderSMTP:
		mailer = notification.SMTPMailer{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword, From: cfg.MailFrom, Timeout: 15 * time.Second,
		}
	default:
		log.Printf("MAILER_PROVIDER=%s: worker captures emails in memory, never delivers", cfg.MailerProvider)
		mailer = &notification.CaptureMailer{}
	}
	mailWorker := notification.Worker{
		Store:    db,
		Cipher:   deliveryCipher,
		Mailer:   mailer,
		Renderer: notification.NewTextTemplateRenderer(),
		WorkerID: "mailer",
	}
	go mailWorker.Run(ctx, 5*time.Second)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if reclaimed, err := notification.ReclaimExpired(ctx, db, 20); err != nil {
					log.Printf("email delivery reclaim failed: %v", err)
				} else if reclaimed > 0 {
					log.Printf("email delivery reclaimed %d expired leases", reclaimed)
				}
			}
		}
	}()
	var objects objectstore.ObjectStore
	var asrProvider transcription.Provider
	if strings.TrimSpace(cfg.OSSRegion) != "" && strings.TrimSpace(cfg.OSSBucket) != "" {
		ossStore, ossErr := objectstore.NewOSS(objectstore.OSSConfig{
			Region: cfg.OSSRegion, Bucket: cfg.OSSBucket, Endpoint: cfg.OSSEndpoint, Prefix: cfg.OSSPrefix,
		})
		if ossErr != nil {
			log.Fatalf("worker object storage startup failed: %v", ossErr)
		}
		objects = ossStore
	}
	if cfg.ASREndpoint != "" && cfg.ASRToken != "" {
		asrProvider = transcription.HTTPProvider{Endpoint: cfg.ASREndpoint, Token: cfg.ASRToken, Model: cfg.ASRModel}
	}
	if strings.EqualFold(cfg.ASRProvider, "tencent") || (cfg.TencentSecretID != "" && cfg.TencentSecretKey != "") {
		asrProvider = transcription.TencentProvider{SecretID: cfg.TencentSecretID, SecretKey: cfg.TencentSecretKey, Region: cfg.ASRRegion, Engine: cfg.ASREngine}
	}
	// Phase 3 retrieval provider registry: the semantic embedding provider is
	// nil when the runtime manifest is incomplete (readiness reports the gap);
	// no hash fallback exists.
	embeddings, semanticAvailable, _, err := retrieval.BuildFromConfig(cfg)
	if err != nil {
		log.Fatalf("retrieval provider registry startup failed: %v", err)
	}
	// The worker-side query service mirrors the API construction (cmd/api:
	// CursorSecret/QueryHashSecret/session TTL/query budget) so the retrieval
	// channels sign and audit identically in both processes.
	queryService := query.Service{
		Store:           db,
		Embeddings:      embeddings,
		CursorSecret:    cfg.SearchCursorSecret,
		QueryHashSecret: cfg.QueryHashSecret,
		SessionTTL:      cfg.RetrievalSessionTTL,
		QueryTimeout:    cfg.RetrievalQueryTimeout,
	}
	var siteServiceForTools = site.Service{Store: db, Events: &events, Policy: authz.WorkspacePolicyService{Store: db}}
	reviewService := review.Service{Store: db, Events: &events, Policy: authz.WorkspacePolicyService{Store: db}}
	contentServiceForTools := content.Service{Store: db, Events: events}
	reactProcessor := &agentruntime.PersistentReActService{
		Store: db, Cipher: checkpointCipher, Models: modelRegistry,
		ToolFactory: agentruntime.DomainToolFactory{
			Store: db, Events: events, Query: queryService, Models: modelRegistry,
			Sites: &siteServiceForTools, Reviews: &reviewService, Contents: &contentServiceForTools,
		},
		Coordinator: agentruntime.Coordinator{Store: db},
	}
	workflowRegistry, err := workflows.DefaultRegistry()
	if err != nil {
		log.Fatalf("workflow registry startup failed: %v", err)
	}
	assetPrepareWorkflow, err := workflows.NewAssetPrepareGraph(agentruntime.AssetCandidateExtractor{Models: modelRegistry})
	if err != nil {
		log.Fatalf("asset_prepare workflow startup failed: %v", err)
	}
	// Phase 4 suggestion stream: relation candidates come from the unified
	// query engine (fail-closed on retrieval errors) and tag suggestions go
	// through the tag domain service.
	preparation := &asset.AssetPreparationService{
		Store: db, Events: events, Permissions: authz.ScopeResolver{Store: db}, Workflow: assetPrepareWorkflow,
		Relations: automation.RelationCandidateQuery{Query: queryService},
		Tags:      tag.SuggestionService{Store: db},
	}
	var attachmentScanner attachment.Scanner
	if cfg.AttachmentClamAVAddr != "" {
		attachmentScanner = attachment.ClamAVScanner{
			Address: cfg.AttachmentClamAVAddr,
			Timeout: time.Duration(cfg.AttachmentScanTimeoutSeconds) * time.Second,
		}
	}
	// Phase 3 retrieval pipeline: the coordinator consumes the asset/tag/
	// model facts and the River workers own build/embed/finalize/backfill.
	coordinator := retrieval.Coordinator{Store: db, Queue: events.Queue}
	manifestFingerprint := ""
	if semanticAvailable && embeddings != nil {
		manifestFingerprint = retrieval.ManifestFingerprint(embeddings.Manifest())
	}
	retrievalEngine := retrieval.Engine{Store: db, Queue: nil, Embeddings: embeddings, Tokenizer: retrieval.NewWordTokenizer()}
	dispatcher := worker.Dispatcher{
		Store:            db,
		Retrieval:        coordinator,
		Transcription:    transcription.Processor{Store: db, Objects: objects, Provider: asrProvider, Timeout: time.Duration(cfg.ASRTimeoutSeconds) * time.Second},
		AttachmentScan:   attachment.ScanProcessor{Store: db, Objects: objects, Scanner: attachmentScanner},
		CacheInvalidator: delivery.NewInvalidator(db),
		Lease:            10 * time.Minute,
		RetryDelay:       10 * time.Second,
		ProcessorVersion: "river-event-worker/v1",
		Logf:             log.Printf,
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, &worker.DispatchEventWorker{Dispatcher: dispatcher})
	river.AddWorker(workers, &worker.RecoverPendingDeliveriesWorker{Store: db, Retrieval: coordinator, Transcription: transcription.Processor{Store: db, Objects: objects, Provider: asrProvider, Timeout: time.Duration(cfg.ASRTimeoutSeconds) * time.Second}, AttachmentScan: attachment.ScanProcessor{Store: db, Objects: objects, Scanner: attachmentScanner}, CacheInvalidator: delivery.NewInvalidator(db), Lease: 10 * time.Minute, Limit: 100})
	river.AddWorker(workers, &worker.RecoverAutomationAttemptsWorker{Service: automation.Service{Store: db}, Limit: 100})
	river.AddWorker(workers, &worker.ExpiredRowsWorker{Store: db, Logf: log.Printf})
	river.AddWorker(workers, &worker.ScheduledPublicationWorker{Reviews: &reviewService})
	river.AddWorker(workers, &retrieval.BuildProjectionRunWorker{Engine: retrievalEngine})
	river.AddWorker(workers, &retrieval.EmbedChunkBatchWorker{Engine: retrievalEngine})
	river.AddWorker(workers, &retrieval.FinalizeProjectionRunWorker{Engine: retrievalEngine})
	river.AddWorker(workers, &retrieval.BackfillProfileWorker{Engine: retrievalEngine})
	river.AddWorker(workers, &retrieval.ReconcileWorker{Engine: retrievalEngine})
	river.AddWorker(workers, &retrieval.CleanupWorker{Engine: retrievalEngine})
	river.AddWorker(workers, &worker.BackgroundJobsWorker{
		Transfers:   asset.TransferProcessor{Store: db, Events: events, Objects: objects, ObjectPrefix: cfg.OSSPrefix},
		Deletions:   deletion.Processor{Store: db},
		Automation:  automation.Service{Store: db},
		Operations:  automation.OperationProcessor{Store: db, Events: events, Workflows: workflows.Executor{Registry: workflowRegistry}, Preparation: preparation, ReAct: reactProcessor},
		Attachments: attachment.ScanProcessor{Store: db, Objects: objects},
		Vision: attachment.VisionProcessor{
			Store: db, Objects: objects,
			Resolver:     visionModelResolver{Registry: modelRegistry, Endpoint: cfg.ImageVisionEndpoint},
			EndpointName: cfg.ImageVisionEndpoint,
			Timeout:      2 * time.Minute,
		},
		WorkerID: fmt.Sprintf("background-%d", os.Getpid()),
		Limit:    20,
	})
	riverClient, err := river.NewClient(riverpgxv5.New(db.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			eventing.QueueEvents:      {MaxWorkers: 4},
			eventing.QueueMaintenance: {MaxWorkers: 1},
			retrieval.BuildQueue:      {MaxWorkers: 4},
			retrieval.EmbedQueue:      {MaxWorkers: 4},
			retrieval.MaintQueue:      {MaxWorkers: 1},
		},
		Workers:     workers,
		MaxAttempts: eventing.DefaultMaxDeliveryAttempts,
		JobTimeout:  10 * time.Minute,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(30*time.Second), func() (river.JobArgs, *river.InsertOpts) {
				return eventing.RecoverPendingDeliveriesArgs{}, nil
			}, nil),
			river.NewPeriodicJob(river.PeriodicInterval(30*time.Second), func() (river.JobArgs, *river.InsertOpts) {
				return eventing.RecoverAutomationAttemptsArgs{}, nil
			}, nil),
			river.NewPeriodicJob(river.PeriodicInterval(5*time.Second), func() (river.JobArgs, *river.InsertOpts) {
				return eventing.ProcessBackgroundJobsArgs{}, nil
			}, nil),
			// Phase 3 retrieval maintenance sweeps.
			river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
				return retrieval.ReconcileArgs{}, nil
			}, nil),
			river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
				return retrieval.CleanupArgs{}, nil
			}, nil),
			// Retention sweep for tables without an application-level expiry
			// path: idempotency replay keys, dead sessions, auth rate-limit
			// buckets.
			river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
				return worker.CleanupExpiredRowsArgs{}, nil
			}, nil),
			// G4 scheduled publishing: flip due approved intents once a
			// minute (effective-time tolerance ≤1m, see design doc §15).
			river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
				return worker.ScheduledPublicationArgs{}, nil
			}, nil),
		},
	})
	if err != nil {
		log.Fatalf("worker River startup failed: %v", err)
	}
	// Phase 3 readiness: register this retrieval worker in
	// system.worker_heartbeats so the API /readyz can verify a live worker
	// with a matching manifest fingerprint; the row is removed on exit.
	hostname, _ := os.Hostname()
	heartbeat := retrieval.NewHeartbeat(db, fmt.Sprintf("%s-%d-%s", hostname, os.Getpid(), randomSuffix()), manifestFingerprint)
	go heartbeat.Run(ctx)
	log.Printf("worker starting environment=%s semantic_available=%v manifest=%s queues=%s,%s,%s,%s,%s",
		cfg.Environment, semanticAvailable, manifestFingerprint,
		eventing.QueueEvents, eventing.QueueMaintenance, retrieval.BuildQueue, retrieval.EmbedQueue, retrieval.MaintQueue)
	if err := riverClient.Start(ctx); err != nil {
		log.Fatalf("worker River start failed: %v", err)
	}
	<-riverClient.Stopped()
	if ctx.Err() != nil {
		log.Println("worker stopped")
		return
	}
	log.Fatal(fmt.Errorf("worker River stopped unexpectedly"))
}

// randomSuffix produces a short process-unique worker suffix.
func randomSuffix() string {
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}

// visionModelResolver adapts the agent model registry to the attachment
// vision pass: the organization's active endpoint named by
// IMAGE_VISION_ENDPOINT answers image-understanding requests.
type visionModelResolver struct {
	Registry *agentruntime.ModelRegistry
	Endpoint string
}

func (r visionModelResolver) ResolveVisionModel(ctx context.Context, organizationID string) (attachment.VisionModel, error) {
	if r.Registry == nil || strings.TrimSpace(r.Endpoint) == "" {
		return nil, errors.New("image vision endpoint is not configured")
	}
	resolved, err := r.Registry.ResolveOrganizationEndpoint(ctx, organizationID, r.Endpoint)
	if err != nil {
		return nil, err
	}
	return resolved.Model, nil
}
