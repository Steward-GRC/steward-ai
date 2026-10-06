// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// seedSuggested inserts one 'suggested' ai_related_policies row via ApplyDiff is
// awkward (it diffs), so seed directly with a fresh ApplyDiff of a single-item
// desired set — exercising the real write path.
func applyDesired(t *testing.T, rs *store.RelatedStore, policyID string, desired []store.Suggestion, corpus int64) store.DiffResult {
	t.Helper()
	d, err := rs.ApplyDiff(context.Background(), policyID, desired, corpus)
	if err != nil {
		t.Fatalf("ApplyDiff(%s): %v", policyID, err)
	}
	return d
}

// TestApplyDiff_AddsRemovesStaleKeepsDecided is the diff contract:
// re-eval adds newly-suggested rows, deletes rows no longer suggested that are
// still 'suggested', and NEVER touches 'accepted'/'dismissed' tombstones (so a
// decided suggestion is never re-proposed).
func TestApplyDiff_AddsRemovesStaleKeepsDecided(t *testing.T) {
	pool := newTestDB(t)
	rs := store.NewRelatedStore(pool)
	ctx := context.Background()

	// First pass: suggest p2, p3, p4 for p1.
	d1 := applyDesired(t, rs, "p1", []store.Suggestion{
		{RelatedID: "p2", Score: 0.9, CentroidDist: 0.1},
		{RelatedID: "p3", Score: 0.8, CentroidDist: 0.2},
		{RelatedID: "p4", Score: 0.7, CentroidDist: 0.3},
	}, 1)
	if len(d1.NewlySuggested) != 3 {
		t.Fatalf("first pass: expected 3 newly suggested, got %v", d1.NewlySuggested)
	}

	// Author accepts p2 (promoted to core) and dismisses p3.
	if err := rs.SetStatus(ctx, "p1", "p2", store.SuggestionStatusAccepted); err != nil {
		t.Fatalf("accept p2: %v", err)
	}
	if err := rs.SetStatus(ctx, "p1", "p3", store.SuggestionStatusDismissed); err != nil {
		t.Fatalf("dismiss p3: %v", err)
	}

	// Second pass: geometry moved — now only p4 (still) and p5 (new) are
	// neighbours; p2/p3 are NOT in the desired set anymore. Re-eval must keep
	// the accepted p2 and dismissed p3 tombstones, keep p4, add p5.
	d2 := applyDesired(t, rs, "p1", []store.Suggestion{
		{RelatedID: "p4", Score: 0.75, CentroidDist: 0.25},
		{RelatedID: "p5", Score: 0.6, CentroidDist: 0.4},
	}, 2)
	if len(d2.NewlySuggested) != 1 || d2.NewlySuggested[0] != "p5" {
		t.Fatalf("second pass: expected only p5 newly suggested, got %v", d2.NewlySuggested)
	}
	if len(d2.NoLongerSuggested) != 0 {
		// p2 is accepted (tombstone, not deleted), p3 is dismissed (tombstone),
		// p4 is still suggested — nothing should be reported as no-longer.
		t.Fatalf("second pass: expected no deletions (tombstones kept, p4 retained), got %v", d2.NoLongerSuggested)
	}

	rows, err := rs.ListByPolicy(ctx, "p1")
	if err != nil {
		t.Fatalf("ListByPolicy: %v", err)
	}
	byID := map[string]store.Suggestion{}
	for _, r := range rows {
		byID[r.RelatedID] = r
	}
	if byID["p2"].Status != store.SuggestionStatusAccepted {
		t.Fatalf("p2 must remain accepted tombstone, got %+v", byID["p2"])
	}
	if byID["p3"].Status != store.SuggestionStatusDismissed {
		t.Fatalf("p3 must remain dismissed tombstone, got %+v", byID["p3"])
	}
	if byID["p4"].Status != store.SuggestionStatusSuggested || byID["p4"].Score != 0.75 {
		t.Fatalf("p4 must remain suggested with updated score, got %+v", byID["p4"])
	}
	if byID["p5"].Status != store.SuggestionStatusSuggested {
		t.Fatalf("p5 must be suggested, got %+v", byID["p5"])
	}
}

