// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/llm"
	"github.com/Steward-GRC/steward-ai/internal/operator"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/reeval"
	"github.com/Steward-GRC/steward-ai/internal/relationship"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// draftLLMResponse is the canned section-body response the stub LLM returns
// for every Draft.Generate per-section Complete call, so each drafted
// section ends up with the same non-empty content (the stub ignores which
// section is being asked for).
const draftLLMResponse = `This policy establishes onboarding requirements for all new employees.`

// reviewLLMResponse is a canned review response the stub LLM returns; it
// carries a real <finding> block so generation.Review parses a non-empty
// findings list.
const reviewLLMResponse = `<finding section="purpose" severity="blocker">
The purpose section does not reference the required citation from ISO 27001 §7.2.
<suggestion>Add a citation to ISO 27001 §7.2 in the purpose section.</suggestion>
</finding>`

// reviseLLMResponse is a canned revise response the stub LLM returns; it
// carries one changed <section> block, one unchanged <section> block, and a
// <new_section> block so generation.Revise parses a non-trivial change set.
const reviseLLMResponse = `<section key="purpose" changed="true">
This policy establishes updated onboarding requirements for all new employees.
</section>
<section key="scope" changed="false"></section>
<new_section title="Definitions" after="purpose">
**New employee**: any individual within their first 90 days of employment.
</new_section>`

// startEnv returns a client on the shared envtest control plane, which
// TestMain starts once with the PolicyAIJob CRD installed. Without
// KUBEBUILDER_ASSETS (setup-envtest's binaries) these tests skip; set it to
// run them, e.g. KUBEBUILDER_ASSETS="$(setup-envtest use -p path)".
func startEnv(t *testing.T) (client.Client, func()) {
	t.Helper()
	if envClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset: skipping the envtest suite (set it to the setup-envtest binaries to run it)")
	}
	return envClient, func() {}
}

func newReconciler(c client.Client, llm provider.Generator, rw operator.ResultWriter, ep operator.EventPublisher) *operator.PolicyAIJobReconciler {
	return newReconcilerWithRetriever(c, llm, rw, ep, &stubRetriever{})
}

// newReconcilerWithRetriever is newReconciler with an explicit QA retriever,
// so a QA-operation reconcile test can inject fixed access-filtered chunks.
func newReconcilerWithRetriever(c client.Client, llm provider.Generator, rw operator.ResultWriter, ep operator.EventPublisher, retr *stubRetriever) *operator.PolicyAIJobReconciler {
	return &operator.PolicyAIJobReconciler{
		Client: c,
		Dispatcher: operator.NewDispatcher(
			generation.NewDraft(llm, generation.DefaultDraftSectionMaxTokens),
			generation.NewReview(llm),
			generation.NewRevise(llm, generation.DefaultReviseMaxTokens),
			retr,
			generation.NewQA(llm),
		),
		Results: rw,
		Events:  ep,
		Enabled: &fakeEnabledChecker{on: true},
	}
}

// newReconcilerFull is newReconciler plus the durable-write, module-switch
// and max-attempts knobs; a nil enabled keeps the module on.
func newReconcilerFull(c client.Client, llm provider.Generator, rw operator.ResultWriter, ep operator.EventPublisher, durable operator.DurableWriter, enabled operator.EnabledChecker, maxAttempts int) *operator.PolicyAIJobReconciler {
	r := newReconciler(c, llm, rw, ep)
	r.Durable = durable
	if enabled != nil {
		r.Enabled = enabled
	}
	r.MaxAttempts = maxAttempts
	return r
}

func draftInputJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(generation.DraftJobInput{
		Title: "Onboarding Policy",
		Brief: "Draft an onboarding policy covering training deadlines.",
		Sections: []generation.DraftJobInputSection{
			{Key: "purpose", Title: "Purpose", Order: 1},
			{Key: "scope", Title: "Scope", Guidance: "Who this applies to.", Order: 2},
		},
	})
	if err != nil {
		t.Fatalf("marshal draft input: %v", err)
	}
	return raw
}

func reviewInputJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(generation.ReviewJobInput{
		Title: "Onboarding Policy",
		Sections: []generation.ReviewJobInputSection{
			{Key: "purpose", Title: "Purpose", Content: "This policy establishes onboarding requirements for all new employees."},
		},
		StandardsRefs:     []string{"ISO 27001 §7.2"},
		RelatedPolicyRefs: []string{"Contractor Access Policy"},
	})
	if err != nil {
		t.Fatalf("marshal review input: %v", err)
	}
	return raw
}

func createJob(t *testing.T, ctx context.Context, c client.Client, spec v1alpha1.PolicyAIJobSpec) *v1alpha1.PolicyAIJob {
	t.Helper()
	job := &v1alpha1.PolicyAIJob{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "aijob-", Namespace: "default"},
		Spec:       spec,
	}
	if err := c.Create(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

func reconcile(t *testing.T, ctx context.Context, r *operator.PolicyAIJobReconciler, job *v1alpha1.PolicyAIJob) {
	t.Helper()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: job.Namespace, Name: job.Name}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func getJob(t *testing.T, ctx context.Context, c client.Client, job *v1alpha1.PolicyAIJob) *v1alpha1.PolicyAIJob {
	t.Helper()
	var out v1alpha1.PolicyAIJob
	if err := c.Get(ctx, types.NamespacedName{Namespace: job.Namespace, Name: job.Name}, &out); err != nil {
		t.Fatalf("get job: %v", err)
	}
	return &out
}

func TestReconcile_draftHappyPath(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	llm := &stubLLM{response: draftLLMResponse}
	rw := &fakeResultWriter{}
	ep := &fakeEventPublisher{}
	r := newReconciler(c, llm, rw, ep)

	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation:       v1alpha1.OperationDraft,
		ActorUserID:     "user-1",
		CategoryID:      "category-hr",
		ReadCategoryIDs: []string{"category-hr"},
		Input:           runtime.RawExtension{Raw: draftInputJSON(t)},
	})

	reconcile(t, ctx, r, job)

	got := getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseSucceeded {
		t.Fatalf("expected Succeeded, got %q (error=%q)", got.Status.Phase, got.Status.Error)
	}
	if got.Status.ResultRef == "" {
		t.Fatal("expected ResultRef set on success")
	}
	if got.Status.StartedAt == nil || got.Status.FinishedAt == nil {
		t.Fatalf("expected StartedAt+FinishedAt set, got %+v", got.Status)
	}
	if got.Status.ObservedGeneration != got.Generation {
		t.Fatalf("expected ObservedGeneration==Generation, got %d vs %d", got.Status.ObservedGeneration, got.Generation)
	}

	// Result written to Redis under the computed key with the expected TTL.
	writes, key, val := rw.snapshot()
	if writes != 1 {
		t.Fatalf("expected exactly 1 Redis write, got %d", writes)
	}
	if key != got.Status.ResultRef {
		t.Fatalf("Redis key %q != status.ResultRef %q", key, got.Status.ResultRef)
	}
	env, ok := val.(operator.ResultEnvelope)
	if !ok {
		t.Fatalf("expected ResultEnvelope written to Redis, got %T", val)
	}
	if env.Operation != string(v1alpha1.OperationDraft) {
		t.Fatalf("expected envelope operation %q, got %q", v1alpha1.OperationDraft, env.Operation)
	}
	resp, ok := env.Result.(generation.DraftResponse)
	if !ok {
		t.Fatalf("expected DraftResponse in envelope, got %T", env.Result)
	}
	if len(resp.Sections) != 2 || resp.Sections[0].Content == "" {
		t.Fatalf("expected 2 non-empty drafted sections, got %+v", resp.Sections)
	}

	// Completion event published, Succeeded, carrying the actor.
	events := ep.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 completion event, got %d", len(events))
	}
	ev := events[0]
	if ev.Phase != string(v1alpha1.PhaseSucceeded) || ev.JobID != got.Name || ev.ActorUserID != "user-1" || ev.ResultRef != got.Status.ResultRef {
		t.Fatalf("unexpected completion event: %+v", ev)
	}
}

