// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"context"
	"strings"
	"testing"
)

func TestReviewSystemPromptNeverMutatesAndTreatsInputAsData(t *testing.T) {
	if !strings.Contains(reviewSystemPrompt, "NEVER edit") {
		t.Fatal("review system prompt must forbid editing/mutating the policy")
	}
	if !strings.Contains(reviewSystemPrompt, "never instructions") {
		t.Fatal("review system prompt must warn that draft/standards/related-policy text is DATA, not instructions")
	}
	if !strings.Contains(reviewSystemPrompt, "blocker") || !strings.Contains(reviewSystemPrompt, "warn") || !strings.Contains(reviewSystemPrompt, "info") {
		t.Fatal("review system prompt must define the info/warn/blocker severity scale")
	}
}

func TestReview_parsesFindingsWithSectionSeverityAndSuggestion(t *testing.T) {
	stub := &stubLLMClient{response: `<finding section="purpose" severity="blocker">
The purpose section omits the mandatory citation required by the assigned standard.
<suggestion>Add a citation to the referenced standard in the purpose section.</suggestion>
</finding>
<finding section="scope" severity="warn">
Scope language is ambiguous about contractor coverage.
</finding>`}
	r := NewReview(stub)

	resp, err := r.Generate(context.Background(), ReviewRequest{
		ActorUserID: "user-1",
		CategoryID:  "cat-hr",
		Title:       "Onboarding Policy",
		Sections: []ReviewSection{
			{Key: "purpose", Title: "Purpose", Content: "This policy establishes onboarding requirements."},
			{Key: "scope", Title: "Scope", Content: "Applies to all staff."},
		},
		StandardsRefs:     []string{"ISO 27001 §7.2"},
		RelatedPolicyRefs: []string{"Contractor Access Policy"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %d: %+v", len(resp.Findings), resp.Findings)
	}

	f0 := resp.Findings[0]
	if f0.SectionKey != "purpose" || f0.Severity != SeverityBlocker {
		t.Fatalf("finding 0: got %+v", f0)
	}
	if f0.Finding == "" || strings.Contains(f0.Finding, "<suggestion>") {
		t.Fatalf("finding 0: text must be non-empty and stripped of the suggestion sub-block, got %q", f0.Finding)
	}
	if f0.Suggestion == "" {
		t.Fatalf("finding 0: expected a suggestion, got none")
	}

	f1 := resp.Findings[1]
	if f1.SectionKey != "scope" || f1.Severity != SeverityWarn {
		t.Fatalf("finding 1: got %+v", f1)
	}
	if f1.Suggestion != "" {
		t.Fatalf("finding 1: expected no suggestion, got %q", f1.Suggestion)
	}
}

func TestReview_documentWideFindingHasEmptySectionKey(t *testing.T) {
	stub := &stubLLMClient{response: `<finding section="" severity="info">
The overall tone is inconsistent between sections.
</finding>`}
	r := NewReview(stub)

	resp, err := r.Generate(context.Background(), ReviewRequest{
		Sections: []ReviewSection{{Key: "purpose", Title: "Purpose", Content: "Body."}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Findings) != 1 || resp.Findings[0].SectionKey != "" {
		t.Fatalf("expected 1 document-wide finding with empty section key, got %+v", resp.Findings)
	}
	if resp.Findings[0].Severity != SeverityInfo {
		t.Fatalf("expected info severity, got %q", resp.Findings[0].Severity)
	}
}

func TestReview_malformedSeverityFallsBackToInfoRatherThanDroppingFinding(t *testing.T) {
	stub := &stubLLMClient{response: `<finding section="purpose" severity="critical">
Something is wrong but the model used a severity we don't recognize.
</finding>`}
	r := NewReview(stub)

	resp, err := r.Generate(context.Background(), ReviewRequest{
		Sections: []ReviewSection{{Key: "purpose", Title: "Purpose", Content: "Body."}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Findings) != 1 {
		t.Fatalf("expected the finding to survive an unrecognized severity, got %+v", resp.Findings)
	}
	if resp.Findings[0].Severity != SeverityInfo {
		t.Fatalf("expected fallback to info severity, got %q", resp.Findings[0].Severity)
	}
}

func TestReview_noFindingsYieldsEmptyResponseNotError(t *testing.T) {
	stub := &stubLLMClient{response: "no issues found"}
	r := NewReview(stub)

	resp, err := r.Generate(context.Background(), ReviewRequest{
		Sections: []ReviewSection{{Key: "purpose", Title: "Purpose", Content: "Body."}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Findings) != 0 {
		t.Fatalf("expected zero findings, got %+v", resp.Findings)
	}
}

func TestReview_requiresAtLeastOneSection(t *testing.T) {
	r := NewReview(&stubLLMClient{response: "irrelevant"})
	_, err := r.Generate(context.Background(), ReviewRequest{})
	if err == nil {
		t.Fatal("expected error when no sections are supplied")
	}
}

func TestBuildReviewUserPrompt_includesTitleSectionsAndReferences(t *testing.T) {
	prompt := buildReviewUserPrompt(ReviewRequest{
		Title: "Remote Work Policy",
		Sections: []ReviewSection{
			{Key: "eligibility", Title: "Eligibility", Content: "Full-time staff may work remotely."},
		},
		StandardsRefs:     []string{"NIST 800-53 AC-17"},
		RelatedPolicyRefs: []string{"Equipment Policy v2"},
	})
	if !strings.Contains(prompt, "Remote Work Policy") {
		t.Fatal("expected working title in prompt")
	}
	if !strings.Contains(prompt, "Full-time staff may work remotely.") {
		t.Fatal("expected section content in prompt")
	}
	if !strings.Contains(prompt, "NIST 800-53 AC-17") {
		t.Fatal("expected standards ref in prompt")
	}
	if !strings.Contains(prompt, "Equipment Policy v2") {
		t.Fatal("expected related policy ref in prompt")
	}
}
