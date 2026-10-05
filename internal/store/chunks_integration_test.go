// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// makeEmbedding creates a deterministic 384-dim vector with a single non-zero
// component at the given index. Distinct seed values produce vectors that
// pgvector's cosine_ops can order unambiguously.
func makeEmbedding(seed int) []float32 {
	v := make([]float32, 384)
	v[seed%384] = 1.0
	return v
}

func TestUpsertChunk_insertsRow(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	c := store.Chunk{
		PolicyID:    "pol-1",
		VersionID:   "ver-1",
		VersionNo:   1,
		SectionKey:  "scope",
		ChunkIndex:  0,
		ContentText: "All employees must comply with this policy.",
		Embedding:   makeEmbedding(0),
		CategoryID:  "hr",
		Sensitivity: "standard",
		PolicyTitle: "HR Policy",
	}
	if err := repo.UpsertChunk(ctx, c); err != nil {
		t.Fatalf("UpsertChunk insert: %v", err)
	}

	var count int
	if err := pool.Pool().QueryRow(ctx,
		`SELECT count(*) FROM ai_chunks WHERE version_id = $1`, c.VersionID).
		Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}
}

func TestUpsertChunk_updatesExistingRow(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	c := store.Chunk{
		PolicyID:    "pol-1",
		VersionID:   "ver-1",
		VersionNo:   1,
		SectionKey:  "scope",
		ChunkIndex:  0,
		ContentText: "first content",
		Embedding:   makeEmbedding(0),
		CategoryID:  "hr",
		Sensitivity: "standard",
		PolicyTitle: "HR Policy",
	}
	if err := repo.UpsertChunk(ctx, c); err != nil {
		t.Fatalf("UpsertChunk insert: %v", err)
	}

	// Same (version_id, section_key, chunk_index) — must update, not duplicate.
	c.ContentText = "updated content"
	c.Embedding = makeEmbedding(5)
	if err := repo.UpsertChunk(ctx, c); err != nil {
		t.Fatalf("UpsertChunk update: %v", err)
	}

	var count int
	var content string
	if err := pool.Pool().QueryRow(ctx,
		`SELECT count(*), max(content_text) FROM ai_chunks WHERE version_id = $1`, c.VersionID).
		Scan(&count, &content); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", count)
	}
	if content != "updated content" {
		t.Fatalf("expected updated content, got %q", content)
	}
}

