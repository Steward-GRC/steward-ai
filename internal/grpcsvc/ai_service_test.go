// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/fixture"
	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

func scope(ids ...string) *aiv1.ReadScope { return &aiv1.ReadScope{CategoryIds: ids} }

func TestSearchAndAnswer_auditEmitted(t *testing.T) {
	f := newFakes()
	_, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{
		Question:   "test question",
		CategoryId: fixture.Facilities,
		Scope:      scope(fixture.Facilities),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.audit.recs) != 1 {
		t.Fatalf("expected 1 audit call, got %d", len(f.audit.recs))
	}
	rec := f.audit.recs[0]
	if rec.Operation != "search_and_answer" {
		t.Fatalf("audit operation: got %q", rec.Operation)
	}
	if rec.Actor.Subject != fixture.Erin || rec.CategoryID != fixture.Facilities {
		t.Fatalf("audit attribution: got %+v", rec)
	}
}

// Each citation carries its document type so the UI can label policies and
// procedures.
func TestSearchAndAnswer_citationsCarryDocumentType(t *testing.T) {
	f := newFakes()
	f.qa.resp = generation.AnswerResponse{
		Answer: "answer",
		Citations: []generation.Citation{
			{PolicyID: "pol-1", PolicyTitle: fixture.DeskBookingPolicy, VersionNo: 3, DocumentType: airules.DocumentTypePolicy},
			{PolicyID: "prc-1", PolicyTitle: "Onboarding Procedure", VersionNo: 2, DocumentType: airules.DocumentTypeProcedure},
		},
	}
	resp, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{
		Question: "how do I onboard?", Scope: scope(fixture.Workplace),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Citations) != 2 {
		t.Fatalf("expected 2 citations, got %d", len(resp.Citations))
	}
	if got := resp.Citations[0].GetDocumentType(); got != airules.DocumentTypePolicy {
		t.Errorf("policy citation document_type: got %q", got)
	}
	if got := resp.Citations[1].GetDocumentType(); got != airules.DocumentTypeProcedure {
		t.Errorf("procedure citation document_type: got %q", got)
	}
}

