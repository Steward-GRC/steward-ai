// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	log "github.com/Bugs5382/go-log"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/reeval"
	"github.com/Steward-GRC/steward-ai/internal/relationship"
	"github.com/Steward-GRC/steward-ai/internal/retrieval"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// ErrOperationNotImplemented is returned for an operation with nothing wired
// to run it: REWRITE, CLARIFY and SUMMARIZE have no job engine, and the
// others report it when their engine was not attached.
var ErrOperationNotImplemented = errors.New("operator: operation not implemented")

type draftEngine interface {
	Generate(ctx context.Context, req generation.DraftRequest) (generation.DraftResponse, error)
}

type reviewEngine interface {
	Generate(ctx context.Context, req generation.ReviewRequest) (generation.ReviewResponse, error)
}

type reviseEngine interface {
	Generate(ctx context.Context, req generation.ReviseRequest) (generation.ReviseResponse, error)
}

type qaRetriever interface {
	Retrieve(ctx context.Context, req retrieval.Request) ([]store.SearchResult, error)
}

type qaGenerator interface {
	Answer(ctx context.Context, req generation.AnswerRequest) (generation.AnswerResponse, error)
}

type relatedReevalEngine interface {
	Reeval(ctx context.Context) (reeval.Result, error)
}

type suggestEngine interface {
	Suggest(ctx context.Context, req generation.SuggestRequest) (generation.EnrichmentSuggestions, error)
}

type relationshipLearnEngine interface {
	Learn(ctx context.Context) (relationship.Result, error)
}

// Dispatcher runs the engine for a job's operation. A new operation is one
// more case here plus its engine and input type; the reconciler and the
// resource don't change.
type Dispatcher struct {
	draft     draftEngine
	review    reviewEngine
	revise    reviseEngine
	retriever qaRetriever
	qa        qaGenerator
	reeval    relatedReevalEngine
	suggest   suggestEngine
	relearn   relationshipLearnEngine
	log       log.Logger
}

// NewDispatcher returns a Dispatcher. A nil retriever or qa leaves QA
// reporting ErrOperationNotImplemented.
func NewDispatcher(draft draftEngine, review reviewEngine, revise reviseEngine, retriever qaRetriever, qa qaGenerator) *Dispatcher {
	return &Dispatcher{draft: draft, review: review, revise: revise, retriever: retriever, qa: qa, log: log.NewLogger("steward-ai-operator")}
}

// WithReeval attaches the RELATED_REEVAL engine.
func (d *Dispatcher) WithReeval(e relatedReevalEngine) *Dispatcher {
	d.reeval = e
	return d
}

// WithSuggest attaches the enrichment engine, which runs SUGGEST_ENRICHMENTS
// and adds suggestions to DRAFT, REVIEW and REVISE results. Without it those
// results carry none.
func (d *Dispatcher) WithSuggest(e suggestEngine) *Dispatcher {
	d.suggest = e
	return d
}

// WithRelationshipLearn attaches the RELATIONSHIP_LEARN engine.
func (d *Dispatcher) WithRelationshipLearn(e relationshipLearnEngine) *Dispatcher {
	d.relearn = e
	return d
}

// WithLogger sets the logger.
func (d *Dispatcher) WithLogger(l log.Logger) *Dispatcher {
	d.log = l
	return d
}

// NeedsModel reports whether running job may call a model. RELATED_REEVAL,
// RELATIONSHIP_LEARN and a SUGGEST_ENRICHMENTS job asking for related
// policies only run on stored vectors; everything else is generative.
func (d *Dispatcher) NeedsModel(job *v1alpha1.PolicyAIJob) bool {
	switch job.Spec.Operation {
	case v1alpha1.OperationRelatedReeval, v1alpha1.OperationRelationshipLearn:
		return false
	case v1alpha1.OperationSuggestEnrichments:
		in, err := decodeInput[generation.SuggestJobInput](job)
		if err != nil {
			return true
		}
		return !in.OptOut.Definitions || !in.OptOut.References
	default:
		return true
	}
}