// TestApplyDiff_RemovesStaleSuggested confirms a previously-suggested,
// still-'suggested' row IS deleted when it drops out of the desired set.
func TestApplyDiff_RemovesStaleSuggested(t *testing.T) {
	pool := newTestDB(t)
	rs := store.NewRelatedStore(pool)
	ctx := context.Background()

	applyDesired(t, rs, "p1", []store.Suggestion{{RelatedID: "p2", Score: 0.9, CentroidDist: 0.1}}, 1)
	d := applyDesired(t, rs, "p1", []store.Suggestion{{RelatedID: "p3", Score: 0.9, CentroidDist: 0.1}}, 2)
	if len(d.NoLongerSuggested) != 1 || d.NoLongerSuggested[0] != "p2" {
		t.Fatalf("expected p2 removed, got %v", d.NoLongerSuggested)
	}
	rows, _ := rs.ListByPolicy(ctx, "p1")
	for _, r := range rows {
		if r.RelatedID == "p2" {
			t.Fatalf("stale suggested p2 should have been deleted, still present: %+v", r)
		}
	}
}

// seedCentroidRow upserts a centroid for the read-path tests.
func seedCentroidRow(t *testing.T, cs *store.CentroidStore, id, group, sensitivity string, seed int) {
	t.Helper()
	if err := cs.UpsertCentroid(context.Background(), store.Centroid{
		PolicyID: id, VersionID: id + "-v1", VersionNo: 1, Embedding: makeEmbedding(seed),
		ChunkCount: 3, CategoryID: group, Sensitivity: sensitivity, PolicyTitle: "Title " + id,
	}); err != nil {
		t.Fatalf("seed centroid %s: %v", id, err)
	}
}

// TestRelatedPolicies_PrefersPrecomputedOverLiveKNN: once suggestions exist,
// the read returns them (ordered by stored score), not the live centroid KNN —
// proven by making the precomputed order differ from centroid proximity.
func TestRelatedPolicies_PrefersPrecomputedOverLiveKNN(t *testing.T) {
	pool := newTestDB(t)
	cs := store.NewCentroidStore(pool)
	rs := store.NewRelatedStore(pool)
	ctx := context.Background()

	// Centroids: p1 anchor at seed 0; p2 closest (seed 1), p3 farther (seed 50).
	seedCentroidRow(t, cs, "p1", "g", "standard", 0)
	seedCentroidRow(t, cs, "p2", "g", "standard", 1)
	seedCentroidRow(t, cs, "p3", "g", "standard", 50)

	// Precomputed suggestions deliberately rank p3 ABOVE p2 (higher score),
	// the OPPOSITE of centroid proximity — so if the read prefers precomputed
	// we see p3 first.
	applyDesired(t, rs, "p1", []store.Suggestion{
		{RelatedID: "p3", Score: 0.95, CentroidDist: 0.9},
		{RelatedID: "p2", Score: 0.10, CentroidDist: 0.1},
	}, 1)

	got, err := cs.RelatedPolicies(ctx, "p1", store.AccessFilter{CategoryIDs: []string{"g", "g1", "g2"}}, 10)
	if err != nil {
		t.Fatalf("RelatedPolicies: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 precomputed neighbours, got %d (%+v)", len(got), got)
	}
	if got[0].PolicyID != "p3" || got[1].PolicyID != "p2" {
		t.Fatalf("expected precomputed score order [p3,p2], got [%s,%s]", got[0].PolicyID, got[1].PolicyID)
	}
}

