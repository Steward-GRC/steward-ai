// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/operator"
	"github.com/Steward-GRC/steward-ai/internal/reeval"
	"github.com/Steward-GRC/steward-ai/internal/relationship"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

func newTestDispatcher(llm *stubLLM) *operator.Dispatcher {
	return operator.NewDispatcher(
		generation.NewDraft(llm, generation.DefaultDraftSectionMaxTokens),
		generation.NewReview(llm),
		generation.NewRevise(llm, generation.DefaultReviseMaxTokens),
		&stubRetriever{},
		generation.NewQA(llm),
	)
}

func TestDispatch_draftReusesGeneration(t *testing.T) {
	d := newTestDispatcher(&stubLLM{response: draftLLMResponse})

	raw, _ := json.Marshal(generation.DraftJobInput{
		Brief:    "draft it",
		Sections: []generation.DraftJobInputSection{{Key: "purpose", Title: "Purpose", Order: 1}},
	})
	job := &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationDraft,
		Input:     runtime.RawExtension{Raw: raw},
	}}

	got, err := d.Dispatch(context.Background(), job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, ok := got.(generation.DraftResponse)
	if !ok {
		t.Fatalf("expected DraftResponse, got %T", got)
	}
	if len(resp.Sections) != 1 || resp.Sections[0].SectionKey != "purpose" {
		t.Fatalf("unexpected draft response: %+v", resp)
	}
}

func TestDispatch_reviewReusesGeneration(t *testing.T) {
	d := newTestDispatcher(&stubLLM{response: reviewLLMResponse})

	raw, _ := json.Marshal(generation.ReviewJobInput{
		Sections: []generation.ReviewJobInputSection{{Key: "purpose", Title: "Purpose", Content: "Body."}},
	})
	job := &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationReview,
		PolicyID:  "pol-1",
		VersionID: "ver-1",
		Input:     runtime.RawExtension{Raw: raw},
	}}

	got, err := d.Dispatch(context.Background(), job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, ok := got.(generation.ReviewResponse)
	if !ok {
		t.Fatalf("expected ReviewResponse, got %T", got)
	}
	if len(resp.Findings) != 1 || resp.Findings[0].SectionKey != "purpose" {
		t.Fatalf("unexpected review response: %+v", resp)
	}
}

func TestDispatch_reviseReusesGeneration(t *testing.T) {
	d := newTestDispatcher(&stubLLM{response: reviseLLMResponse})

	raw, _ := json.Marshal(generation.ReviseJobInput{
		Instruction: "add a data retention clause",
		PolicyID:    "pol-1",
		VersionID:   "ver-1",
		Sections: []generation.ReviseJobInputSection{
			{Key: "purpose", Title: "Purpose", Content: "Original body.", Order: 1},
			{Key: "scope", Title: "Scope", Content: "Original scope.", Order: 2},
		},
	})
	job := &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationRevise,
		PolicyID:  "pol-1",
		VersionID: "ver-1",
		Input:     runtime.RawExtension{Raw: raw},
	}}

	got, err := d.Dispatch(context.Background(), job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, ok := got.(generation.ReviseResponse)
	if !ok {
		t.Fatalf("expected ReviseResponse, got %T", got)
	}
	if len(resp.Sections) != 2 {
		t.Fatalf("expected both input sections represented, got %+v", resp.Sections)
	}
	if !resp.Sections[0].Changed || resp.Sections[0].SectionKey != "purpose" || resp.Sections[0].Content == "" {
		t.Fatalf("expected purpose section changed with content, got %+v", resp.Sections[0])
	}
	if resp.Sections[1].Changed || resp.Sections[1].SectionKey != "scope" || resp.Sections[1].Content != "" {
		t.Fatalf("expected scope section unchanged with no content, got %+v", resp.Sections[1])
	}
	if len(resp.NewSections) != 1 || resp.NewSections[0].Title != "Definitions" || resp.NewSections[0].AfterKey != "purpose" {
		t.Fatalf("expected one new section after purpose, got %+v", resp.NewSections)
	}
}

func TestDispatch_unimplementedOperations(t *testing.T) {
	d := newTestDispatcher(&stubLLM{response: draftLLMResponse})
	for _, op := range []v1alpha1.JobOperation{
		v1alpha1.OperationRewrite, v1alpha1.OperationClarify,
		v1alpha1.OperationSummarize,
	} {
		_, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{Operation: op}})
		if !errors.Is(err, operator.ErrOperationNotImplemented) {
			t.Fatalf("op %q: expected ErrOperationNotImplemented, got %v", op, err)
		}
	}
}

