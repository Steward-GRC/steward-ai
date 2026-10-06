// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package reeval

import (
	"context"

	"github.com/Bugs5382/go-log"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// Drainer is the part of PendingSet the Engine uses.
type Drainer interface {
	Drain(ctx context.Context) ([]string, error)
	CorpusVersion(ctx context.Context) (int64, error)
}

// NeighborSource returns a policy's centroid neighbours.
// *store.CentroidStore satisfies it.
type NeighborSource interface {
	NeighborsByCentroid(ctx context.Context, policyID string, filter store.AccessFilter, topN int) ([]store.RelatedPolicy, error)
}

// DiffApplier writes a policy's recomputed suggestions as a diff.
// *store.RelatedStore satisfies it.
type DiffApplier interface {
	ApplyDiff(ctx context.Context, policyID string, desired []store.Suggestion, corpusVersion int64) (store.DiffResult, error)
}

// BlendedScorer returns the learned blended score for a policy's candidate
// related ids. *store.RelationshipStore satisfies it. With one wired, a pair
// with a learned edge scores by it and the rest by centroid similarity, so
// the centroid starts a new policy off and the learned score takes over.
type BlendedScorer interface {
	BlendedScoresFor(ctx context.Context, policyID string, relatedIDs []string) (map[string]float64, error)
}

// PolicyDiff is one policy's change in suggestions, for the completion event
// so the interface can badge new and dropped suggestions.
type PolicyDiff struct {
	PolicyID          string   `json:"policyId"`
	NewlySuggested    []string `json:"newlySuggested,omitempty"`
	NoLongerSuggested []string `json:"noLongerSuggested,omitempty"`
}

// Result is a RELATED_REEVAL job's result.
type Result struct {
	// Changed are the ids drained from the pending set.
	Changed []string `json:"changed,omitempty"`
	// Recomputed counts the policies recomputed: the changed ones and their
	// neighbours.
	Recomputed int `json:"recomputed"`
	// Diffs lists every policy whose suggested set changed.
	Diffs []PolicyDiff `json:"diffs,omitempty"`
}

// Engine is the drain half: it drains the pending set and rewrites the
// affected policies' suggestions. Postgres and Valkey only.
type Engine struct {
	pending   Drainer
	neighbors NeighborSource
	differ    DiffApplier
	scorer    BlendedScorer
	topN      int
	logger    log.Logger
}

// NewEngine returns an Engine; a non-positive topN uses DefaultTopN.
func NewEngine(pending Drainer, neighbors NeighborSource, differ DiffApplier, topN int) *Engine {
	if topN <= 0 {
		topN = DefaultTopN
	}
	return &Engine{pending: pending, neighbors: neighbors, differ: differ, topN: topN, logger: log.Nop()}
}

// WithBlendedScores scores by the learned relationships where they exist and
// returns the Engine.
func (e *Engine) WithBlendedScores(s BlendedScorer) *Engine {
	e.scorer = s
	return e
}

// WithLogger sets the logger and returns the Engine.
func (e *Engine) WithLogger(l log.Logger) *Engine {
	e.logger = l
	return e
}

// allAccess is the scope re-evaluation reads neighbours under: every
// category, sensitive included, so the geometry is complete. Suggestions are
// stored unfiltered and the caller's scope is applied when they are read.
var allAccess = store.AccessFilter{AllCategories: true}

// Reeval drains the set and, for each drained policy and each of its centroid
// neighbours (whose own lists may gain or lose it), recomputes the suggested
// set and applies it as a diff. A failure for one policy is logged and
// skipped, never ending the pass. An empty set does nothing.
func (e *Engine) Reeval(ctx context.Context) (Result, error) {
	l := e.logger.Ctx(ctx)

	changed, err := e.pending.Drain(ctx)
	if err != nil {
		return Result{}, err
	}
	if len(changed) == 0 {
		l.Debug("reeval: nothing pending")
		return Result{}, nil
	}

	corpusVersion, err := e.pending.CorpusVersion(ctx)
	if err != nil {
		l.Warn("reeval: corpus version read failed; stamping suggestions with version 0", log.F("error", err.Error()))
		corpusVersion = 0
	}

	affected := make(map[string]struct{}, len(changed)*(e.topN+1))
	for _, p := range changed {
		affected[p] = struct{}{}
		nbrs, err := e.neighbors.NeighborsByCentroid(ctx, p, allAccess, e.topN)
		if err != nil {
			l.Warn("reeval: neighbour lookup failed; recomputing the changed policy only",
				log.F("policy_id", p), log.F("error", err.Error()))
			continue
		}
		for _, n := range nbrs {
			affected[n.PolicyID] = struct{}{}
		}
	}

	result := Result{Changed: changed}
	for policyID := range affected {
		desired, err := e.desiredSuggestions(ctx, policyID, corpusVersion)
		if err != nil {
			l.Warn("reeval: recompute suggestions failed; policy skipped",
				log.F("policy_id", policyID), log.F("error", err.Error()))
			continue
		}
		diff, err := e.differ.ApplyDiff(ctx, policyID, desired, corpusVersion)
		if err != nil {
			l.Warn("reeval: apply diff failed; policy skipped",
				log.F("policy_id", policyID), log.F("error", err.Error()))
			continue
		}
		result.Recomputed++
		if len(diff.NewlySuggested) > 0 || len(diff.NoLongerSuggested) > 0 {
			result.Diffs = append(result.Diffs, PolicyDiff{
				PolicyID:          policyID,
				NewlySuggested:    diff.NewlySuggested,
				NoLongerSuggested: diff.NoLongerSuggested,
			})
		}
	}

	l.Info("reeval: RELATED_REEVAL completed",
		log.F("changed", len(changed)),
		log.F("recomputed", result.Recomputed),
		log.F("diffs", len(result.Diffs)),
		log.F("corpus_version", corpusVersion))
	return result, nil
}

// desiredSuggestions is the policy's top-N neighbours as suggestions, scored
// 1 - cosine distance (higher is more related) or by the learned blended
// score where there is one. A scorer failure falls back to the centroid, so
// a degraded learner never holds up freshness.
func (e *Engine) desiredSuggestions(ctx context.Context, policyID string, corpusVersion int64) ([]store.Suggestion, error) {
	nbrs, err := e.neighbors.NeighborsByCentroid(ctx, policyID, allAccess, e.topN)
	if err != nil {
		return nil, err
	}

	var blended map[string]float64
	if e.scorer != nil && len(nbrs) > 0 {
		ids := make([]string, 0, len(nbrs))
		for _, n := range nbrs {
			ids = append(ids, n.PolicyID)
		}
		if b, err := e.scorer.BlendedScoresFor(ctx, policyID, ids); err != nil {
			l := e.logger.Ctx(ctx)
			l.Warn("reeval: blended score lookup failed; using centroid similarity",
				log.F("policy_id", policyID), log.F("error", err.Error()))
		} else {
			blended = b
		}
	}

	out := make([]store.Suggestion, 0, len(nbrs))
	for _, n := range nbrs {
		score := 1 - n.Distance
		source := store.SuggestionSourceCentroid
		if bs, ok := blended[n.PolicyID]; ok {
			score = float32(bs)
			source = store.SuggestionSourceBlend
		}
		out = append(out, store.Suggestion{
			PolicyID:      policyID,
			RelatedID:     n.PolicyID,
			Score:         score,
			CentroidDist:  n.Distance,
			Source:        source,
			Status:        store.SuggestionStatusSuggested,
			CorpusVersion: corpusVersion,
		})
	}
	return out, nil
}
