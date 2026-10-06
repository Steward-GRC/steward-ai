// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package relationship

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// fakeAggregator is an in-memory aggregator so the blend/normalization/candidate
// -union logic is exercised without Postgres (the SQL is covered by store
// integration tests).
type fakeAggregator struct {
	co         []store.CoRetrievalPair
	cand       []store.CandidatePair
	cross      []store.Pair
	sims       map[string]float64 // for CentroidSimForPairs, keyed by store.PairKey
	categories map[string]string
	upserted   []store.RelationshipEdge
	err        error
}

func (f *fakeAggregator) CoRetrievalPairs(context.Context, time.Duration) ([]store.CoRetrievalPair, error) {
	return f.co, f.err
}
func (f *fakeAggregator) CentroidCandidatePairs(context.Context, int) ([]store.CandidatePair, error) {
	return f.cand, nil
}
func (f *fakeAggregator) CrossrefPairs(context.Context) ([]store.Pair, error) { return f.cross, nil }
func (f *fakeAggregator) CentroidSimForPairs(_ context.Context, pairs []store.Pair) (map[string]float64, error) {
	out := map[string]float64{}
	for _, p := range pairs {
		if s, ok := f.sims[store.PairKey(p.PolicyA, p.PolicyB)]; ok {
			out[store.PairKey(p.PolicyA, p.PolicyB)] = s
		}
	}
	return out, nil
}
func (f *fakeAggregator) PolicyCategories(context.Context) (map[string]string, error) {
	return f.categories, nil
}
func (f *fakeAggregator) UpsertEdges(_ context.Context, edges []store.RelationshipEdge) (int, error) {
	f.upserted = edges
	return len(edges), nil
}

type fakeCoeffs struct {
	b store.BlendCoefficients
}

func (f fakeCoeffs) BlendCoefficients(context.Context) (store.BlendCoefficients, error) {
	return f.b, nil
}

func edgeByPair(edges []store.RelationshipEdge, a, b string) (store.RelationshipEdge, bool) {
	key := store.PairKey(a, b)
	for _, e := range edges {
		if store.PairKey(e.PolicyA, e.PolicyB) == key {
			return e, true
		}
	}
	return store.RelationshipEdge{}, false
}

func approx(a, b float64) bool {
	d := a - b
	return d < 1e-6 && d > -1e-6
}

// TestLearn_BlendMathWithTunableCoefficients is the blend contract: the
// blended score is w_c*centroid + w_u*usage + w_x*crossref + w_e*entity, usage
// is min-max normalized, entity fires on a shared category, crossref on an explicit
// link, and the coefficients come from the (tunable) coefficient source.
func TestLearn_BlendMathWithTunableCoefficients(t *testing.T) {
	agg := &fakeAggregator{
		// two centroid candidate pairs with known similarity
		cand: []store.CandidatePair{
			{PolicyA: "a", PolicyB: "b", CentroidSim: 0.8},
			{PolicyA: "a", PolicyB: "c", CentroidSim: 0.4},
		},
		// a-b co-retrieved a lot (raw 100), a-c a little (raw 25) -> normalized 1.0 and 0.25
		co: []store.CoRetrievalPair{
			{PolicyA: "a", PolicyB: "b", UsageRaw: 100, CoRetrievalN: 10},
			{PolicyA: "a", PolicyB: "c", UsageRaw: 25, CoRetrievalN: 3},
		},
		// a-b explicitly cross-referenced
		cross: []store.Pair{{PolicyA: "a", PolicyB: "b"}},
		// a and b share a category; c is in another
		categories: map[string]string{"a": "g1", "b": "g1", "c": "g2"},
	}
	coeffs := store.BlendCoefficients{Centroid: 0.5, Usage: 0.3, Crossref: 0.15, Entity: 0.05}
	e := NewEngine(agg, fakeCoeffs{coeffs}, 0, 0)

	res, err := e.Learn(context.Background())
	if err != nil {
		t.Fatalf("Learn: %v", err)
	}
	if res.EdgeCount != 2 {
		t.Fatalf("expected 2 edges, got %d", res.EdgeCount)
	}

	ab, ok := edgeByPair(agg.upserted, "a", "b")
	if !ok {
		t.Fatal("missing a-b edge")
	}
	// usage: 100/100=1.0; entity: 1 (shared g1); crossref: 1.
	if !approx(ab.UsageWeight, 1.0) || !approx(ab.EntityWeight, 1.0) || !approx(ab.CrossrefWeight, 1.0) {
		t.Fatalf("a-b weights: usage=%v entity=%v crossref=%v", ab.UsageWeight, ab.EntityWeight, ab.CrossrefWeight)
	}
	wantAB := 0.5*0.8 + 0.3*1.0 + 0.15*1.0 + 0.05*1.0 // = 0.9
	if !approx(ab.BlendedScore, wantAB) {
		t.Fatalf("a-b blended: want %v got %v", wantAB, ab.BlendedScore)
	}
	if ab.CoRetrievalN != 10 {
		t.Fatalf("a-b co_retrieval_n want 10 got %d", ab.CoRetrievalN)
	}

	ac, _ := edgeByPair(agg.upserted, "a", "c")
	// usage 25/100=0.25; no shared category; no crossref.
	wantAC := 0.5*0.4 + 0.3*0.25 + 0.15*0.0 + 0.05*0.0 // = 0.275
	if !approx(ac.UsageWeight, 0.25) || !approx(ac.EntityWeight, 0) || !approx(ac.CrossrefWeight, 0) {
		t.Fatalf("a-c weights: usage=%v entity=%v crossref=%v", ac.UsageWeight, ac.EntityWeight, ac.CrossrefWeight)
	}
	if !approx(ac.BlendedScore, wantAC) {
		t.Fatalf("a-c blended: want %v got %v", wantAC, ac.BlendedScore)
	}

	// Tunability: swapping coefficients changes the score with no other input change.
	e2 := NewEngine(agg, fakeCoeffs{store.BlendCoefficients{Centroid: 1, Usage: 0, Crossref: 0, Entity: 0}}, 0, 0)
	if _, err := e2.Learn(context.Background()); err != nil {
		t.Fatalf("Learn (retuned): %v", err)
	}
	ab2, _ := edgeByPair(agg.upserted, "a", "b")
	if !approx(ab2.BlendedScore, 0.8) { // pure centroid now
		t.Fatalf("retuned a-b blended: want 0.8 got %v", ab2.BlendedScore)
	}
}