// TestDispatch_qaRetrievesThenGenerates verifies the QA operation retrieves
// under the job's read scope (ReadCategoryIDs, IncludeSensitive and
// AllCategories threaded into the retrieval request) and then generates a
// grounded AnswerResponse.
func TestDispatch_qaRetrievesThenGenerates(t *testing.T) {
	retr := &stubRetriever{results: []store.SearchResult{
		{Chunk: store.Chunk{ID: "chunk-1", PolicyID: "pol-1", VersionID: "ver-1", VersionNo: 1, SectionKey: "scope", ChunkIndex: 0, PolicyTitle: "P", ContentText: "Employees may travel up to 5000 km.", CategoryID: "c", Sensitivity: "standard"}},
	}}
	d := operator.NewDispatcher(
		generation.NewDraft(&stubLLM{}, generation.DefaultDraftSectionMaxTokens),
		generation.NewReview(&stubLLM{}),
		generation.NewRevise(&stubLLM{}, generation.DefaultReviseMaxTokens),
		retr,
		generation.NewQA(&stubLLM{response: "Employees may travel up to 5000 km per year."}),
	)

	raw, _ := json.Marshal(generation.QAJobInput{Question: "how much travel?"})
	job := &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{
		Operation:        v1alpha1.OperationQA,
		PolicyID:         "pol-1",
		VersionID:        "ver-1",
		ReadCategoryIDs:  []string{"category-hr"},
		IncludeSensitive: true,
		AllCategories:    true,
		Input:            runtime.RawExtension{Raw: raw},
	}}

	got, err := d.Dispatch(context.Background(), job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, ok := got.(generation.AnswerResponse)
	if !ok {
		t.Fatalf("expected AnswerResponse, got %T", got)
	}
	if resp.Answer == "" || len(resp.Citations) != 1 {
		t.Fatalf("expected a grounded answer with 1 citation, got %+v", resp)
	}

	calls, req := retr.snapshot()
	if calls != 1 {
		t.Fatalf("expected exactly 1 retrieval call, got %d", calls)
	}
	if req.Question != "how much travel?" {
		t.Fatalf("expected the question threaded into retrieval, got %q", req.Question)
	}
	if !req.AllCategories {
		t.Fatal("expected the job's AllCategories threaded into retrieval")
	}
	if len(req.CategoryIDs) != 1 || req.CategoryIDs[0] != "category-hr" {
		t.Fatalf("expected the job's ReadCategoryIDs threaded into retrieval, got %v", req.CategoryIDs)
	}
	if !req.IncludeSensitive {
		t.Fatal("expected the job's IncludeSensitive threaded into retrieval")
	}
}

// TestDispatch_qaNotWiredReportsUnimplemented verifies a dispatcher built
// without a retriever/qa engine (e.g. the operator running DB-less) reports
// QA as not-implemented rather than panicking.
func TestDispatch_qaNotWiredReportsUnimplemented(t *testing.T) {
	d := operator.NewDispatcher(
		generation.NewDraft(&stubLLM{}, generation.DefaultDraftSectionMaxTokens),
		generation.NewReview(&stubLLM{}),
		generation.NewRevise(&stubLLM{}, generation.DefaultReviseMaxTokens),
		nil, nil,
	)
	_, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationQA}})
	if !errors.Is(err, operator.ErrOperationNotImplemented) {
		t.Fatalf("expected ErrOperationNotImplemented when QA is unwired, got %v", err)
	}
}

// stubReevalEngine is a fake relatedReevalEngine for the RELATED_REEVAL
// dispatch tests.
type stubReevalEngine struct {
	res    reeval.Result
	err    error
	called bool
}

func (s *stubReevalEngine) Reeval(context.Context) (reeval.Result, error) {
	s.called = true
	return s.res, s.err
}