func TestReconcile_draftFailurePath(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	llm := &stubLLM{err: context.DeadlineExceeded} // LLM error → generation fails
	rw := &fakeResultWriter{}
	ep := &fakeEventPublisher{}
	// MaxAttempts=1: the very first (and only) failed attempt exhausts the
	// budget, so this test's single reconcile call reaches permanent Failed
	// — also exercises "maxAttempts is honored" (see TestReconcile_maxAttemptsConfigurable).
	r := newReconcilerFull(c, llm, rw, ep, nil, nil, 1)

	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation:   v1alpha1.OperationDraft,
		ActorUserID: "user-2",
		Input:       runtime.RawExtension{Raw: draftInputJSON(t)},
	})

	reconcile(t, ctx, r, job)

	got := getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("expected Failed, got %q", got.Status.Phase)
	}
	if got.Status.Error == "" {
		t.Fatal("expected status.error populated on failure")
	}
	if got.Status.FinishedAt == nil {
		t.Fatal("expected FinishedAt set on failure")
	}
	if writes, _, _ := rw.snapshot(); writes != 0 {
		t.Fatalf("expected no Redis write on generation failure, got %d", writes)
	}
	events := ep.snapshot()
	if len(events) != 1 || events[0].Phase != string(v1alpha1.PhaseFailed) || events[0].Error == "" {
		t.Fatalf("expected 1 Failed completion event with error, got %+v", events)
	}
}

func TestReconcile_unimplementedOperationFails(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	r := newReconcilerFull(c, &stubLLM{response: draftLLMResponse}, &fakeResultWriter{}, &fakeEventPublisher{}, nil, nil, 1)

	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation:   v1alpha1.OperationSummarize, // registered but not wired
		ActorUserID: "user-3",
		PolicyID:    "pol-1",
		VersionID:   "ver-1",
	})

	reconcile(t, ctx, r, job)

	got := getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("expected Failed for unimplemented op, got %q", got.Status.Phase)
	}
	if got.Status.Error == "" {
		t.Fatal("expected error message for unimplemented op")
	}
}

// TestReconcile_qaHappyPath drives a QA-operation job end to end through the
// reconciler: it retrieves (stubbed chunks), generates a grounded answer, and
// writes an AnswerResponse into the Redis result envelope under the version-
// keyed result ref. This is the async-QA analogue of the draft/review happy
// paths.
func TestReconcile_qaHappyPath(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	retr := &stubRetriever{results: []store.SearchResult{
		{Chunk: store.Chunk{ID: "chunk-1", PolicyID: "pol-1", VersionID: "ver-1", VersionNo: 1, SectionKey: "allowances", ChunkIndex: 0, PolicyTitle: "Travel", ContentText: "Employees may travel up to 5000 km per year.", CategoryID: "category-hr", Sensitivity: "standard"}},
	}}
	llm := &stubLLM{response: "Employees may travel up to 5000 km per year."}
	rw := &fakeResultWriter{}
	ep := &fakeEventPublisher{}
	r := newReconcilerWithRetriever(c, llm, rw, ep, retr)

	raw, err := json.Marshal(generation.QAJobInput{Question: "how much travel is allowed?"})
	if err != nil {
		t.Fatalf("marshal qa input: %v", err)
	}
	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation:       v1alpha1.OperationQA,
		ActorUserID:     "user-qa",
		CategoryID:      "category-hr",
		PolicyID:        "pol-1",
		VersionID:       "ver-1",
		ReadCategoryIDs: []string{"category-hr"},
		AllCategories:   true,
		Input:           runtime.RawExtension{Raw: raw},
	})

	reconcile(t, ctx, r, job)

	got := getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseSucceeded {
		t.Fatalf("expected Succeeded, got %q (error=%q)", got.Status.Phase, got.Status.Error)
	}
	if want := "ai:job-result:pol-1:ver-1:QA"; got.Status.ResultRef != want {
		t.Fatalf("expected version-keyed result ref %q, got %q", want, got.Status.ResultRef)
	}

	// Retrieval got the job's read scope.
	calls, rreq := retr.snapshot()
	if calls != 1 || !rreq.AllCategories || len(rreq.CategoryIDs) != 1 {
		t.Fatalf("expected 1 retrieval with the CR's access scope, got calls=%d req=%+v", calls, rreq)
	}

	writes, key, val := rw.snapshot()
	if writes != 1 || key != got.Status.ResultRef {
		t.Fatalf("expected 1 Redis write under the result ref, got writes=%d key=%q", writes, key)
	}
	env, ok := val.(operator.ResultEnvelope)
	if !ok {
		t.Fatalf("expected ResultEnvelope, got %T", val)
	}
	if env.Operation != string(v1alpha1.OperationQA) {
		t.Fatalf("expected envelope operation QA, got %q", env.Operation)
	}
	resp, ok := env.Result.(generation.AnswerResponse)
	if !ok {
		t.Fatalf("expected AnswerResponse in envelope, got %T", env.Result)
	}
	if resp.Answer == "" || len(resp.Citations) != 1 || resp.Citations[0].ChunkID != "chunk-1" {
		t.Fatalf("expected a grounded answer citing chunk-1, got %+v", resp)
	}
}

