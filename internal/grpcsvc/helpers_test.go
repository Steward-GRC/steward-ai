// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Bugs5382/go-apperr/apperrgrpc"
	grpcactor "github.com/Bugs5382/go-grpc-actor"
	log "github.com/Bugs5382/go-log"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	"github.com/Steward-GRC/steward-ai/internal/aiconfig"
	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/fixture"
	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/llm"
	"github.com/Steward-GRC/steward-ai/internal/retrieval"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// stubRetrieval returns a fixed set of chunks and records the request.
type stubRetrieval struct {
	results    []store.SearchResult
	embedErr   error
	searchErr  error
	gotRequest retrieval.Request
	searched   int
}

func (s *stubRetrieval) EmbedQuestion(context.Context, string) ([]float32, error) {
	return []float32{}, s.embedErr
}

func (s *stubRetrieval) RetrieveWithEmbedding(_ context.Context, _ []float32, req retrieval.Request) ([]store.SearchResult, error) {
	s.searched++
	s.gotRequest = req
	return s.results, s.searchErr
}

type stubQA struct {
	resp  generation.AnswerResponse
	err   error
	calls int
}

func (s *stubQA) Answer(context.Context, generation.AnswerRequest) (generation.AnswerResponse, error) {
	s.calls++
	return s.resp, s.err
}

type stubAssist struct {
	err error
	got generation.AssistRequest
}

func (s *stubAssist) Generate(_ context.Context, req generation.AssistRequest) (generation.AssistResponse, error) {
	s.got = req
	if s.err != nil {
		return generation.AssistResponse{}, s.err
	}
	return generation.AssistResponse{Suggestion: "stub suggestion"}, nil
}

// stubJobs records created jobs and serves seeded ones. Generation is never
// exercised here; that is the operator's.
type stubJobs struct {
	created   []v1alpha1.PolicyAIJobSpec
	createErr error
	nextName  string

	jobs   map[string]*v1alpha1.PolicyAIJob
	getErr error
}

func (s *stubJobs) CreateJob(_ context.Context, spec v1alpha1.PolicyAIJobSpec) (*v1alpha1.PolicyAIJob, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	s.created = append(s.created, spec)
	name := s.nextName
	if name == "" {
		name = "aijob-stub"
	}
	return &v1alpha1.PolicyAIJob{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}, nil
}

func (s *stubJobs) GetJob(_ context.Context, jobID string) (*v1alpha1.PolicyAIJob, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	job, ok := s.jobs[jobID]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "policyaijobs"}, jobID)
	}
	return job, nil
}

type stubJobResultReader struct {
	raw    []byte
	found  bool
	err    error
	gotKey string
	calls  int
}

func (s *stubJobResultReader) ReadResult(_ context.Context, key string) ([]byte, bool, error) {
	s.calls++
	s.gotKey = key
	return s.raw, s.found, s.err
}

// stubAudit records every audit record.
type stubAudit struct{ recs []AuditRecord }

func (s *stubAudit) EmitAIAudit(_ context.Context, rec AuditRecord) error {
	s.recs = append(s.recs, rec)
	return nil
}

func (s *stubAudit) ops() []string {
	out := make([]string, 0, len(s.recs))
	for _, r := range s.recs {
		out = append(out, r.Operation)
	}
	return out
}

// stubSettings is an in-memory settingsStore.
type stubSettings struct {
	st         aiconfig.Settings
	readErr    error
	writeErr   error
	enabledErr error

	setEnabled    *bool
	setTopK       *int
	setProvider   *store.ProviderSettings
	setCredential *string
	accepted      *struct {
		version, actor string
		at             time.Time
	}
}

func (s *stubSettings) Settings(context.Context) (aiconfig.Settings, error) {
	if s.readErr != nil {
		return aiconfig.Settings{}, s.readErr
	}
	return s.st, nil
}

