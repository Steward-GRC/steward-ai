// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/provider"
)

// recordingLLMClient records every provider.Request it receives, in call
// order, and returns one canned response per call (consumed in order, by
// call index) — so draft's per-section tests can assert one Complete call
// PER requested section, each call's prompt shape, and that the returned
// DraftedSections line up with request order rather than call/response
// order.
type recordingLLMClient struct {
	responses []string
	requests  []provider.Request
}

func (r *recordingLLMClient) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	idx := len(r.requests)
	r.requests = append(r.requests, req)
	if idx < len(r.responses) {
		return provider.Response{Text: r.responses[idx]}, nil
	}
	return provider.Response{Text: fmt.Sprintf("default response %d", idx)}, nil
}

func TestDraftSectionSystemPromptConstrainsToOutlineAndHumanReview(t *testing.T) {
	if !strings.Contains(draftSectionSystemPrompt, "OUTLINE") {
		t.Fatal("draft section system prompt must frame the template sections as a fixed outline")
	}
	if !strings.Contains(draftSectionSystemPrompt, "human review") {
		t.Fatal("draft section system prompt must frame output as a suggestion for human review")
	}
	if !strings.Contains(draftSectionSystemPrompt, "never instructions") {
		t.Fatal("draft section system prompt must warn that brief/guidance/hints/reference documents are DATA, not instructions")
	}
	if !strings.Contains(draftSectionSystemPrompt, "PRIMARY SOURCE") {
		t.Fatal("draft section system prompt must instruct the model to treat reference documents as the primary source")
	}
	if !strings.Contains(draftSectionSystemPrompt, "EXPAND") {
		t.Fatal("draft section system prompt must instruct the model to expand on source material rather than summarize it")
	}
}