func TestReconcile_reviewHappyPath(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	llm := &stubLLM{response: reviewLLMResponse}
	rw := &fakeResultWriter{}
	ep := &fakeEventPublisher{}
	r := newReconciler(c, llm, rw, ep)

	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation:       v1alpha1.OperationReview,
		ActorUserID:     "user-5",
		CategoryID:      "category-hr",
		PolicyID:        "pol-1",
		VersionID:       "ver-1",
		ReadCategoryIDs: []string{"category-hr"},
		Input:           runtime.RawExtension{Raw: reviewInputJSON(t)},
	})

	reconcile(t, ctx, r, job)

	got := getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseSucceeded {
		t.Fatalf("expected Succeeded, got %q (error=%q)", got.Status.Phase, got.Status.Error)
	}
	if got.Status.ResultRef == "" {
		t.Fatal("expected ResultRef set on success")
	}
	// REVIEW acts on an existing policy version, so the result key must be
	// version-keyed (policyId+versionId+operation), not the DRAFT CR-name fallback.
	if want := "ai:job-result:pol-1:ver-1:REVIEW"; got.Status.ResultRef != want {
		t.Fatalf("expected version-keyed result ref %q, got %q", want, got.Status.ResultRef)
	}

	writes, key, val := rw.snapshot()
	if writes != 1 {
		t.Fatalf("expected exactly 1 Redis write, got %d", writes)
	}
	if key != got.Status.ResultRef {
		t.Fatalf("Redis key %q != status.ResultRef %q", key, got.Status.ResultRef)
	}
	env, ok := val.(operator.ResultEnvelope)
	if !ok {
		t.Fatalf("expected ResultEnvelope written to Redis, got %T", val)
	}
	if env.Operation != string(v1alpha1.OperationReview) {
		t.Fatalf("expected envelope operation %q, got %q", v1alpha1.OperationReview, env.Operation)
	}
	resp, ok := env.Result.(generation.ReviewResponse)
	if !ok {
		t.Fatalf("expected ReviewResponse in envelope, got %T", env.Result)
	}
	if len(resp.Findings) != 1 || resp.Findings[0].SectionKey != "purpose" || resp.Findings[0].Severity != generation.SeverityBlocker {
		t.Fatalf("expected 1 blocker finding on purpose, got %+v", resp.Findings)
	}

	events := ep.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 completion event, got %d", len(events))
	}
	ev := events[0]
	if ev.Phase != string(v1alpha1.PhaseSucceeded) || ev.JobID != got.Name || ev.ActorUserID != "user-5" || ev.ResultRef != got.Status.ResultRef {
		t.Fatalf("unexpected completion event: %+v", ev)
	}
}

