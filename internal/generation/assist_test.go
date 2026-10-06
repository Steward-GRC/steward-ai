// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"context"
	"strings"
	"testing"
)

func TestAssistSystemPromptConstrainsToEditableRegion(t *testing.T) {
	if !strings.Contains(assistSystemPrompt, "editable region") {
		t.Fatal("assist system prompt must explicitly constrain output to the editable region")
	}
	if !strings.Contains(assistSystemPrompt, "suggestion") {
		t.Fatal("assist system prompt must frame output as a suggestion for human review")
	}
}

// TestAssistSystemPromptAllowsWholeDocumentReadOnlyContext verifies the
// prompt's context wording is reframed to cover ContextHint carrying either
// a section's boilerplate or the entire policy — not just section
// boilerplate — while still requiring the read-only context never be
// modified/restated in the output.
func TestAssistSystemPromptAllowsWholeDocumentReadOnlyContext(t *testing.T) {
	if !strings.Contains(assistSystemPrompt, "READ-ONLY") {
		t.Fatal("assist system prompt must label context READ-ONLY")
	}
	if !strings.Contains(assistSystemPrompt, "ENTIRE policy") {
		t.Fatal("assist system prompt must allow context to carry the entire policy")
	}
	if !strings.Contains(assistSystemPrompt, "do NOT modify, restate, or reproduce") {
		t.Fatal("assist system prompt must forbid modifying/restating/reproducing the read-only context")
	}
}