// TestDispatch_relatedReevalRunsEngine verifies the RELATED_REEVAL op dispatches
// to the wired engine and returns its diff result (which the reconciler writes
// to the result tiers / completion event).
func TestDispatch_relatedReevalRunsEngine(t *testing.T) {
	eng := &stubReevalEngine{res: reeval.Result{
		Changed:    []string{"p1"},
		Recomputed: 2,
		Diffs:      []reeval.PolicyDiff{{PolicyID: "p1", NewlySuggested: []string{"p2"}}},
	}}
	d := newTestDispatcher(&stubLLM{}).WithReeval(eng)

	got, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{
		Spec: v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationRelatedReeval},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !eng.called {
		t.Fatal("expected reeval engine to be invoked")
	}
	res, ok := got.(reeval.Result)
	if !ok {
		t.Fatalf("expected reeval.Result, got %T", got)
	}
	if res.Recomputed != 2 || len(res.Diffs) != 1 || res.Diffs[0].PolicyID != "p1" {
		t.Fatalf("unexpected reeval result: %+v", res)
	}
}

// TestDispatch_relatedReevalNotWiredReportsUnimplemented: without an engine
// (DB/Redis-less operator) RELATED_REEVAL reports not-implemented, mirroring QA.
func TestDispatch_relatedReevalNotWiredReportsUnimplemented(t *testing.T) {
	d := newTestDispatcher(&stubLLM{}) // no WithReeval
	_, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{
		Spec: v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationRelatedReeval},
	})
	if !errors.Is(err, operator.ErrOperationNotImplemented) {
		t.Fatalf("expected ErrOperationNotImplemented when reeval is unwired, got %v", err)
	}
}

// TestDispatch_relatedReevalEngineErrorPropagates: an engine error surfaces so
// the CRD retry framework can retry the job.
func TestDispatch_relatedReevalEngineErrorPropagates(t *testing.T) {
	d := newTestDispatcher(&stubLLM{}).WithReeval(&stubReevalEngine{err: errors.New("redis down")})
	_, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{
		Spec: v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationRelatedReeval},
	})
	if err == nil {
		t.Fatal("expected engine error to propagate")
	}
}

// stubSuggester is a fake suggestEngine for the SUGGEST_ENRICHMENTS + folding
// dispatch tests.
type stubSuggester struct {
	out     generation.EnrichmentSuggestions
	err     error
	called  bool
	lastReq generation.SuggestRequest
}

func (s *stubSuggester) Suggest(_ context.Context, req generation.SuggestRequest) (generation.EnrichmentSuggestions, error) {
	s.called = true
	s.lastReq = req
	return s.out, s.err
}

// TestDispatch_suggestEnrichmentsRunsEngine: the standalone SUGGEST_ENRICHMENTS
// op dispatches to the wired engine and returns its suggestions block.
func TestDispatch_suggestEnrichmentsRunsEngine(t *testing.T) {
	eng := &stubSuggester{out: generation.EnrichmentSuggestions{
		Related: []generation.RelatedEnrichmentSuggestion{{PolicyID: "p2", Title: "T"}},
	}}
	d := newTestDispatcher(&stubLLM{}).WithSuggest(eng)

	raw, _ := json.Marshal(generation.SuggestJobInput{
		Sections: []generation.SuggestJobInputSection{{Key: "s1", Title: "S1", Content: "body"}},
	})
	got, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationSuggestEnrichments,
		PolicyID:  "p1",
		Input:     runtime.RawExtension{Raw: raw},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !eng.called || eng.lastReq.PolicyID != "p1" {
		t.Fatalf("expected suggester invoked with policy p1, got called=%v policy=%q", eng.called, eng.lastReq.PolicyID)
	}
	res, ok := got.(generation.SuggestEnrichmentsResult)
	if !ok {
		t.Fatalf("expected SuggestEnrichmentsResult, got %T", got)
	}
	if len(res.Suggestions.Related) != 1 || res.Suggestions.Related[0].PolicyID != "p2" {
		t.Fatalf("unexpected suggestions: %+v", res.Suggestions)
	}
}

// TestDispatch_suggestNotWiredReportsUnimplemented mirrors QA/RELATED_REEVAL.
func TestDispatch_suggestNotWiredReportsUnimplemented(t *testing.T) {
	d := newTestDispatcher(&stubLLM{}) // no WithSuggest
	_, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{
		Spec: v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationSuggestEnrichments},
	})
	if !errors.Is(err, operator.ErrOperationNotImplemented) {
		t.Fatalf("expected ErrOperationNotImplemented when suggest is unwired, got %v", err)
	}
}