func TestSearchAndAnswer_noAuthorizedSource(t *testing.T) {
	f := newFakes()
	resp, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{
		Question: "anything", Scope: scope(fixture.Workplace),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.NoAuthorizedSource {
		t.Fatal("expected NoAuthorizedSource=true in gRPC response")
	}
}

// An all-categories scope reaches retrieval as AllCategories, so the read
// predicate in SQL opens every category.
func TestSearchAndAnswer_threadsAllCategoriesIntoRetrieval(t *testing.T) {
	f := newFakes()
	_, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{
		Question: "what is the expense policy?",
		Scope:    &aiv1.ReadScope{AllCategories: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.retr.gotRequest.AllCategories {
		t.Fatal("expected scope.all_categories threaded into retrieval.Request.AllCategories")
	}
}

func TestSearchAndAnswer_narrowScopeUnchanged(t *testing.T) {
	f := newFakes()
	_, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{
		Question: "what is the desk booking policy?",
		Scope:    &aiv1.ReadScope{CategoryIds: []string{fixture.Facilities}, IncludeSensitive: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := f.retr.gotRequest
	if got.AllCategories {
		t.Fatal("expected AllCategories=false for a narrow scope")
	}
	if len(got.CategoryIDs) != 1 || got.CategoryIDs[0] != fixture.Facilities {
		t.Fatalf("expected category ids threaded through unchanged, got %v", got.CategoryIDs)
	}
	if !got.IncludeSensitive {
		t.Fatal("expected IncludeSensitive threaded through unchanged")
	}
}

// A category_id narrows retrieval to that one category, even for a scope
// that reads every category.
func TestSearchAndAnswer_categoryNarrowsRetrieval(t *testing.T) {
	f := newFakes()
	_, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{
		Question:   "q",
		CategoryId: fixture.Finance,
		Scope:      &aiv1.ReadScope{AllCategories: true, IncludeSensitive: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := f.retr.gotRequest
	if got.AllCategories || len(got.CategoryIDs) != 1 || got.CategoryIDs[0] != fixture.Finance || !got.IncludeSensitive {
		t.Fatalf("expected retrieval narrowed to %s, got %+v", fixture.Finance, got)
	}
}

func TestSearchAndAnswer_surfacesSegmentsAndChunkCitations(t *testing.T) {
	f := newFakes()
	f.qa.resp = generation.AnswerResponse{
		Answer:    "Employees may book a desk up to 14 days ahead.",
		Citations: []generation.Citation{{PolicyID: "pol-desk", VersionID: "ver-3", SectionKey: "booking", ChunkID: "chunk-1", ChunkIndex: 2}},
		Segments: []generation.AnswerSegment{{
			Start: 0, End: 44,
			Sources: []generation.SegmentSource{{PolicyID: "pol-desk", VersionID: "ver-3", SectionKey: "booking", ChunkID: "chunk-1", ChunkIndex: 2}},
		}},
	}
	f.retr.results = []store.SearchResult{{Chunk: store.Chunk{PolicyID: "pol-desk"}}}
	resp, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{Question: "how far ahead?", Scope: scope(fixture.Facilities)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Citations) != 1 || resp.Citations[0].ChunkId != "chunk-1" || resp.Citations[0].ChunkIndex != 2 {
		t.Fatalf("expected citation chunk id/index surfaced, got %+v", resp.Citations)
	}
	if len(resp.Segments) != 1 {
		t.Fatalf("expected 1 segment surfaced, got %d", len(resp.Segments))
	}
	seg := resp.Segments[0]
	if seg.Start != 0 || seg.End != 44 || len(seg.Sources) != 1 || seg.Sources[0].ChunkId != "chunk-1" {
		t.Fatalf("unexpected segment mapping: %+v", seg)
	}
}

func TestSearchAndAnswer_surfacesHasSensitiveSource(t *testing.T) {
	f := newFakes()
	f.qa.resp = generation.AnswerResponse{
		Answer:             "This references a restricted policy.",
		Citations:          []generation.Citation{{PolicyID: "pol-fin", VersionID: "ver-1"}},
		HasSensitiveSource: true,
	}
	resp, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{Question: "any restricted refs?", Scope: scope(fixture.Finance)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.HasSensitiveSource {
		t.Fatal("expected HasSensitiveSource=true surfaced on the response")
	}
	rec := f.audit.recs[0]
	if !rec.HasSensitive || len(rec.SourceIDs) != 1 || rec.SourceIDs[0] != "ver-1" {
		t.Fatalf("expected the audit to carry the cited version and has_sensitive, got %+v", rec)
	}
}

func TestSearchAndAnswer_requiresQuestion(t *testing.T) {
	f := newFakes()
	_, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{Scope: scope(fixture.Workplace)})
	info := requireCode(t, err, errcodes.CodeRequestInvalid, codes.InvalidArgument)
	if info.Metadata["field"] != "question" {
		t.Fatalf("expected field=question, got %v", info.Metadata)
	}
}

func TestAuthoringAssist_auditEmitted(t *testing.T) {
	f := newFakes()
	resp, err := f.server().AuthoringAssist(erinCtx(), &aiv1.AuthoringAssistRequest{
		PolicyId:        "pol-1",
		VersionId:       "ver-draft",
		SectionKey:      "scope",
		EditableContent: "old text",
		ContextHint:     "Section context",
		Operation:       aiv1.AssistOperation_ASSIST_OPERATION_REWRITE,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Suggestion == "" {
		t.Fatal("expected non-empty suggestion")
	}
	if ops := f.audit.ops(); len(ops) != 1 || ops[0] != "authoring_assist" {
		t.Fatalf("audit calls: %v", ops)
	}
	if f.assist.got.ActorUserID != fixture.Erin {
		t.Fatalf("expected the actor's subject on the assist request, got %q", f.assist.got.ActorUserID)
	}
}

func TestSubmitAIJob_createsJobAndEmitsAudit(t *testing.T) {
	f := newFakes()
	f.jobs.nextName = "aijob-abc123"
	resp, err := f.server().SubmitAIJob(erinCtx(), &aiv1.SubmitAIJobRequest{
		CategoryId: fixture.Facilities,
		Scope:      &aiv1.ReadScope{CategoryIds: []string{fixture.Facilities}, IncludeSensitive: true},
		Operation:  aiv1.JobOperation_JOB_OPERATION_DRAFT,
		InputJson:  `{"brief":"draft a desk booking policy","sections":[{"key":"purpose","title":"Purpose","order":1}]}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.JobId != "aijob-abc123" {
		t.Fatalf("expected job id from CreateJob, got %q", resp.JobId)
	}
	if len(f.jobs.created) != 1 {
		t.Fatalf("expected 1 job created, got %d", len(f.jobs.created))
	}
	created := f.jobs.created[0]
	if created.Operation != v1alpha1.OperationDraft {
		t.Fatalf("expected DRAFT operation on the CR, got %q", created.Operation)
	}
	if created.ActorUserID != fixture.Erin || created.ImpersonatorUserID != "" {
		t.Fatalf("expected the actor as job owner, got %+v", created)
	}
	if created.CategoryID != fixture.Facilities || len(created.ReadCategoryIDs) != 1 || !created.IncludeSensitive || created.AllCategories {
		t.Fatalf("expected category and read scope carried onto the CR, got %+v", created)
	}
	if string(created.Input.Raw) == "" {
		t.Fatal("expected input_json carried onto the CR as spec.input")
	}
	if ops := f.audit.ops(); len(ops) != 1 || ops[0] != "submit_ai_job" {
		t.Fatalf("audit calls: %v", ops)
	}
}

func TestSubmitAIJob_mapsReviseOperation(t *testing.T) {
	f := newFakes()
	f.jobs.nextName = "aijob-revise-1"
	resp, err := f.server().SubmitAIJob(erinCtx(), &aiv1.SubmitAIJobRequest{
		CategoryId: fixture.Facilities,
		PolicyId:   "pol-1",
		VersionId:  "ver-1",
		Operation:  aiv1.JobOperation_JOB_OPERATION_REVISE,
		InputJson:  `{"instruction":"add a data retention clause","policyId":"pol-1","versionId":"ver-1","sections":[{"key":"purpose","title":"Purpose","content":"Body.","order":1}]}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.JobId != "aijob-revise-1" {
		t.Fatalf("expected job id from CreateJob, got %q", resp.JobId)
	}
	if created := f.jobs.created[0]; created.Operation != v1alpha1.OperationRevise {
		t.Fatalf("expected REVISE operation on the CR, got %q", created.Operation)
	}
}

func TestSubmitAIJob_requiresActor(t *testing.T) {
	f := newFakes()
	_, err := f.server().SubmitAIJob(context.Background(), &aiv1.SubmitAIJobRequest{
		Operation: aiv1.JobOperation_JOB_OPERATION_DRAFT,
	})
	requireCode(t, err, errcodes.CodeActorRequired, codes.Unauthenticated)
	if len(f.jobs.created) != 0 {
		t.Fatal("expected no job without an actor")
	}
}

func TestSubmitAIJob_rejectsUnsubmittableOperations(t *testing.T) {
	for _, op := range []aiv1.JobOperation{
		aiv1.JobOperation_JOB_OPERATION_UNSPECIFIED,
		aiv1.JobOperation_JOB_OPERATION_RELATED_REEVAL,
		aiv1.JobOperation_JOB_OPERATION_RELATIONSHIP_LEARN,
	} {
		t.Run(op.String(), func(t *testing.T) {
			f := newFakes()
			_, err := f.server().SubmitAIJob(erinCtx(), &aiv1.SubmitAIJobRequest{Operation: op})
			info := requireCode(t, err, errcodes.CodeRequestInvalid, codes.InvalidArgument)
			if info.Metadata["field"] != "operation" {
				t.Fatalf("expected field=operation, got %v", info.Metadata)
			}
			if len(f.jobs.created) != 0 || f.quota.calls != 0 {
				t.Fatal("an invalid job is neither created nor counted")
			}
		})
	}
}

func erinJob(name string, op v1alpha1.JobOperation, st v1alpha1.PolicyAIJobStatus) *v1alpha1.PolicyAIJob {
	return &v1alpha1.PolicyAIJob{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.PolicyAIJobSpec{Operation: op, ActorUserID: fixture.Erin},
		Status:     st,
	}
}

func TestGetAIJob_returnsStatusFromCR(t *testing.T) {
	startedAt := metav1.Now()
	f := newFakes()
	f.jobs.jobs = map[string]*v1alpha1.PolicyAIJob{
		"aijob-abc123": erinJob("aijob-abc123", v1alpha1.OperationDraft, v1alpha1.PolicyAIJobStatus{
			Phase: v1alpha1.PhaseSucceeded, ResultRef: "ai:job-result:aijob-abc123:DRAFT", StartedAt: &startedAt,
		}),
	}
	resp, err := f.server().GetAIJob(erinCtx(), &aiv1.GetAIJobRequest{JobId: "aijob-abc123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Phase != aiv1.JobPhase_JOB_PHASE_SUCCEEDED {
		t.Fatalf("expected SUCCEEDED phase, got %v", resp.Phase)
	}
	if resp.ResultRef != "ai:job-result:aijob-abc123:DRAFT" {
		t.Fatalf("expected result_ref from status, got %q", resp.ResultRef)
	}
	if resp.StartedAt == "" {
		t.Fatal("expected non-empty started_at")
	}
}

func TestGetAIJob_hydratesQAResultFromEnvelope(t *testing.T) {
	answer := generation.AnswerResponse{
		Answer:    "Employees may book a desk up to 14 days ahead.",
		Citations: []generation.Citation{{PolicyID: "pol-desk", VersionID: "ver-3", SectionKey: "booking", ChunkID: "chunk-1", ChunkIndex: 2}},
		Segments: []generation.AnswerSegment{{
			Start: 0, End: 44,
			Sources: []generation.SegmentSource{{PolicyID: "pol-desk", VersionID: "ver-3", SectionKey: "booking", ChunkID: "chunk-1", ChunkIndex: 2}},
		}},
	}
	raw, err := json.Marshal(struct {
		Operation string                    `json:"operation"`
		Result    generation.AnswerResponse `json:"result"`
	}{Operation: "QA", Result: answer})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	f := newFakes()
	f.jobs.jobs = map[string]*v1alpha1.PolicyAIJob{
		"aijob-qa": erinJob("aijob-qa", v1alpha1.OperationQA, v1alpha1.PolicyAIJobStatus{
			Phase: v1alpha1.PhaseSucceeded, ResultRef: "ai:job-result:pol-1:ver-1:QA",
		}),
	}
	f.results.raw, f.results.found = raw, true

	resp, err := f.server().GetAIJob(erinCtx(), &aiv1.GetAIJobRequest{JobId: "aijob-qa"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.results.gotKey != "ai:job-result:pol-1:ver-1:QA" {
		t.Fatalf("expected the reader called with the result ref, got %q", f.results.gotKey)
	}
	if resp.QaResult == nil {
		t.Fatal("expected qa_result populated for a SUCCEEDED QA job")
	}
	if resp.QaResult.Answer != answer.Answer {
		t.Fatalf("unexpected qa answer: %q", resp.QaResult.Answer)
	}
	if len(resp.QaResult.Citations) != 1 || resp.QaResult.Citations[0].ChunkId != "chunk-1" || resp.QaResult.Citations[0].ChunkIndex != 2 {
		t.Fatalf("unexpected qa citations: %+v", resp.QaResult.Citations)
	}
	if len(resp.QaResult.Segments) != 1 || resp.QaResult.Segments[0].End != 44 {
		t.Fatalf("unexpected qa segments: %+v", resp.QaResult.Segments)
	}
}

func TestGetAIJob_qaResultAbsentIsGraceful(t *testing.T) {
	f := newFakes()
	f.jobs.jobs = map[string]*v1alpha1.PolicyAIJob{
		"aijob-qa": erinJob("aijob-qa", v1alpha1.OperationQA, v1alpha1.PolicyAIJobStatus{
			Phase: v1alpha1.PhaseSucceeded, ResultRef: "ai:job-result:pol-1:ver-1:QA",
		}),
	}
	resp, err := f.server().GetAIJob(erinCtx(), &aiv1.GetAIJobRequest{JobId: "aijob-qa"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.QaResult != nil {
		t.Fatalf("expected nil qa_result when the result key is absent, got %+v", resp.QaResult)
	}
	if resp.Phase != aiv1.JobPhase_JOB_PHASE_SUCCEEDED {
		t.Fatal("expected the status read to still succeed")
	}
}

func TestGetAIJob_nonQAJobNeverHydrates(t *testing.T) {
	f := newFakes()
	f.jobs.jobs = map[string]*v1alpha1.PolicyAIJob{
		"aijob-draft": erinJob("aijob-draft", v1alpha1.OperationDraft, v1alpha1.PolicyAIJobStatus{
			Phase: v1alpha1.PhaseSucceeded, ResultRef: "ai:job-result:aijob-draft:DRAFT",
		}),
	}
	f.results.raw, f.results.found = []byte(`{}`), true
	resp, err := f.server().GetAIJob(erinCtx(), &aiv1.GetAIJobRequest{JobId: "aijob-draft"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.results.calls != 0 {
		t.Fatalf("expected the result reader NOT called for a non-QA job, got %d calls", f.results.calls)
	}
	if resp.QaResult != nil {
		t.Fatal("expected nil qa_result for a non-QA job")
	}
}

func TestGetAIJob_notFound(t *testing.T) {
	f := newFakes()
	f.jobs.jobs = map[string]*v1alpha1.PolicyAIJob{}
	_, err := f.server().GetAIJob(erinCtx(), &aiv1.GetAIJobRequest{JobId: "does-not-exist"})
	requireCode(t, err, errcodes.CodeJobNotFound, codes.NotFound)
}

func TestGetAIJob_requiresJobID(t *testing.T) {
	f := newFakes()
	_, err := f.server().GetAIJob(erinCtx(), &aiv1.GetAIJobRequest{})
	requireCode(t, err, errcodes.CodeRequestInvalid, codes.InvalidArgument)
}

// While the module is off every intake call is refused with AI_DISABLED and
// nothing is created.
func TestSubmitAIJob_disabled_refuses(t *testing.T) {
	f := newFakes()
	f.settings.st.Enabled = false
	_, err := f.server().SubmitAIJob(erinCtx(), &aiv1.SubmitAIJobRequest{Operation: aiv1.JobOperation_JOB_OPERATION_DRAFT})
	requireCode(t, err, errcodes.CodeDisabled, codes.FailedPrecondition)
	if len(f.jobs.created) != 0 {
		t.Fatalf("expected no job created while disabled, got %d", len(f.jobs.created))
	}
}

// Off is off: a scope that reads every category is refused like anyone
// else's; there is no admin bypass of the switch.
func TestSubmitAIJob_disabled_noBypassForAllCategories(t *testing.T) {
	f := newFakes()
	f.settings.st.Enabled = false
	_, err := f.server().SubmitAIJob(actAsCtx(), &aiv1.SubmitAIJobRequest{
		Operation: aiv1.JobOperation_JOB_OPERATION_DRAFT,
		Scope:     &aiv1.ReadScope{AllCategories: true, IncludeSensitive: true},
	})
	requireCode(t, err, errcodes.CodeDisabled, codes.FailedPrecondition)
	if len(f.jobs.created) != 0 {
		t.Fatal("expected no job created while disabled")
	}
}

// The model is fixed at submit from the llm client's default model.
func TestSubmitAIJob_snapshotsDefaultModel(t *testing.T) {
	f := newFakes()
	f.llm.model = "model-b"
	if _, err := f.server().SubmitAIJob(erinCtx(), &aiv1.SubmitAIJobRequest{Operation: aiv1.JobOperation_JOB_OPERATION_DRAFT}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.jobs.created) != 1 || f.jobs.created[0].ModelID != "model-b" {
		t.Fatalf("expected snapshotted modelId on the CR, got %+v", f.jobs.created)
	}
}

// An empty default model is snapshotted as empty: the adapter's own default.
func TestSubmitAIJob_emptyModelMeansAdapterDefault(t *testing.T) {
	f := newFakes()
	f.llm.model = ""
	if _, err := f.server().SubmitAIJob(erinCtx(), &aiv1.SubmitAIJobRequest{Operation: aiv1.JobOperation_JOB_OPERATION_DRAFT}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.jobs.created[0].ModelID != "" {
		t.Fatalf("expected an empty modelId, got %q", f.jobs.created[0].ModelID)
	}
}

func TestGetAIEnabled_reportsTheSwitch(t *testing.T) {
	f := newFakes()
	f.settings.st.Enabled = false
	resp, err := f.server().GetAIEnabled(context.Background(), &aiv1.GetAIEnabledRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Enabled {
		t.Fatal("expected enabled=false from the settings")
	}
}

func TestSetAIEnabled_persistsThroughStore(t *testing.T) {
	f := newFakes()
	svc := f.server()
	resp, err := svc.SetAIEnabled(erinCtx(), &aiv1.SetAIEnabledRequest{Enabled: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Enabled {
		t.Fatal("expected echoed enabled=false")
	}
	if f.settings.setEnabled == nil || *f.settings.setEnabled {
		t.Fatalf("expected SetEnabled(false) to reach the store, got %+v", f.settings.setEnabled)
	}
	getResp, err := svc.GetAIEnabled(erinCtx(), &aiv1.GetAIEnabledRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if getResp.Enabled {
		t.Fatal("expected GetAIEnabled to reflect the SetAIEnabled write")
	}
	if ops := f.audit.ops(); len(ops) != 1 || ops[0] != "set_ai_enabled" {
		t.Fatalf("expected a set_ai_enabled audit, got %v", ops)
	}
}

func TestGetAIConfig_reportsSettingsNeverTheCredential(t *testing.T) {
	f := newFakes()
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	f.settings.st.Provider = store.ProviderSettings{Provider: "azure_openai", Model: "model-c", BaseURL: "https://ai.example.org", Deployment: "steward"}
	f.settings.st.CredentialSet, f.settings.st.CredentialLast4 = true, "0001"
	f.settings.st.MonthlyLimit = 1000
	f.settings.st.OrgContext = "Example Organisation"
	f.settings.st.Notice = store.DataNotice{Version: airules.DataNoticeVersion, AcceptedBy: fixture.Alice, AcceptedAt: &at}

	resp, err := f.server().GetAIConfig(erinCtx(), &aiv1.GetAIConfigRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := resp.GetConfig()
	if !c.Enabled || c.Provider != aiv1.Provider_PROVIDER_AZURE_OPENAI || c.Model != "model-c" ||
		c.BaseUrl != "https://ai.example.org" || c.Deployment != "steward" {
		t.Fatalf("unexpected provider settings: %+v", c)
	}
	if !c.CredentialSet || c.CredentialLast4 != "0001" {
		t.Fatalf("expected credential set with last4, got %+v", c)
	}
	if c.EmbeddingsProvider != "tei" || c.EmbeddingsModel != "embed-small" {
		t.Fatalf("expected the deployment's embeddings, got %q/%q", c.EmbeddingsProvider, c.EmbeddingsModel)
	}
	if c.MonthlyLimit != 1000 || c.OrgContext != "Example Organisation" {
		t.Fatalf("unexpected limit or org context: %+v", c)
	}
	n := c.GetDataNotice()
	if n.CurrentVersion != airules.DataNoticeVersion || n.AcceptedVersion != airules.DataNoticeVersion ||
		n.AcceptedBy != fixture.Alice || n.AcceptedAt != "2026-10-05T09:00:00Z" {
		t.Fatalf("unexpected data notice: %+v", n)
	}
}

func TestGetAIConfig_modelPrefersStoredElseDefault(t *testing.T) {
	f := newFakes()
	f.settings.st.Provider.Model = "model-c"
	resp, err := f.server().GetAIConfig(erinCtx(), &aiv1.GetAIConfigRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetConfig().Model != "model-c" {
		t.Fatalf("expected the stored model, got %q", resp.GetConfig().Model)
	}

	f = newFakes()
	f.llm.model = "model-a"
	resp, err = f.server().GetAIConfig(erinCtx(), &aiv1.GetAIConfigRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetConfig().Model != "model-a" {
		t.Fatalf("expected the llm default model when none is stored, got %q", resp.GetConfig().Model)
	}
}

func TestGetAIConfig_topKReportsEffective(t *testing.T) {
	f := newFakes()
	resp, err := f.server().GetAIConfig(erinCtx(), &aiv1.GetAIConfigRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetConfig().TopK != 50 {
		t.Fatalf("expected default top_k=50 with no override, got %d", resp.GetConfig().TopK)
	}

	f.settings.st.TopK = 25
	resp, err = f.server().GetAIConfig(erinCtx(), &aiv1.GetAIConfigRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetConfig().TopK != 25 {
		t.Fatalf("expected the stored top_k override reported, got %d", resp.GetConfig().TopK)
	}
}

func TestSetAIRetrievalConfig_setClampClear(t *testing.T) {
	f := newFakes()
	f.settings.st.TopK = 50
	svc := f.server()

	resp, err := svc.SetAIRetrievalConfig(actAsCtx(), &aiv1.SetAIRetrievalConfigRequest{TopK: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.settings.setTopK == nil || *f.settings.setTopK != 30 {
		t.Fatalf("expected SetTopK(30) to reach the store, got %+v", f.settings.setTopK)
	}
	if resp.TopK != 30 {
		t.Fatalf("expected echoed top_k=30, got %d", resp.TopK)
	}
	if ops := f.audit.ops(); len(ops) != 1 || ops[0] != "set_ai_retrieval_config" {
		t.Fatalf("expected a set_ai_retrieval_config audit event, got %v", ops)
	}

	resp, err = svc.SetAIRetrievalConfig(actAsCtx(), &aiv1.SetAIRetrievalConfigRequest{TopK: 100000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *f.settings.setTopK != 200 || resp.TopK != 200 {
		t.Fatalf("expected top_k clamped to 200, got stored %d echoed %d", *f.settings.setTopK, resp.TopK)
	}

	resp, err = svc.SetAIRetrievalConfig(actAsCtx(), &aiv1.SetAIRetrievalConfigRequest{TopK: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *f.settings.setTopK != 0 || resp.TopK != 50 {
		t.Fatalf("expected the override cleared (stored 0, effective 50), got stored %d echoed %d", *f.settings.setTopK, resp.TopK)
	}
}

func TestSearchAndAnswer_passesEffectiveTopK(t *testing.T) {
	f := newFakes()
	if _, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{Question: "q", Scope: scope(fixture.Workplace)}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.retr.gotRequest.TopK != 50 {
		t.Fatalf("expected default top_k=50 threaded into retrieval, got %d", f.retr.gotRequest.TopK)
	}

	f = newFakes()
	f.settings.st.TopK = 25
	if _, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{Question: "q", Scope: scope(fixture.Workplace)}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.retr.gotRequest.TopK != 25 {
		t.Fatalf("expected top_k override 25 threaded into retrieval, got %d", f.retr.gotRequest.TopK)
	}
}

func TestGetProviderStatus_reflectsSource(t *testing.T) {
	f := newFakes()
	f.llm.available, f.llm.reason = false, "auth_failed"
	resp, err := f.server().GetProviderStatus(context.Background(), &aiv1.GetProviderStatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Available || resp.Reason != "auth_failed" {
		t.Fatalf("expected unavailable/auth_failed, got %+v", resp)
	}
}

// errSummaryNotFound is the shape store.SummaryStore.GetSummary returns for
// a missing row.
var errSummaryNotFound = fmt.Errorf("store: get summary: %w", pgx.ErrNoRows)

func TestGetPolicySummary_found(t *testing.T) {
	generatedAt := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	f := newFakes()
	f.sums.sum = store.Summary{VersionID: "ver-1", SummaryText: "- Employees must comply.", GeneratedAt: generatedAt}
	resp, err := f.server().GetPolicySummary(erinCtx(), &aiv1.GetPolicySummaryRequest{VersionId: "ver-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Found || resp.SummaryText != "- Employees must comply." || resp.GeneratedAt != generatedAt.Format(time.RFC3339) {
		t.Fatalf("unexpected summary: %+v", resp)
	}
}

func TestGetPolicySummary_notFound(t *testing.T) {
	f := newFakes()
	f.sums.err = errSummaryNotFound
	resp, err := f.server().GetPolicySummary(erinCtx(), &aiv1.GetPolicySummaryRequest{VersionId: "does-not-exist"})
	if err != nil {
		t.Fatalf("expected nil error for a not-found summary, got: %v", err)
	}
	if resp.Found || resp.SummaryText != "" || resp.GeneratedAt != "" {
		t.Fatalf("expected an empty not-found response, got %+v", resp)
	}
}

func TestGetPolicySummary_storeErrorPropagates(t *testing.T) {
	f := newFakes()
	f.sums.err = errBoom
	_, err := f.server().GetPolicySummary(erinCtx(), &aiv1.GetPolicySummaryRequest{VersionId: "ver-1"})
	if got := status.Convert(err).Code(); got != codes.Internal {
		t.Fatalf("expected Internal, got %v", got)
	}
}

func TestGetPolicySummary_requiresVersionID(t *testing.T) {
	f := newFakes()
	_, err := f.server().GetPolicySummary(erinCtx(), &aiv1.GetPolicySummaryRequest{})
	requireCode(t, err, errcodes.CodeRequestInvalid, codes.InvalidArgument)
}

func TestGetTopQuestions_returnsStoreResults(t *testing.T) {
	f := newFakes()
	f.topQ.questions = []string{"how do I book a desk?", "how do I claim expenses?"}
	resp, err := f.server().GetTopQuestions(erinCtx(), &aiv1.GetTopQuestionsRequest{
		Limit: 2, Scope: &aiv1.ReadScope{CategoryIds: []string{fixture.Facilities}, IncludeSensitive: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Questions) != 2 || resp.Questions[0] != "how do I book a desk?" {
		t.Fatalf("unexpected questions: %v", resp.Questions)
	}
	if f.topQ.gotLimit != 2 {
		t.Fatalf("expected limit 2 threaded through, got %d", f.topQ.gotLimit)
	}
	if fl := f.topQ.gotFilter; len(fl.CategoryIDs) != 1 || fl.CategoryIDs[0] != fixture.Facilities || !fl.IncludeSensitive || fl.AllCategories {
		t.Fatalf("expected the read scope as the access filter, got %+v", fl)
	}
}

func TestGetTopQuestions_storeErrorPropagates(t *testing.T) {
	f := newFakes()
	f.topQ.err = errBoom
	_, err := f.server().GetTopQuestions(erinCtx(), &aiv1.GetTopQuestionsRequest{})
	if got := status.Convert(err).Code(); got != codes.Internal {
		t.Fatalf("expected Internal, got %v", got)
	}
}

func TestGetRelatedPolicies_returnsMappedNeighbors(t *testing.T) {
	f := newFakes()
	f.related.neighbors = []store.RelatedPolicy{
		{PolicyID: "p2", PolicyTitle: fixture.DeskBookingPolicy, CategoryID: fixture.Facilities, VersionNo: 4, Distance: 0.12},
		{PolicyID: "p3", PolicyTitle: fixture.ExpenseClaimsPolicy, CategoryID: fixture.Expenses, VersionNo: 2, Distance: 0.31},
	}
	resp, err := f.server().GetRelatedPolicies(erinCtx(), &aiv1.GetRelatedPoliciesRequest{
		PolicyId: "p1", TopN: 5, Scope: &aiv1.ReadScope{CategoryIds: []string{fixture.Facilities}, IncludeSensitive: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Related) != 2 {
		t.Fatalf("expected 2 related, got %d", len(resp.Related))
	}
	first := resp.Related[0]
	if first.PolicyId != "p2" || first.PolicyTitle != fixture.DeskBookingPolicy || first.CategoryId != fixture.Facilities ||
		first.VersionNo != 4 || first.Distance != 0.12 {
		t.Fatalf("unexpected first neighbor mapping: %+v", first)
	}
}

func TestGetRelatedPolicies_passesAccessScopeThrough(t *testing.T) {
	f := newFakes()
	_, err := f.server().GetRelatedPolicies(erinCtx(), &aiv1.GetRelatedPoliciesRequest{
		PolicyId: "p1",
		TopN:     7,
		Scope:    &aiv1.ReadScope{CategoryIds: []string{fixture.Workplace, fixture.Finance}, IncludeSensitive: true, AllCategories: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.related.gotPolicy != "p1" || f.related.gotTopN != 7 {
		t.Fatalf("policy/topN: got %q/%d", f.related.gotPolicy, f.related.gotTopN)
	}
	fl := f.related.gotFilter
	if !fl.AllCategories || !fl.IncludeSensitive || len(fl.CategoryIDs) != 2 || fl.CategoryIDs[0] != fixture.Workplace {
		t.Fatalf("expected the read scope threaded, got %+v", fl)
	}
}

func TestGetRelatedPolicies_requiresPolicyID(t *testing.T) {
	f := newFakes()
	_, err := f.server().GetRelatedPolicies(erinCtx(), &aiv1.GetRelatedPoliciesRequest{})
	requireCode(t, err, errcodes.CodeRequestInvalid, codes.InvalidArgument)
}

func TestGetRelatedPolicies_storeErrorPropagates(t *testing.T) {
	f := newFakes()
	f.related.err = errBoom
	_, err := f.server().GetRelatedPolicies(erinCtx(), &aiv1.GetRelatedPoliciesRequest{PolicyId: "p1"})
	if got := status.Convert(err).Code(); got != codes.Internal {
		t.Fatalf("expected Internal, got %v", got)
	}
}
