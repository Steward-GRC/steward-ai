// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// seedCentroid is a small helper to upsert one centroid row.
func seedCentroid(t *testing.T, repo *store.CentroidStore, c store.Centroid) {
	t.Helper()
	if len(c.Embedding) == 0 {
		c.Embedding = makeEmbedding(0)
	}
	if err := repo.UpsertCentroid(context.Background(), c); err != nil {
		t.Fatalf("UpsertCentroid seed: %v", err)
	}
}

func TestUpsertCentroid_insertsThenUpdatesOnePerPolicy(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewCentroidStore(pool)
	ctx := context.Background()

	seedCentroid(t, repo, store.Centroid{
		PolicyID: "p1", VersionID: "v1", VersionNo: 1, Embedding: makeEmbedding(0),
		ChunkCount: 3, CategoryID: "g", Sensitivity: "standard", PolicyTitle: "First",
	})
	// Re-publish (new version) → upsert must REPLACE, keyed on policy_id.
	seedCentroid(t, repo, store.Centroid{
		PolicyID: "p1", VersionID: "v2", VersionNo: 2, Embedding: makeEmbedding(5),
		ChunkCount: 4, CategoryID: "g", Sensitivity: "standard", PolicyTitle: "First v2",
	})

	var count, versionNo, chunkCount int
	var title string
	if err := pool.Pool().QueryRow(ctx,
		`SELECT count(*), max(version_no), max(chunk_count), max(policy_title)
		   FROM ai_policy_centroids WHERE policy_id = 'p1'`).
		Scan(&count, &versionNo, &chunkCount, &title); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 centroid row per policy, got %d", count)
	}
	if versionNo != 2 || chunkCount != 4 || title != "First v2" {
		t.Fatalf("expected the row to be updated to v2 metadata, got version=%d chunks=%d title=%q", versionNo, chunkCount, title)
	}
}

