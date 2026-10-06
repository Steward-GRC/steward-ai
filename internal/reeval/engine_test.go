// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package reeval

import (
	"context"
	"errors"
	"slices"
	"sort"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// fakeDrainer returns a scripted pending set + corpus version.
type fakeDrainer struct {
	ids        []string
	corpus     int64
	drainErr   error
	corpusErr  error
	drainCalls int
}

func (f *fakeDrainer) Drain(context.Context) ([]string, error) {
	f.drainCalls++
	return f.ids, f.drainErr
}
func (f *fakeDrainer) CorpusVersion(context.Context) (int64, error) { return f.corpus, f.corpusErr }

// fakeNeighbors returns scripted neighbours per policy, or an error per policy.
type fakeNeighbors struct {
	byPolicy map[string][]store.RelatedPolicy
	errFor   map[string]error
	calls    map[string]int
}

func (f *fakeNeighbors) NeighborsByCentroid(_ context.Context, policyID string, filter store.AccessFilter, _ int) ([]store.RelatedPolicy, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[policyID]++
	if !filter.AllCategories {
		// re-eval must always compute the full geometry (every category).
		return nil, errors.New("re-eval used a scoped filter")
	}
	if err := f.errFor[policyID]; err != nil {
		return nil, err
	}
	return f.byPolicy[policyID], nil
}

// fakeDiffer records the desired set applied per policy and returns a scripted
// diff.
type fakeDiffer struct {
	applied map[string][]store.Suggestion
	diffFor map[string]store.DiffResult
	errFor  map[string]error
}

func (f *fakeDiffer) ApplyDiff(_ context.Context, policyID string, desired []store.Suggestion, _ int64) (store.DiffResult, error) {
	if f.applied == nil {
		f.applied = map[string][]store.Suggestion{}
	}
	f.applied[policyID] = desired
	if err := f.errFor[policyID]; err != nil {
		return store.DiffResult{}, err
	}
	return f.diffFor[policyID], nil
}

func rp(id string, dist float32) store.RelatedPolicy {
	return store.RelatedPolicy{PolicyID: id, Distance: dist}
}

// TestEngine_ReevalRecomputesChangedPlusNeighbours: draining {p1} where p1's
// neighbours are {p2,p3} recomputes p1, p2 AND p3 (incremental scope) and
// aggregates the diffs each ApplyDiff reports.
func TestEngine_ReevalRecomputesChangedPlusNeighbours(t *testing.T) {
	drain := &fakeDrainer{ids: []string{"p1"}, corpus: 5}
	nbrs := &fakeNeighbors{byPolicy: map[string][]store.RelatedPolicy{
		"p1": {rp("p2", 0.10), rp("p3", 0.20)},
		"p2": {rp("p1", 0.10)},
		"p3": {rp("p1", 0.20)},
	}}
	differ := &fakeDiffer{diffFor: map[string]store.DiffResult{
		"p1": {NewlySuggested: []string{"p2", "p3"}},
		"p2": {NewlySuggested: []string{"p1"}},
		// p3 diff is empty (no change) -> should NOT appear in result.Diffs
	}}
	e := NewEngine(drain, nbrs, differ, 10)

	res, err := e.Reeval(context.Background())
	if err != nil {
		t.Fatalf("Reeval: %v", err)
	}
	if res.Recomputed != 3 {
		t.Fatalf("expected 3 policies recomputed (p1,p2,p3), got %d", res.Recomputed)
	}
	for _, id := range []string{"p1", "p2", "p3"} {
		if _, ok := differ.applied[id]; !ok {
			t.Fatalf("expected ApplyDiff for %s", id)
		}
	}
	// p1's desired set must carry centroid scores (1 - dist) and source.
	got := differ.applied["p1"]
	if len(got) != 2 || got[0].RelatedID != "p2" || got[0].Source != store.SuggestionSourceCentroid {
		t.Fatalf("p1 desired set wrong: %+v", got)
	}
	if got[0].Score != 1-0.10 || got[0].CentroidDist != 0.10 || got[0].CorpusVersion != 5 {
		t.Fatalf("p1 suggestion scoring wrong: %+v", got[0])
	}
	// Only policies with non-empty diffs appear in Diffs (p1,p2), not p3.
	if len(res.Diffs) != 2 {
		t.Fatalf("expected 2 non-empty diffs, got %d (%+v)", len(res.Diffs), res.Diffs)
	}
}

// TestEngine_EmptyPendingIsNoOp: nothing drained -> no recompute, no error.
func TestEngine_EmptyPendingIsNoOp(t *testing.T) {
	differ := &fakeDiffer{}
	e := NewEngine(&fakeDrainer{ids: nil}, &fakeNeighbors{}, differ, 10)
	res, err := e.Reeval(context.Background())
	if err != nil {
		t.Fatalf("Reeval: %v", err)
	}
	if res.Recomputed != 0 || len(differ.applied) != 0 {
		t.Fatalf("empty pending should be a no-op, got %+v", res)
	}
}

// TestEngine_BestEffortDegradation: a changed policy whose centroid read fails
// (pX) is skipped without aborting the pass, and a diff failure for one
// affected policy (p2) skips only that policy — the healthy policy (p1) still
// gets recomputed.
func TestEngine_BestEffortDegradation(t *testing.T) {
	drain := &fakeDrainer{ids: []string{"p1", "pX"}}
	nbrs := &fakeNeighbors{
		byPolicy: map[string][]store.RelatedPolicy{
			"p1": {rp("p2", 0.1)},
			"p2": {rp("p1", 0.1)},
		},
		errFor: map[string]error{"pX": errors.New("centroid read blip")},
	}
	differ := &fakeDiffer{
		diffFor: map[string]store.DiffResult{"p1": {NewlySuggested: []string{"p2"}}},
		errFor:  map[string]error{"p2": errors.New("db blip")},
	}
	e := NewEngine(drain, nbrs, differ, 10)

	res, err := e.Reeval(context.Background())
	if err != nil {
		t.Fatalf("Reeval must not fail on per-policy errors: %v", err)
	}
	// affected = {p1, p2 (p1's neighbour), pX}. pX's centroid read fails so its
	// desired set can't be computed (skipped, no ApplyDiff); p2's ApplyDiff
	// errors (skipped from the recomputed count); only p1 succeeds.
	applied := keys(differ.applied)
	sort.Strings(applied)
	if contains(applied, "pX") {
		t.Fatalf("pX (centroid read failed) must not reach ApplyDiff, got %v", applied)
	}
	if !contains(applied, "p1") {
		t.Fatalf("healthy policy p1 must be applied, got %v", applied)
	}
	if res.Recomputed != 1 {
		t.Fatalf("expected 1 successful recompute (p1), got %d", res.Recomputed)
	}
	if len(res.Diffs) != 1 || res.Diffs[0].PolicyID != "p1" {
		t.Fatalf("expected only p1's diff, got %+v", res.Diffs)
	}
}

// TestEngine_DrainErrorPropagates: a drain failure (Valkey down) is a hard error
// — there is nothing to work on and the job should retry via the CRD framework.
func TestEngine_DrainErrorPropagates(t *testing.T) {
	e := NewEngine(&fakeDrainer{drainErr: errors.New("valkey down")}, &fakeNeighbors{}, &fakeDiffer{}, 10)
	if _, err := e.Reeval(context.Background()); err == nil {
		t.Fatal("expected drain error to propagate")
	}
}

// fakeScorer is an in-memory BlendedScorer for the convergence tests.
type fakeScorer struct {
	scores map[string]float64 // keyed by related id
	err    error
	called bool
}

func (f *fakeScorer) BlendedScoresFor(_ context.Context, _ string, relatedIDs []string) (map[string]float64, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]float64)
	for _, id := range relatedIDs {
		if s, ok := f.scores[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

// TestEngine_PrefersBlendedScoreWithCentroidFallback: when the learned
// relationships are wired, a pair WITH a learned
// edge uses its blended_score (and is stamped source='blend'); a pair with no
// edge falls back to centroid similarity (1 - distance, source='centroid').
func TestEngine_PrefersBlendedScoreWithCentroidFallback(t *testing.T) {
	drain := &fakeDrainer{ids: []string{"p1"}, corpus: 7}
	nbrs := &fakeNeighbors{byPolicy: map[string][]store.RelatedPolicy{
		"p1": {rp("p2", 0.10), rp("p3", 0.40)},
		"p2": {rp("p1", 0.10)},
		"p3": {rp("p1", 0.40)},
	}}
	differ := &fakeDiffer{}
	// p2 has a learned edge (blended 0.9); p3 does not (falls back to 1-0.40).
	scorer := &fakeScorer{scores: map[string]float64{"p2": 0.9}}
	e := NewEngine(drain, nbrs, differ, 10).WithBlendedScores(scorer)

	if _, err := e.Reeval(context.Background()); err != nil {
		t.Fatalf("Reeval: %v", err)
	}
	if !scorer.called {
		t.Fatal("expected blended scorer to be consulted")
	}
	got := map[string]store.Suggestion{}
	for _, s := range differ.applied["p1"] {
		got[s.RelatedID] = s
	}
	p2, ok := got["p2"]
	if !ok {
		t.Fatal("expected a suggestion for p2")
	}
	if p2.Score != 0.9 || p2.Source != store.SuggestionSourceBlend {
		t.Fatalf("p2 should use blended score 0.9 / source=blend, got score=%v source=%s", p2.Score, p2.Source)
	}
	// centroid_dist is always preserved for explainability.
	if p2.CentroidDist != 0.10 {
		t.Fatalf("p2 centroid_dist should be preserved (0.10), got %v", p2.CentroidDist)
	}
	p3 := got["p3"]
	if p3.Source != store.SuggestionSourceCentroid {
		t.Fatalf("p3 (no edge) should fall back to source=centroid, got %s", p3.Source)
	}
	if diff := p3.Score - 0.60; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("p3 should fall back to 1-distance=0.60, got %v", p3.Score)
	}
}

// TestEngine_BlendedScorerErrorFallsBackToCentroid: a scorer failure is
// non-fatal — the re-eval logs and falls back to pure centroid geometry.
func TestEngine_BlendedScorerErrorFallsBackToCentroid(t *testing.T) {
	drain := &fakeDrainer{ids: []string{"p1"}, corpus: 1}
	nbrs := &fakeNeighbors{byPolicy: map[string][]store.RelatedPolicy{
		"p1": {rp("p2", 0.25)},
		"p2": {rp("p1", 0.25)},
	}}
	differ := &fakeDiffer{}
	e := NewEngine(drain, nbrs, differ, 10).WithBlendedScores(&fakeScorer{err: errors.New("db down")})

	if _, err := e.Reeval(context.Background()); err != nil {
		t.Fatalf("Reeval should not fail on a scorer error: %v", err)
	}
	for _, s := range differ.applied["p1"] {
		if s.RelatedID == "p2" && s.Source != store.SuggestionSourceCentroid {
			t.Fatalf("scorer error must fall back to source=centroid, got %s", s.Source)
		}
	}
}

func keys(m map[string][]store.Suggestion) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func contains(s []string, v string) bool {
	return slices.Contains(s, v)
}
