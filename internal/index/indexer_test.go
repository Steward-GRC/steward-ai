// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// stubEmbedder returns fixed-dimension zero vectors.
type stubEmbedder struct{}

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, 384)
	}
	return out, nil
}

// stubStore records upserted chunks and deletes.
type stubStore struct {
	upserted        []store.Chunk
	deletedVersions []string
	deletedPolicies []string
}

func (s *stubStore) UpsertChunk(_ context.Context, c store.Chunk) error {
	s.upserted = append(s.upserted, c)
	return nil
}

func (s *stubStore) DeleteByVersionID(_ context.Context, versionID string) error {
	s.deletedVersions = append(s.deletedVersions, versionID)
	return nil
}

func (s *stubStore) DeleteByPolicyID(_ context.Context, policyID string) error {
	s.deletedPolicies = append(s.deletedPolicies, policyID)
	return nil
}

func TestIndexPublishedVersion(t *testing.T) {
	em := &stubEmbedder{}
	st := &stubStore{}
	idx := New(em, st, 50, 10) // chunkSize=50, overlap=10

	pv := PolicyVersionContent{
		PolicyID:    "pol-001",
		VersionID:   "ver-abc",
		VersionNo:   3,
		CategoryID:  "cat-hr",
		Sensitivity: "standard",
		PolicyTitle: "HR Onboarding",
		Sections: []SectionContent{
			{
				Key:  "scope",
				Text: "This policy applies to all full-time employees hired after January 1 2025. It does not apply to contractors or part-time staff.",
			},
		},
	}

	res, err := idx.IndexVersion(context.Background(), pv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.upserted) == 0 {
		t.Fatal("expected at least one chunk to be upserted")
	}
	if res.ChunkCount != len(st.upserted) {
		t.Fatalf("IndexVersion returned chunk count %d, want %d", res.ChunkCount, len(st.upserted))
	}
	if len(res.Centroid) != 384 {
		t.Fatalf("IndexVersion centroid dim: got %d, want 384", len(res.Centroid))
	}
	// All chunks must carry the correct metadata.
	for _, c := range st.upserted {
		if c.PolicyID != "pol-001" {
			t.Errorf("chunk PolicyID: got %q", c.PolicyID)
		}
		if c.CategoryID != "cat-hr" {
			t.Errorf("chunk CategoryID: got %q", c.CategoryID)
		}
		if c.Sensitivity != "standard" {
			t.Errorf("chunk Sensitivity: got %q", c.Sensitivity)
		}
		if len(c.Embedding) != 384 {
			t.Errorf("chunk Embedding dim: got %d", len(c.Embedding))
		}
	}
}

// TestIndexVersion_procedureDocumentTypeDenormalized verifies a procedure
// version's DocumentType is normalized onto every chunk, so retrieval can
// label the source as a procedure.
func TestIndexVersion_procedureDocumentTypeDenormalized(t *testing.T) {
	st := &stubStore{}
	idx := New(&stubEmbedder{}, st, 50, 10)

	pv := PolicyVersionContent{
		PolicyID: "prc-001", VersionID: "ver-p", VersionNo: 1,
		CategoryID: "cat-hr", Sensitivity: "standard", PolicyTitle: "Onboarding Procedure",
		DocumentType: "PROCEDURE",
		Sections:     []SectionContent{{Key: "steps", Text: "First do this then do that for every new hire."}},
	}
	if _, err := idx.IndexVersion(context.Background(), pv); err != nil {
		t.Fatalf("IndexVersion: %v", err)
	}
	if len(st.upserted) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for _, c := range st.upserted {
		if c.DocumentType != "PROCEDURE" {
			t.Fatalf("chunk DocumentType: got %q, want PROCEDURE", c.DocumentType)
		}
	}
}

// TestIndexVersion_emptyDocumentTypeDefaultsToPolicy verifies the unchanged
// policy path: a policy.published event carries no DocumentType, so the indexer
// normalizes empty → POLICY on every chunk.
func TestIndexVersion_emptyDocumentTypeDefaultsToPolicy(t *testing.T) {
	st := &stubStore{}
	idx := New(&stubEmbedder{}, st, 50, 10)

	pv := PolicyVersionContent{
		PolicyID: "pol-1", VersionID: "v-1", VersionNo: 1,
		CategoryID: "g", Sensitivity: "standard", PolicyTitle: "P",
		// DocumentType intentionally unset (policy path).
		Sections: []SectionContent{{Key: "s", Text: "All employees must comply with this policy."}},
	}
	if _, err := idx.IndexVersion(context.Background(), pv); err != nil {
		t.Fatalf("IndexVersion: %v", err)
	}
	if len(st.upserted) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for _, c := range st.upserted {
		if c.DocumentType != "POLICY" {
			t.Fatalf("chunk DocumentType: got %q, want POLICY", c.DocumentType)
		}
	}
}

// seededEmbedder returns a distinct deterministic vector per input text so a
// centroid (mean) is meaningfully non-trivial and assertable. The i-th text of
// each batch gets the value (base+i) in component 0.
type seededEmbedder struct{ base float32 }

func (s *seededEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, 384)
		v[0] = s.base + float32(i)
		out[i] = v
	}
	return out, nil
}

