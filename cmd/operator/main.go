// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Command operator runs the PolicyAIJob controller. The ai service only
// creates and reads the jobs; this binary runs them, writes their results to
// Valkey and Postgres and publishes their completion events. Replicas share
// the work through leader election: only the leader reconciles and schedules
// the nightly relationship job, and every replica serves its probes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	buildinfo "github.com/Bugs5382/go-buildinfo"
	"github.com/Bugs5382/go-buildinfo/health"
	log "github.com/Bugs5382/go-log"
	gootel "github.com/Bugs5382/go-otel"
	postgres "github.com/Bugs5382/go-postgres"
	pgotel "github.com/Bugs5382/go-postgres/otel"
	"github.com/Bugs5382/go-rabbitmq"
	rmqotel "github.com/Bugs5382/go-rabbitmq/otel"
	redis "github.com/Bugs5382/go-redis"
	"github.com/go-logr/zerologr"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	"github.com/Steward-GRC/steward-ai/internal/aiconfig"
	"github.com/Steward-GRC/steward-ai/internal/config"
	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/llm"
	"github.com/Steward-GRC/steward-ai/internal/operator"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/build"
	"github.com/Steward-GRC/steward-ai/internal/readiness"
	"github.com/Steward-GRC/steward-ai/internal/reeval"
	"github.com/Steward-GRC/steward-ai/internal/relationship"
	"github.com/Steward-GRC/steward-ai/internal/retrieval"
	"github.com/Steward-GRC/steward-ai/internal/server"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

const (
	serviceName      = "steward-ai-operator"
	leaderElectionID = "steward-ai-operator.ai.steward-grc.com"
	// kubernetesTimeout bounds the readiness call to the API server.
	kubernetesTimeout = 2 * time.Second
	// embeddingsRecheck keeps a passing embeddings check for this long, so
	// the probe doesn't embed on every call.
	embeddingsRecheck = 30 * time.Second
)

func main() {
	logger := log.NewLoggerWithOptions(serviceName)
	if err := run(ctrl.SetupSignalHandler(), logger); err != nil {
		logger.Fatal(err, "operator stopped")
	}
}