func TestDeleteByPolicyID_removesTheRow(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewCentroidStore(pool)
	ctx := context.Background()

	seedCentroid(t, repo, store.Centroid{PolicyID: "p1", VersionID: "v1", VersionNo: 1,
		Embedding: makeEmbedding(0), ChunkCount: 1, CategoryID: "g", Sensitivity: "standard", PolicyTitle: "P"})

	if err := repo.DeleteByPolicyID(ctx, "p1"); err != nil {
		t.Fatalf("DeleteByPolicyID: %v", err)
	}
	var count int
	if err := pool.Pool().QueryRow(ctx, `SELECT count(*) FROM ai_policy_centroids WHERE policy_id = 'p1'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the centroid row to be deleted, got %d", count)
	}
}

// TestRelatedPolicies_excludesSelfAndOrdersByDistance is the headline
// case: the anchor policy is never returned as its own neighbor, and neighbors
// come back nearest-first by centroid cosine distance.
func TestRelatedPolicies_excludesSelfAndOrdersByDistance(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewCentroidStore(pool)
	ctx := context.Background()

	// Anchor at seed 0; a near neighbor at seed 1 (small angle) and a far one at
	// seed 200 (orthogonal). makeEmbedding puts a single 1.0 at index seed%384.
	seedCentroid(t, repo, store.Centroid{PolicyID: "anchor", VersionID: "va", VersionNo: 1,
		Embedding: mixEmbedding(0, 1, 1.0, 0.1), CategoryID: "g", Sensitivity: "standard", PolicyTitle: "Anchor"})
	seedCentroid(t, repo, store.Centroid{PolicyID: "near", VersionID: "vn", VersionNo: 1,
		Embedding: mixEmbedding(0, 1, 1.0, 0.9), CategoryID: "g", Sensitivity: "standard", PolicyTitle: "Near"})
	seedCentroid(t, repo, store.Centroid{PolicyID: "far", VersionID: "vf", VersionNo: 1,
		Embedding: makeEmbedding(200), CategoryID: "g", Sensitivity: "standard", PolicyTitle: "Far"})

	results, err := repo.RelatedPolicies(ctx, "anchor", store.AccessFilter{CategoryIDs: []string{"g", "g1", "g2"}}, 10)
	if err != nil {
		t.Fatalf("RelatedPolicies: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 neighbors (self excluded), got %d: %+v", len(results), results)
	}
	for _, r := range results {
		if r.PolicyID == "anchor" {
			t.Fatalf("anchor policy leaked into its own neighbor list: %+v", r)
		}
	}
	if results[0].PolicyID != "near" {
		t.Fatalf("expected 'near' ranked first by distance, got %q (%+v)", results[0].PolicyID, results)
	}
}

// TestRelatedPolicies_appliesAccessFilterToNeighbors is the security
// invariant: a neighbor in a category outside the scope never surfaces,
// standard or sensitive, so a relatedness edge can't leak a policy the viewer
// can't read. Mirrors the ChunkStore.SearchByCosine cases.
func TestRelatedPolicies_appliesAccessFilterToNeighbors(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewCentroidStore(pool)
	ctx := context.Background()

	seedCentroid(t, repo, store.Centroid{PolicyID: "anchor", VersionID: "va", VersionNo: 1,
		Embedding: makeEmbedding(0), CategoryID: "hr", Sensitivity: "standard", PolicyTitle: "Anchor"})
	// Standard neighbor in another category: outside the scope.
	seedCentroid(t, repo, store.Centroid{PolicyID: "std-other", VersionID: "v1", VersionNo: 1,
		Embedding: makeEmbedding(1), CategoryID: "finance", Sensitivity: "standard", PolicyTitle: "Std Other"})
	// Sensitive neighbor in another category: outside the scope.
	seedCentroid(t, repo, store.Centroid{PolicyID: "sec-fin", VersionID: "v2", VersionNo: 1,
		Embedding: makeEmbedding(2), CategoryID: "finance", Sensitivity: "sensitive", PolicyTitle: "Sec Fin"})

	// Sensitive neighbor in the scoped category: visible to a sensitive reader.
	seedCentroid(t, repo, store.Centroid{PolicyID: "sec-hr", VersionID: "v3", VersionNo: 1,
		Embedding: makeEmbedding(3), CategoryID: "hr", Sensitivity: "sensitive", PolicyTitle: "Sec HR"})

	// The scope holds hr only, with sensitive documents.
	results, err := repo.RelatedPolicies(ctx, "anchor", store.AccessFilter{
		CategoryIDs:      []string{"hr"},
		IncludeSensitive: true,
	}, 10)
	if err != nil {
		t.Fatalf("RelatedPolicies: %v", err)
	}
	if len(results) != 1 || results[0].PolicyID != "sec-hr" {
		t.Fatalf("expected only the in-scope neighbor, got %+v", results)
	}

	// An all-categories scope sees every neighbor.
	adminResults, err := repo.RelatedPolicies(ctx, "anchor", store.AccessFilter{AllCategories: true}, 10)
	if err != nil {
		t.Fatalf("RelatedPolicies (admin): %v", err)
	}
	var adminSawSensitive bool
	for _, r := range adminResults {
		if r.PolicyID == "sec-fin" {
			adminSawSensitive = true
		}
	}
	if !adminSawSensitive {
		t.Fatal("expected the all-categories scope to see the sensitive neighbor")
	}
}

// TestRelatedPolicies_missingAnchorReturnsEmpty verifies a policy with no
// centroid row (never published / not yet backfilled) yields an empty result
// rather than an error.
func TestRelatedPolicies_missingAnchorReturnsEmpty(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewCentroidStore(pool)
	ctx := context.Background()

	seedCentroid(t, repo, store.Centroid{PolicyID: "other", VersionID: "v1", VersionNo: 1,
		Embedding: makeEmbedding(0), CategoryID: "g", Sensitivity: "standard", PolicyTitle: "Other"})

	results, err := repo.RelatedPolicies(ctx, "no-such-policy", store.AccessFilter{CategoryIDs: []string{"g", "g1", "g2"}}, 10)
	if err != nil {
		t.Fatalf("RelatedPolicies: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty result for a policy without a centroid, got %d", len(results))
	}
}

// TestRelatedPolicies_respectsTopN verifies the topN limit is applied.
func TestRelatedPolicies_respectsTopN(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewCentroidStore(pool)
	ctx := context.Background()

	seedCentroid(t, repo, store.Centroid{PolicyID: "anchor", VersionID: "va", VersionNo: 1,
		Embedding: makeEmbedding(0), CategoryID: "g", Sensitivity: "standard", PolicyTitle: "Anchor"})
	for i := 1; i <= 5; i++ {
		seedCentroid(t, repo, store.Centroid{PolicyID: "n" + string(rune('0'+i)), VersionID: "v", VersionNo: 1,
			Embedding: makeEmbedding(i), CategoryID: "g", Sensitivity: "standard", PolicyTitle: "N"})
	}

	results, err := repo.RelatedPolicies(ctx, "anchor", store.AccessFilter{CategoryIDs: []string{"g", "g1", "g2"}}, 2)
	if err != nil {
		t.Fatalf("RelatedPolicies: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected topN=2 to cap results at 2, got %d", len(results))
	}
}

// mixEmbedding builds a 384-dim vector with weight a at index i and weight b at
// index j, so two vectors sharing a dominant component can be ordered by their
// secondary weight (a smaller angle → smaller cosine distance).
func mixEmbedding(i, j int, a, b float32) []float32 {
	v := make([]float32, 384)
	v[i%384] = a
	v[j%384] = b
	return v
}
