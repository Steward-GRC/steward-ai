// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pg "github.com/Bugs5382/go-postgres"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// insertQA inserts one ai_qa_cache row citing the given policies, with the given
// ask_count and created_at (for time-decay tests). The co-retrieval aggregation
// reads only citations_json, ask_count and created_at; question_embedding is a
// throwaway vector required by the NOT NULL column.
func insertQA(t *testing.T, pool *pg.DB, id string, askCount int, createdAt time.Time, policyIDs ...string) {
	t.Helper()
	cites := make([]map[string]string, 0, len(policyIDs))
	for _, p := range policyIDs {
		cites = append(cites, map[string]string{"policyId": p})
	}
	raw, err := json.Marshal(cites)
	if err != nil {
		t.Fatalf("marshal citations: %v", err)
	}
	_, err = pool.Pool().Exec(context.Background(), `
		INSERT INTO ai_qa_cache
			(question_text, question_embedding, scope_hash, answer_text,
			 citations_json, ask_count, created_at)
		VALUES ($1, $2::vector, $3, $4, $5::jsonb, $6, $7)`,
		"q-"+id, store.PgVectorLiteral(makeEmbedding(0)), "h-"+id, "ans", string(raw), askCount, createdAt)
	if err != nil {
		t.Fatalf("insert qa row %s: %v", id, err)
	}
}

// insertRelated inserts one ai_related_policies row with the given status (the
// crossref signal source: only 'accepted' rows count).
func insertRelated(t *testing.T, pool *pg.DB, policyID, relatedID, status string) {
	t.Helper()
	_, err := pool.Pool().Exec(context.Background(), `
		INSERT INTO ai_related_policies
			(policy_id, related_id, score, centroid_dist, source, status, corpus_version)
		VALUES ($1, $2, 0.5, 0.5, 'centroid', $3, 0)`,
		policyID, relatedID, status)
	if err != nil {
		t.Fatalf("insert related %s-%s: %v", policyID, relatedID, err)
	}
}

// TestCoRetrievalPairs_aggregatesAndDecays is the co-retrieval contract:
// co-cited policies form pairs, ask_count sums per pair, and older rows decay.
func TestCoRetrievalPairs_aggregatesAndDecays(t *testing.T) {
	pool := newTestDB(t)
	rs := store.NewRelationshipStore(pool)
	ctx := context.Background()

	// A recent row co-citing {a,b,c} (ask_count 10) → pairs a-b, a-c, b-c.
	// An OLD row (200 days) co-citing {a,b} (ask_count 10) → decayed heavily.
	insertQA(t, pool, "q1", 10, time.Now(), `a`, `b`, `c`)
	insertQA(t, pool, "q2", 10, time.Now().Add(-200*24*time.Hour), `a`, `b`)

	pairs, err := rs.CoRetrievalPairs(ctx, 30*24*time.Hour) // 30-day half-life
	if err != nil {
		t.Fatalf("CoRetrievalPairs: %v", err)
	}
	byKey := map[string]store.CoRetrievalPair{}
	for _, p := range pairs {
		byKey[store.PairKey(p.PolicyA, p.PolicyB)] = p
	}
	ab, ok := byKey[store.PairKey("a", "b")]
	if !ok {
		t.Fatal("missing a-b pair")
	}
	// a-b co-cited by both rows → co_retrieval_n = 2.
	if ab.CoRetrievalN != 2 {
		t.Fatalf("a-b co_retrieval_n want 2 got %d", ab.CoRetrievalN)
	}
	ac := byKey[store.PairKey("a", "c")]
	// a-c only in the recent row: undecayed ~10; a-b's second contribution is
	// heavily decayed (~200 days ≈ >4 half-lives), so a-b usage_raw is only
	// modestly above a-c despite twice the co-citation count.
	if ab.UsageRaw <= ac.UsageRaw {
		t.Fatalf("a-b usage_raw (%.3f) should exceed a-c (%.3f)", ab.UsageRaw, ac.UsageRaw)
	}
	if ab.UsageRaw >= 20 {
		t.Fatalf("a-b usage_raw should be time-decayed below the raw sum (20), got %.3f", ab.UsageRaw)
	}
	// Canonical ordering: policy_a < policy_b.
	for _, p := range pairs {
		if p.PolicyA >= p.PolicyB {
			t.Fatalf("pair not canonicalized: %s,%s", p.PolicyA, p.PolicyB)
		}
	}
}