func (s *stubSettings) SetEnabled(_ context.Context, enabled bool) error {
	if s.enabledErr != nil {
		return s.enabledErr
	}
	if s.writeErr != nil {
		return s.writeErr
	}
	s.setEnabled = &enabled
	s.st.Enabled = enabled
	return nil
}

func (s *stubSettings) SetProviderSettings(_ context.Context, p store.ProviderSettings) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.setProvider = &p
	s.st.Provider = p
	return nil
}

func (s *stubSettings) SetCredential(_ context.Context, credential string) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.setCredential = &credential
	s.st.CredentialSet = credential != ""
	s.st.CredentialLast4 = store.Last4(credential)
	return nil
}

func (s *stubSettings) AcceptNotice(_ context.Context, version, actor string, now time.Time) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.accepted = &struct {
		version, actor string
		at             time.Time
	}{version, actor, now}
	s.st.Notice = store.DataNotice{Version: version, AcceptedBy: actor, AcceptedAt: &now}
	return nil
}

func (s *stubSettings) SetMonthlyLimit(_ context.Context, limit int64) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.st.MonthlyLimit = limit
	return nil
}

func (s *stubSettings) SetOrgContext(_ context.Context, oc string) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.st.OrgContext = oc
	return nil
}

func (s *stubSettings) SetTopK(_ context.Context, topK int) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.setTopK = &topK
	s.st.TopK = topK
	return nil
}

// stubLLM is a fake llmClient.
type stubLLM struct {
	available bool
	reason    string
	test      llm.TestResult
	testErr   error
	model     string
	modelErr  error
	tested    int
}

func (s *stubLLM) ProviderStatus() (bool, string) { return s.available, s.reason }

func (s *stubLLM) Test(context.Context) (llm.TestResult, error) {
	s.tested++
	return s.test, s.testErr
}

func (s *stubLLM) DefaultModel(context.Context) (string, error) { return s.model, s.modelErr }

type stubUsage struct {
	u   store.Usage
	err error
}

func (s *stubUsage) Month(context.Context, time.Time) (store.Usage, error) { return s.u, s.err }

type stubSummaryReader struct {
	sum store.Summary
	err error
}

func (s *stubSummaryReader) GetSummary(context.Context, string) (store.Summary, error) {
	return s.sum, s.err
}

type stubTopQuestions struct {
	questions []string
	err       error
	gotLimit  int
	gotFilter store.AccessFilter
}

func (s *stubTopQuestions) TopQuestions(_ context.Context, f store.AccessFilter, limit int) ([]string, error) {
	s.gotLimit, s.gotFilter = limit, f
	return s.questions, s.err
}

type stubRelated struct {
	neighbors []store.RelatedPolicy
	err       error
	gotPolicy string
	gotFilter store.AccessFilter
	gotTopN   int
}

func (s *stubRelated) RelatedPolicies(_ context.Context, policyID string, f store.AccessFilter, topN int) ([]store.RelatedPolicy, error) {
	s.gotPolicy, s.gotFilter, s.gotTopN = policyID, f, topN
	return s.neighbors, s.err
}

// stubQuotaCounter returns a scripted count and records who was counted.
type stubQuotaCounter struct {
	used    int64
	err     error
	calls   int
	lastUID string
}

func (s *stubQuotaCounter) Consume(_ context.Context, userID string, _ time.Time) (int64, error) {
	s.calls++
	s.lastUID = userID
	if s.err != nil {
		return 0, s.err
	}
	return s.used, nil
}

type stubUserLimitStore struct {
	limits map[string]int
	getErr error
}

func newStubUserLimitStore() *stubUserLimitStore {
	return &stubUserLimitStore{limits: map[string]int{}}
}

func (s *stubUserLimitStore) Get(_ context.Context, userID string) (int, bool, error) {
	if s.getErr != nil {
		return 0, false, s.getErr
	}
	v, ok := s.limits[userID]
	return v, ok, nil
}