// Dispatch runs job's operation and returns its JSON-serialisable result.
// Everything about the caller comes from the job spec, captured at
// submission.
func (d *Dispatcher) Dispatch(ctx context.Context, job *v1alpha1.PolicyAIJob) (any, error) {
	switch job.Spec.Operation {
	case v1alpha1.OperationDraft:
		return d.dispatchDraft(ctx, job)
	case v1alpha1.OperationReview:
		return d.dispatchReview(ctx, job)
	case v1alpha1.OperationRevise:
		return d.dispatchRevise(ctx, job)
	case v1alpha1.OperationQA:
		return d.dispatchQA(ctx, job)
	case v1alpha1.OperationRelatedReeval:
		return d.dispatchRelatedReeval(ctx, job)
	case v1alpha1.OperationSuggestEnrichments:
		return d.dispatchSuggestEnrichments(ctx, job)
	case v1alpha1.OperationRelationshipLearn:
		return d.dispatchRelationshipLearn(ctx, job)
	case v1alpha1.OperationRewrite, v1alpha1.OperationClarify, v1alpha1.OperationSummarize:
		return nil, fmt.Errorf("%w: %s", ErrOperationNotImplemented, job.Spec.Operation)
	default:
		return nil, fmt.Errorf("operator: unknown operation %q", job.Spec.Operation)
	}
}

func decodeInput[T any](job *v1alpha1.PolicyAIJob) (T, error) {
	var in T
	if len(job.Spec.Input.Raw) == 0 {
		return in, nil
	}
	if err := json.Unmarshal(job.Spec.Input.Raw, &in); err != nil {
		return in, fmt.Errorf("operator: decode %s input: %w", job.Spec.Operation, err)
	}
	return in, nil
}

func access(job *v1alpha1.PolicyAIJob) store.AccessFilter {
	return store.AccessFilter{
		CategoryIDs:      job.Spec.ReadCategoryIDs,
		IncludeSensitive: job.Spec.IncludeSensitive,
		AllCategories:    job.Spec.AllCategories,
	}
}

func (d *Dispatcher) dispatchDraft(ctx context.Context, job *v1alpha1.PolicyAIJob) (any, error) {
	in, err := decodeInput[generation.DraftJobInput](job)
	if err != nil {
		return nil, err
	}
	resp, err := d.draft.Generate(ctx, in.ToDraftRequest(job.Spec.ActorUserID, job.Spec.CategoryID, job.Spec.ModelID))
	if err != nil {
		return nil, fmt.Errorf("operator: draft generate: %w", err)
	}
	// A new draft has no policy id, so it gets no related suggestions;
	// definitions and references still work.
	titleByKey := make(map[string]string, len(in.Sections))
	for _, s := range in.Sections {
		titleByKey[s.Key] = s.Title
	}
	sections := make([]generation.SuggestSection, 0, len(resp.Sections))
	for _, s := range resp.Sections {
		sections = append(sections, generation.SuggestSection{Key: s.SectionKey, Title: titleByKey[s.SectionKey], Content: s.Content})
	}
	resp.Suggestions = d.foldSuggestions(ctx, job, in.Title, sections, in.EnrichmentOptOut, in.EnrichmentLibrary)
	return resp, nil
}

func (d *Dispatcher) dispatchReview(ctx context.Context, job *v1alpha1.PolicyAIJob) (any, error) {
	in, err := decodeInput[generation.ReviewJobInput](job)
	if err != nil {
		return nil, err
	}
	resp, err := d.review.Generate(ctx, in.ToReviewRequest(job.Spec.ActorUserID, job.Spec.CategoryID, job.Spec.ModelID))
	if err != nil {
		return nil, fmt.Errorf("operator: review generate: %w", err)
	}
	sections := make([]generation.SuggestSection, 0, len(in.Sections))
	for _, s := range in.Sections {
		sections = append(sections, generation.SuggestSection(s))
	}
	resp.Suggestions = d.foldSuggestions(ctx, job, in.Title, sections, in.EnrichmentOptOut, in.EnrichmentLibrary)
	return resp, nil
}

func (d *Dispatcher) dispatchRevise(ctx context.Context, job *v1alpha1.PolicyAIJob) (any, error) {
	in, err := decodeInput[generation.ReviseJobInput](job)
	if err != nil {
		return nil, err
	}
	resp, err := d.revise.Generate(ctx, in.ToReviseRequest(job.Spec.ActorUserID, job.Spec.CategoryID, job.Spec.ModelID))
	if err != nil {
		return nil, fmt.Errorf("operator: revise generate: %w", err)
	}
	// Suggestions reason over the whole current policy, not only the
	// sections the revision changed.
	sections := make([]generation.SuggestSection, 0, len(in.Sections))
	for _, s := range in.Sections {
		sections = append(sections, generation.SuggestSection{Key: s.Key, Title: s.Title, Content: s.Content})
	}
	resp.Suggestions = d.foldSuggestions(ctx, job, "", sections, in.EnrichmentOptOut, in.EnrichmentLibrary)
	return resp, nil
}

