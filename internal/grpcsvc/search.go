// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"errors"
	"time"

	log "github.com/Bugs5382/go-log"
	"github.com/jackc/pgx/v5"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/retrieval"
)

// SearchAndAnswer retrieves what the scope may read and answers from it
// only. Every answered call is audited, whether or not a source was found
// and whether or not it came from the cache.
func (s *AiServiceServer) SearchAndAnswer(ctx context.Context, req *aiv1.SearchAndAnswerRequest) (*aiv1.SearchAndAnswerResponse, error) {
	const rpc = "search_and_answer"
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.enabledSettings(ctx, rpc)
	if err != nil {
		return nil, err
	}
	if req.GetQuestion() == "" {
		return nil, errcodes.Invalid(ctx, "question", "required")
	}
	if err := s.checkCategory(ctx, req.GetCategoryId(), req.GetScope()); err != nil {
		return nil, err
	}
	if err := s.checkQuota(ctx, actor.Subject, rpc); err != nil {
		return nil, err
	}
	if err := s.checkMonthlyLimit(ctx, st, rpc); err != nil {
		return nil, err
	}

	rreq := retrieval.Request{
		Question:         req.GetQuestion(),
		CategoryIDs:      req.GetScope().GetCategoryIds(),
		IncludeSensitive: req.GetScope().GetIncludeSensitive(),
		AllCategories:    req.GetScope().GetAllCategories(),
		TopK:             effectiveTopK(st.TopK),
	}
	if c := req.GetCategoryId(); c != "" {
		rreq.CategoryIDs, rreq.AllCategories = []string{c}, false
	}
	scope := rreq.Filter()
	l := s.log.Ctx(ctx).With(log.F("rpc", rpc))

	queryVec, err := s.d.Retrieval.EmbedQuestion(ctx, req.GetQuestion())
	if err != nil {
		return nil, s.retrievalUnavailable(ctx, "embed_question", err)
	}

	var (
		answer   generation.AnswerResponse
		cacheHit bool
	)
	if s.cache != nil {
		cached, hit, cerr := s.cache.Lookup(ctx, req.GetQuestion(), queryVec, scope)
		switch {
		case cerr != nil:
			l.Warn("qa cache lookup failed", log.F("error", cerr.Error()))
		case hit:
			cacheHit = true
			answer = fromCacheAnswer(cached)
		}
	}

	if !cacheHit {
		chunks, rerr := s.d.Retrieval.RetrieveWithEmbedding(ctx, queryVec, rreq)
		if rerr != nil {
			return nil, s.retrievalUnavailable(ctx, "retrieval", rerr)
		}
		answer, err = s.d.QA.Answer(ctx, generation.AnswerRequest{Question: req.GetQuestion(), Chunks: chunks})
		if err != nil {
			return nil, s.providerError(ctx, err)
		}
		if s.cache != nil {
			if perr := s.cache.Put(ctx, req.GetQuestion(), queryVec, scope, toCacheAnswer(answer)); perr != nil {
				l.Warn("qa cache write failed", log.F("error", perr.Error()))
			}
		}
	}
	l.Debug("question answered", log.F("cache_hit", cacheHit), log.F("citations", len(answer.Citations)),
		log.F("no_authorized_source", answer.NoAuthorizedSource))

	sourceIDs := make([]string, 0, len(answer.Citations))
	for _, c := range answer.Citations {
		sourceIDs = append(sourceIDs, c.VersionID)
	}
	s.emit(ctx, AuditRecord{
		Operation: rpc, Actor: actor, CategoryID: req.GetCategoryId(), Query: req.GetQuestion(),
		SourceIDs: sourceIDs, HasSensitive: answer.HasSensitiveSource,
	})

	return &aiv1.SearchAndAnswerResponse{
		Answer:             answer.Answer,
		Citations:          toPBCitations(answer.Citations),
		NoAuthorizedSource: answer.NoAuthorizedSource,
		Segments:           toPBSegments(answer.Segments),
		HasSensitiveSource: answer.HasSensitiveSource,
	}, nil
}

// effectiveTopK is the stored override, or the default when none is set.
func effectiveTopK(stored int) int {
	if stored > 0 {
		return stored
	}
	return airules.RetrievalTopKDefault
}

// GetPolicySummary reads the summary stored at publish. It never generates
// one: a version without a stored summary reports found=false.
func (s *AiServiceServer) GetPolicySummary(ctx context.Context, req *aiv1.GetPolicySummaryRequest) (*aiv1.GetPolicySummaryResponse, error) {
	if _, err := s.requireActor(ctx); err != nil {
		return nil, err
	}
	if req.GetVersionId() == "" {
		return nil, errcodes.Invalid(ctx, "version_id", "required")
	}
	sum, err := s.d.Summaries.GetSummary(ctx, req.GetVersionId())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &aiv1.GetPolicySummaryResponse{Found: false}, nil
		}
		s.log.Ctx(ctx).Error(err, "policy summary read failed")
		return nil, errcodes.Error(ctx, err)
	}
	return &aiv1.GetPolicySummaryResponse{
		Found:       true,
		SummaryText: sum.SummaryText,
		GeneratedAt: sum.GeneratedAt.Format(time.RFC3339),
	}, nil
}

// GetTopQuestions returns the most-asked questions whose cited policies the
// scope may all read; the store applies the scope in its query.
func (s *AiServiceServer) GetTopQuestions(ctx context.Context, req *aiv1.GetTopQuestionsRequest) (*aiv1.GetTopQuestionsResponse, error) {
	if _, err := s.requireActor(ctx); err != nil {
		return nil, err
	}
	questions, err := s.d.TopQuestions.TopQuestions(ctx, filter(req.GetScope()), int(req.GetLimit()))
	if err != nil {
		s.log.Ctx(ctx).Error(err, "top questions read failed")
		return nil, errcodes.Error(ctx, err)
	}
	return &aiv1.GetTopQuestionsResponse{Questions: questions}, nil
}

// GetRelatedPolicies returns the policies nearest to one policy by centroid,
// filtered by the scope inside the query so a relation never reveals a
// policy the person can't read. It never calls a provider, so neither the
// switch nor the quota applies.
func (s *AiServiceServer) GetRelatedPolicies(ctx context.Context, req *aiv1.GetRelatedPoliciesRequest) (*aiv1.GetRelatedPoliciesResponse, error) {
	if _, err := s.requireActor(ctx); err != nil {
		return nil, err
	}
	if req.GetPolicyId() == "" {
		return nil, errcodes.Invalid(ctx, "policy_id", "required")
	}
	neighbors, err := s.d.Related.RelatedPolicies(ctx, req.GetPolicyId(), filter(req.GetScope()), int(req.GetTopN()))
	if err != nil {
		s.log.Ctx(ctx).Error(err, "related policies read failed", log.F("policy_id", req.GetPolicyId()))
		return nil, errcodes.Error(ctx, err)
	}
	related := make([]*aiv1.RelatedPolicy, 0, len(neighbors))
	for _, n := range neighbors {
		related = append(related, &aiv1.RelatedPolicy{
			PolicyId:    n.PolicyID,
			PolicyTitle: n.PolicyTitle,
			CategoryId:  n.CategoryID,
			VersionNo:   toInt32(n.VersionNo),
			Distance:    n.Distance,
		})
	}
	s.log.Ctx(ctx).Debug("related policies", log.F("policy_id", req.GetPolicyId()), log.F("count", len(related)))
	return &aiv1.GetRelatedPoliciesResponse{Related: related}, nil
}
