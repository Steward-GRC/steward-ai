// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Command server runs the ai service: the gRPC API the gateway calls, and the
// consumers that index what core publishes. Jobs are created here as
// PolicyAIJob resources and run by cmd/operator.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Bugs5382/go-buildinfo"
	"github.com/Bugs5382/go-buildinfo/health"
	log "github.com/Bugs5382/go-log"
	gootel "github.com/Bugs5382/go-otel"
	postgres "github.com/Bugs5382/go-postgres"
	pgotel "github.com/Bugs5382/go-postgres/otel"
	"github.com/Bugs5382/go-rabbitmq"
	rmqotel "github.com/Bugs5382/go-rabbitmq/otel"
	redis "github.com/Bugs5382/go-redis"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/aiconfig"
	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/audit"
	"github.com/Steward-GRC/steward-ai/internal/cache"
	"github.com/Steward-GRC/steward-ai/internal/config"
	"github.com/Steward-GRC/steward-ai/internal/consumer"
	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/grpcsvc"
	"github.com/Steward-GRC/steward-ai/internal/index"
	"github.com/Steward-GRC/steward-ai/internal/llm"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/build"
	"github.com/Steward-GRC/steward-ai/internal/quota"
	"github.com/Steward-GRC/steward-ai/internal/readiness"
	"github.com/Steward-GRC/steward-ai/internal/reeval"
	"github.com/Steward-GRC/steward-ai/internal/retrieval"
	"github.com/Steward-GRC/steward-ai/internal/server"
	"github.com/Steward-GRC/steward-ai/internal/store"
	"github.com/Steward-GRC/steward-ai/internal/workloadauth"
)

const serviceName = "ai"

// embeddingsRecheck keeps a good embeddings check for a minute, so readiness
// doesn't embed on every probe.
const embeddingsRecheck = time.Minute

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := log.NewLogger(serviceName)
	if err := run(ctx, logger); err != nil {
		logger.Fatal(err, "ai service stopped")
	}
}

