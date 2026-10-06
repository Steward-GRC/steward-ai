// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"context"
	"fmt"

	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/provider"
)

// AssistOperation is one authoring-assist operation.
type AssistOperation int

// The authoring-assist operations.
const (
	AssistOperationDraft AssistOperation = iota + 1
	AssistOperationExpand
	AssistOperationRewrite
	AssistOperationClarify
	AssistOperationSummarize
)

// assistSystemPrompt is the instruction for the region-editing operations
// (draft, expand, rewrite, clarify). The output covers the editable region
// only; locked text and the section structure are never touched.
const assistSystemPrompt = `You are an authoring assistant for a policy management system.

Your role is to help authors improve the text within a designated editable region — which may be
a whole section, or just a selected passage within one — of a policy.

Rules:
1. Your output is a suggestion for the editable region only. It is for human review and will
   never be auto-applied without an editor's explicit acceptance.
2. You are also given READ-ONLY context: this may be just the section's surrounding boilerplate
   and title, or the ENTIRE policy (provided so a selection- or section-level rewrite stays
   consistent with the rest of the document). Either way it is context only — use it to keep your
   suggestion coherent with it, but do NOT modify, restate, or reproduce any of it in your output.
3. Do NOT propose changes to section structure, titles, or ordering.
4. Return ONLY the improved text for the editable region — no preamble, no explanation.
5. Match the formal, clear tone typical of organizational policy documents.`

// summarizeSystemPrompt is the instruction for the summarize operation, which
// writes a short overview of the whole policy rather than replacement text.
const summarizeSystemPrompt = `You are a summarization assistant for a policy management system.

Your role is to produce a short, skimmable overview of a policy for staff who need the gist
without reading the full document.

Rules:
1. Output ONLY a Markdown bulleted list: 3 to 5 bullets, each starting with "- ".
2. Each bullet must be one short sentence or clause capturing a distinct key point of the policy.
3. Together the bullets must cover the policy's key points as a whole — not just its opening section.
4. Do NOT include a heading, preamble, closing remark, or any text outside the bullets.
5. Do NOT modify or reference locked boilerplate text (provided as context only).
6. Match the formal, clear tone typical of organizational policy documents.`

// maxTokensFor returns the output budget for op. Summaries get a much smaller
// one, which is what keeps them short.
func maxTokensFor(op AssistOperation) int {
	if op == AssistOperationSummarize {
		return airules.SummarizeMaxTokens
	}
	return airules.AssistMaxTokens
}

// AssistRequest is the input to Assist.Generate.
type AssistRequest struct {
	ActorUserID string
	PolicyID    string
	VersionID   string
	SectionKey  string
	// EditableContent is the region's current text; empty for a draft.
	EditableContent string
	// ContextHint is read-only context, from a section's boilerplate up to
	// the whole policy, given only so the suggestion stays coherent with it.
	ContextHint string
	Operation   AssistOperation
	// Instruction is an optional extra instruction from the author.
	Instruction string
}

// AssistResponse is the output of Assist.Generate.
type AssistResponse struct {
	Suggestion string
	// OperationID echoes the operation's name for the client.
	OperationID string
}

// Assist writes authoring suggestions for an editable region.
type Assist struct {
	gen provider.Generator
}

// NewAssist returns an Assist over gen.
func NewAssist(gen provider.Generator) *Assist {
	return &Assist{gen: gen}
}

// Generate returns a suggestion for the editable region. Nothing is applied;
// an editor accepts or discards it.
func (a *Assist) Generate(ctx context.Context, req AssistRequest) (AssistResponse, error) {
	opLabel, err := operationLabel(req.Operation)
	if err != nil {
		return AssistResponse{}, err
	}

	systemPrompt := assistSystemPrompt
	userPrompt := fmt.Sprintf(
		"Policy context (READ-ONLY, for coherence only — rewrite ONLY the editable content per "+
			"the operation; keep it consistent with this context; do not restate or modify it):\n%s\n\n"+
			"Operation: %s\n\nCurrent editable content:\n%s\n",
		req.ContextHint, opLabel, req.EditableContent,
	)
	if req.Instruction != "" {
		userPrompt += fmt.Sprintf("\nAdditional instruction: %s\n", req.Instruction)
	}
	if req.Operation == AssistOperationSummarize {
		systemPrompt = summarizeSystemPrompt
		userPrompt += "\nSummarize the whole policy above as 3-5 concise Markdown bullet points (\"- \"). " +
			"No heading, no preamble, no closing remark — output only the bullets."
	} else {
		userPrompt += "\nReturn only the suggested replacement text for the editable region."
	}

	resp, err := a.gen.Complete(ctx, provider.Request{
		System:    systemPrompt,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: userPrompt}},
		MaxTokens: maxTokensFor(req.Operation),
		Operation: opLabel,
	})
	if err != nil {
		return AssistResponse{}, fmt.Errorf("assist: complete: %w", err)
	}

	return AssistResponse{
		Suggestion:  resp.Text,
		OperationID: opLabel,
	}, nil
}

func operationLabel(op AssistOperation) (string, error) {
	switch op {
	case AssistOperationDraft:
		return "draft", nil
	case AssistOperationExpand:
		return "expand", nil
	case AssistOperationRewrite:
		return "rewrite", nil
	case AssistOperationClarify:
		return "clarify", nil
	case AssistOperationSummarize:
		return "summarize", nil
	default:
		return "", fmt.Errorf("assist: unknown operation %d", op)
	}
}
