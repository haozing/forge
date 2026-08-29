package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	adminservice "agentchunzhi/internal/admin"
	"agentchunzhi/internal/agentapp"
	"agentchunzhi/internal/agentruntime"
	agenttask "agentchunzhi/internal/agenttask"
	assetservice "agentchunzhi/internal/asset"
	"agentchunzhi/internal/attachment"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/automation"
	"agentchunzhi/internal/config"
	"agentchunzhi/internal/container"
	contentservice "agentchunzhi/internal/content"
	"agentchunzhi/internal/conversation"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/httpapi"
	"agentchunzhi/internal/identity"
	"agentchunzhi/internal/modelendpoint"
	"agentchunzhi/internal/notification"
	"agentchunzhi/internal/objectstore"
	"agentchunzhi/internal/organization"
	"agentchunzhi/internal/query"
	"agentchunzhi/internal/resourcemodel"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/review"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"
	"agentchunzhi/internal/workspace"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func main() {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("configuration invalid: %v", err)
	}
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	db, err := store.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		startupCancel()
		log.Fatalf("database startup failed: %v", err)
	}
	if err := store.VerifySchemaContract(startupCtx, db, cfg.MigrationPath); err != nil {
		db.Close()
		startupCancel()
		log.Fatalf("schema contract verification failed: %v", err)
	}
	startupCancel()
	defer db.Close()
	credentialCipher, err := modelendpoint.NewCredentialCipher(cfg.AgentModelSecretEncryptionKey)
	if err != nil {
		log.Fatalf("model credential encryption startup failed: %v", err)
	}
	modelRegistry := &agentruntime.ModelRegistry{
		Source:  agentruntime.PostgresConfigSource{Store: db},
		Cipher:  credentialCipher,
		Secrets: agentruntime.EnvironmentSecretResolver{},
		Factory: agentruntime.OpenAIModelFactory{
			AllowedHosts: cfg.AgentModelAllowedHosts,
			Limiter:      agentruntime.NewModelRequestLimiter(cfg.AgentModelMaxConcurrentRequests),
		},
		MaxEntries: cfg.AgentModelMaxCacheEntries,
	}
	events, err := eventing.NewEventStore(db.Pool)
	if err != nil {
		log.Fatalf("event store startup failed: %v", err)
	}
	// Phase 1 email delivery: the API encrypts payloads with the AES-256-GCM
	// key ring; the worker owns the mailer transport.
	deliveryKeys, currentKeyVersion, err := cfg.EmailKeyRing()
	if err != nil {
		log.Fatalf("email delivery key ring startup failed: %v", err)
	}
	deliveryCipher, err := notification.NewCipher(notification.KeyRing{Keys: deliveryKeys, Current: currentKeyVersion})
	if err != nil {
		log.Fatalf("email delivery cipher startup failed: %v", err)
	}
	deliveryKeyVersion, err := notification.KeyVersionNumber(currentKeyVersion)
	if err != nil {
		log.Fatalf("email delivery key version startup failed: %v", err)
	}
	rateLimitHMACKey := []byte(cfg.RateLimitHMACKey)
	if len(rateLimitHMACKey) == 0 {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			log.Fatalf("rate limit hmac key generation failed: %v", err)
		}
		rateLimitHMACKey = []byte(hex.EncodeToString(raw))
		log.Printf("WARNING: RATE_LIMIT_HMAC_KEY is not set; using an ephemeral key - login throttling resets on restart")
	}
	var objects objectstore.ObjectStore
	ossRegion, ossBucket := strings.TrimSpace(cfg.OSSRegion), strings.TrimSpace(cfg.OSSBucket)
	if (ossRegion == "") != (ossBucket == "") {
		log.Fatalf("OSS_REGION and OSS_BUCKET must be provided together")
	}
	if ossRegion != "" && ossBucket != "" {
		ossStore, ossErr := objectstore.NewOSS(objectstore.OSSConfig{
			Region:   ossRegion,
			Bucket:   ossBucket,
			Endpoint: cfg.OSSEndpoint,
			Prefix:   cfg.OSSPrefix,
		})
		if ossErr != nil {
			log.Fatalf("object storage startup failed: %v", ossErr)
		}
		objects = ossStore
	}
	// Phase 3 retrieval provider registry: the semantic embedding provider is
	// nil when the runtime manifest is incomplete; readiness reports the gap.
	// No hash fallback exists. The legacy query service keeps its own reranker
	// shape until the query rewrite lands.
	embeddings, semanticAvailable, _, err := retrieval.BuildFromConfig(cfg)
	if err != nil {
		log.Fatalf("retrieval provider registry startup failed: %v", err)
	}
	manifestFingerprint := ""
	if semanticAvailable && embeddings != nil {
		manifestFingerprint = retrieval.ManifestFingerprint(embeddings.Manifest())
	}
	registry := retrieval.RegistryFromConfig(cfg)
	registeredManifests, err := registry.RegisteredManifests()
	if err != nil {
		log.Fatalf("retrieval manifest registry startup failed: %v", err)
	}
	// Insert-only River client: the API enqueues retrieval rebuild backfill
	// jobs while the worker process runs the handlers.
	rebuildQueue, err := river.NewClient(riverpgxv5.New(db.Pool), &river.Config{})
	if err != nil {
		log.Fatalf("retrieval rebuild queue startup failed: %v", err)
	}
	var reranker query.Reranker
	if cfg.RerankerEndpoint != "" {
		reranker = query.HTTPReranker{Endpoint: cfg.RerankerEndpoint, Token: cfg.RerankerToken, ModelVersion: cfg.RerankerModelVersion, Protocol: cfg.RerankerProtocol, Timeout: time.Second}
	}
	scopeResolver := authz.ScopeResolver{Store: db}
	queryService := query.Service{
		Store:           db,
		Embeddings:      embeddings,
		Reranker:        reranker,
		CursorSecret:    cfg.SearchCursorSecret,
		QueryHashSecret: cfg.QueryHashSecret,
		SessionTTL:      cfg.RetrievalSessionTTL,
		QueryTimeout:    cfg.RetrievalQueryTimeout,
	}
	ragRuntime := agentruntime.RAGRuntime{
		Models: modelRegistry,
		Retriever: agentruntime.QueryKnowledgeRetriever{
			Scope: scopeResolver,
			Query: queryService,
		},
	}
	memberAssetService := assetservice.MemberService{Store: db, Events: &events, Policy: authz.WorkspacePolicyService{Store: db}}
	// Cookie identity is fixed from configuration, never from the request:
	// production uses the __Host- prefix which forces Secure, Path=/ and no
	// Domain.
	if cfg.Environment == "production" {
		auth.SessionCookieConfig.Name = "__Host-agent_session"
		auth.SessionCookieConfig.Secure = true
	}
	deps := httpapi.Dependencies{
		Store:           db,
		Authenticator:   auth.APIKeyAuthenticator{Store: db},
		SessionService:  auth.SessionService{Store: db},
		ScopeResolver:   scopeResolver,
		WorkspacePolicy: authz.WorkspacePolicyService{Store: db},
		QueryService:    queryService,
		AttachmentService: attachment.Service{
			Store:        db,
			Events:       &events,
			Objects:      objects,
			ObjectPrefix: cfg.OSSPrefix,
			MaxBytes:     cfg.AttachmentMaxBytes,
		},
		AssetService:     assetservice.Service{Store: db, Events: &events},
		AgentAppService:  agentapp.Service{Store: db},
		AgentRuntime:     ragRuntime,
		AgentTaskService: agenttask.Service{Store: db},
		ModelEndpointService: modelendpoint.Service{
			Store: db, Cipher: credentialCipher, AllowedHosts: cfg.AgentModelAllowedHosts,
			DefaultTimeout: cfg.AgentModelDefaultTimeout, Health: modelRegistry,
		},
		AdminService:         adminservice.Service{Store: db},
		WorkspaceService:     workspace.Service{Store: db, Events: &events},
		ResourceModelService: resourcemodel.Service{Store: db, Policy: authz.WorkspacePolicyService{Store: db}},
		MemberAssetService:   memberAssetService,
		TransferService:      assetservice.TransferService{Store: db, Policy: authz.WorkspacePolicyService{Store: db}},
		ReviewService:        review.Service{Store: db, Policy: authz.WorkspacePolicyService{Store: db}, Events: &events, Committer: memberAssetService},
		ContainerService:     container.Service{Store: db, Policy: authz.WorkspacePolicyService{Store: db}},
		ConversationService:  conversation.Service{Store: db, Policy: authz.WorkspacePolicyService{Store: db}, Content: contentservice.Service{Store: db, Events: events}},
		AutomationService:    automation.Service{Store: db, Policy: authz.WorkspacePolicyService{Store: db}},
		OrganizationService:  organization.Service{Store: db, Events: &events},
		TagService:           tag.Service{Store: db, Events: &events},
		FacetService:         tag.FacetService{Store: db},
		// Phase 3 retrieval operations: profiles (list via the repository,
		// lifecycle via the service) and rebuild batches.
		RetrievalProfiles: retrievalProfileAdapter{
			service: retrieval.ProfileService{
				Store:              db,
				Manifests:          registeredManifests,
				DefaultManifestKey: registry.ManifestKey,
			},
			repo: retrieval.ProfileRepository{Store: db},
		},
		RetrievalRebuilds: retrieval.RebuildService{Store: db, Queue: rebuildQueue},
		InvitationService: &organization.InvitationService{
			Store: db, Events: &events, Cipher: deliveryCipher,
			KeyVersion: deliveryKeyVersion, BaseURL: cfg.PublicAppBaseURL,
		},
		IdentityService: &identity.Service{
			Store: db, Events: &events, Cipher: deliveryCipher,
			KeyVersion: deliveryKeyVersion, BaseURL: cfg.PublicAppBaseURL,
		},
		LoginThrottle:     auth.NewLoginThrottle(db, rateLimitHMACKey),
		TrustedProxyCIDRs: cfg.TrustedProxyCIDRs,
		AllowedOrigins:    cfg.MemberAllowedOrigins,
		AppEnv:            cfg.Environment,
		// Phase 3 retrieval readiness inputs (/readyz wiring is completed by
		// the query/httpapi work package).
		SemanticAvailable:   semanticAvailable,
		ManifestFingerprint: manifestFingerprint,
		SearchCursorSecret:  cfg.SearchCursorSecret,
		QueryHashSecret:     cfg.QueryHashSecret,
	}
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewHandlerWithDeps(deps),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("api listening on %s environment=%s", cfg.HTTPAddr, cfg.Environment)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("api server failed: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("api shutdown failed: %v", err)
	}
}

// retrievalProfileAdapter joins the profile lifecycle service with the read
// repository behind the single httpapi.RetrievalProfileService interface.
type retrievalProfileAdapter struct {
	service retrieval.ProfileService
	repo    retrieval.ProfileRepository
}

func (a retrievalProfileAdapter) ListProfiles(ctx context.Context, organizationID string) ([]retrieval.Profile, error) {
	return a.repo.ListProfiles(ctx, organizationID)
}

func (a retrievalProfileAdapter) Create(ctx context.Context, organizationID, manifestKey, createdBy string) (retrieval.Profile, error) {
	return a.service.Create(ctx, organizationID, manifestKey, createdBy)
}

func (a retrievalProfileAdapter) Activate(ctx context.Context, organizationID, profileID, activatedBy string) (retrieval.Profile, error) {
	return a.service.Activate(ctx, organizationID, profileID, activatedBy)
}
