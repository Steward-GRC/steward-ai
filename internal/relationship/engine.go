// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package relationship

import (
	"context"
	"sort"
	"time"

	"github.com/Bugs5382/go-log"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// Aggregator is the part of *store.RelationshipStore the Engine uses: the
// Postgres aggregations the blend combines, and the upsert.
type Aggregator interface {
	CoRetrievalPairs(ctx context.Context, halfLife time.Duration) ([]store.CoRetrievalPair, error)
	CentroidCandidatePairs(ctx context.Context, topK int) ([]store.CandidatePair, error)
	CrossrefPairs(ctx context.Context) ([]store.Pair, error)
	CentroidSimForPairs(ctx context.Context, pairs []store.Pair) (map[string]float64, error)
	PolicyCategories(ctx context.Context) (map[string]string, error)
	UpsertEdges(ctx context.Context, edges []store.RelationshipEdge) (int, error)
}

// CoefficientSource supplies the blend coefficients, tunable at runtime from
// the AI settings.
type CoefficientSource interface {
	BlendCoefficients(ctx context.Context) (store.BlendCoefficients, error)
}

// Result is a RELATIONSHIP_LEARN job's result and metrics: the edge count and
// score percentiles are what a run reports.
type Result struct {
	EdgeCount              int     `json:"edgeCount"`
	CoRetrievalPairs       int     `json:"coRetrievalPairs"`
	CrossrefPairs          int     `json:"crossrefPairs"`
	CentroidCandidatePairs int     `json:"centroidCandidatePairs"`
	ScoreP50               float64 `json:"scoreP50"`
	ScoreP90               float64 `json:"scoreP90"`
	ScoreMax               float64 `json:"scoreMax"`
	UsageWeightMax         float64 `json:"usageWeightMax"`
}

// Engine runs RELATIONSHIP_LEARN: it gathers candidate pairs (centroid-near,
// co-retrieved or cross-referenced, never all N²), works out each pair's four
// signals, blends them and upserts the edges.
type Engine struct {
	store         Aggregator
	coeffs        CoefficientSource
	halfLife      time.Duration
	candidateTopK int
	logger        log.Logger
}

// NewEngine returns an Engine. A non-positive halfLife uses
// DefaultUsageHalfLife and a non-positive candidateTopK uses
// DefaultCandidateTopK.
func NewEngine(s Aggregator, coeffs CoefficientSource, halfLife time.Duration, candidateTopK int) *Engine {
	if halfLife <= 0 {
		halfLife = DefaultUsageHalfLife
	}
	if candidateTopK <= 0 {
		candidateTopK = DefaultCandidateTopK
	}
	return &Engine{store: s, coeffs: coeffs, halfLife: halfLife, candidateTopK: candidateTopK, logger: log.Nop()}
}

// WithLogger sets the logger and returns the Engine.
func (e *Engine) WithLogger(l log.Logger) *Engine {
	e.logger = l
	return e
}

// edgeAccum collects one pair's signals from the three candidate sources.
type edgeAccum struct {
	a, b         string
	centroidSim  float64
	hasSim       bool
	usageRaw     float64
	coRetrievalN int
	crossref     bool
}

// Learn runs one pass and returns its metrics. The coefficients are read once
// at the start, so a retune applies from the next run. It unions the candidate
// pairs, fills in the centroid similarity of pairs that came only from
// co-retrieval or cross-references, scales usage into [0,1], blends and
// upserts. An empty corpus does nothing.
func (e *Engine) Learn(ctx context.Context) (Result, error) {
	l := e.logger.Ctx(ctx)

	coeffs, err := e.coeffs.BlendCoefficients(ctx)
	if err != nil {
		return Result{}, err
	}

	co, err := e.store.CoRetrievalPairs(ctx, e.halfLife)
	if err != nil {
		return Result{}, err
	}
	cand, err := e.store.CentroidCandidatePairs(ctx, e.candidateTopK)
	if err != nil {
		return Result{}, err
	}
	cross, err := e.store.CrossrefPairs(ctx)
	if err != nil {
		return Result{}, err
	}
	categories, err := e.store.PolicyCategories(ctx)
	if err != nil {
		return Result{}, err
	}

	// Pairs are keyed canonically. Centroid candidates bring their similarity;
	// the others get it filled in below.
	acc := make(map[string]*edgeAccum)
	get := func(a, b string) *edgeAccum {
		k := store.PairKey(a, b)
		e, ok := acc[k]
		if !ok {
			if a > b {
				a, b = b, a
			}
			e = &edgeAccum{a: a, b: b}
			acc[k] = e
		}
		return e
	}
	for _, c := range cand {
		ea := get(c.PolicyA, c.PolicyB)
		ea.centroidSim = c.CentroidSim
		ea.hasSim = true
	}
	for _, c := range co {
		ea := get(c.PolicyA, c.PolicyB)
		ea.usageRaw = c.UsageRaw
		ea.coRetrievalN = c.CoRetrievalN
	}
	for _, c := range cross {
		get(c.PolicyA, c.PolicyB).crossref = true
	}

	// The centroid similarity is the base of every edge, so fill it in for
	// pairs that came from co-retrieval or cross-references only.
	var missing []store.Pair
	for _, ea := range acc {
		if !ea.hasSim {
			missing = append(missing, store.Pair{PolicyA: ea.a, PolicyB: ea.b})
		}
	}
	if len(missing) > 0 {
		sims, err := e.store.CentroidSimForPairs(ctx, missing)
		if err != nil {
			return Result{}, err
		}
		for _, p := range missing {
			if sim, ok := sims[store.PairKey(p.PolicyA, p.PolicyB)]; ok {
				ea := acc[store.PairKey(p.PolicyA, p.PolicyB)]
				ea.centroidSim = sim
				ea.hasSim = true
			}
		}
	}

	// Scale the decayed usage into [0,1]. The minimum is 0 (no co-retrieval),
	// so this is raw/max; with no usage at all every term stays 0.
	var usageMax float64
	for _, ea := range acc {
		if ea.usageRaw > usageMax {
			usageMax = ea.usageRaw
		}
	}

	edges := make([]store.RelationshipEdge, 0, len(acc))
	scores := make([]float64, 0, len(acc))
	for _, ea := range acc {
		usage := 0.0
		if usageMax > 0 {
			usage = ea.usageRaw / usageMax
		}
		entity := 0.0
		if ca, oka := categories[ea.a]; oka && ca != "" {
			if cb, okb := categories[ea.b]; okb && cb == ca {
				entity = 1.0
			}
		}
		crossref := 0.0
		if ea.crossref {
			crossref = 1.0
		}
		blended := coeffs.Centroid*ea.centroidSim +
			coeffs.Usage*usage +
			coeffs.Crossref*crossref +
			coeffs.Entity*entity
		edges = append(edges, store.RelationshipEdge{
			PolicyA:        ea.a,
			PolicyB:        ea.b,
			CentroidSim:    ea.centroidSim,
			UsageWeight:    usage,
			CrossrefWeight: crossref,
			EntityWeight:   entity,
			BlendedScore:   blended,
			CoRetrievalN:   ea.coRetrievalN,
		})
		scores = append(scores, blended)
	}

	written, err := e.store.UpsertEdges(ctx, edges)
	if err != nil {
		return Result{}, err
	}

	res := Result{
		EdgeCount:              written,
		CoRetrievalPairs:       len(co),
		CrossrefPairs:          len(cross),
		CentroidCandidatePairs: len(cand),
		UsageWeightMax:         usageMax,
	}
	res.ScoreP50 = percentile(scores, 0.50)
	res.ScoreP90 = percentile(scores, 0.90)
	res.ScoreMax = percentile(scores, 1.0)

	l.Info("relationship: RELATIONSHIP_LEARN completed",
		log.F("edges", res.EdgeCount),
		log.F("co_retrieval_pairs", res.CoRetrievalPairs),
		log.F("crossref_pairs", res.CrossrefPairs),
		log.F("centroid_candidate_pairs", res.CentroidCandidatePairs),
		log.F("score_p50", res.ScoreP50),
		log.F("score_p90", res.ScoreP90),
		log.F("score_max", res.ScoreMax))
	return res, nil
}

// percentile returns the q-quantile (0..1) of vals using nearest-rank on a
// sorted copy. Empty input is 0. q>=1 returns the max.
func percentile(vals []float64, q float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(q * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