func TestReconcile_idempotentOnTerminalJob(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	llm := &stubLLM{response: draftLLMResponse}
	rw := &fakeResultWriter{}
	ep := &fakeEventPublisher{}
	r := newReconciler(c, llm, rw, ep)

	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation:   v1alpha1.OperationDraft,
		ActorUserID: "user-4",
		Input:       runtime.RawExtension{Raw: draftInputJSON(t)},
	})

	// draftInputJSON has 2 sections, and Draft.Generate now makes one
	// Complete call per section (never one call for the whole draft).
	reconcile(t, ctx, r, job)
	if callsAfterFirst := llm.callCount(); callsAfterFirst != 2 {
		t.Fatalf("expected 2 LLM calls (one per section) after first reconcile, got %d", callsAfterFirst)
	}

	// Re-reconcile the now-terminal job: generation must NOT run again.
	reconcile(t, ctx, r, getJob(t, ctx, c, job))

	if calls := llm.callCount(); calls != 2 {
		t.Fatalf("expected LLM NOT re-invoked on terminal job, got %d calls", calls)
	}
	if writes, _, _ := rw.snapshot(); writes != 1 {
		t.Fatalf("expected exactly 1 Redis write across two reconciles, got %d", writes)
	}
	if events := ep.snapshot(); len(events) != 1 {
		t.Fatalf("expected exactly 1 completion event across two reconciles, got %d", len(events))
	}
}

// TestReconcile_durableWriteAlongsideRedis verifies a succeeded job's result
// is persisted to BOTH tiers — Redis (hot) and the durable Postgres-tier
// writer (the "memory" that survives Redis's ResultTTL eviction) — not just
// one.
func TestReconcile_durableWriteAlongsideRedis(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	llm := &stubLLM{response: draftLLMResponse}
	rw := &fakeResultWriter{}
	durable := &fakeDurableWriter{}
	r := newReconcilerFull(c, llm, rw, &fakeEventPublisher{}, durable, nil, 0)

	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation:   v1alpha1.OperationDraft,
		ActorUserID: "user-durable",
		Input:       runtime.RawExtension{Raw: draftInputJSON(t)},
	})

	reconcile(t, ctx, r, job)

	got := getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseSucceeded {
		t.Fatalf("expected Succeeded, got %q (error=%q)", got.Status.Phase, got.Status.Error)
	}
	if writes, _, _ := rw.snapshot(); writes != 1 {
		t.Fatalf("expected exactly 1 Redis write, got %d", writes)
	}
	saves := durable.snapshot()
	if len(saves) != 1 {
		t.Fatalf("expected exactly 1 durable write, got %d", len(saves))
	}
	if saves[0].JobID != got.Name || saves[0].Operation != "DRAFT" || saves[0].ActorUserID != "user-durable" || saves[0].ResultJSON == "" {
		t.Fatalf("unexpected durable write: %+v", saves[0])
	}
}

