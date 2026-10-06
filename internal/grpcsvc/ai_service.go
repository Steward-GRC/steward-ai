// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package grpcsvc implements steward.ai.v1.AiService: the module's intake
// guardrails, grounded answers, authoring assist, asynchronous jobs and
// settings. The gateway is the only caller; the person comes from the
// go-grpc-actor actor and no request carries a user id.
package grpcsvc

import (
	"context"
	"time"

	log "github.com/Bugs5382/go-log"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/aiconfig"
	"github.com/Steward-GRC/steward-ai/internal/cache"
	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/llm"
	"github.com/Steward-GRC/steward-ai/internal/retrieval"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// retriever embeds the question once so the cache lookup and, on a miss, the
// vector search share one embedding.
type retriever interface {
	EmbedQuestion(ctx context.Context, question string) ([]float32, error)
	RetrieveWithEmbedding(ctx context.Context, queryVec []float32, req retrieval.Request) ([]store.SearchResult, error)
}

type qaEngine interface {
	Answer(ctx context.Context, req generation.AnswerRequest) (generation.AnswerResponse, error)
}

type assistEngine interface {
	Generate(ctx context.Context, req generation.AssistRequest) (generation.AssistResponse, error)
}

// answerCache is the two-tier Q&A cache; cache.QACache satisfies it.
type answerCache interface {
	Lookup(ctx context.Context, question string, embedding []float32, scope store.AccessFilter) (cache.Answer, bool, error)
	Put(ctx context.Context, question string, embedding []float32, scope store.AccessFilter, answer cache.Answer) error
}

// jobClient creates and reads PolicyAIJob resources; JobAdapter satisfies
// it. The service only submits and reads jobs: the operator runs them.
type jobClient interface {
	CreateJob(ctx context.Context, spec v1alpha1.PolicyAIJobSpec) (*v1alpha1.PolicyAIJob, error)
	GetJob(ctx context.Context, jobID string) (*v1alpha1.PolicyAIJob, error)
}

// jobResultReader reads a finished job's result envelope from Valkey;
// RedisJobResultReader satisfies it.
type jobResultReader interface {
	// ReadResult reports found=false for an absent (evicted) key.
	ReadResult(ctx context.Context, key string) (raw []byte, found bool, err error)
}

// settingsStore is the module's settings; aiconfig.Store satisfies it.
type settingsStore interface {
	Settings(ctx context.Context) (aiconfig.Settings, error)
	SetEnabled(ctx context.Context, enabled bool) error
	SetProviderSettings(ctx context.Context, p store.ProviderSettings) error
	SetCredential(ctx context.Context, credential string) error
	AcceptNotice(ctx context.Context, version, actor string, now time.Time) error
	SetMonthlyLimit(ctx context.Context, limit int64) error
	SetOrgContext(ctx context.Context, orgContext string) error
	SetTopK(ctx context.Context, topK int) error
}

// llmClient is the provider path; llm.Client satisfies it.
type llmClient interface {
	ProviderStatus() (available bool, reason string)
	Test(ctx context.Context) (llm.TestResult, error)
	DefaultModel(ctx context.Context) (string, error)
}

// usageReader reads the monthly usage meter; store.UsageStore satisfies it.
type usageReader interface {
	Month(ctx context.Context, at time.Time) (store.Usage, error)
}

type summaryReader interface {
	GetSummary(ctx context.Context, versionID string) (store.Summary, error)
}

type topQuestionsSource interface {
	TopQuestions(ctx context.Context, filter store.AccessFilter, limit int) ([]string, error)
}

// relatedSource applies the retrieval read predicate inside its query, so a
// neighbour the scope can't read never reaches the handler.
type relatedSource interface {
	RelatedPolicies(ctx context.Context, policyID string, filter store.AccessFilter, topN int) ([]store.RelatedPolicy, error)
}

// quotaCounter is the per-person daily counter; quota.Counter satisfies it.
type quotaCounter interface {
	Consume(ctx context.Context, userID string, now time.Time) (used int64, err error)
}

// userLimitStore holds per-person daily limit overrides;
// store.UserQueryLimitStore satisfies it.
type userLimitStore interface {
	Get(ctx context.Context, userID string) (limit int, found bool, err error)
	Set(ctx context.Context, userID string, limit int) error
	Delete(ctx context.Context, userID string) error
}

// auditEmitter records AI calls and settings changes; AuditAdapter
// satisfies it.
type auditEmitter interface {
	EmitAIAudit(ctx context.Context, rec AuditRecord) error
}

// Embeddings names the embeddings provider and model the deployment runs.
// They are set at deployment and only reported by GetAIConfig.
type Embeddings struct {
	Provider string
	Model    string
}

// Deps are the server's dependencies. Every interface field is required.
type Deps struct {
	Settings     settingsStore
	LLM          llmClient
	Usage        usageReader
	Retrieval    retriever
	QA           qaEngine
	Assist       assistEngine
	Jobs         jobClient
	JobResults   jobResultReader
	Summaries    summaryReader
	TopQuestions topQuestionsSource
	Related      relatedSource
	Quota        quotaCounter
	UserLimits   userLimitStore
	Audit        auditEmitter

	// DefaultQueryLimit is the daily limit for anyone without an override;
	// airules.UnlimitedQueryQuota lifts it.
	DefaultQueryLimit int
	Embeddings        Embeddings
}

// AiServiceServer implements aiv1.AiServiceServer.
type AiServiceServer struct {
	aiv1.UnimplementedAiServiceServer
	d Deps

	cache answerCache
	log   log.Logger
	now   func() time.Time
}

// Option configures an AiServiceServer.
type Option func(*AiServiceServer)

// WithCache attaches the Q&A cache. Without it every question goes to
// retrieval and the model.
func WithCache(c answerCache) Option { return func(s *AiServiceServer) { s.cache = c } }

// WithLogger sets the logger; the default is go-log's "ai" logger.
func WithLogger(l log.Logger) Option { return func(s *AiServiceServer) { s.log = l } }

// WithClock replaces time.Now for the quota day, the month and the notice
// acceptance time.
func WithClock(now func() time.Time) Option { return func(s *AiServiceServer) { s.now = now } }

// New returns the server over d.
func New(d Deps, opts ...Option) *AiServiceServer {
	s := &AiServiceServer{d: d, log: log.NewLogger("ai"), now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

var _ aiv1.AiServiceServer = (*AiServiceServer)(nil)
