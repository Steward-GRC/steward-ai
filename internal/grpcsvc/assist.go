// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"

	log "github.com/Bugs5382/go-log"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/generation"
)

// AuthoringAssist suggests text for one editable region of a draft. The
// suggestion is never applied here; an editor accepts or discards it.
func (s *AiServiceServer) AuthoringAssist(ctx context.Context, req *aiv1.AuthoringAssistRequest) (*aiv1.AuthoringAssistResponse, error) {
	const rpc = "authoring_assist"
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.enabledSettings(ctx, rpc)
	if err != nil {
		return nil, err
	}
	if req.GetSectionKey() == "" {
		return nil, errcodes.Invalid(ctx, "section_key", "required")
	}
	op, ok := assistOps[req.GetOperation()]
	if !ok {
		return nil, errcodes.Invalid(ctx, "operation", "unknown assist operation")
	}
	if err := s.checkQuota(ctx, actor.Subject, rpc); err != nil {
		return nil, err
	}
	if err := s.checkMonthlyLimit(ctx, st, rpc); err != nil {
		return nil, err
	}

	resp, err := s.d.Assist.Generate(ctx, generation.AssistRequest{
		ActorUserID:     actor.Subject,
		PolicyID:        req.GetPolicyId(),
		VersionID:       req.GetVersionId(),
		SectionKey:      req.GetSectionKey(),
		EditableContent: req.GetEditableContent(),
		ContextHint:     req.GetContextHint(),
		Operation:       op,
		Instruction:     req.GetInstruction(),
	})
	if err != nil {
		return nil, s.providerError(ctx, err)
	}
	s.log.Ctx(ctx).Debug("assist suggestion generated", log.F("operation", resp.OperationID))

	s.emit(ctx, AuditRecord{
		Operation: rpc, Actor: actor, Query: req.GetInstruction(), SourceIDs: nonEmpty(req.GetVersionId()),
	})
	return &aiv1.AuthoringAssistResponse{Suggestion: resp.Suggestion, OperationId: resp.OperationID}, nil
}

var assistOps = map[aiv1.AssistOperation]generation.AssistOperation{
	aiv1.AssistOperation_ASSIST_OPERATION_DRAFT:     generation.AssistOperationDraft,
	aiv1.AssistOperation_ASSIST_OPERATION_EXPAND:    generation.AssistOperationExpand,
	aiv1.AssistOperation_ASSIST_OPERATION_REWRITE:   generation.AssistOperationRewrite,
	aiv1.AssistOperation_ASSIST_OPERATION_CLARIFY:   generation.AssistOperationClarify,
	aiv1.AssistOperation_ASSIST_OPERATION_SUMMARIZE: generation.AssistOperationSummarize,
}

func nonEmpty(ids ...string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}