// TestReconcile_maxAttemptsExhausted_permanentFailure verifies a job that
// fails every attempt stops retrying once MaxAttempts is reached — jobs
// must not retry forever.
func TestReconcile_maxAttemptsExhausted_permanentFailure(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	llm := &stubLLM{err: context.DeadlineExceeded}
	r := newReconcilerFull(c, llm, &fakeResultWriter{}, &fakeEventPublisher{}, nil, nil, 3)

	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation:   v1alpha1.OperationDraft,
		ActorUserID: "user-fail",
		Input:       runtime.RawExtension{Raw: draftInputJSON(t)},
	})

	// Attempt 1: fails, requeues (Running, Attempts=1).
	reconcile(t, ctx, r, job)
	got := getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseRunning || got.Status.Attempts != 1 {
		t.Fatalf("after attempt 1: expected Running/Attempts=1, got phase=%q attempts=%d", got.Status.Phase, got.Status.Attempts)
	}

	// Attempt 2: fails, requeues again (Running, Attempts=2).
	reconcile(t, ctx, r, got)
	got = getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseRunning || got.Status.Attempts != 2 {
		t.Fatalf("after attempt 2: expected Running/Attempts=2, got phase=%q attempts=%d", got.Status.Phase, got.Status.Attempts)
	}

	// Attempt 3: fails, exhausts MaxAttempts=3 → permanently Failed.
	reconcile(t, ctx, r, got)
	got = getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("after attempt 3: expected permanently Failed, got phase=%q attempts=%d", got.Status.Phase, got.Status.Attempts)
	}
	if got.Status.Attempts != 3 {
		t.Fatalf("expected Attempts=3 at exhaustion, got %d", got.Status.Attempts)
	}

	// A further reconcile is a no-op (terminal + same generation): no more calls.
	callsBefore := llm.callCount()
	reconcile(t, ctx, r, got)
	if llm.callCount() != callsBefore {
		t.Fatalf("expected no further LLM calls once permanently Failed, calls went %d -> %d", callsBefore, llm.callCount())
	}
}

// TestReconcile_maxAttemptsConfigurable verifies a different MaxAttempts
// value is honored (5, not the default 3): the job is still Running with
// Attempts=4 after 4 failures, and only becomes Failed on the 5th.
func TestReconcile_maxAttemptsConfigurable(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	llm := &stubLLM{err: context.DeadlineExceeded}
	r := newReconcilerFull(c, llm, &fakeResultWriter{}, &fakeEventPublisher{}, nil, nil, 5)

	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation:   v1alpha1.OperationDraft,
		ActorUserID: "user-cfg",
		Input:       runtime.RawExtension{Raw: draftInputJSON(t)},
	})

	var got *v1alpha1.PolicyAIJob
	for i := 1; i <= 4; i++ {
		reconcile(t, ctx, r, job)
		got = getJob(t, ctx, c, job)
		if got.Status.Phase != v1alpha1.PhaseRunning || got.Status.Attempts != i {
			t.Fatalf("after attempt %d (maxAttempts=5): expected Running/Attempts=%d, got phase=%q attempts=%d", i, i, got.Status.Phase, got.Status.Attempts)
		}
		job = got
	}

	reconcile(t, ctx, r, got) // attempt 5: exhausts MaxAttempts=5
	got = getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Attempts != 5 {
		t.Fatalf("after attempt 5 (maxAttempts=5): expected Failed/Attempts=5, got phase=%q attempts=%d", got.Status.Phase, got.Status.Attempts)
	}
}