func TestAssist_draft(t *testing.T) {
	stub := &stubLLMClient{response: "All employees are required to complete onboarding training within 30 days of hire."}
	a := NewAssist(stub)

	resp, err := a.Generate(context.Background(), AssistRequest{
		ActorUserID:     "user-1",
		PolicyID:        "pol-001",
		VersionID:       "ver-draft",
		SectionKey:      "requirements",
		EditableContent: "",
		ContextHint:     "Section: Requirements. This section defines mandatory employee requirements.",
		Operation:       AssistOperationDraft,
		Instruction:     "Draft a requirement for onboarding training.",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Suggestion == "" {
		t.Fatal("expected a non-empty suggestion")
	}
}

func TestAssist_rewrite(t *testing.T) {
	stub := &stubLLMClient{response: "Employees must complete onboarding within 30 days."}
	a := NewAssist(stub)

	resp, err := a.Generate(context.Background(), AssistRequest{
		ActorUserID:     "user-2",
		PolicyID:        "pol-001",
		VersionID:       "ver-draft",
		SectionKey:      "requirements",
		EditableContent: "All people need to do onboarding in their first month.",
		ContextHint:     "Section: Requirements",
		Operation:       AssistOperationRewrite,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Suggestion == "" {
		t.Fatal("expected a non-empty suggestion for rewrite")
	}
}

// TestAssist_summarize_usesSmallTokenBudgetAndBulletPrompt verifies the
// summarize operation is dispatched with a much smaller MaxTokens than the
// section-editing operations and a prompt that asks for 3-5 concise Markdown
// bullet points — the fix for summaries coming back as long prose blocks.
func TestAssist_summarize_usesSmallTokenBudgetAndBulletPrompt(t *testing.T) {
	stub := &stubLLMClient{response: "- Point one\n- Point two\n- Point three"}
	a := NewAssist(stub)

	resp, err := a.Generate(context.Background(), AssistRequest{
		ActorUserID:     "user-3",
		PolicyID:        "pol-001",
		VersionID:       "ver-1",
		SectionKey:      "whole-policy",
		EditableContent: "This policy requires employees to complete onboarding within 30 days.",
		ContextHint:     "Policy: Onboarding",
		Operation:       AssistOperationSummarize,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Suggestion == "" {
		t.Fatal("expected a non-empty summary")
	}

	if stub.lastReq.MaxTokens != 512 {
		t.Fatalf("expected summarize MaxTokens=512, got %d", stub.lastReq.MaxTokens)
	}
	if !strings.Contains(stub.lastReq.System, "3 to 5 bullets") {
		t.Fatal("expected summarize system prompt to require 3-5 bullets")
	}
	if !strings.Contains(stub.lastReq.Messages[0].Content, "3-5 concise Markdown bullet points") {
		t.Fatal("expected summarize user prompt to instruct 3-5 concise Markdown bullet points")
	}
	if strings.Contains(stub.lastReq.Messages[0].Content, "suggested replacement text for the editable region") {
		t.Fatal("summarize prompt must not reuse the section-edit \"replacement text\" wording")
	}
}

// TestAssist_otherOps_keepOriginalTokenBudgetAndPrompt verifies draft/expand/
// rewrite/clarify are unaffected by the summarize-specific change: they still
// run at MaxTokens=2048 with the original assistSystemPrompt and "replacement
// text" closing instruction.
func TestAssist_otherOps_keepOriginalTokenBudgetAndPrompt(t *testing.T) {
	ops := []AssistOperation{
		AssistOperationDraft,
		AssistOperationExpand,
		AssistOperationRewrite,
		AssistOperationClarify,
	}
	for _, op := range ops {
		label, err := operationLabel(op)
		if err != nil {
			t.Fatalf("operationLabel(%d): %v", op, err)
		}
		t.Run(label, func(t *testing.T) {
			stub := &stubLLMClient{response: "Suggested text."}
			a := NewAssist(stub)

			_, err := a.Generate(context.Background(), AssistRequest{
				EditableContent: "Existing text.",
				ContextHint:     "Section: Requirements",
				Operation:       op,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if stub.lastReq.MaxTokens != 2048 {
				t.Fatalf("expected MaxTokens=2048 for %s, got %d", label, stub.lastReq.MaxTokens)
			}
			if stub.lastReq.System != assistSystemPrompt {
				t.Fatalf("expected %s to use the original assistSystemPrompt", label)
			}
			if !strings.Contains(stub.lastReq.Messages[0].Content, "suggested replacement text for the editable region") {
				t.Fatalf("expected %s to keep the original \"replacement text\" closing instruction", label)
			}
		})
	}
}

// TestAssistUserPrompt_contextBlockCarriesWholeDocumentReadOnly verifies the
// user prompt's context block is reframed for a ContextHint carrying the
// ENTIRE policy (as the UI does for selection/section rewrites) — labelled
// READ-ONLY for coherence, with an explicit instruction not to restate or
// modify it and to rewrite only the editable content.
func TestAssistUserPrompt_contextBlockCarriesWholeDocumentReadOnly(t *testing.T) {
	stub := &stubLLMClient{response: "Employees must complete onboarding within 14 days."}
	a := NewAssist(stub)

	wholePolicy := "# Onboarding Policy\n\n## Purpose\nThis policy governs onboarding.\n\n" +
		"## Requirements\nAll people need to do onboarding in their first month."

	_, err := a.Generate(context.Background(), AssistRequest{
		ActorUserID:     "user-4",
		PolicyID:        "pol-001",
		VersionID:       "ver-draft",
		SectionKey:      "requirements",
		EditableContent: "All people need to do onboarding in their first month.",
		ContextHint:     wholePolicy,
		Operation:       AssistOperationRewrite,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prompt := stub.lastReq.Messages[0].Content
	if !strings.Contains(prompt, wholePolicy) {
		t.Fatal("expected the whole-policy context to appear in the user prompt")
	}
	if !strings.Contains(prompt, "READ-ONLY") {
		t.Fatal("expected the context block to be labelled READ-ONLY")
	}
	if !strings.Contains(prompt, "do not restate or modify it") {
		t.Fatal("expected the context block to instruct not restating/modifying it")
	}
	if !strings.Contains(prompt, "rewrite ONLY the editable content") {
		t.Fatal("expected the context block to instruct rewriting only the editable content")
	}
}

func TestAssist_unknownOperationErrors(t *testing.T) {
	stub := &stubLLMClient{response: "irrelevant"}
	a := NewAssist(stub)
	_, err := a.Generate(context.Background(), AssistRequest{
		Operation: AssistOperation(99),
	})
	if err == nil {
		t.Fatal("expected error for unknown operation")
	}
}