// dispatchQA retrieves under the submitter's read scope, captured on the job,
// then answers from what it found.
func (d *Dispatcher) dispatchQA(ctx context.Context, job *v1alpha1.PolicyAIJob) (any, error) {
	if d.retriever == nil || d.qa == nil {
		return nil, fmt.Errorf("%w: %s (retriever or qa engine not wired)", ErrOperationNotImplemented, job.Spec.Operation)
	}
	in, err := decodeInput[generation.QAJobInput](job)
	if err != nil {
		return nil, err
	}
	chunks, err := d.retriever.Retrieve(ctx, retrieval.Request{
		Question:         in.Question,
		CategoryIDs:      job.Spec.ReadCategoryIDs,
		IncludeSensitive: job.Spec.IncludeSensitive,
		AllCategories:    job.Spec.AllCategories,
	})
	if err != nil {
		return nil, fmt.Errorf("operator: qa retrieve: %w", err)
	}
	resp, err := d.qa.Answer(ctx, in.ToAnswerRequest(chunks))
	if err != nil {
		return nil, fmt.Errorf("operator: qa generate: %w", err)
	}
	return resp, nil
}

// foldSuggestions adds enrichment suggestions to a generation result. It is
// best effort: a failure drops the suggestions, never the result.
func (d *Dispatcher) foldSuggestions(
	ctx context.Context,
	job *v1alpha1.PolicyAIJob,
	title string,
	sections []generation.SuggestSection,
	optOut generation.EnrichmentOptOut,
	library generation.EnrichmentLibrary,
) *generation.EnrichmentSuggestions {
	if d.suggest == nil {
		return nil
	}
	sugg, err := d.suggest.Suggest(ctx, generation.SuggestRequest{
		PolicyID: job.Spec.PolicyID,
		Title:    title,
		Sections: sections,
		OptOut:   optOut,
		Library:  library,
		Access:   access(job),
		ModelID:  job.Spec.ModelID,
	})
	if err != nil {
		d.log.Ctx(ctx).Warn("enrichment suggestions dropped from the result",
			log.F("job", job.Name), log.F("operation", string(job.Spec.Operation)), log.F("error", err.Error()))
		return nil
	}
	if sugg.IsEmpty() {
		return nil
	}
	return &sugg
}

func (d *Dispatcher) dispatchSuggestEnrichments(ctx context.Context, job *v1alpha1.PolicyAIJob) (any, error) {
	if d.suggest == nil {
		return nil, fmt.Errorf("%w: %s (suggest engine not wired)", ErrOperationNotImplemented, job.Spec.Operation)
	}
	in, err := decodeInput[generation.SuggestJobInput](job)
	if err != nil {
		return nil, err
	}
	sugg, err := d.suggest.Suggest(ctx, in.ToSuggestRequest(job.Spec.PolicyID, job.Spec.ModelID, access(job)))
	if err != nil {
		return nil, fmt.Errorf("operator: suggest enrichments: %w", err)
	}
	return generation.SuggestEnrichmentsResult{Suggestions: sugg}, nil
}

func (d *Dispatcher) dispatchRelatedReeval(ctx context.Context, job *v1alpha1.PolicyAIJob) (any, error) {
	if d.reeval == nil {
		return nil, fmt.Errorf("%w: %s (reeval engine not wired)", ErrOperationNotImplemented, job.Spec.Operation)
	}
	res, err := d.reeval.Reeval(ctx)
	if err != nil {
		return nil, fmt.Errorf("operator: related reeval: %w", err)
	}
	return res, nil
}

func (d *Dispatcher) dispatchRelationshipLearn(ctx context.Context, job *v1alpha1.PolicyAIJob) (any, error) {
	if d.relearn == nil {
		return nil, fmt.Errorf("%w: %s (relationship-learn engine not wired)", ErrOperationNotImplemented, job.Spec.Operation)
	}
	res, err := d.relearn.Learn(ctx)
	if err != nil {
		return nil, fmt.Errorf("operator: relationship learn: %w", err)
	}
	return res, nil
}