func run(ctx context.Context, logger log.TraceLogger) error {
	bi := buildinfo.Get()
	logger.Info("starting", log.F("version", bi.Version), log.F("commit", bi.Commit), log.F("go_version", bi.GoVersion))

	cfg, err := config.LoadOperator(os.Getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// controller-runtime logs through logr; zerologr bridges it onto go-log's
	// zerolog, so its lines share the format, level and fields.
	zl := log.New(serviceName)
	ctrl.SetLogger(zerologr.New(&zl))

	otelShutdown, err := gootel.Init(ctx, serviceName, cfg.OTLPEndpoint)
	if err != nil {
		return fmt.Errorf("otel: %w", err)
	}
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			logger.Warn("otel shutdown", log.F("error", err.Error()))
		}
	}()

	db, err := postgres.New(ctx, cfg.DatabaseDSN, pgotel.WithTracing())
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer db.Close()

	rc, err := redis.Connect(ctx, redis.WithAddr(cfg.RedisAddr), redis.WithPassword(cfg.RedisPass),
		redis.WithTimeouts(300*time.Millisecond, 200*time.Millisecond, 200*time.Millisecond))
	if err != nil {
		return fmt.Errorf("valkey: %w", err)
	}
	defer func() { _ = rc.Close() }()
	rdb := rc.Redis()

	// The first dial is bounded so an unreachable broker fails the boot
	// before the startup probe gives up; reconnects after that are the
	// connection's own.
	connectCtx, cancelConnect := context.WithTimeout(ctx, 30*time.Second)
	conn, err := rabbitmq.Connect(connectCtx, cfg.RabbitURL, append(rmqotel.Instrument(), rabbitmq.WithLogger(rabbitLogger{logger}))...)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("rabbitmq: %w", err)
	}
	defer func() { _ = conn.Close() }()
	completions := conn.NewPublisher(operator.CompletionExchange,
		rabbitmq.WithExchangeDeclare(rabbitmq.ExchangeConfig{Name: operator.CompletionExchange, Kind: "topic", Durable: true}))

	box, err := store.NewSecretBox(cfg.SettingsKey)
	if err != nil {
		return fmt.Errorf("settings key: %w", err)
	}
	settings := aiconfig.New(store.NewAIConfigStore(db, box), rdb)

	llmOpts := []llm.Option{llm.WithLogger(logger)}
	if cfg.GenerationStub {
		logger.Warn("AI_GENERATION_STUB is on: jobs get labelled stand-in text, not a provider's answer (local runs only)")
		llmOpts = append(llmOpts, llm.WithStub())
	}
	factory := func(s provider.Settings) (provider.Generator, error) { return build.New(s, build.WithLogger(logger)) }
	llmClient := llm.New(settings, store.NewUsageStore(db), factory, llmOpts...)

	embedder, err := build.NewEmbedder(cfg.Embed.Provider, cfg.Embed.Endpoint, cfg.Embed.Model, cfg.Embed.Credential)
	if err != nil {
		return fmt.Errorf("embeddings: %w", err)
	}
	retriever := retrieval.New(embedder, store.NewChunkStore(db), 0)
	centroids := store.NewCentroidStore(db)
	related := store.NewRelatedStore(db)
	relationships := store.NewRelationshipStore(db)

	dispatcher := operator.NewDispatcher(
		generation.NewDraft(llmClient, cfg.DraftSectionMaxTokens),
		generation.NewReview(llmClient),
		generation.NewRevise(llmClient, cfg.ReviseMaxTokens),
		retriever,
		generation.NewQA(llmClient),
	).
		WithReeval(reeval.NewEngine(reeval.NewPendingSet(rdb, reeval.WindowFromEnv()), centroids, related, reeval.DefaultTopN).
			WithBlendedScores(relationships).WithLogger(logger)).
		WithRelationshipLearn(relationship.NewEngine(relationships, settings,
			relationship.UsageHalfLifeFromEnv(), relationship.CandidateTopKFromEnv()).WithLogger(logger)).
		WithSuggest(generation.NewSuggester(llmClient, centroids, retriever).WithPersister(related)).
		WithLogger(logger)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("kubernetes config: %w", err)
	}
	metricsAddr := "0"
	if cfg.MetricsPort != "0" {
		metricsAddr = ":" + cfg.MetricsPort
	}
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: metricsAddr},
		// The probes are go-buildinfo's, below, not controller-runtime's.
		HealthProbeBindAddress:  "0",
		LeaderElection:          cfg.LeaderElection,
		LeaderElectionID:        leaderElectionID,
		LeaderElectionNamespace: cfg.JobNamespace,
		// A namespaced Role can only list and watch its own namespace; a
		// cluster-wide cache would never sync.
		Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{cfg.JobNamespace: {}}},
	})
	if err != nil {
		return fmt.Errorf("manager: %w", err)
	}

	jobResults := store.NewJobResultStore(db)
	if err := (&operator.PolicyAIJobReconciler{
		Client:       mgr.GetClient(),
		Dispatcher:   dispatcher,
		Results:      operator.NewValkeyResultWriter(rdb),
		Events:       operator.NewMQEventPublisher(publisher{completions}),
		Durable:      jobResults,
		Enabled:      settings,
		MaxAttempts:  cfg.MaxAttempts,
		Verifier:     jobResults,
		SucceededTTL: cfg.SucceededTTL,
		FailedTTL:    cfg.FailedTTL,
		Log:          logger,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("reconciler: %w", err)
	}

	learnInterval := relationship.ScheduleIntervalFromEnv()
	if err := mgr.Add(operator.NewScheduler(operator.ClientJobCreator{Client: mgr.GetClient(), Namespace: cfg.JobNamespace}, learnInterval).
		WithLogger(logger)); err != nil {
		return fmt.Errorf("scheduler: %w", err)
	}

	dc, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("discovery client: %w", err)
	}
	kube := dc.RESTClient()
	checker, err := readiness.New(readiness.Deps{
		Postgres:   readiness.PostgresDB(db),
		Broker:     conn,
		Valkey:     func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		Embeddings: readiness.RecheckEvery(embeddingsCheck(embedder), embeddingsRecheck, time.Now),
		Provider:   providerCheck(llmClient),
		Kubernetes: func(ctx context.Context) error { _, err := kubernetesVersion(ctx, kube); return err },
	}, health.WithTTL(5*time.Second), health.WithTimeout(2*time.Second), health.WithLogger(logger))
	if err != nil {
		return fmt.Errorf("readiness: %w", err)
	}
	if v, err := kubernetesVersion(ctx, kube); err != nil {
		logger.Warn("kubernetes API server not reachable yet", log.F("error", err.Error()))
	} else {
		logger.Info("kubernetes API server", log.F("version", v))
	}

	var lc net.ListenConfig
	probeLis, err := lc.Listen(ctx, "tcp", ":"+cfg.ProbePort)
	if err != nil {
		return fmt.Errorf("probe listen: %w", err)
	}
	if err := mgr.Add(probes{lis: probeLis, checker: checker}); err != nil {
		return fmt.Errorf("probes: %w", err)
	}

	logger.Info("manager starting",
		log.F("job_namespace", cfg.JobNamespace),
		log.F("leader_election", cfg.LeaderElection),
		log.F("probe_port", cfg.ProbePort),
		log.F("metrics_port", cfg.MetricsPort),
		log.F("max_attempts", cfg.MaxAttempts),
		log.F("succeeded_ttl", cfg.SucceededTTL.String()),
		log.F("failed_ttl", cfg.FailedTTL.String()),
		log.F("relationship_learn_interval", learnInterval.String()),
		log.F("embeddings", cfg.Embed.Provider),
		log.F("generation_stub", cfg.GenerationStub))
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("manager: %w", err)
	}
	logger.Info("stopped")
	return nil
}

