// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReviseSystemPromptIsSurgicalAndTreatsInputAsData(t *testing.T) {
	if !strings.Contains(reviseSystemPrompt, "MINIMAL") {
		t.Fatal("revise system prompt must require minimal edits")
	}
	if !strings.Contains(reviseSystemPrompt, "EXACTLY as") {
		t.Fatal("revise system prompt must require unchanged sections to be left exactly as-is")
	}
	if !strings.Contains(reviseSystemPrompt, "never instructions") {
		t.Fatal("revise system prompt must warn that the instruction/section content is DATA, not instructions")
	}
	if !strings.Contains(reviseSystemPrompt, "new_section") {
		t.Fatal("revise system prompt must define the new_section output block")
	}
}

// TestReviseSystemPromptRequiresPreservingContentAndCleanRemovals verifies
// the instruction-fidelity rewrite: existing content must be preserved
// faithfully (no dropping/paraphrasing/fluffing of untouched content within
// a changed section), and removed/replaced content must be cleaned up
// everywhere it is referenced, not just where it was removed from.
func TestReviseSystemPromptRequiresPreservingContentAndCleanRemovals(t *testing.T) {
	if !strings.Contains(reviseSystemPrompt, "PRESERVE existing content faithfully") {
		t.Fatal("revise system prompt must require faithfully preserving existing content")
	}
	if !strings.Contains(reviseSystemPrompt, "fluff") {
		t.Fatal("revise system prompt must warn against fluffing out untouched content")
	}
	if !strings.Contains(reviseSystemPrompt, "CLEAN REMOVALS") {
		t.Fatal("revise system prompt must require clean removals")
	}
	if !strings.Contains(reviseSystemPrompt, "dangling reference") {
		t.Fatal("revise system prompt must require cleaning up dangling references to removed content")
	}
}

// TestReviseSystemPromptRequiresInstructionFidelityBothDirections verifies
// the prompt requires doing exactly what the instruction asks in either
// direction — including actually expanding content when asked, with no
// anti-fluff bias suppressing a requested expansion.
func TestReviseSystemPromptRequiresInstructionFidelityBothDirections(t *testing.T) {
	if !strings.Contains(reviseSystemPrompt, "INSTRUCTION FIDELITY") {
		t.Fatal("revise system prompt must call out instruction fidelity explicitly")
	}
	if !strings.Contains(reviseSystemPrompt, "NO anti-fluff bias") {
		t.Fatal("revise system prompt must disclaim any anti-fluff bias against requested expansion")
	}
	if !strings.Contains(reviseSystemPrompt, "actually expand it") {
		t.Fatal("revise system prompt must require actually expanding content when the instruction asks for it")
	}
}

// TestReviseSystemPromptHonorsHeaders verifies the prompt requires
// preserving existing sections/headers — including author-added ones beyond
// the standard template — and only removing/restructuring a header when the
// revision genuinely calls for it.
func TestReviseSystemPromptHonorsHeaders(t *testing.T) {
	if !strings.Contains(reviseSystemPrompt, "HONOR HEADERS") {
		t.Fatal("revise system prompt must call out honoring headers explicitly")
	}
	if !strings.Contains(reviseSystemPrompt, "an author added beyond the standard template") {
		t.Fatal("revise system prompt must require preserving author-added headers beyond the template")
	}
	if !strings.Contains(reviseSystemPrompt, "never do it gratuitously") {
		t.Fatal("revise system prompt must forbid gratuitous header removal/restructuring")
	}
}

// TestReviseSystemPromptStylePerInstruction verifies the prompt defaults to
// making a change in place with no before/after narration, only narrating
// when the instruction itself asks for a documented/traceable change.
func TestReviseSystemPromptStylePerInstruction(t *testing.T) {
	if !strings.Contains(reviseSystemPrompt, "STYLE PER INSTRUCTION") {
		t.Fatal("revise system prompt must call out style-per-instruction explicitly")
	}
	if !strings.Contains(reviseSystemPrompt, `"previously X, now Y"`) {
		t.Fatal("revise system prompt must forbid default before/after narration")
	}
	if !strings.Contains(reviseSystemPrompt, "when the instruction asks you to document or trace the change") {
		t.Fatal("revise system prompt must allow narration only when the instruction asks for it")
	}
}

// TestReviseSystemPromptDefinesRemovedSectionMarker verifies the prompt
// documents the removed="true" output marker for whole-section deletion.
func TestReviseSystemPromptDefinesRemovedSectionMarker(t *testing.T) {
	if !strings.Contains(reviseSystemPrompt, `removed="true"`) {
		t.Fatal(`revise system prompt must define the removed="true" section marker`)
	}
}