func TestDeleteByVersionID_removesAllRowsForVersion(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	// Two chunks under ver-1, one under ver-2.
	rows := []store.Chunk{
		{
			PolicyID: "pol-1", VersionID: "ver-1", VersionNo: 1,
			SectionKey: "scope", ChunkIndex: 0,
			ContentText: "a", Embedding: makeEmbedding(0),
			CategoryID: "g", Sensitivity: "standard", PolicyTitle: "P",
		},
		{
			PolicyID: "pol-1", VersionID: "ver-1", VersionNo: 1,
			SectionKey: "scope", ChunkIndex: 1,
			ContentText: "b", Embedding: makeEmbedding(1),
			CategoryID: "g", Sensitivity: "standard", PolicyTitle: "P",
		},
		{
			PolicyID: "pol-1", VersionID: "ver-2", VersionNo: 2,
			SectionKey: "scope", ChunkIndex: 0,
			ContentText: "c", Embedding: makeEmbedding(2),
			CategoryID: "g", Sensitivity: "standard", PolicyTitle: "P",
		},
	}
	for _, r := range rows {
		if err := repo.UpsertChunk(ctx, r); err != nil {
			t.Fatalf("UpsertChunk seed: %v", err)
		}
	}

	if err := repo.DeleteByVersionID(ctx, "ver-1"); err != nil {
		t.Fatalf("DeleteByVersionID: %v", err)
	}

	var ver1Count, ver2Count int
	if err := pool.Pool().QueryRow(ctx,
		`SELECT
		   (SELECT count(*) FROM ai_chunks WHERE version_id = 'ver-1'),
		   (SELECT count(*) FROM ai_chunks WHERE version_id = 'ver-2')`).
		Scan(&ver1Count, &ver2Count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if ver1Count != 0 {
		t.Fatalf("expected 0 rows for ver-1, got %d", ver1Count)
	}
	if ver2Count != 1 {
		t.Fatalf("expected 1 row for ver-2, got %d", ver2Count)
	}
}

// TestSearchByCosine_scopeGatesStandardAndSensitive: nearest-neighbour
// ordering within the scoped category, while a standard chunk and a
// sensitive chunk in a category outside the scope are both excluded, even
// though the caller may read sensitive documents in their own scope.
func TestSearchByCosine_scopeGatesStandardAndSensitive(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	seed := []store.Chunk{
		{PolicyID: "p1", VersionID: "v1", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
			ContentText: "hr-near", Embedding: makeEmbedding(0),
			CategoryID: "hr", Sensitivity: "standard", PolicyTitle: "HR"},
		{PolicyID: "p1", VersionID: "v1", VersionNo: 1, SectionKey: "s", ChunkIndex: 1,
			ContentText: "hr-mid", Embedding: makeEmbedding(10),
			CategoryID: "hr", Sensitivity: "standard", PolicyTitle: "HR"},
		{PolicyID: "p1", VersionID: "v1", VersionNo: 1, SectionKey: "s", ChunkIndex: 2,
			ContentText: "hr-far", Embedding: makeEmbedding(100),
			CategoryID: "hr", Sensitivity: "standard", PolicyTitle: "HR"},
		{PolicyID: "p2", VersionID: "v2", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
			ContentText: "fin-near-standard", Embedding: makeEmbedding(0),
			CategoryID: "finance", Sensitivity: "standard", PolicyTitle: "FIN"},
		{PolicyID: "p2", VersionID: "v2", VersionNo: 1, SectionKey: "s", ChunkIndex: 1,
			ContentText: "fin-near-sensitive", Embedding: makeEmbedding(0),
			CategoryID: "finance", Sensitivity: "sensitive", PolicyTitle: "FIN"},
	}
	for _, c := range seed {
		if err := repo.UpsertChunk(ctx, c); err != nil {
			t.Fatalf("seed UpsertChunk: %v", err)
		}
	}

	results, err := repo.SearchByCosine(ctx, makeEmbedding(0), 3, store.AccessFilter{
		CategoryIDs:      []string{"hr"},
		IncludeSensitive: true,
	})
	if err != nil {
		t.Fatalf("SearchByCosine: %v", err)
	}
	want := []string{"hr-near", "hr-mid", "hr-far"}
	if len(results) != len(want) {
		t.Fatalf("expected %d results, got %d: %+v", len(want), len(results), results)
	}
	for i, r := range results {
		if r.ContentText != want[i] {
			t.Fatalf("result %d = %q, want %q (a chunk outside the scope leaked or the order is wrong)", i, r.ContentText, want[i])
		}
	}
}

// TestSearchHybrid_keywordBreaksVectorTie is the headline hybrid-retrieval
// case: two chunks are exactly equidistant by vector, but only one contains
// the query keywords. Pure cosine would order the tie arbitrarily; the hybrid
// fuse + re-rank must surface the keyword-relevant chunk first.
func TestSearchHybrid_keywordBreaksVectorTie(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	seed := []store.Chunk{
		{PolicyID: "p1", VersionID: "v1", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
			ContentText: "Employees may accrue vacation leave each year.", Embedding: makeEmbedding(0),
			CategoryID: "g", Sensitivity: "standard", PolicyTitle: "Leave"},
		{PolicyID: "p2", VersionID: "v2", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
			ContentText: "Parking permits and facilities access rules.", Embedding: makeEmbedding(0),
			CategoryID: "g", Sensitivity: "standard", PolicyTitle: "Facilities"},
		{PolicyID: "p3", VersionID: "v3", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
			ContentText: "Remote work eligibility guidance.", Embedding: makeEmbedding(120),
			CategoryID: "g", Sensitivity: "standard", PolicyTitle: "Remote"},
	}
	for _, c := range seed {
		if err := repo.UpsertChunk(ctx, c); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Query embedding ties p1 and p2 (both makeEmbedding(0)); the query TEXT
	// only matches p1's "vacation leave".
	results, err := repo.SearchHybrid(ctx, makeEmbedding(0), "vacation leave accrual", 3, store.AccessFilter{CategoryIDs: []string{"g"}})
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected hybrid results")
	}
	if results[0].PolicyID != "p1" {
		t.Fatalf("expected the keyword-relevant chunk (p1) ranked first, got %q (%+v)", results[0].PolicyID, results)
	}
}

// TestSearchHybrid_recallsKeywordExactEvenWhenVectorFar verifies the keyword
// arm gives recall a pure-vector search would miss: a chunk that is far in
// embedding space but an exact keyword match is still retrieved.
func TestSearchHybrid_recallsKeywordExactEvenWhenVectorFar(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	seed := []store.Chunk{
		{PolicyID: "near", VersionID: "vn", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
			ContentText: "General onboarding overview.", Embedding: makeEmbedding(0),
			CategoryID: "g", Sensitivity: "standard", PolicyTitle: "Onboarding"},
		{PolicyID: "far-kw", VersionID: "vf", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
			ContentText: "Whistleblower retaliation reporting procedure.", Embedding: makeEmbedding(200),
			CategoryID: "g", Sensitivity: "standard", PolicyTitle: "Whistleblower"},
	}
	for _, c := range seed {
		if err := repo.UpsertChunk(ctx, c); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	results, err := repo.SearchHybrid(ctx, makeEmbedding(0), "whistleblower retaliation reporting", 2, store.AccessFilter{CategoryIDs: []string{"g"}})
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	var sawFar bool
	for _, r := range results {
		if r.PolicyID == "far-kw" {
			sawFar = true
		}
	}
	if !sawFar {
		t.Fatalf("expected the vector-far but keyword-exact chunk to be recalled, got %+v", results)
	}
	if results[0].PolicyID != "far-kw" {
		t.Fatalf("expected the exact keyword match ranked first, got %q", results[0].PolicyID)
	}
}

// TestSearchHybrid_appliesSameAccessFilterAsCosine verifies the hybrid path
// enforces the identical access predicate: a sensitive chunk in an
// unauthorized group must not leak even on a strong keyword match.
func TestSearchHybrid_appliesSameAccessFilterAsCosine(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	seed := []store.Chunk{
		{PolicyID: "pub", VersionID: "v1", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
			ContentText: "Executive compensation summary public part.", Embedding: makeEmbedding(0),
			CategoryID: "hr", Sensitivity: "standard", PolicyTitle: "Comp"},
		{PolicyID: "sec", VersionID: "v2", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
			ContentText: "Executive compensation confidential bonus detail.", Embedding: makeEmbedding(0),
			CategoryID: "finance", Sensitivity: "sensitive", PolicyTitle: "Comp"},
	}
	for _, c := range seed {
		if err := repo.UpsertChunk(ctx, c); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Sensitive-reader but only authorized for group-hr, querying keywords that
	// match BOTH chunks — the group-fin sensitive chunk must still be excluded.
	results, err := repo.SearchHybrid(ctx, makeEmbedding(0), "executive compensation", 10, store.AccessFilter{
		CategoryIDs:      []string{"hr"},
		IncludeSensitive: true,
	})
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	for _, r := range results {
		if r.PolicyID == "sec" {
			t.Fatalf("sensitive chunk in an unauthorized group leaked past the hybrid access filter: %+v", r)
		}
	}
}

// TestUpsertChunk_persistsAndReturnsDocumentType verifies document_type is
// persisted on ai_chunks and returned by SearchByCosine so retrieval can label
// a source PRC-… vs POL-…. A policy chunk seeded with an
// empty DocumentType must come back as the 'POLICY' default (COALESCE guard +
// column default); a procedure chunk must come back as 'PROCEDURE'.
func TestUpsertChunk_persistsAndReturnsDocumentType(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	seed := []store.Chunk{
		{PolicyID: "pol-1", VersionID: "v-pol", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
			ContentText: "policy chunk", Embedding: makeEmbedding(0),
			CategoryID: "g", Sensitivity: "standard", PolicyTitle: "P"}, // DocumentType empty → POLICY
		{PolicyID: "prc-1", VersionID: "v-prc", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
			ContentText: "procedure chunk", Embedding: makeEmbedding(1),
			CategoryID: "g", Sensitivity: "standard", PolicyTitle: "Proc", DocumentType: "PROCEDURE"},
	}
	for _, c := range seed {
		if err := repo.UpsertChunk(ctx, c); err != nil {
			t.Fatalf("seed UpsertChunk: %v", err)
		}
	}

	results, err := repo.SearchByCosine(ctx, makeEmbedding(0), 10, store.AccessFilter{CategoryIDs: []string{"g"}})
	if err != nil {
		t.Fatalf("SearchByCosine: %v", err)
	}
	got := map[string]string{}
	for _, r := range results {
		got[r.PolicyID] = r.DocumentType
	}
	if got["pol-1"] != "POLICY" {
		t.Fatalf("expected policy chunk to default to POLICY, got %q", got["pol-1"])
	}
	if got["prc-1"] != "PROCEDURE" {
		t.Fatalf("expected procedure chunk to persist/return PROCEDURE, got %q", got["prc-1"])
	}
}

func TestSearchByCosine_excludesSensitiveWhenNotAuthorized(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	seed := []store.Chunk{
		{PolicyID: "p1", VersionID: "v1", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
			ContentText: "standard-chunk", Embedding: makeEmbedding(0),
			CategoryID: "g", Sensitivity: "standard", PolicyTitle: "P"},
		{PolicyID: "p1", VersionID: "v1", VersionNo: 1, SectionKey: "s", ChunkIndex: 1,
			ContentText: "sensitive-chunk", Embedding: makeEmbedding(0),
			CategoryID: "g", Sensitivity: "sensitive", PolicyTitle: "P"},
	}
	for _, c := range seed {
		if err := repo.UpsertChunk(ctx, c); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Caller not authorized for sensitive content.
	results, err := repo.SearchByCosine(ctx, makeEmbedding(0), 10, store.AccessFilter{
		CategoryIDs:      []string{"g"},
		IncludeSensitive: false,
	})
	if err != nil {
		t.Fatalf("SearchByCosine: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (sensitive excluded), got %d", len(results))
	}
	if results[0].ContentText != "standard-chunk" {
		t.Fatalf("unexpected row leaked: %q", results[0].ContentText)
	}

	// Now authorized — both should appear.
	resultsAuth, err := repo.SearchByCosine(ctx, makeEmbedding(0), 10, store.AccessFilter{
		CategoryIDs:      []string{"g"},
		IncludeSensitive: true,
	})
	if err != nil {
		t.Fatalf("SearchByCosine (authorized): %v", err)
	}
	if len(resultsAuth) != 2 {
		t.Fatalf("expected 2 results when authorized for sensitive, got %d", len(resultsAuth))
	}
}

// TestSearchByCosine_emptyAuthorizedGroups asserts the security invariant on a live DB:
// a SENSITIVE chunk must not match when CategoryIDs is empty, even
// with IncludeSensitive=true — group membership is still required for
// sensitive content under the READ access model (see AccessFilter).
func TestSearchByCosine_emptyAuthorizedGroups_returnsNothing(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	if err := repo.UpsertChunk(ctx, store.Chunk{
		PolicyID: "p1", VersionID: "v1", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
		ContentText: "secret", Embedding: makeEmbedding(0),
		CategoryID: "g", Sensitivity: "sensitive", PolicyTitle: "P",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	results, err := repo.SearchByCosine(ctx, makeEmbedding(0), 10, store.AccessFilter{
		CategoryIDs:      []string{},
		IncludeSensitive: true,
	})
	if err != nil {
		t.Fatalf("SearchByCosine: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for a sensitive chunk when no groups are authorized, got %d", len(results))
	}
}

// TestSearchByCosine_standardOutsideScopeIsHidden: category rules decide
// who reads a standard document too, so a caller whose scope doesn't hold the
// category sees nothing from it.
func TestSearchByCosine_standardOutsideScopeIsHidden(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	if err := repo.UpsertChunk(ctx, store.Chunk{
		PolicyID: "p1", VersionID: "v1", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
		ContentText: "facilities only", Embedding: makeEmbedding(0),
		CategoryID: "facilities", Sensitivity: "standard", PolicyTitle: "P",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, scope := range [][]string{nil, {"hr"}} {
		results, err := repo.SearchByCosine(ctx, makeEmbedding(0), 10, store.AccessFilter{CategoryIDs: scope})
		if err != nil {
			t.Fatalf("SearchByCosine: %v", err)
		}
		if len(results) != 0 {
			t.Fatalf("scope %v: expected nothing outside the scope, got %+v", scope, results)
		}
	}
	results, err := repo.SearchByCosine(ctx, makeEmbedding(0), 10, store.AccessFilter{CategoryIDs: []string{"facilities"}})
	if err != nil {
		t.Fatalf("SearchByCosine: %v", err)
	}
	if len(results) != 1 || results[0].ContentText != "facilities only" {
		t.Fatalf("expected the chunk inside the scope, got %+v", results)
	}
}

// TestSearchByCosine_sensitiveRequiresGroupAndIncludeSensitive verifies the
// sensitive-chunk gate is the conjunction of BOTH IncludeSensitive and group
// membership — neither alone is sufficient (and SiteAdmin bypasses both, see
// TestSearchByCosine_allCategoriesSeesEverything).
func TestSearchByCosine_sensitiveRequiresGroupAndIncludeSensitive(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	if err := repo.UpsertChunk(ctx, store.Chunk{
		PolicyID: "p1", VersionID: "v1", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
		ContentText: "exec comp detail", Embedding: makeEmbedding(0),
		CategoryID: "hr", Sensitivity: "sensitive", PolicyTitle: "HR",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name             string
		authorizedGroups []string
		includeSensitive bool
		wantResults      int
	}{
		{"authorized group but sensitive-reader role missing", []string{"hr"}, false, 0},
		{"sensitive-reader role but wrong group", []string{"finance"}, true, 0},
		{"authorized group AND sensitive-reader role", []string{"hr"}, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results, err := repo.SearchByCosine(ctx, makeEmbedding(0), 10, store.AccessFilter{
				CategoryIDs:      tc.authorizedGroups,
				IncludeSensitive: tc.includeSensitive,
			})
			if err != nil {
				t.Fatalf("SearchByCosine: %v", err)
			}
			if len(results) != tc.wantResults {
				t.Fatalf("expected %d results, got %d", tc.wantResults, len(results))
			}
		})
	}
}

// TestSearchByCosine_allCategoriesSeesEverything: AllCategories bypasses the
// category filter and the sensitivity gate, so a scope with no categories and
// IncludeSensitive=false still sees a sensitive chunk.
func TestSearchByCosine_allCategoriesSeesEverything(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	if err := repo.UpsertChunk(ctx, store.Chunk{
		PolicyID: "p1", VersionID: "v1", VersionNo: 1, SectionKey: "s", ChunkIndex: 0,
		ContentText: "exec comp detail", Embedding: makeEmbedding(0),
		CategoryID: "hr", Sensitivity: "sensitive", PolicyTitle: "HR",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	results, err := repo.SearchByCosine(ctx, makeEmbedding(0), 10, store.AccessFilter{
		CategoryIDs:      nil,
		IncludeSensitive: false,
		AllCategories:    true,
	})
	if err != nil {
		t.Fatalf("SearchByCosine: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected the all-categories scope to see the sensitive, out-of-scope chunk, got %d results", len(results))
	}
	if results[0].ContentText != "exec comp detail" {
		t.Fatalf("unexpected result: %+v", results[0])
	}
}