// TestLearn_CentroidBaseBackfilledForCoRetrievedOnlyPairs: a pair that is
// co-retrieved (or cross-referenced) but NOT among the centroid candidates still
// gets the always-present centroid base, fetched via CentroidSimForPairs.
func TestLearn_CentroidBaseBackfilledForCoRetrievedOnlyPairs(t *testing.T) {
	agg := &fakeAggregator{
		cand: nil, // no centroid candidates
		co:   []store.CoRetrievalPair{{PolicyA: "x", PolicyB: "y", UsageRaw: 10, CoRetrievalN: 2}},
		sims: map[string]float64{store.PairKey("x", "y"): 0.6},
	}
	e := NewEngine(agg, fakeCoeffs{store.BlendCoefficients{Centroid: 0.5, Usage: 0.3}}, 0, 0)
	if _, err := e.Learn(context.Background()); err != nil {
		t.Fatalf("Learn: %v", err)
	}
	xy, ok := edgeByPair(agg.upserted, "x", "y")
	if !ok {
		t.Fatal("missing x-y edge")
	}
	if !approx(xy.CentroidSim, 0.6) {
		t.Fatalf("x-y centroid base should be backfilled to 0.6, got %v", xy.CentroidSim)
	}
	// blended = 0.5*0.6 + 0.3*1.0 (usage normalized to 1) = 0.6
	if !approx(xy.BlendedScore, 0.6) {
		t.Fatalf("x-y blended want 0.6 got %v", xy.BlendedScore)
	}
}

// TestLearn_EmptyCorpusIsNoOp: no candidates anywhere -> no edges, clean result.
func TestLearn_EmptyCorpusIsNoOp(t *testing.T) {
	agg := &fakeAggregator{}
	e := NewEngine(agg, fakeCoeffs{}, 0, 0)
	res, err := e.Learn(context.Background())
	if err != nil {
		t.Fatalf("Learn: %v", err)
	}
	if res.EdgeCount != 0 || len(agg.upserted) != 0 {
		t.Fatalf("expected no edges on empty corpus, got %d", res.EdgeCount)
	}
}

// TestLearn_PropagatesAggregationError: a Postgres aggregation failure aborts so
// the CRD retry framework can retry the whole job.
func TestLearn_PropagatesAggregationError(t *testing.T) {
	agg := &fakeAggregator{err: errors.New("pg down")}
	e := NewEngine(agg, fakeCoeffs{}, 0, 0)
	if _, err := e.Learn(context.Background()); err == nil {
		t.Fatal("expected aggregation error to propagate")
	}
}

// TestLearn_ScorePercentiles: the metrics summary reports edge count and score
// percentiles.
func TestLearn_ScorePercentiles(t *testing.T) {
	// four centroid candidate pairs with sims 0.1,0.2,0.3,0.4; pure-centroid blend
	agg := &fakeAggregator{cand: []store.CandidatePair{
		{PolicyA: "a", PolicyB: "b", CentroidSim: 0.1},
		{PolicyA: "a", PolicyB: "c", CentroidSim: 0.2},
		{PolicyA: "a", PolicyB: "d", CentroidSim: 0.3},
		{PolicyA: "a", PolicyB: "e", CentroidSim: 0.4},
	}}
	e := NewEngine(agg, fakeCoeffs{store.BlendCoefficients{Centroid: 1}}, 0, 0)
	res, err := e.Learn(context.Background())
	if err != nil {
		t.Fatalf("Learn: %v", err)
	}
	if res.EdgeCount != 4 {
		t.Fatalf("edge count want 4 got %d", res.EdgeCount)
	}
	if !approx(res.ScoreMax, 0.4) {
		t.Fatalf("score max want 0.4 got %v", res.ScoreMax)
	}
	if res.ScoreP50 <= 0 || res.ScoreP90 <= 0 {
		t.Fatalf("expected positive percentiles, got p50=%v p90=%v", res.ScoreP50, res.ScoreP90)
	}
}