func TestRevise_parsesChangedAndUnchangedSectionsAndNewSection(t *testing.T) {
	stub := &stubLLMClient{response: `<section key="purpose" changed="true">
This policy establishes updated onboarding requirements, including a data retention clause.
</section>
<section key="scope" changed="false"></section>
<new_section title="Data Retention" after="purpose">
Records must be retained for seven years.
</new_section>`}
	r := NewRevise(stub, 0)

	resp, err := r.Generate(context.Background(), ReviseRequest{
		ActorUserID: "user-1",
		CategoryID:  "cat-hr",
		PolicyID:    "pol-1",
		VersionID:   "ver-1",
		Instruction: "add a data retention clause",
		Sections: []ReviseSection{
			{Key: "purpose", Title: "Purpose", Content: "This policy establishes onboarding requirements.", Order: 1},
			{Key: "scope", Title: "Scope", Content: "Applies to all staff.", Order: 2},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Sections) != 2 {
		t.Fatalf("expected 2 sections (one per input section), got %d: %+v", len(resp.Sections), resp.Sections)
	}

	purpose := resp.Sections[0]
	if purpose.SectionKey != "purpose" || !purpose.Changed || purpose.Content == "" {
		t.Fatalf("expected purpose changed with content, got %+v", purpose)
	}

	scope := resp.Sections[1]
	if scope.SectionKey != "scope" || scope.Changed || scope.Content != "" {
		t.Fatalf("expected scope unchanged with no content, got %+v", scope)
	}

	if len(resp.NewSections) != 1 {
		t.Fatalf("expected 1 new section, got %+v", resp.NewSections)
	}
	ns := resp.NewSections[0]
	if ns.Title != "Data Retention" || ns.AfterKey != "purpose" || ns.Content == "" {
		t.Fatalf("unexpected new section: %+v", ns)
	}
}

func TestRevise_everyInputSectionRepresentedEvenWhenModelOmitsABlock(t *testing.T) {
	// The model only emits a block for "purpose", never mentioning "scope" or
	// "definitions" at all — the parser must still surface both as unchanged,
	// per ReviseResponse's guarantee that nothing is silently dropped.
	stub := &stubLLMClient{response: `<section key="purpose" changed="true">
Updated purpose text.
</section>`}
	r := NewRevise(stub, 0)

	resp, err := r.Generate(context.Background(), ReviseRequest{
		Instruction: "tighten the purpose statement",
		Sections: []ReviseSection{
			{Key: "purpose", Title: "Purpose", Content: "Original purpose."},
			{Key: "scope", Title: "Scope", Content: "Original scope."},
			{Key: "definitions", Title: "Definitions", Content: "Original definitions."},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Sections) != 3 {
		t.Fatalf("expected all 3 input sections represented, got %d: %+v", len(resp.Sections), resp.Sections)
	}
	if resp.Sections[0].SectionKey != "purpose" || !resp.Sections[0].Changed {
		t.Fatalf("expected purpose changed, got %+v", resp.Sections[0])
	}
	for _, s := range resp.Sections[1:] {
		if s.Changed || s.Content != "" {
			t.Fatalf("expected section %q to default to unchanged with no content, got %+v", s.SectionKey, s)
		}
	}
}

// TestRevise_parsesRemovedSection verifies a <section key="..." removed="true">
// block is parsed as a removed section (no content, Changed=false), and that
// mixing changed/unchanged/removed sections in one response still surfaces
// every input section exactly once, in order.
func TestRevise_parsesRemovedSection(t *testing.T) {
	stub := &stubLLMClient{response: `<section key="purpose" changed="false"></section>
<section key="legacy_clause" removed="true"></section>
<section key="scope" changed="true">
Applies to all staff and contractors.
</section>`}
	r := NewRevise(stub, 0)

	resp, err := r.Generate(context.Background(), ReviseRequest{
		Instruction: "remove the legacy clause section and broaden scope to contractors",
		Sections: []ReviseSection{
			{Key: "purpose", Title: "Purpose", Content: "This policy establishes onboarding requirements.", Order: 1},
			{Key: "legacy_clause", Title: "Legacy Clause", Content: "This clause is obsolete.", Order: 2},
			{Key: "scope", Title: "Scope", Content: "Applies to all staff.", Order: 3},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Sections) != 3 {
		t.Fatalf("expected all 3 input sections represented, got %d: %+v", len(resp.Sections), resp.Sections)
	}

	purpose := resp.Sections[0]
	if purpose.SectionKey != "purpose" || purpose.Changed || purpose.Removed || purpose.Content != "" {
		t.Fatalf("expected purpose unchanged, got %+v", purpose)
	}

	removed := resp.Sections[1]
	if removed.SectionKey != "legacy_clause" || !removed.Removed || removed.Changed || removed.Content != "" {
		t.Fatalf("expected legacy_clause removed with no content and Changed=false, got %+v", removed)
	}

	scope := resp.Sections[2]
	if scope.SectionKey != "scope" || !scope.Changed || scope.Removed || scope.Content == "" {
		t.Fatalf("expected scope changed with content, got %+v", scope)
	}
}

// TestRevisedSection_MarshalJSON_removedShape verifies a removed section's
// JSON carries only sectionKey and removed:true — no changed field, no
// content.
func TestRevisedSection_MarshalJSON_removedShape(t *testing.T) {
	rs := RevisedSection{SectionKey: "legacy_clause", Removed: true}
	b, err := json.Marshal(rs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"sectionKey":"legacy_clause","removed":true}`
	if string(b) != want {
		t.Fatalf("expected %s, got %s", want, string(b))
	}
}

// TestRevisedSection_MarshalJSON_changedAndUnchangedShapesUnaffected
// verifies the pre-existing changed:true/false JSON shapes are unaffected by
// adding the removed state.
func TestRevisedSection_MarshalJSON_changedAndUnchangedShapesUnaffected(t *testing.T) {
	changed := RevisedSection{SectionKey: "purpose", Changed: true, Content: "New body."}
	b, err := json.Marshal(changed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"sectionKey":"purpose","changed":true,"content":"New body."}`
	if string(b) != want {
		t.Fatalf("expected %s, got %s", want, string(b))
	}

	unchanged := RevisedSection{SectionKey: "scope", Changed: false}
	b, err = json.Marshal(unchanged)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want = `{"sectionKey":"scope","changed":false}`
	if string(b) != want {
		t.Fatalf("expected %s, got %s", want, string(b))
	}
}

func TestRevise_unchangedSectionsCarryNoContent(t *testing.T) {
	stub := &stubLLMClient{response: `<section key="purpose" changed="false"></section>`}
	r := NewRevise(stub, 0)

	resp, err := r.Generate(context.Background(), ReviseRequest{
		Instruction: "no-op request",
		Sections:    []ReviseSection{{Key: "purpose", Title: "Purpose", Content: "Body."}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Sections) != 1 || resp.Sections[0].Changed || resp.Sections[0].Content != "" {
		t.Fatalf("expected one unchanged section with empty content, got %+v", resp.Sections)
	}
}

func TestRevise_noNewSectionsYieldsNilSlice(t *testing.T) {
	stub := &stubLLMClient{response: `<section key="purpose" changed="false"></section>`}
	r := NewRevise(stub, 0)

	resp, err := r.Generate(context.Background(), ReviseRequest{
		Instruction: "no-op request",
		Sections:    []ReviseSection{{Key: "purpose", Title: "Purpose", Content: "Body."}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.NewSections) != 0 {
		t.Fatalf("expected no new sections, got %+v", resp.NewSections)
	}
}

func TestRevise_requiresAtLeastOneSection(t *testing.T) {
	r := NewRevise(&stubLLMClient{response: "irrelevant"}, 0)
	_, err := r.Generate(context.Background(), ReviseRequest{Instruction: "do something"})
	if err == nil {
		t.Fatal("expected error when no sections are supplied")
	}
}

func TestRevise_requiresInstruction(t *testing.T) {
	r := NewRevise(&stubLLMClient{response: "irrelevant"}, 0)
	_, err := r.Generate(context.Background(), ReviseRequest{
		Sections: []ReviseSection{{Key: "purpose", Title: "Purpose", Content: "Body."}},
	})
	if err == nil {
		t.Fatal("expected error when instruction is missing")
	}
}

func TestNewRevise_nonPositiveMaxTokensFallsBackToDefault(t *testing.T) {
	stub := &stubLLMClient{response: `<section key="purpose" changed="false"></section>`}
	r := NewRevise(stub, -1)

	_, err := r.Generate(context.Background(), ReviseRequest{
		Instruction: "no-op",
		Sections:    []ReviseSection{{Key: "purpose", Title: "Purpose", Content: "Body."}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.lastReq.MaxTokens != DefaultReviseMaxTokens {
		t.Fatalf("expected MaxTokens to fall back to %d, got %d", DefaultReviseMaxTokens, stub.lastReq.MaxTokens)
	}
	if stub.lastReq.Operation != "revise" {
		t.Fatalf("expected Operation label %q, got %q", "revise", stub.lastReq.Operation)
	}
}

func TestBuildReviseUserPrompt_includesInstructionAndSections(t *testing.T) {
	prompt := buildReviseUserPrompt(ReviseRequest{
		Instruction: "add a whistleblower clause",
		Sections: []ReviseSection{
			{Key: "purpose", Title: "Purpose", Content: "This policy governs reporting.", Order: 1},
		},
	})
	if !strings.Contains(prompt, "add a whistleblower clause") {
		t.Fatal("expected instruction in prompt")
	}
	if !strings.Contains(prompt, "This policy governs reporting.") {
		t.Fatal("expected section content in prompt")
	}
	if !strings.Contains(prompt, `key="purpose"`) {
		t.Fatal("expected section key in prompt")
	}
}
