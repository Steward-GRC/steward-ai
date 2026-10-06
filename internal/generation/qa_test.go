// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"context"
	"strings"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// QA_SystemPrompt is checked here to confirm the injection-guard text is present.
func TestQASystemPromptContainsInjectionGuard(t *testing.T) {
	if !strings.Contains(qaSystemPrompt, "DATA") {
		t.Fatal("QA system prompt must instruct the model to treat retrieved text as DATA")
	}
	if !strings.Contains(qaSystemPrompt, "no authorized source") {
		t.Fatal("QA system prompt must include the 'no authorized source' fallback instruction")
	}
}

func TestAnswerGrounded_noSources(t *testing.T) {
	stub := &stubLLMClient{response: "no authorized source found"}
	qa := NewQA(stub)

	resp, err := qa.Answer(context.Background(), AnswerRequest{
		Question: "What is the travel policy?",
		Chunks:   []store.SearchResult{}, // no authorized results
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.NoAuthorizedSource {
		t.Fatal("expected NoAuthorizedSource=true when no chunks provided")
	}
}

func TestAnswerGrounded_withSources(t *testing.T) {
	stub := &stubLLMClient{response: "Employees may travel up to 5000 km per year."}
	qa := NewQA(stub)

	chunks := []store.SearchResult{
		{
			Chunk: store.Chunk{
				PolicyID:    "pol-travel",
				VersionID:   "ver-3",
				VersionNo:   3,
				SectionKey:  "allowances",
				PolicyTitle: "Travel Policy",
				ContentText: "Employees may travel up to 5000 km per year.",
				CategoryID:  "hr",
				Sensitivity: "standard",
			},
			Distance: 0.05,
		},
	}

	resp, err := qa.Answer(context.Background(), AnswerRequest{
		Question: "How much can I travel?",
		Chunks:   chunks,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.NoAuthorizedSource {
		t.Fatal("expected NoAuthorizedSource=false when chunks are provided")
	}
	if resp.Answer == "" {
		t.Fatal("expected a non-empty answer")
	}
	if len(resp.Citations) != 1 {
		t.Fatalf("expected 1 citation, got %d", len(resp.Citations))
	}
	if resp.Citations[0].PolicyID != "pol-travel" {
		t.Fatalf("citation PolicyID: got %q", resp.Citations[0].PolicyID)
	}
}

// TestAnswerGrounded_citationCarriesDocumentType verifies the cited chunk's
// DocumentType surfaces on the citation so the interface can label the source
// a procedure or a policy. A procedure chunk yields a PROCEDURE citation; a
// chunk with no DocumentType defaults to POLICY.
func TestAnswerGrounded_citationCarriesDocumentType(t *testing.T) {
	stub := &stubLLMClient{response: "Follow the onboarding steps."}
	qa := NewQA(stub)

	chunks := []store.SearchResult{
		{Chunk: store.Chunk{
			PolicyID: "prc-onboard", VersionID: "vp", VersionNo: 1, SectionKey: "steps",
			PolicyTitle: "Onboarding Procedure", ContentText: "Follow the onboarding steps.",
			CategoryID: "hr", Sensitivity: "standard", DocumentType: "PROCEDURE",
		}},
		{Chunk: store.Chunk{
			PolicyID: "pol-hr", VersionID: "vq", VersionNo: 1, SectionKey: "scope",
			PolicyTitle: "HR Policy", ContentText: "Applies to all employees.",
			CategoryID: "hr", Sensitivity: "standard", // DocumentType empty → POLICY
		}},
	}

	resp, err := qa.Answer(context.Background(), AnswerRequest{Question: "onboarding?", Chunks: chunks})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byPolicy := map[string]string{}
	for _, c := range resp.Citations {
		byPolicy[c.PolicyID] = c.DocumentType
	}
	if byPolicy["prc-onboard"] != "PROCEDURE" {
		t.Fatalf("expected procedure citation DocumentType PROCEDURE, got %q", byPolicy["prc-onboard"])
	}
	if byPolicy["pol-hr"] != "POLICY" {
		t.Fatalf("expected policy citation DocumentType to default to POLICY, got %q", byPolicy["pol-hr"])
	}
}

func TestAnswerGrounded_segmentsAttributedToBestChunk(t *testing.T) {
	// Two sentences: the first matches the travel chunk, the second matches
	// the parking chunk. Each answer segment should be attributed to the
	// chunk it shares the most terms with.
	stub := &stubLLMClient{response: "Employees may travel up to 5000 kilometers per year. Parking permits require manager approval."}
	qa := NewQA(stub)

	chunks := []store.SearchResult{
		{Chunk: store.Chunk{ID: "chunk-travel", PolicyID: "pol-travel", VersionID: "ver-3", VersionNo: 3, SectionKey: "allowances", ChunkIndex: 2, PolicyTitle: "Travel Policy", ContentText: "Employees may travel up to 5000 kilometers per year.", CategoryID: "hr", Sensitivity: "standard"}, Distance: 0.05},
		{Chunk: store.Chunk{ID: "chunk-parking", PolicyID: "pol-parking", VersionID: "ver-1", VersionNo: 1, SectionKey: "permits", ChunkIndex: 0, PolicyTitle: "Parking Policy", ContentText: "Parking permits require manager approval.", CategoryID: "hr", Sensitivity: "standard"}, Distance: 0.09},
	}

	resp, err := qa.Answer(context.Background(), AnswerRequest{Question: "travel and parking?", Chunks: chunks})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Citations carry the chunk id/index of the best-scoring chunk per policy.
	if len(resp.Citations) != 2 {
		t.Fatalf("expected 2 citations, got %d", len(resp.Citations))
	}
	if resp.Citations[0].ChunkID != "chunk-travel" || resp.Citations[0].ChunkIndex != 2 {
		t.Fatalf("expected travel citation to carry chunk id/index, got %+v", resp.Citations[0])
	}

	if len(resp.Segments) != 2 {
		t.Fatalf("expected 2 attributed segments (one per sentence), got %d: %+v", len(resp.Segments), resp.Segments)
	}
	// Segment byte offsets must bracket real answer text.
	for _, seg := range resp.Segments {
		if seg.Start < 0 || seg.End > len(resp.Answer) || seg.Start >= seg.End {
			t.Fatalf("segment offsets out of range: %+v (answer len %d)", seg, len(resp.Answer))
		}
		if len(seg.Sources) != 1 {
			t.Fatalf("expected exactly 1 source per segment, got %+v", seg)
		}
	}
	// First sentence -> travel chunk; second -> parking chunk.
	if got := resp.Segments[0].Sources[0].ChunkID; got != "chunk-travel" {
		t.Fatalf("expected first segment attributed to chunk-travel, got %q", got)
	}
	if got := resp.Segments[1].Sources[0].ChunkID; got != "chunk-parking" {
		t.Fatalf("expected second segment attributed to chunk-parking, got %q", got)
	}
	if resp.Segments[0].Sources[0].SectionKey != "allowances" {
		t.Fatalf("expected section key carried on the segment source, got %q", resp.Segments[0].Sources[0].SectionKey)
	}
}

func TestAnswerGrounded_citationsDedupedByPolicy(t *testing.T) {
	stub := &stubLLMClient{response: "Devices valued at $500 or more must be asset-tagged."}
	qa := NewQA(stub)

	// Nearest-first: pol-asset appears in 3 chunks (best section first), pol-it
	// in 2 chunks. Without dedup this would yield 5 citations for 2 policies.
	chunks := []store.SearchResult{
		{Chunk: store.Chunk{PolicyID: "pol-asset", VersionID: "ver-2", VersionNo: 2, SectionKey: "threshold", PolicyTitle: "Asset Tagging", ContentText: "Devices >= $500 must be tagged.", CategoryID: "it", Sensitivity: "standard"}, Distance: 0.03},
		{Chunk: store.Chunk{PolicyID: "pol-it", VersionID: "ver-1", VersionNo: 1, SectionKey: "scope", PolicyTitle: "IT Policy", ContentText: "Applies to all company devices.", CategoryID: "it", Sensitivity: "standard"}, Distance: 0.06},
		{Chunk: store.Chunk{PolicyID: "pol-asset", VersionID: "ver-2", VersionNo: 2, SectionKey: "procedure", PolicyTitle: "Asset Tagging", ContentText: "Tag within 5 business days.", CategoryID: "it", Sensitivity: "standard"}, Distance: 0.09},
		{Chunk: store.Chunk{PolicyID: "pol-asset", VersionID: "ver-2", VersionNo: 2, SectionKey: "audit", PolicyTitle: "Asset Tagging", ContentText: "Audited quarterly.", CategoryID: "it", Sensitivity: "standard"}, Distance: 0.12},
		{Chunk: store.Chunk{PolicyID: "pol-it", VersionID: "ver-1", VersionNo: 1, SectionKey: "roles", PolicyTitle: "IT Policy", ContentText: "IT owns the register.", CategoryID: "it", Sensitivity: "standard"}, Distance: 0.15},
	}

	resp, err := qa.Answer(context.Background(), AnswerRequest{Question: "What is the value of the device needed to asset tag?", Chunks: chunks})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Citations) != 2 {
		t.Fatalf("expected 2 citations (one per unique policy), got %d: %+v", len(resp.Citations), resp.Citations)
	}
	// Relevance order preserved: best-scoring policy first.
	if resp.Citations[0].PolicyID != "pol-asset" || resp.Citations[1].PolicyID != "pol-it" {
		t.Fatalf("citation order not preserved by relevance: got %q then %q", resp.Citations[0].PolicyID, resp.Citations[1].PolicyID)
	}
	// Kept the best (first-seen) section per policy, not a later chunk.
	if resp.Citations[0].SectionKey != "threshold" {
		t.Fatalf("expected best section 'threshold' for pol-asset, got %q", resp.Citations[0].SectionKey)
	}
	if resp.Citations[1].SectionKey != "scope" {
		t.Fatalf("expected best section 'scope' for pol-it, got %q", resp.Citations[1].SectionKey)
	}
}

func TestAnswerGrounded_sensitiveDetectedOnDuplicatePolicyChunk(t *testing.T) {
	stub := &stubLLMClient{response: "answer"}
	qa := NewQA(stub)

	// Same policy twice; the SECOND (deduped-away) chunk is the sensitive one.
	// hasSensitive must still be true — sensitivity is scanned across all chunks.
	chunks := []store.SearchResult{
		{Chunk: store.Chunk{PolicyID: "pol-x", VersionID: "v1", VersionNo: 1, SectionKey: "a", PolicyTitle: "X", ContentText: "public part", CategoryID: "g", Sensitivity: "standard"}, Distance: 0.02},
		{Chunk: store.Chunk{PolicyID: "pol-x", VersionID: "v1", VersionNo: 1, SectionKey: "b", PolicyTitle: "X", ContentText: "sensitive part", CategoryID: "g", Sensitivity: "sensitive"}, Distance: 0.04},
	}

	resp, err := qa.Answer(context.Background(), AnswerRequest{Question: "q", Chunks: chunks})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Citations) != 1 {
		t.Fatalf("expected 1 citation, got %d", len(resp.Citations))
	}
	if !resp.HasSensitiveSource {
		t.Fatal("expected HasSensitiveSource=true even though the sensitive chunk was deduped out of citations")
	}
}

func TestAnswerGrounded_sensitiveChunkFlagged(t *testing.T) {
	stub := &stubLLMClient{response: "Sensitive information here."}
	qa := NewQA(stub)

	chunks := []store.SearchResult{
		{
			Chunk: store.Chunk{
				PolicyID:    "pol-exec",
				VersionID:   "ver-1",
				VersionNo:   1,
				SectionKey:  "compensation",
				PolicyTitle: "Executive Compensation",
				ContentText: "Executive bonuses are calculated based on...",
				CategoryID:  "exec",
				Sensitivity: "sensitive",
			},
			Distance: 0.08,
		},
	}

	resp, err := qa.Answer(context.Background(), AnswerRequest{
		Question: "What are executive bonuses?",
		Chunks:   chunks,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.HasSensitiveSource {
		t.Fatal("expected HasSensitiveSource=true when a sensitive chunk is in results")
	}
}
