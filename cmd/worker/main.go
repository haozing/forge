package main

import (
	"context"
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
	"agentchunzhi/internal/deletion"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/modelendpoint"
	"agentchunzhi/internal/objectstore"
	"agentchunzhi/internal/query"
	"agentchunzhi/internal/resourcemodel"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/transcription"
	"agentchunzhi/internal/worker"
	"agentchunzhi/internal/workflows"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func main() {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startupCtx, startupCancel := context.WithTimeout(ctx, 15*time.Second)
	db, err := store.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		startupCancel()
		log.Fatalf("worker database startup failed: %v", err)
	}
	if err := store.ApplyMigration(startupCtx, db, cfg.MigrationPath); err != nil {
		db.Close()
		startupCancel()
		log.Fatalf("worker database migration failed: %v", err)
	}

	// 0043 is self-idempotent (ON CONFLICT DO NOTHING): replaying it on boot
	// seeds organizations created after its checksum was first recorded.
	if err := store.ReplayIdempotentSeed(startupCtx, db, cfg.MigrationPath, "0043_builtin_resource_model_seeds.sql"); err != nil {
		log.Fatalf("replay builtin resource model seed: %v", err)
	}
	if err := eventing.Migrate(startupCtx, db.Pool); err != nil {
		db.Close()
		startupCancel()
		log.Fatalf("worker River migration failed: %v", err)
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
			AllowedHosts: cfg.AgentModelAllowedHosts,
			Limiter:      agentruntime.NewModelRequestLimiter(cfg.AgentModelMaxConcurrentRequests),
		},
		MaxEntries: cfg.AgentModelMaxCacheEntries,
	}

	events, err := eventing.NewEventStore(db.Pool)
	if err != nil {
		log.Fatalf("worker event store startup failed: %v", err)
	}
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
	var embeddings retrieval.EmbeddingProvider = retrieval.HTTPEmbeddingProvider{Endpoint: cfg.EmbeddingEndpoint, Token: cfg.EmbeddingToken, Model: cfg.EmbeddingModel, Protocol: cfg.EmbeddingProtocol, Dimension: cfg.EmbeddingDimension, Timeout: 5 * time.Second}
	if cfg.EmbeddingEndpoint == "" && (cfg.Environment == "development" || cfg.Environment == "test") {
		embeddings = retrieval.HashEmbeddingProvider{Dimensions: retrieval.DefaultEmbeddingDimensions}
	} else if cfg.EmbeddingEndpoint != "" {
		embeddings = retrieval.HTTPEmbeddingProvider{Endpoint: cfg.EmbeddingEndpoint, Token: cfg.EmbeddingToken, Model: cfg.EmbeddingModel, Protocol: cfg.EmbeddingProtocol, Dimension: cfg.EmbeddingDimension, Timeout: 5 * time.Second}
	}
	queryService := query.Service{Store: db, Embeddings: embeddings, CursorSecret: cfg.SearchCursorSecret}
	reactProcessor := &agentruntime.PersistentReActService{
		Store: db, Cipher: checkpointCipher, Models: modelRegistry,
		ToolFactory: agentruntime.DomainToolFactory{Store: db, Events: events, Query: queryService},
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
	preparation := &asset.AssetPreparationService{
		Store: db, Events: events, Permissions: authz.ScopeResolver{Store: db}, Workflow: assetPrepareWorkflow,
	}
	var attachmentScanner attachment.Scanner
	if cfg.AttachmentClamAVAddr != "" {
		attachmentScanner = attachment.ClamAVScanner{
			Address: cfg.AttachmentClamAVAddr,
			Timeout: time.Duration(cfg.AttachmentScanTimeoutSeconds) * time.Second,
		}
	}
	dispatcher := worker.Dispatcher{
		Store:            db,
		Projection:       retrieval.Projector{Store: db, Embeddings: embeddings, EmbeddingModel: cfg.EmbeddingModel},
		Transcription:    transcription.Processor{Store: db, Objects: objects, Provider: asrProvider, Timeout: time.Duration(cfg.ASRTimeoutSeconds) * time.Second},
		AttachmentScan:   attachment.ScanProcessor{Store: db, Objects: objects, Scanner: attachmentScanner},
		Lease:            10 * time.Minute,
		RetryDelay:       10 * time.Second,
		ProcessorVersion: "river-event-worker/v1",
		Logf:             log.Printf,
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, &worker.DispatchEventWorker{Dispatcher: dispatcher})
	river.AddWorker(workers, &worker.RecoverPendingDeliveriesWorker{Store: db, Limit: 100})
	river.AddWorker(workers, &worker.RecoverAutomationAttemptsWorker{Service: automation.Service{Store: db}, Limit: 100})
	river.AddWorker(workers, &worker.BackgroundJobsWorker{
		Migrations: resourcemodel.MigrationProcessor{Store: db, Events: events},
		Transfers:  asset.TransferProcessor{Store: db, Events: events, Objects: objects, ObjectPrefix: cfg.OSSPrefix},
		Deletions:  deletion.Processor{Store: db},
		Automation: automation.Service{Store: db},
		Operations: automation.OperationProcessor{Store: db, Events: events, Transfers: &asset.TransferProcessor{Store: db, Events: events, Objects: objects, ObjectPrefix: cfg.OSSPrefix}, Workflows: workflows.Executor{Registry: workflowRegistry}, Preparation: preparation, ReAct: reactProcessor},
		WorkerID:   fmt.Sprintf("background-%d", os.Getpid()),
		Limit:      20,
	})
	riverClient, err := river.NewClient(riverpgxv5.New(db.Pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			eventing.QueueEvents:      {MaxWorkers: 4},
			eventing.QueueMaintenance: {MaxWorkers: 1},
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
		},
	})
	if err != nil {
		log.Fatalf("worker River startup failed: %v", err)
	}
	// Built-in cron trigger loop (runs alongside the River workers): every 30s
	// it evaluates enabled automation.jobs with trigger.type='cron' and
	// enqueues due runs via Service.CreateScheduledRun. Window identity is a
	// structured idempotency key checked against automation.runs, so ticks,
	// restarts, or parallel worker processes cannot double-fire one minute.
	cronScheduler := automation.NewScheduler(automation.NewServiceScheduleEnqueuer(db), log.Printf)
	go cronScheduler.Run(ctx, automation.DefaultSchedulerInterval)
	log.Printf("worker starting environment=%s queues=%s,%s", cfg.Environment, eventing.QueueEvents, eventing.QueueMaintenance)
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