// TestDispatch_draftFoldsSuggestions: a DRAFT result carries the folded-in
// suggestions block when the suggester is wired.
func TestDispatch_draftFoldsSuggestions(t *testing.T) {
	eng := &stubSuggester{out: generation.EnrichmentSuggestions{
		Definitions: []generation.DefinitionSuggestion{{Term: "PHI", Action: generation.SuggestionActionCreateNew}},
	}}
	d := newTestDispatcher(&stubLLM{response: draftLLMResponse}).WithSuggest(eng)

	raw, _ := json.Marshal(generation.DraftJobInput{
		Brief:    "draft it",
		Sections: []generation.DraftJobInputSection{{Key: "purpose", Title: "Purpose", Order: 1}},
	})
	got, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationDraft,
		Input:     runtime.RawExtension{Raw: raw},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp := got.(generation.DraftResponse)
	if resp.Suggestions == nil || len(resp.Suggestions.Definitions) != 1 {
		t.Fatalf("expected folded-in suggestions on draft, got %+v", resp.Suggestions)
	}
	// The suggester saw the GENERATED section content.
	if len(eng.lastReq.Sections) == 0 || eng.lastReq.Sections[0].Key != "purpose" {
		t.Fatalf("suggester did not receive generated sections: %+v", eng.lastReq.Sections)
	}
}

// TestDispatch_draftSuggestBestEffort: a suggest failure never fails the draft;
// the primary result is returned with no suggestions block.
func TestDispatch_draftSuggestBestEffort(t *testing.T) {
	eng := &stubSuggester{err: errors.New("boom")}
	d := newTestDispatcher(&stubLLM{response: draftLLMResponse}).WithSuggest(eng)

	raw, _ := json.Marshal(generation.DraftJobInput{
		Brief:    "draft it",
		Sections: []generation.DraftJobInputSection{{Key: "purpose", Title: "Purpose", Order: 1}},
	})
	got, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationDraft,
		Input:     runtime.RawExtension{Raw: raw},
	}})
	if err != nil {
		t.Fatalf("draft must not fail on suggest error: %v", err)
	}
	resp := got.(generation.DraftResponse)
	if len(resp.Sections) != 1 {
		t.Fatalf("expected draft sections intact, got %+v", resp.Sections)
	}
	if resp.Suggestions != nil {
		t.Fatalf("expected no suggestions block on suggest failure, got %+v", resp.Suggestions)
	}
}

// TestDispatch_suggestErrorPropagatesStandalone: for the STANDALONE op a suggest
// error surfaces so the CRD retry framework can retry.
func TestDispatch_suggestErrorPropagatesStandalone(t *testing.T) {
	d := newTestDispatcher(&stubLLM{}).WithSuggest(&stubSuggester{err: errors.New("boom")})
	_, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{
		Spec: v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationSuggestEnrichments},
	})
	if err == nil {
		t.Fatal("expected standalone suggest error to propagate")
	}
}

// stubRelationshipLearnEngine is a fake relationshipLearnEngine for the
// RELATIONSHIP_LEARN dispatch tests.
type stubRelationshipLearnEngine struct {
	res    relationship.Result
	err    error
	called bool
}

func (s *stubRelationshipLearnEngine) Learn(context.Context) (relationship.Result, error) {
	s.called = true
	return s.res, s.err
}

// TestDispatch_relationshipLearnRunsEngine verifies the RELATIONSHIP_LEARN op
// dispatches to the wired engine and returns its metrics summary.
func TestDispatch_relationshipLearnRunsEngine(t *testing.T) {
	eng := &stubRelationshipLearnEngine{res: relationship.Result{EdgeCount: 42, ScoreMax: 0.9}}
	d := newTestDispatcher(&stubLLM{}).WithRelationshipLearn(eng)

	got, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{
		Spec: v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationRelationshipLearn},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !eng.called {
		t.Fatal("expected relationship-learn engine to be invoked")
	}
	res, ok := got.(relationship.Result)
	if !ok {
		t.Fatalf("expected relationship.Result, got %T", got)
	}
	if res.EdgeCount != 42 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// TestDispatch_relationshipLearnNotWiredReportsUnimplemented: without an engine
// (DB-less operator) RELATIONSHIP_LEARN reports not-implemented, mirroring QA.
func TestDispatch_relationshipLearnNotWiredReportsUnimplemented(t *testing.T) {
	d := newTestDispatcher(&stubLLM{}) // no WithRelationshipLearn
	_, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{
		Spec: v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationRelationshipLearn},
	})
	if !errors.Is(err, operator.ErrOperationNotImplemented) {
		t.Fatalf("expected ErrOperationNotImplemented when relationship-learn is unwired, got %v", err)
	}
}