func run(ctx context.Context, logger log.Logger) error {
	bi := buildinfo.Get()
	logger.Info("starting", log.F("version", bi.Version), log.F("commit", bi.Commit))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	otelShutdown, err := gootel.Init(ctx, serviceName, cfg.OTLPEndpoint)
	if err != nil {
		return fmt.Errorf("otel: %w", err)
	}
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			logger.Warn("otel shutdown", log.F("error", err.Error()))
		}
	}()

	if err := pgotel.InstrumentMigrate(ctx, serviceName, func() error {
		return postgres.Migrate(cfg.MigrateDSN, cfg.MigrationsDir)
	}); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	db, err := postgres.New(ctx, cfg.DatabaseDSN, pgotel.WithTracing())
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer db.Close()

	if cfg.BackfillCentroids {
		n, err := store.NewCentroidStore(db).BackfillCentroids(ctx)
		if err != nil {
			return fmt.Errorf("centroid backfill: %w", err)
		}
		logger.Info("centroid backfill complete", log.F("centroids", n))
		return nil
	}

	rc, err := redis.Connect(ctx, redis.WithAddr(cfg.RedisAddr), redis.WithPassword(cfg.RedisPass),
		redis.WithTimeouts(300*time.Millisecond, 200*time.Millisecond, 200*time.Millisecond))
	if err != nil {
		return fmt.Errorf("valkey: %w", err)
	}
	defer func() { _ = rc.Close() }()
	rdb := rc.Redis()

	// The first dial is bounded so an unreachable broker fails the boot
	// before the startup probe gives up; reconnects are the connection's own.
	connectCtx, cancelConnect := context.WithTimeout(ctx, 30*time.Second)
	conn, err := rabbitmq.Connect(connectCtx, cfg.RabbitURL, append(rmqotel.Instrument(), rabbitmq.WithLogger(rabbitLogger{logger}))...)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("rabbitmq: %w", err)
	}
	defer func() { _ = conn.Close() }()
	auditPub := conn.NewPublisher(audit.Exchange,
		rabbitmq.WithExchangeDeclare(rabbitmq.ExchangeConfig{Name: audit.Exchange, Kind: "topic", Durable: true}),
		rabbitmq.WithDefaultContentType(audit.ContentType))
	auditor := audit.New(publisher{auditPub})

	box, err := store.NewSecretBox(cfg.SettingsKey)
	if err != nil {
		return fmt.Errorf("settings key: %w", err)
	}
	settings := aiconfig.New(store.NewAIConfigStore(db, box), rdb)
	usage := store.NewUsageStore(db)
	llmOpts := []llm.Option{llm.WithLogger(logger)}
	if cfg.GenerationStub {
		logger.Warn("AI_GENERATION_STUB is on: answers are labelled stand-ins, not a provider's (local runs only)")
		llmOpts = append(llmOpts, llm.WithStub())
	}
	factory := func(s provider.Settings) (provider.Generator, error) { return build.New(s, build.WithLogger(logger)) }
	llmClient := llm.New(settings, usage, factory, llmOpts...)

	rawEmbedder, err := build.NewEmbedder(cfg.Embed.Provider, cfg.Embed.Endpoint, cfg.Embed.Model, cfg.Embed.Credential)
	if err != nil {
		return fmt.Errorf("embeddings: %w", err)
	}
	embedder := llm.GateEmbedder(rawEmbedder, settings, cfg.Embed.Provider == config.EmbedOpenAI)

	chunks := store.NewChunkStore(db)
	centroids := store.NewCentroidStore(db)
	summaries := store.NewSummaryStore(db)
	qaCache := cache.New(rdb, db)

	jobClient, err := kubeClient()
	if err != nil {
		return err
	}
	jobs := grpcsvc.NewJobAdapter(jobClient, cfg.JobNamespace)

	pending := reeval.NewPendingSet(rdb, reeval.WindowFromEnv())
	assist := generation.NewAssist(llmClient)
	publishConsumer := consumer.NewPublishEventConsumerWithBumper(index.New(embedder, chunks, cfg.ChunkSize, cfg.ChunkOverlap), qaCache).
		WithLogger(logger).
		WithSummary(assist, summaries, settings).
		WithCentroids(centroids).
		WithReeval(reeval.NewDebouncer(pending, reevalJobCreator{jobs}, pending.Window()).WithLogger(logger))

	go consume(ctx, conn, publishConsumer, logger, airules.PublishQueueName,
		[]string{airules.PublishRoutingKey, policyRetiredRoutingKey}, "ai-indexer")
	go consume(ctx, conn, publishConsumer, logger, airules.ProcedurePublishQueueName,
		[]string{airules.ProcedurePublishRoutingKey, airules.ProcedureRetireRoutingKey}, "ai-procedure-indexer")

	aiSvc := grpcsvc.New(grpcsvc.Deps{
		Settings:          settings,
		LLM:               llmClient,
		Usage:             usage,
		Retrieval:         retrieval.New(embedder, chunks, cfg.RetrievalTopK),
		QA:                generation.NewQA(llmClient),
		Assist:            assist,
		Jobs:              jobs,
		JobResults:        grpcsvc.NewRedisJobResultReader(rdb),
		Summaries:         summaries,
		TopQuestions:      store.NewQAQuestionStore(db),
		Related:           centroids,
		Quota:             quota.New(rdb),
		UserLimits:        store.NewUserQueryLimitStore(db),
		Audit:             grpcsvc.NewAuditAdapter(auditor),
		DefaultQueryLimit: cfg.DefaultQueryLimit,
		Embeddings:        grpcsvc.Embeddings{Provider: cfg.Embed.Provider, Model: cfg.Embed.Model},
	}, grpcsvc.WithCache(qaCache), grpcsvc.WithLogger(logger))

	deps := readiness.Deps{
		Postgres:   readiness.PostgresDB(db),
		Broker:     conn,
		Valkey:     func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		Embeddings: readiness.RecheckEvery(embeddingsCheck(embedder), embeddingsRecheck, time.Now),
		Provider:   providerCheck(llmClient),
	}
	serveOpts := server.Options{}
	if cfg.WorkloadAuth {
		verifier, err := workloadauth.NewVerifier(cfg.Workload, logger)
		if err != nil {
			return fmt.Errorf("workloadauth: %w", err)
		}
		go verifier.Run(ctx)
		serveOpts.Auth = &server.Auth{Verifier: verifier, Policy: grpcsvc.CallerPolicy,
			Options: []workloadauth.Option{workloadauth.WithDenyHook(auditDenial(auditor, logger))}}
		deps.JWKS = readiness.RecheckEvery(verifier.Refresh, time.Minute, time.Now)
	} else {
		go workloadauth.WarnDisabled(ctx, logger, workloadauth.DisabledWarnInterval)
		deps.WorkloadAuthDisabled = true
	}
	checker, err := readiness.New(deps, health.WithTTL(5*time.Second), health.WithTimeout(2*time.Second), health.WithLogger(logger))
	if err != nil {
		return fmt.Errorf("readiness: %w", err)
	}
	serveOpts.Checker = checker

	var lc net.ListenConfig
	grpcLis, err := lc.Listen(ctx, "tcp", ":"+cfg.GRPCPort)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	probeLis, err := lc.Listen(ctx, "tcp", ":"+cfg.ProbePort)
	if err != nil {
		return fmt.Errorf("probe listen: %w", err)
	}
	logger.Info("serving", log.F("grpc_port", cfg.GRPCPort), log.F("probe_port", cfg.ProbePort),
		log.F("embeddings", cfg.Embed.Provider), log.F("workload_auth", cfg.WorkloadAuth), log.F("generation_stub", cfg.GenerationStub))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	probesDone := make(chan error, 1)
	go func() {
		probesDone <- server.ServeProbes(ctx, probeLis, checker)
		cancel()
	}()
	err = server.Serve(ctx, grpcLis, logger, serveOpts, func(s *grpc.Server) {
		aiv1.RegisterAiServiceServer(s, aiSvc)
	})
	cancel()
	if perr := <-probesDone; err == nil {
		err = perr
	}
	return err
}