// TestReconcile_adminOff_abortsRetry_andFirstPass: off is off. A job in
// retry fails the moment the module is turned off, without another attempt,
// and so does a fresh job on its first pass: neither reaches the model, and
// nothing is written.
func TestReconcile_adminOff_abortsRetry_andFirstPass(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	// Job A: fails its first attempt (Attempts becomes 1, now in retry).
	failingLLM := &stubLLM{err: context.DeadlineExceeded}
	enabled := &fakeEnabledChecker{on: true}
	rA := newReconcilerFull(c, failingLLM, &fakeResultWriter{}, &fakeEventPublisher{}, nil, enabled, 3)
	jobA := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation:   v1alpha1.OperationDraft,
		ActorUserID: "user-a",
		Input:       runtime.RawExtension{Raw: draftInputJSON(t)},
	})
	reconcile(t, ctx, rA, jobA)
	gotA := getJob(t, ctx, c, jobA)
	if gotA.Status.Phase != v1alpha1.PhaseRunning || gotA.Status.Attempts != 1 {
		t.Fatalf("job A after attempt 1: expected Running/Attempts=1, got phase=%q attempts=%d", gotA.Status.Phase, gotA.Status.Attempts)
	}

	enabled.set(false)

	callsBefore := failingLLM.callCount()
	reconcile(t, ctx, rA, gotA)
	gotA = getJob(t, ctx, c, jobA)
	if gotA.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("job A after admin-off retry: expected Failed (aborted), got phase=%q", gotA.Status.Phase)
	}
	if gotA.Status.Attempts != 1 {
		t.Fatalf("expected the abort to NOT consume another attempt, got Attempts=%d", gotA.Status.Attempts)
	}
	if failingLLM.callCount() != callsBefore {
		t.Fatal("expected no model call once the module is off")
	}

	// Job B: a fresh job on its first pass while the module is off.
	healthyLLM := &stubLLM{response: draftLLMResponse}
	rwB := &fakeResultWriter{}
	durableB := &fakeDurableWriter{}
	epB := &fakeEventPublisher{}
	rB := newReconcilerFull(c, healthyLLM, rwB, epB, durableB, enabled, 3)
	jobB := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation:   v1alpha1.OperationDraft,
		ActorUserID: "user-b",
		Input:       runtime.RawExtension{Raw: draftInputJSON(t)},
	})

	reconcile(t, ctx, rB, jobB)

	gotB := getJob(t, ctx, c, jobB)
	if gotB.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("job B (first pass, module off): expected Failed, got phase=%q", gotB.Status.Phase)
	}
	if gotB.Status.Error != operator.ErrModuleOff.Error() {
		t.Fatalf("expected the error to name the module being off, got %q", gotB.Status.Error)
	}
	if healthyLLM.callCount() != 0 {
		t.Fatalf("expected no model call, got %d", healthyLLM.callCount())
	}
	if writes, _, _ := rwB.snapshot(); writes != 0 {
		t.Fatalf("expected no result write, got %d", writes)
	}
	if saves := durableB.snapshot(); len(saves) != 0 {
		t.Fatalf("expected no durable save, got %d", len(saves))
	}
	if events := epB.snapshot(); len(events) != 1 || events[0].Phase != string(v1alpha1.PhaseFailed) {
		t.Fatalf("expected one Failed completion event, got %+v", events)
	}
}

// TestReconcile_moduleSwitchUnreadable_failsGenerativeJob: a switch that
// can't be read fails the job rather than run it.
func TestReconcile_moduleSwitchUnreadable_failsGenerativeJob(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	llmStub := &stubLLM{response: draftLLMResponse}
	r := newReconcilerFull(c, llmStub, &fakeResultWriter{}, &fakeEventPublisher{}, nil,
		&fakeEnabledChecker{err: errors.New("postgres down")}, 3)
	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationDraft,
		Input:     runtime.RawExtension{Raw: draftInputJSON(t)},
	})
	reconcile(t, ctx, r, job)

	got := getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("expected Failed, got %q", got.Status.Phase)
	}
	if llmStub.callCount() != 0 {
		t.Fatalf("expected no model call, got %d", llmStub.callCount())
	}
}

// TestReconcile_nilSwitchCountsAsOff: a reconciler built without the switch
// runs no generative job.
func TestReconcile_nilSwitchCountsAsOff(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	llmStub := &stubLLM{response: draftLLMResponse}
	r := newReconciler(c, llmStub, &fakeResultWriter{}, &fakeEventPublisher{})
	r.Enabled = nil
	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationDraft,
		Input:     runtime.RawExtension{Raw: draftInputJSON(t)},
	})
	reconcile(t, ctx, r, job)

	if got := getJob(t, ctx, c, job); got.Status.Phase != v1alpha1.PhaseFailed || llmStub.callCount() != 0 {
		t.Fatalf("expected Failed with no model call, got phase=%q calls=%d", got.Status.Phase, llmStub.callCount())
	}
}

