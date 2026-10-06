// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"encoding/json"
	"time"

	log "github.com/Bugs5382/go-log"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/generation"
)

// SubmitAIJob creates a PolicyAIJob and returns its name at once; the
// operator runs it. The model is fixed here, so a later model change never
// affects the job.
func (s *AiServiceServer) SubmitAIJob(ctx context.Context, req *aiv1.SubmitAIJobRequest) (*aiv1.SubmitAIJobResponse, error) {
	const rpc = "submit_ai_job"
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.enabledSettings(ctx, rpc)
	if err != nil {
		return nil, err
	}
	op, ok := submittableOps[req.GetOperation()]
	if !ok {
		return nil, errcodes.Invalid(ctx, "operation", "not a job a person can submit")
	}
	if err := s.checkQuota(ctx, actor.Subject, rpc); err != nil {
		return nil, err
	}
	if err := s.checkMonthlyLimit(ctx, st, rpc); err != nil {
		return nil, err
	}
	model, err := s.d.LLM.DefaultModel(ctx)
	if err != nil {
		return nil, s.providerError(ctx, err)
	}

	var input runtime.RawExtension
	if req.GetInputJson() != "" {
		input = runtime.RawExtension{Raw: []byte(req.GetInputJson())}
	}
	job, err := s.d.Jobs.CreateJob(ctx, v1alpha1.PolicyAIJobSpec{
		Operation:          op,
		ActorUserID:        actor.Subject,
		ImpersonatorUserID: actor.Impersonator,
		PolicyID:           req.GetPolicyId(),
		VersionID:          req.GetVersionId(),
		CategoryID:         req.GetCategoryId(),
		ReadCategoryIDs:    req.GetScope().GetCategoryIds(),
		IncludeSensitive:   req.GetScope().GetIncludeSensitive(),
		AllCategories:      req.GetScope().GetAllCategories(),
		Input:              input,
		ModelID:            model,
	})
	if err != nil {
		s.log.Ctx(ctx).Error(err, "ai job not created", log.F("operation", string(op)))
		return nil, errcodes.Error(ctx, err)
	}
	s.log.Ctx(ctx).Info("ai job submitted", log.F("job", job.Name), log.F("operation", string(op)))

	s.emit(ctx, AuditRecord{
		Operation: rpc, Actor: actor, CategoryID: req.GetCategoryId(), Query: string(op),
		SourceIDs: nonEmpty(job.Name, req.GetPolicyId(), req.GetVersionId()),
	})
	return &aiv1.SubmitAIJobResponse{JobId: job.Name}, nil
}

// submittableOps are the operations a person may submit. RELATED_REEVAL and
// RELATIONSHIP_LEARN are started by the service and the operator only.
var submittableOps = map[aiv1.JobOperation]v1alpha1.JobOperation{
	aiv1.JobOperation_JOB_OPERATION_DRAFT:               v1alpha1.OperationDraft,
	aiv1.JobOperation_JOB_OPERATION_REWRITE:             v1alpha1.OperationRewrite,
	aiv1.JobOperation_JOB_OPERATION_CLARIFY:             v1alpha1.OperationClarify,
	aiv1.JobOperation_JOB_OPERATION_SUMMARIZE:           v1alpha1.OperationSummarize,
	aiv1.JobOperation_JOB_OPERATION_REVIEW:              v1alpha1.OperationReview,
	aiv1.JobOperation_JOB_OPERATION_REVISE:              v1alpha1.OperationRevise,
	aiv1.JobOperation_JOB_OPERATION_QA:                  v1alpha1.OperationQA,
	aiv1.JobOperation_JOB_OPERATION_SUGGEST_ENRICHMENTS: v1alpha1.OperationSuggestEnrichments,
}

// GetAIJob reports a job's state. A job submitted by someone else reads as
// not found, so a job id reveals nothing to anyone but its submitter.
func (s *AiServiceServer) GetAIJob(ctx context.Context, req *aiv1.GetAIJobRequest) (*aiv1.GetAIJobResponse, error) {
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetJobId() == "" {
		return nil, errcodes.Invalid(ctx, "job_id", "required")
	}
	l := s.log.Ctx(ctx).With(log.F("job", req.GetJobId()))
	job, err := s.d.Jobs.GetJob(ctx, req.GetJobId())
	if err != nil {
		if apierrors.IsNotFound(err) {
			l.Debug("ai job not found")
			return nil, errcodes.New(ctx, errcodes.CodeJobNotFound)
		}
		l.Error(err, "ai job read failed")
		return nil, errcodes.Error(ctx, err)
	}
	if job.Spec.ActorUserID != actor.Subject {
		l.Warn("ai job read refused: not the submitter")
		return nil, errcodes.New(ctx, errcodes.CodeJobNotFound)
	}

	resp := &aiv1.GetAIJobResponse{
		Phase:      jobPhaseToProto(job.Status.Phase),
		ResultRef:  job.Status.ResultRef,
		Error:      job.Status.Error,
		StartedAt:  formatJobTime(job.Status.StartedAt),
		FinishedAt: formatJobTime(job.Status.FinishedAt),
	}
	// A succeeded QA job carries its answer inline, the same shape
	// SearchAndAnswer returns. Best effort: a miss leaves result_ref only.
	if job.Spec.Operation == v1alpha1.OperationQA && job.Status.Phase == v1alpha1.PhaseSucceeded && job.Status.ResultRef != "" {
		resp.QaResult = s.readQAResult(ctx, job.Status.ResultRef)
	}
	return resp, nil
}

// qaResultEnvelope is the operator's result envelope for a QA job, decoded
// here so this package doesn't depend on the operator's.
type qaResultEnvelope struct {
	Operation string                    `json:"operation"`
	Result    generation.AnswerResponse `json:"result"`
}

func (s *AiServiceServer) readQAResult(ctx context.Context, key string) *aiv1.QaJobResult {
	l := s.log.Ctx(ctx).With(log.F("key", key))
	raw, found, err := s.d.JobResults.ReadResult(ctx, key)
	if err != nil {
		l.Warn("qa job result read failed; returning result_ref only", log.F("error", err.Error()))
		return nil
	}
	if !found {
		return nil
	}
	var env qaResultEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		l.Warn("qa job result decode failed; returning result_ref only", log.F("error", err.Error()))
		return nil
	}
	a := env.Result
	return &aiv1.QaJobResult{
		Answer:             a.Answer,
		Citations:          toPBCitations(a.Citations),
		NoAuthorizedSource: a.NoAuthorizedSource,
		HasSensitiveSource: a.HasSensitiveSource,
		Segments:           toPBSegments(a.Segments),
	}
}

// jobPhaseToProto maps an untouched job's empty phase to PENDING.
func jobPhaseToProto(phase v1alpha1.JobPhase) aiv1.JobPhase {
	switch phase {
	case v1alpha1.PhaseRunning:
		return aiv1.JobPhase_JOB_PHASE_RUNNING
	case v1alpha1.PhaseSucceeded:
		return aiv1.JobPhase_JOB_PHASE_SUCCEEDED
	case v1alpha1.PhaseFailed:
		return aiv1.JobPhase_JOB_PHASE_FAILED
	default:
		return aiv1.JobPhase_JOB_PHASE_PENDING
	}
}

func formatJobTime(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