func (s *stubUserLimitStore) Set(_ context.Context, userID string, limit int) error {
	s.limits[userID] = limit
	return nil
}

func (s *stubUserLimitStore) Delete(_ context.Context, userID string) error {
	delete(s.limits, userID)
	return nil
}

// fakes is one server's dependencies, so a test can reach any of them.
type fakes struct {
	settings *stubSettings
	llm      *stubLLM
	usage    *stubUsage
	retr     *stubRetrieval
	qa       *stubQA
	assist   *stubAssist
	jobs     *stubJobs
	results  *stubJobResultReader
	sums     *stubSummaryReader
	topQ     *stubTopQuestions
	related  *stubRelated
	quota    *stubQuotaCounter
	limits   *stubUserLimitStore
	audit    *stubAudit
}

// testNow is a fixed instant, so quota days and months are predictable.
var testNow = time.Date(2026, 10, 5, 14, 30, 0, 0, time.UTC)

// newFakes is an enabled module with the notice accepted, no monthly limit
// and a quota counter well under the default.
func newFakes() *fakes {
	return &fakes{
		settings: &stubSettings{st: aiconfig.Settings{
			Enabled: true,
			Notice:  store.DataNotice{Version: airules.DataNoticeVersion, AcceptedBy: fixture.Alice},
		}},
		llm:     &stubLLM{available: true, model: "model-a"},
		usage:   &stubUsage{},
		retr:    &stubRetrieval{},
		qa:      &stubQA{resp: generation.AnswerResponse{NoAuthorizedSource: true}},
		assist:  &stubAssist{},
		jobs:    &stubJobs{},
		results: &stubJobResultReader{},
		sums:    &stubSummaryReader{},
		topQ:    &stubTopQuestions{},
		related: &stubRelated{},
		quota:   &stubQuotaCounter{used: 1},
		limits:  newStubUserLimitStore(),
		audit:   &stubAudit{},
	}
}

func (f *fakes) deps() Deps {
	return Deps{
		Settings: f.settings, LLM: f.llm, Usage: f.usage, Retrieval: f.retr, QA: f.qa, Assist: f.assist,
		Jobs: f.jobs, JobResults: f.results, Summaries: f.sums, TopQuestions: f.topQ, Related: f.related,
		Quota: f.quota, UserLimits: f.limits, Audit: f.audit,
		DefaultQueryLimit: airules.QueryQuotaDefault,
		Embeddings:        Embeddings{Provider: "tei", Model: "embed-small"},
	}
}

func (f *fakes) server(opts ...Option) *AiServiceServer {
	return New(f.deps(), append([]Option{WithLogger(log.Nop()), WithClock(func() time.Time { return testNow })}, opts...)...)
}

// erinCtx is a call from the gateway on behalf of Erin, a reader.
func erinCtx() context.Context {
	return grpcactor.WithActor(context.Background(), grpcactor.Actor{Subject: fixture.Erin})
}

// actAsCtx is Alice, a site admin, acting as Erin.
func actAsCtx() context.Context {
	return grpcactor.WithActor(context.Background(), grpcactor.Actor{Subject: fixture.Erin, Impersonator: fixture.Alice})
}

// requireCode asserts err is the coded gRPC error code with grpc status c.
func requireCode(t *testing.T, err error, code int, c codes.Code) apperrgrpc.Info {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %d, got nil", code)
	}
	st := status.Convert(err)
	if st.Code() != c {
		t.Fatalf("expected gRPC %v, got %v (%s)", c, st.Code(), st.Message())
	}
	info, ok := apperrgrpc.FromStatus(st)
	if !ok {
		t.Fatalf("expected an ErrorInfo on %v", err)
	}
	if info.Code != code {
		t.Fatalf("expected code %d, got %d (%s)", code, info.Code, info.Symbol)
	}
	if info.Domain != "ai" {
		t.Fatalf("expected domain ai, got %q", info.Domain)
	}
	return info
}

var errBoom = errors.New("db unreachable")