// TestReconcile_moduleOff_zeroModelJobsStillRun: RELATED_REEVAL,
// RELATIONSHIP_LEARN and a related-only SUGGEST_ENRICHMENTS make no model
// call, so they run while the module is off.
func TestReconcile_moduleOff_zeroModelJobsStillRun(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	llmStub := &stubLLM{response: draftLLMResponse}
	rw := &fakeResultWriter{}
	r := newReconcilerFull(c, llmStub, rw, &fakeEventPublisher{}, nil, &fakeEnabledChecker{on: false}, 3)
	r.Dispatcher = r.Dispatcher.
		WithReeval(&stubReevalEngine{res: reeval.Result{Recomputed: 1}}).
		WithRelationshipLearn(&stubRelationshipLearnEngine{res: relationship.Result{EdgeCount: 3}}).
		WithSuggest(&stubSuggester{out: generation.EnrichmentSuggestions{
			Related: []generation.RelatedEnrichmentSuggestion{{PolicyID: "pol-2", Title: "Travel"}},
		}})

	relatedOnly, err := json.Marshal(generation.SuggestJobInput{
		Sections: []generation.SuggestJobInputSection{{Key: "s1", Title: "S1", Content: "body"}},
		OptOut:   generation.EnrichmentOptOut{Definitions: true, References: true},
	})
	if err != nil {
		t.Fatalf("marshal suggest input: %v", err)
	}
	for _, spec := range []v1alpha1.PolicyAIJobSpec{
		{Operation: v1alpha1.OperationRelatedReeval},
		{Operation: v1alpha1.OperationRelationshipLearn},
		{Operation: v1alpha1.OperationSuggestEnrichments, PolicyID: "pol-1", Input: runtime.RawExtension{Raw: relatedOnly}},
	} {
		job := createJob(t, ctx, c, spec)
		reconcile(t, ctx, r, job)
		if got := getJob(t, ctx, c, job); got.Status.Phase != v1alpha1.PhaseSucceeded {
			t.Fatalf("%s with the module off: expected Succeeded, got %q (error=%q)", spec.Operation, got.Status.Phase, got.Status.Error)
		}
	}
	if writes, _, _ := rw.snapshot(); writes != 3 {
		t.Fatalf("expected 3 result writes, got %d", writes)
	}
	if llmStub.callCount() != 0 {
		t.Fatalf("expected no model call, got %d", llmStub.callCount())
	}
}

// TestReconcile_modelRefusalFailsWithoutRetry: a refusal from the model
// client (the monthly limit, the module turned off mid-job) is final, so the
// job fails on that attempt with the refusal as its reason.
func TestReconcile_modelRefusalFailsWithoutRetry(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	for _, refusal := range []error{
		&llm.MonthlyLimitError{Limit: 100, ResetAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		llm.ErrDisabled,
	} {
		llmStub := &stubLLM{err: refusal}
		r := newReconcilerFull(c, llmStub, &fakeResultWriter{}, &fakeEventPublisher{}, nil, nil, 3)
		job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
			Operation: v1alpha1.OperationReview,
			Input:     runtime.RawExtension{Raw: reviewInputJSON(t)},
		})
		reconcile(t, ctx, r, job)

		got := getJob(t, ctx, c, job)
		if got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Attempts != 1 {
			t.Fatalf("%v: expected Failed after one attempt, got phase=%q attempts=%d", refusal, got.Status.Phase, got.Status.Attempts)
		}
		if !strings.Contains(got.Status.Error, refusal.Error()) {
			t.Fatalf("expected the refusal as the reason, got %q", got.Status.Error)
		}
	}
}

// TestClientJobCreator_createsRelationshipLearnJob: the scheduler's creator
// makes an input-less RELATIONSHIP_LEARN job in its namespace.
func TestClientJobCreator_createsRelationshipLearnJob(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	if err := (operator.ClientJobCreator{Client: c, Namespace: "default"}).CreateRelationshipLearnJob(ctx); err != nil {
		t.Fatalf("create: %v", err)
	}
	var list v1alpha1.PolicyAIJobList
	if err := c.List(ctx, &list, client.InNamespace("default")); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, j := range list.Items {
		if j.Spec.Operation == v1alpha1.OperationRelationshipLearn && len(j.Spec.Input.Raw) == 0 {
			return
		}
	}
	t.Fatal("expected a RELATIONSHIP_LEARN job with no input")
}