// TestRelatedPolicies_FallsBackToLiveKNNWhenCold: with NO precomputed
// suggestions the read falls back to live centroid KNN.
func TestRelatedPolicies_FallsBackToLiveKNNWhenCold(t *testing.T) {
	pool := newTestDB(t)
	cs := store.NewCentroidStore(pool)
	ctx := context.Background()

	seedCentroidRow(t, cs, "p1", "g", "standard", 0)
	seedCentroidRow(t, cs, "p2", "g", "standard", 1)

	got, err := cs.RelatedPolicies(ctx, "p1", store.AccessFilter{CategoryIDs: []string{"g", "g1", "g2"}}, 10)
	if err != nil {
		t.Fatalf("RelatedPolicies: %v", err)
	}
	if len(got) != 1 || got[0].PolicyID != "p2" {
		t.Fatalf("cold-cache fallback should return live KNN [p2], got %+v", got)
	}
}

// TestRelatedPolicies_PrecomputedAccessFilters: a sensitive suggested neighbour
// is filtered out for a caller without sensitive access, even though it is in
// the precomputed set (a relatedness edge must not leak an unreadable
// policy).
func TestRelatedPolicies_PrecomputedAccessFilters(t *testing.T) {
	pool := newTestDB(t)
	cs := store.NewCentroidStore(pool)
	rs := store.NewRelatedStore(pool)
	ctx := context.Background()

	seedCentroidRow(t, cs, "p1", "g1", "standard", 0)
	seedCentroidRow(t, cs, "p2", "g1", "standard", 1)
	seedCentroidRow(t, cs, "sensitive", "g2", "sensitive", 2)

	applyDesired(t, rs, "p1", []store.Suggestion{
		{RelatedID: "p2", Score: 0.9, CentroidDist: 0.1},
		{RelatedID: "sensitive", Score: 0.8, CentroidDist: 0.2},
	}, 1)

	// Standard caller (no sensitive access): sees only p2.
	std, err := cs.RelatedPolicies(ctx, "p1", store.AccessFilter{CategoryIDs: []string{"g", "g1", "g2"}}, 10)
	if err != nil {
		t.Fatalf("RelatedPolicies (std): %v", err)
	}
	if len(std) != 1 || std[0].PolicyID != "p2" {
		t.Fatalf("standard caller must see only p2, got %+v", std)
	}

	// All categories: sees both.
	admin, err := cs.RelatedPolicies(ctx, "p1", store.AccessFilter{AllCategories: true, IncludeSensitive: true}, 10)
	if err != nil {
		t.Fatalf("RelatedPolicies (admin): %v", err)
	}
	if len(admin) != 2 {
		t.Fatalf("the all-categories scope must see both, got %+v", admin)
	}
}

// TestRelatedPolicies_PrecomputedDropsUnpublishedTarget: a suggestion whose
// target lost its centroid (unpublished) is not surfaced even before re-eval
// prunes it (the join drops it).
func TestRelatedPolicies_PrecomputedDropsUnpublishedTarget(t *testing.T) {
	pool := newTestDB(t)
	cs := store.NewCentroidStore(pool)
	rs := store.NewRelatedStore(pool)
	ctx := context.Background()

	seedCentroidRow(t, cs, "p1", "g", "standard", 0)
	seedCentroidRow(t, cs, "p2", "g", "standard", 1)
	// p3 is suggested but has NO centroid row (unpublished).
	applyDesired(t, rs, "p1", []store.Suggestion{
		{RelatedID: "p2", Score: 0.9, CentroidDist: 0.1},
		{RelatedID: "p3", Score: 0.8, CentroidDist: 0.2},
	}, 1)

	got, err := cs.RelatedPolicies(ctx, "p1", store.AccessFilter{CategoryIDs: []string{"g", "g1", "g2"}}, 10)
	if err != nil {
		t.Fatalf("RelatedPolicies: %v", err)
	}
	if len(got) != 1 || got[0].PolicyID != "p2" {
		t.Fatalf("expected only p2 (p3 has no centroid), got %+v", got)
	}
}