func TestDraft_makesOneCompleteCallPerRequestedSection(t *testing.T) {
	llm := &recordingLLMClient{responses: []string{
		"This policy establishes onboarding requirements for all new employees.",
		"Applies to all full-time and part-time staff.",
	}}
	d := NewDraft(llm, DefaultDraftSectionMaxTokens)

	resp, err := d.Generate(context.Background(), DraftRequest{
		ActorUserID: "user-1",
		CategoryID:  "cat-hr",
		Title:       "Onboarding Policy",
		Brief:       "Draft an onboarding policy covering training deadlines.",
		Sections: []DraftSection{
			{Key: "purpose", Title: "Purpose", Order: 1},
			{Key: "scope", Title: "Scope", Guidance: "Who this policy applies to.", Order: 2},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(llm.requests) != 2 {
		t.Fatalf("expected exactly 2 Complete calls (one per requested section), got %d", len(llm.requests))
	}

	if len(resp.Sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(resp.Sections))
	}
	if resp.Sections[0].SectionKey != "purpose" || resp.Sections[0].Content != "This policy establishes onboarding requirements for all new employees." {
		t.Fatalf("purpose section: got %+v", resp.Sections[0])
	}
	if resp.Sections[1].SectionKey != "scope" || resp.Sections[1].Content != "Applies to all full-time and part-time staff." {
		t.Fatalf("scope section: got %+v", resp.Sections[1])
	}
}

func TestDraft_everyCallUsesTheConfiguredPerSectionMaxTokens(t *testing.T) {
	const configured = 24000 // an explicit, non-default value so the assertion is deterministic
	llm := &recordingLLMClient{responses: []string{"purpose body", "scope body"}}
	d := NewDraft(llm, configured)

	_, err := d.Generate(context.Background(), DraftRequest{
		Brief: "brief",
		Sections: []DraftSection{
			{Key: "purpose", Title: "Purpose", Order: 1},
			{Key: "scope", Title: "Scope", Order: 2},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, req := range llm.requests {
		if req.MaxTokens != configured {
			t.Fatalf("call %d: expected MaxTokens=%d, got %d", i, configured, req.MaxTokens)
		}
		if req.Operation != "draft" {
			t.Fatalf("call %d: expected Operation=\"draft\", got %q", i, req.Operation)
		}
		if !req.EnableWebFetch {
			t.Fatalf("call %d: expected EnableWebFetch=true", i)
		}
	}
}

func TestNewDraft_nonPositiveMaxTokensFallsBackToDefault(t *testing.T) {
	llm := &recordingLLMClient{responses: []string{"body"}}
	for _, mt := range []int{0, -1} {
		d := NewDraft(llm, mt)
		if d.maxTokens != DefaultDraftSectionMaxTokens {
			t.Fatalf("NewDraft(llm, %d): expected maxTokens to fall back to %d, got %d", mt, DefaultDraftSectionMaxTokens, d.maxTokens)
		}
	}
}

func TestDraftSectionMaxTokensFromEnv(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		want int
	}{
		{"unset falls back to default", false, "", DefaultDraftSectionMaxTokens},
		{"empty falls back to default", true, "", DefaultDraftSectionMaxTokens},
		{"non-numeric falls back to default", true, "lots", DefaultDraftSectionMaxTokens},
		{"zero falls back to default", true, "0", DefaultDraftSectionMaxTokens},
		{"negative falls back to default", true, "-100", DefaultDraftSectionMaxTokens},
		{"valid value is honored", true, "48000", 48000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("AI_DRAFT_SECTION_MAX_TOKENS", tc.val)
			} else {
				_ = os.Unsetenv("AI_DRAFT_SECTION_MAX_TOKENS")
			}
			if got := DraftSectionMaxTokensFromEnv(); got != tc.want {
				t.Fatalf("DraftSectionMaxTokensFromEnv() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDraft_eachCallPromptIncludesFullSectionTitleListAndReferenceDocuments(t *testing.T) {
	llm := &recordingLLMClient{responses: []string{"purpose body", "scope body", "equipment body"}}
	d := NewDraft(llm, DefaultDraftSectionMaxTokens)

	_, err := d.Generate(context.Background(), DraftRequest{
		Brief: "Cover onboarding end to end.",
		Sections: []DraftSection{
			{Key: "purpose", Title: "Purpose", Order: 1},
			{Key: "scope", Title: "Scope", Order: 2},
			{Key: "equipment", Title: "Equipment", Order: 3},
		},
		ReferenceDocuments: []ReferenceDocument{
			{Title: "Prior Onboarding Policy", Content: "New hires must complete training within 30 days and receive a laptop and badge."},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(llm.requests) != 3 {
		t.Fatalf("expected 3 Complete calls, got %d", len(llm.requests))
	}
	for i, req := range llm.requests {
		prompt := req.Messages[len(req.Messages)-1].Content
		for _, title := range []string{"Purpose", "Scope", "Equipment"} {
			if !strings.Contains(prompt, title) {
				t.Fatalf("call %d prompt missing section title %q from the full outline: %s", i, title, prompt)
			}
		}
		if !strings.Contains(prompt, "New hires must complete training within 30 days and receive a laptop and badge.") {
			t.Fatalf("call %d prompt missing reference document content", i)
		}
		if !strings.Contains(prompt, `title="Prior Onboarding Policy"`) {
			t.Fatalf("call %d prompt missing reference document title", i)
		}
	}

	// Each call's <current_section> must identify only its own section.
	wantCurrent := []string{`<current_section key="purpose"`, `<current_section key="scope"`, `<current_section key="equipment"`}
	for i, want := range wantCurrent {
		if !strings.Contains(llm.requests[i].Messages[0].Content, want) {
			t.Fatalf("call %d: expected prompt to contain %q, got: %s", i, want, llm.requests[i].Messages[0].Content)
		}
	}
}

func TestDraft_returnsSectionsInRequestOrderRegardlessOfCallOrder(t *testing.T) {
	llm := &recordingLLMClient{responses: []string{"purpose body", "scope body", "equipment body"}}
	d := NewDraft(llm, DefaultDraftSectionMaxTokens)

	resp, err := d.Generate(context.Background(), DraftRequest{
		Brief: "brief",
		Sections: []DraftSection{
			{Key: "purpose", Title: "Purpose", Order: 1},
			{Key: "scope", Title: "Scope", Order: 2},
			{Key: "equipment", Title: "Equipment", Order: 3},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(resp.Sections))
	}
	wantKeys := []string{"purpose", "scope", "equipment"}
	wantContent := []string{"purpose body", "scope body", "equipment body"}
	for i, s := range resp.Sections {
		if s.SectionKey != wantKeys[i] || s.Content != wantContent[i] {
			t.Fatalf("section %d: got %+v, want key=%q content=%q", i, s, wantKeys[i], wantContent[i])
		}
	}
}

func TestDraft_singleSectionCallFailureFailsTheWholeRequest(t *testing.T) {
	llm := &erroringLLMClient{failOnCall: 1} // fail on the second section's call
	d := NewDraft(llm, DefaultDraftSectionMaxTokens)

	_, err := d.Generate(context.Background(), DraftRequest{
		Brief: "brief",
		Sections: []DraftSection{
			{Key: "purpose", Title: "Purpose", Order: 1},
			{Key: "scope", Title: "Scope", Order: 2},
		},
	})
	if err == nil {
		t.Fatal("expected an error when a single section's Complete call fails")
	}
	if llm.calls != 2 {
		t.Fatalf("expected the failing call to be the 2nd (calls=2), got %d", llm.calls)
	}
}

// erroringLLMClient succeeds on every call except the zero-indexed
// failOnCall, which returns an error — used to verify Draft.Generate fails
// the whole request (rather than silently dropping a section) when one
// section's Complete call fails.
type erroringLLMClient struct {
	failOnCall int
	calls      int
}

func (e *erroringLLMClient) Complete(_ context.Context, _ provider.Request) (provider.Response, error) {
	idx := e.calls
	e.calls++
	if idx == e.failOnCall {
		return provider.Response{}, fmt.Errorf("simulated failure")
	}
	return provider.Response{Text: "ok"}, nil
}

func TestDraft_requiresAtLeastOneSection(t *testing.T) {
	d := NewDraft(&stubLLMClient{response: "irrelevant"}, DefaultDraftSectionMaxTokens)
	_, err := d.Generate(context.Background(), DraftRequest{Brief: "brief"})
	if err == nil {
		t.Fatal("expected error when no sections are supplied")
	}
}

func TestDraft_requiresNonEmptyBrief(t *testing.T) {
	d := NewDraft(&stubLLMClient{response: "irrelevant"}, DefaultDraftSectionMaxTokens)
	_, err := d.Generate(context.Background(), DraftRequest{
		Sections: []DraftSection{{Key: "purpose", Title: "Purpose", Order: 1}},
	})
	if err == nil {
		t.Fatal("expected error when brief is empty")
	}
}

func TestBuildDraftSectionUserPrompt_includesReferenceDocumentsWhenPresent(t *testing.T) {
	prompt := buildDraftSectionUserPrompt(DraftRequest{
		Brief: "Cover eligibility and equipment.",
		Sections: []DraftSection{
			{Key: "eligibility", Title: "Eligibility", Order: 1},
		},
		ReferenceDocuments: []ReferenceDocument{
			{Title: "Prior Remote Work Policy", Content: "Employees must log hours weekly."},
		},
	}, DraftSection{Key: "eligibility", Title: "Eligibility", Order: 1})
	if !strings.Contains(prompt, "<reference_documents>") || !strings.Contains(prompt, "</reference_documents>") {
		t.Fatal("expected a <reference_documents> block when reference documents are present")
	}
	if !strings.Contains(prompt, `title="Prior Remote Work Policy"`) {
		t.Fatal("expected the reference document title in the prompt")
	}
	if !strings.Contains(prompt, "Employees must log hours weekly.") {
		t.Fatal("expected the reference document content in the prompt")
	}
}

func TestBuildDraftSectionUserPrompt_omitsReferenceDocumentsBlockWhenAbsent(t *testing.T) {
	prompt := buildDraftSectionUserPrompt(DraftRequest{
		Brief: "Cover eligibility and equipment.",
		Sections: []DraftSection{
			{Key: "eligibility", Title: "Eligibility", Order: 1},
		},
	}, DraftSection{Key: "eligibility", Title: "Eligibility", Order: 1})
	if strings.Contains(prompt, "<reference_documents>") {
		t.Fatal("expected no <reference_documents> block when no reference documents are supplied")
	}
}

func TestBuildDraftSectionUserPrompt_includesGuidanceTitleAndReferenceHints(t *testing.T) {
	section := DraftSection{Key: "eligibility", Title: "Eligibility", Guidance: "List who may work remotely.", Order: 1}
	prompt := buildDraftSectionUserPrompt(DraftRequest{
		Title:          "Remote Work Policy",
		Brief:          "Cover eligibility and equipment.",
		Sections:       []DraftSection{section},
		ReferenceHints: []string{"Prior Remote Work Policy v3"},
	}, section)
	if !strings.Contains(prompt, "Remote Work Policy") {
		t.Fatal("expected working title in prompt")
	}
	if !strings.Contains(prompt, "List who may work remotely.") {
		t.Fatal("expected section guidance in prompt")
	}
	if !strings.Contains(prompt, "Cover eligibility and equipment.") {
		t.Fatal("expected brief in prompt")
	}
	if !strings.Contains(prompt, "Prior Remote Work Policy v3") {
		t.Fatal("expected reference hint in prompt")
	}
}

func TestBuildDraftSectionUserPrompt_identifiesTheCurrentSectionAmongOthers(t *testing.T) {
	sections := []DraftSection{
		{Key: "purpose", Title: "Purpose", Order: 1},
		{Key: "scope", Title: "Scope", Order: 2},
	}
	prompt := buildDraftSectionUserPrompt(DraftRequest{
		Brief:    "brief",
		Sections: sections,
	}, sections[1])

	if !strings.Contains(prompt, `<current_section key="scope" title="Scope">`) {
		t.Fatalf("expected a <current_section> tag identifying scope, got: %s", prompt)
	}
	// The full outline (both sections) must still be present for boundary context.
	if !strings.Contains(prompt, `<section key="purpose"`) || !strings.Contains(prompt, `<section key="scope"`) {
		t.Fatalf("expected the full section outline in the prompt, got: %s", prompt)
	}
}