// policyRetiredRoutingKey is core's retire event for a policy. Its body names
// only the policy, and the consumer drops every chunk of it.
const policyRetiredRoutingKey = "policy.retired"

func kubeClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}
	c, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	return c, nil
}

// consume runs one durable queue bound to core's "jobs" exchange on each
// routing key. Core's reindex reaches the same queue through the default
// exchange, by its name. The go-rabbitmq connection re-declares and resumes
// after a drop; consume returns when ctx ends.
func consume(ctx context.Context, conn *rabbitmq.Conn, c *consumer.PublishEventConsumer, logger log.Logger,
	queue string, routingKeys []string, tag string) {
	bindings := make([]rabbitmq.BindingConfig, 0, len(routingKeys))
	for _, key := range routingKeys {
		bindings = append(bindings, rabbitmq.BindingConfig{Queue: queue, Exchange: airules.PublishExchange, RoutingKey: key})
	}
	err := conn.Consume(ctx, rabbitmq.ConsumerConfig{
		Exchange: rabbitmq.ExchangeConfig{Name: airules.PublishExchange, Kind: "topic", Durable: true},
		// Classic, with no arguments, so the declaration matches queues
		// created by earlier installs.
		Queue:       rabbitmq.QueueConfig{Name: queue, Type: rabbitmq.QueueClassic, Durable: true},
		Bindings:    bindings,
		ConsumerTag: tag,
		Prefetch:    10,
	}, func(ctx context.Context, d rabbitmq.Delivery) error {
		handleErr := c.Handle(ctx, d.Body)
		decision := consumer.DecideDelivery(handleErr, d.Redelivered)
		l := logger.Ctx(ctx)
		if handleErr != nil {
			if decision.Requeue {
				l.Error(handleErr, "publish event failed; requeued for one retry", log.F("queue", queue))
			} else {
				l.Error(handleErr, "publish event failed again; dropped", log.F("queue", queue))
			}
		}
		if decision.Ack || !decision.Requeue {
			return nil
		}
		return handleErr
	})
	if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
		logger.Error(err, "consumer stopped", log.F("queue", queue))
	}
}

// reevalJobCreator starts one RELATED_REEVAL job when a quiet period ends;
// the operator drains the whole pending set.
type reevalJobCreator struct{ jobs *grpcsvc.JobAdapter }

func (c reevalJobCreator) CreateReevalJob(ctx context.Context) error {
	_, err := c.jobs.CreateJob(ctx, v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationRelatedReeval})
	return err
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

// auditDenial audits every call workloadauth refuses: who called what and
// why, never the request.
func auditDenial(auditor *audit.Emitter, logger log.Logger) workloadauth.DenyHook {
	return func(ctx context.Context, d workloadauth.Denial) {
		if err := auditor.Emit(ctx, audit.Event{
			Tier:    audit.TierAudit,
			Action:  "ai.call.refused",
			Subject: "method:" + d.Method,
			Attributes: map[string]string{
				"caller":          d.Caller.Name,
				"service_account": d.Caller.ServiceAccount,
				"code":            d.Code.String(),
				"reason":          d.Reason,
			},
		}); err != nil {
			logger.Ctx(ctx).Warn("emit ai.call.refused", log.F("error", err.Error()))
		}
	}
}

// publisher narrows a go-rabbitmq publisher to the Publish the emitter uses.
type publisher struct{ p *rabbitmq.Publisher }

func (p publisher) Publish(ctx context.Context, routingKey string, body []byte) error {
	return p.p.Publish(ctx, routingKey, body)
}

type rabbitLogger struct{ l log.Logger }

func (r rabbitLogger) Debugf(f string, a ...any) { r.l.Debug(fmt.Sprintf(f, a...)) }
func (r rabbitLogger) Infof(f string, a ...any)  { r.l.Info(fmt.Sprintf(f, a...)) }
func (r rabbitLogger) Warnf(f string, a ...any)  { r.l.Warn(fmt.Sprintf(f, a...)) }
func (r rabbitLogger) Errorf(f string, a ...any) { r.l.Error(nil, fmt.Sprintf(f, a...)) }