// kubernetesVersion reads the API server's /version, which is also the
// readiness check.
func kubernetesVersion(ctx context.Context, c rest.Interface) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, kubernetesTimeout)
	defer cancel()
	raw, err := c.Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil {
		return "", fmt.Errorf("kubernetes version: %w", err)
	}
	var v struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("kubernetes version: %w", err)
	}
	return v.GitVersion, nil
}

func embeddingsCheck(e provider.Embedder) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := e.Embed(ctx, []string{"readiness"})
		return err
	}
}

// providerCheck reports the provider's last known status; it never calls the
// provider.
func providerCheck(c *llm.Client) func(context.Context) error {
	return func(context.Context) error {
		if ok, reason := c.ProviderStatus(); !ok {
			return fmt.Errorf("generative provider unavailable: %s", reason)
		}
		return nil
	}
}

// probes serves /livez and /readyz on every replica, leader or not.
type probes struct {
	lis     net.Listener
	checker *health.Checker
}

func (probes) NeedLeaderElection() bool { return false }

func (p probes) Start(ctx context.Context) error { return server.ServeProbes(ctx, p.lis, p.checker) }

// publisher narrows a go-rabbitmq publisher to operator.Publisher.
type publisher struct{ p *rabbitmq.Publisher }

func (p publisher) Publish(ctx context.Context, routingKey string, body []byte) error {
	return p.p.Publish(ctx, routingKey, body)
}

type rabbitLogger struct{ l log.Logger }

func (r rabbitLogger) Debugf(f string, a ...any) { r.l.Debug(fmt.Sprintf(f, a...)) }
func (r rabbitLogger) Infof(f string, a ...any)  { r.l.Info(fmt.Sprintf(f, a...)) }
func (r rabbitLogger) Warnf(f string, a ...any)  { r.l.Warn(fmt.Sprintf(f, a...)) }
func (r rabbitLogger) Errorf(f string, a ...any) { r.l.Error(nil, fmt.Sprintf(f, a...)) }