// TestIndexVersion_centroidIsMeanOfChunkEmbeddings verifies IndexVersion
// returns the element-wise mean of every chunk embedding across sections,
// computed from the embeddings it already holds.
func TestIndexVersion_centroidIsMeanOfChunkEmbeddings(t *testing.T) {
	st := &stubStore{}
	// chunkSize=2, overlap=0 over a 4-word section yields exactly 2 chunks,
	// embedded as component-0 values {0, 1}; their mean is 0.5.
	idx := New(&seededEmbedder{base: 0}, st, 2, 0)

	pv := PolicyVersionContent{
		PolicyID: "pol-1", VersionID: "v-1", VersionNo: 1,
		CategoryID: "g", Sensitivity: "standard", PolicyTitle: "P",
		Sections: []SectionContent{{Key: "s", Text: "one two three four"}},
	}
	res, err := idx.IndexVersion(context.Background(), pv)
	if err != nil {
		t.Fatalf("IndexVersion: %v", err)
	}
	if res.ChunkCount != 2 {
		t.Fatalf("chunk count: got %d, want 2", res.ChunkCount)
	}
	if len(res.Centroid) != 384 {
		t.Fatalf("centroid dim: got %d, want 384", len(res.Centroid))
	}
	if res.Centroid[0] != 0.5 {
		t.Fatalf("centroid[0]: got %v, want 0.5 (mean of {0,1})", res.Centroid[0])
	}
	for i := 1; i < 384; i++ {
		if res.Centroid[i] != 0 {
			t.Fatalf("centroid[%d]: got %v, want 0", i, res.Centroid[i])
		}
	}
}

// TestIndexVersion_noChunksNoCentroid verifies an empty-content version
// produces no chunks and a nil centroid, so the consumer skips the centroid
// upsert (there is nothing to store).
func TestIndexVersion_noChunksNoCentroid(t *testing.T) {
	st := &stubStore{}
	idx := New(&seededEmbedder{}, st, 50, 10)
	res, err := idx.IndexVersion(context.Background(), PolicyVersionContent{
		PolicyID: "pol-1", VersionID: "v-1", VersionNo: 1,
		Sections: []SectionContent{{Key: "s", Text: "   "}},
	})
	if err != nil {
		t.Fatalf("IndexVersion: %v", err)
	}
	if res.ChunkCount != 0 {
		t.Fatalf("chunk count: got %d, want 0", res.ChunkCount)
	}
	if res.Centroid != nil {
		t.Fatalf("centroid: got %v, want nil for empty content", res.Centroid)
	}
}

// TestNew_defaultChunkSizeIsTokenSafe pins the default chunk size at 350
// words, which stays under a 512-token embeddings input with margin; 512
// words ran over it and one oversized chunk failed a section's whole batch.
func TestNew_defaultChunkSizeIsTokenSafe(t *testing.T) {
	idx := New(&stubEmbedder{}, &stubStore{}, 0, -1)
	if idx.chunkSize != 350 {
		t.Fatalf("default chunkSize: got %d, want 350", idx.chunkSize)
	}
	if idx.chunkOverlap != 64 {
		t.Fatalf("default chunkOverlap: got %d, want 64", idx.chunkOverlap)
	}
}

func TestRemoveVersion(t *testing.T) {
	em := &stubEmbedder{}
	st := &stubStore{}
	idx := New(em, st, 50, 10)
	// Should not error on removal; further assertion requires integration test.
	if err := idx.RemoveVersion(context.Background(), "ver-old"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.deletedVersions) != 1 || st.deletedVersions[0] != "ver-old" {
		t.Fatalf("deleted versions: got %v", st.deletedVersions)
	}
}

// TestRemovePolicy deletes by policy id, for a retire event that names only
// the policy.
func TestRemovePolicy(t *testing.T) {
	st := &stubStore{}
	idx := New(&stubEmbedder{}, st, 50, 10)
	if err := idx.RemovePolicy(context.Background(), "prc-001"); err != nil {
		t.Fatalf("RemovePolicy: %v", err)
	}
	if len(st.deletedPolicies) != 1 || st.deletedPolicies[0] != "prc-001" {
		t.Fatalf("deleted policies: got %v", st.deletedPolicies)
	}
	if len(st.deletedVersions) != 0 {
		t.Fatalf("RemovePolicy must not delete by version, got %v", st.deletedVersions)
	}
}

// shortEmbedder returns one vector fewer than asked for.
type shortEmbedder struct{}

func (shortEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	return make([][]float32, len(texts)-1), nil
}

// TestIndexVersion_embedderCountMismatchFails checks a short embeddings
// response fails the version instead of panicking or storing partial chunks.
func TestIndexVersion_embedderCountMismatchFails(t *testing.T) {
	st := &stubStore{}
	idx := New(shortEmbedder{}, st, 2, 0)
	_, err := idx.IndexVersion(context.Background(), PolicyVersionContent{
		PolicyID: "pol-1", VersionID: "v-1", VersionNo: 1,
		Sections: []SectionContent{{Key: "s", Text: "one two three four"}},
	})
	if err == nil {
		t.Fatal("expected an error for a short embeddings response")
	}
	if len(st.upserted) != 0 {
		t.Fatalf("no chunk may be stored, got %d", len(st.upserted))
	}
}

// Verify interface compliance: Indexer uses the Embedder interface.
var _ provider.Embedder = &stubEmbedder{}