// TestDispatch_relationshipLearnEngineErrorPropagates: an engine error surfaces
// so the CRD retry framework can retry the job.
func TestDispatch_relationshipLearnEngineErrorPropagates(t *testing.T) {
	d := newTestDispatcher(&stubLLM{}).WithRelationshipLearn(&stubRelationshipLearnEngine{err: errors.New("pg down")})
	_, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{
		Spec: v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationRelationshipLearn},
	})
	if err == nil {
		t.Fatal("expected engine error to propagate")
	}
}

func TestResultKey_scheme(t *testing.T) {
	// With policyId+versionId the key is version-keyed.
	withIDs := &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationRewrite, PolicyID: "pol-1", VersionID: "ver-2",
	}}
	if got := operator.ResultKey(withIDs); got != "ai:job-result:pol-1:ver-2:REWRITE" {
		t.Fatalf("version-keyed scheme: got %q", got)
	}

	// Without ids (e.g. DRAFT ahead of policy creation) → CR-name fallback.
	noIDs := &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationDraft}}
	noIDs.Name = "aijob-abc"
	got := operator.ResultKey(noIDs)
	if !strings.HasPrefix(got, "ai:job-result:aijob-abc:DRAFT") {
		t.Fatalf("name-fallback scheme: got %q", got)
	}
}

// TestDispatch_suggestFoldUsesTheJobScope: the folded suggestions read under
// the job's captured scope.
func TestDispatch_suggestFoldUsesTheJobScope(t *testing.T) {
	eng := &stubSuggester{}
	d := newTestDispatcher(&stubLLM{response: reviewLLMResponse}).WithSuggest(eng)

	raw, _ := json.Marshal(generation.ReviewJobInput{
		Sections: []generation.ReviewJobInputSection{{Key: "purpose", Title: "Purpose", Content: "Body."}},
	})
	_, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{
		Operation:        v1alpha1.OperationReview,
		PolicyID:         "pol-1",
		ReadCategoryIDs:  []string{"category-hr"},
		IncludeSensitive: true,
		Input:            runtime.RawExtension{Raw: raw},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := eng.lastReq.Access
	if len(got.CategoryIDs) != 1 || got.CategoryIDs[0] != "category-hr" || !got.IncludeSensitive || got.AllCategories {
		t.Fatalf("expected the job's scope on the suggest request, got %+v", got)
	}
}

func TestDispatch_needsModel(t *testing.T) {
	d := newTestDispatcher(&stubLLM{})
	relatedOnly, _ := json.Marshal(generation.SuggestJobInput{OptOut: generation.EnrichmentOptOut{Definitions: true, References: true}})
	withDefinitions, _ := json.Marshal(generation.SuggestJobInput{OptOut: generation.EnrichmentOptOut{References: true}})
	cases := []struct {
		name string
		op   v1alpha1.JobOperation
		raw  []byte
		want bool
	}{
		{"draft", v1alpha1.OperationDraft, nil, true},
		{"review", v1alpha1.OperationReview, nil, true},
		{"revise", v1alpha1.OperationRevise, nil, true},
		{"qa", v1alpha1.OperationQA, nil, true},
		{"summarize", v1alpha1.OperationSummarize, nil, true},
		{"related reeval", v1alpha1.OperationRelatedReeval, nil, false},
		{"relationship learn", v1alpha1.OperationRelationshipLearn, nil, false},
		{"suggest, every category", v1alpha1.OperationSuggestEnrichments, nil, true},
		{"suggest, definitions wanted", v1alpha1.OperationSuggestEnrichments, withDefinitions, true},
		{"suggest, related only", v1alpha1.OperationSuggestEnrichments, relatedOnly, false},
		{"suggest, unreadable input", v1alpha1.OperationSuggestEnrichments, []byte("{"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{Operation: tc.op, Input: runtime.RawExtension{Raw: tc.raw}}}
			if got := d.NeedsModel(job); got != tc.want {
				t.Fatalf("NeedsModel(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
