// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"context"
	"strings"
	"testing"
)

func sampleEnrichments() EnrichmentContext {
	return EnrichmentContext{
		Definitions: []AttachedDefinition{{Term: "PHI", Definition: "Protected Health Information."}},
		References: []AttachedReference{
			{Label: "NIST 800-53", Kind: "STANDARD", Citation: "AC-6"},
			{Label: "OWASP", Kind: "LINK", URL: "https://owasp.org"},
		},
		RelatedPolicies: []AttachedRelatedPolicy{{PolicyID: "p2", Title: "Secure Coding", Summary: "How we write secure code."}},
	}
}

func TestEnrichmentContextRender(t *testing.T) {
	got := sampleEnrichments().render()
	for _, want := range []string{
		"<attached_enrichments>",
		`<definition term="PHI">`,
		"Protected Health Information.",
		`<reference label="NIST 800-53"`,
		"AC-6",
		"https://owasp.org",
		`<related title="Secure Coding">`,
		"How we write secure code.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("render() missing %q in:\n%s", want, got)
		}
	}
}

func TestEnrichmentContextRenderEmpty(t *testing.T) {
	var c EnrichmentContext
	if got := c.render(); got != "" {
		t.Fatalf("empty enrichment should render empty, got %q", got)
	}
	if !c.IsEmpty() {
		t.Fatalf("expected IsEmpty for zero value")
	}
}

// TestDraftPromptIncludesEnrichments proves the ingest block reaches the draft
// generation prompt as grounding.
func TestDraftPromptIncludesEnrichments(t *testing.T) {
	stub := &stubLLMClient{response: "section body"}
	d := NewDraft(stub, 0)
	_, err := d.Generate(context.Background(), DraftRequest{
		Brief:       "brief",
		Sections:    []DraftSection{{Key: "purpose", Title: "Purpose", Order: 1}},
		Enrichments: sampleEnrichments(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prompt := stub.lastReq.Messages[0].Content
	if !strings.Contains(prompt, "<attached_enrichments>") || !strings.Contains(prompt, "Secure Coding") {
		t.Fatalf("draft prompt missing attached enrichments:\n%s", prompt)
	}
}

func TestReviewPromptIncludesEnrichments(t *testing.T) {
	stub := &stubLLMClient{response: ""}
	r := NewReview(stub)
	_, err := r.Generate(context.Background(), ReviewRequest{
		Sections:    []ReviewSection{{Key: "s1", Title: "S1", Content: "content"}},
		Enrichments: sampleEnrichments(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stub.lastReq.Messages[0].Content, "<attached_enrichments>") {
		t.Fatalf("review prompt missing attached enrichments")
	}
}

func TestRevisePromptIncludesEnrichments(t *testing.T) {
	stub := &stubLLMClient{response: ""}
	r := NewRevise(stub, 0)
	_, err := r.Generate(context.Background(), ReviseRequest{
		Instruction: "tighten wording",
		Sections:    []ReviseSection{{Key: "s1", Title: "S1", Content: "content", Order: 1}},
		Enrichments: sampleEnrichments(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stub.lastReq.Messages[0].Content, "<attached_enrichments>") {
		t.Fatalf("revise prompt missing attached enrichments")
	}
}

// TestJobInputEnrichmentPassthrough proves the ingest context survives the
// job-input round trip and reaches the generation request.
func TestJobInputEnrichmentPassthrough(t *testing.T) {
	enr := sampleEnrichments()
	in := DraftJobInput{
		Brief:       "b",
		Sections:    []DraftJobInputSection{{Key: "s1", Title: "S1"}},
		Enrichments: &enr,
	}
	raw, err := MarshalJobInput(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := UnmarshalDraftJobInput([]byte(raw))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	req := decoded.ToDraftRequest("u1", "g1", "m1")
	if len(req.Enrichments.Definitions) != 1 || req.Enrichments.Definitions[0].Term != "PHI" {
		t.Fatalf("enrichments lost across job-input round trip: %+v", req.Enrichments)
	}
}