// TestCentroidCandidatePairs_boundedByTopK verifies the candidate set is the
// per-policy KNN (bounded), not the full N², and carries centroid similarity.
func TestCentroidCandidatePairs_boundedByTopK(t *testing.T) {
	pool := newTestDB(t)
	cs := store.NewCentroidStore(pool)
	rs := store.NewRelationshipStore(pool)
	ctx := context.Background()

	// Four policies. a is closest to b, then c, then d (by embedding seed).
	for i, id := range []string{"a", "b", "c", "d"} {
		seedCentroid(t, cs, store.Centroid{
			PolicyID: id, VersionID: "v" + id, VersionNo: 1,
			Embedding: makeEmbedding(i), ChunkCount: 2, CategoryID: "g",
			Sensitivity: "standard", PolicyTitle: id,
		})
	}

	pairs, err := rs.CentroidCandidatePairs(ctx, 1) // top-1 neighbour each
	if err != nil {
		t.Fatalf("CentroidCandidatePairs: %v", err)
	}
	// With topK=1 and 4 policies, at most 4 directed edges collapse to <=4
	// canonical pairs — never the 6 the full N² would produce.
	if len(pairs) > 4 {
		t.Fatalf("top-1 candidates must be bounded (<=4), got %d (N² would be 6)", len(pairs))
	}
	for _, p := range pairs {
		if p.CentroidSim < -1.01 || p.CentroidSim > 1.01 {
			t.Fatalf("centroid_sim out of range: %v", p.CentroidSim)
		}
		if p.PolicyA >= p.PolicyB {
			t.Fatalf("pair not canonicalized: %s,%s", p.PolicyA, p.PolicyB)
		}
	}
}

// TestCrossrefPairs_fromAcceptedSuggestions: only 'accepted' ai_related_policies
// rows are cross-refs, canonicalized either-direction.
func TestCrossrefPairs_fromAcceptedSuggestions(t *testing.T) {
	pool := newTestDB(t)
	rs := store.NewRelationshipStore(pool)
	ctx := context.Background()

	insertRelated(t, pool, "a", "b", "accepted")
	insertRelated(t, pool, "c", "a", "accepted")  // reverse direction
	insertRelated(t, pool, "a", "d", "suggested") // NOT a crossref
	insertRelated(t, pool, "a", "e", "dismissed") // NOT a crossref

	pairs, err := rs.CrossrefPairs(ctx)
	if err != nil {
		t.Fatalf("CrossrefPairs: %v", err)
	}
	got := map[string]bool{}
	for _, p := range pairs {
		got[store.PairKey(p.PolicyA, p.PolicyB)] = true
	}
	if !got[store.PairKey("a", "b")] || !got[store.PairKey("a", "c")] {
		t.Fatalf("expected accepted a-b and a-c, got %v", got)
	}
	if got[store.PairKey("a", "d")] || got[store.PairKey("a", "e")] {
		t.Fatal("suggested/dismissed rows must not be cross-refs")
	}
}

// TestUpsertEdges_andBlendedScoresFor: upsert is idempotent and BlendedScoresFor
// reads the edge for either policy in the undirected pair.
func TestUpsertEdges_andBlendedScoresFor(t *testing.T) {
	pool := newTestDB(t)
	rs := store.NewRelationshipStore(pool)
	ctx := context.Background()

	edges := []store.RelationshipEdge{
		{PolicyA: "a", PolicyB: "b", CentroidSim: 0.8, UsageWeight: 0.5, BlendedScore: 0.7, CoRetrievalN: 3},
		{PolicyA: "a", PolicyB: "c", CentroidSim: 0.4, BlendedScore: 0.2},
	}
	if n, err := rs.UpsertEdges(ctx, edges); err != nil || n != 2 {
		t.Fatalf("UpsertEdges: n=%d err=%v", n, err)
	}
	// Idempotent re-run with a changed score updates in place (still 2 rows).
	edges[0].BlendedScore = 0.95
	if _, err := rs.UpsertEdges(ctx, edges); err != nil {
		t.Fatalf("UpsertEdges re-run: %v", err)
	}
	var count int
	if err := pool.Pool().QueryRow(ctx, `SELECT count(*) FROM ai_policy_relationship`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("upsert must not duplicate rows, got %d", count)
	}

	// BlendedScoresFor("a", {b,c}) returns both, keyed by the other id.
	scores, err := rs.BlendedScoresFor(ctx, "a", []string{"b", "c"})
	if err != nil {
		t.Fatalf("BlendedScoresFor: %v", err)
	}
	if abs(scores["b"]-0.95) > 1e-5 || abs(scores["c"]-0.2) > 1e-5 {
		t.Fatalf("unexpected scores from a: %v", scores)
	}
	// Undirected: querying from b's side finds the a-b edge too.
	fromB, err := rs.BlendedScoresFor(ctx, "b", []string{"a"})
	if err != nil {
		t.Fatalf("BlendedScoresFor(b): %v", err)
	}
	if abs(fromB["a"]-0.95) > 1e-5 {
		t.Fatalf("undirected lookup from b failed: %v", fromB)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
