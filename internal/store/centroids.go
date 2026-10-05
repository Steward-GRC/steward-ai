// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"

	postgres "github.com/Bugs5382/go-postgres"
	"github.com/jackc/pgx/v5"

	"github.com/Steward-GRC/steward-ai/internal/airules"
)

// Centroid is one per-policy centroid row (ai_policy_centroids): the mean of a
// published version's ai_chunks embeddings, plus the denormalized
// category_id/sensitivity/policy_title the access filter and surface rendering
// need.
type Centroid struct {
	PolicyID      string
	VersionID     string
	VersionNo     int
	Embedding     []float32 // the centroid vector (384-dim, mean of the version's chunk embeddings)
	ChunkCount    int
	CategoryID    string
	Sensitivity   string // airules.SensitivityStandard | airules.SensitivitySensitive
	PolicyTitle   string
	CorpusVersion int64
}

// RelatedPolicy is one access-filtered centroid neighbor returned by
// RelatedPolicies: a policy whose centroid is near the queried policy's, with
// the cosine Distance between the two centroids (lower = more related).
type RelatedPolicy struct {
	PolicyID    string
	PolicyTitle string
	CategoryID  string
	VersionNo   int
	Distance    float32
}

// CentroidStore reads and writes rows in the ai_policy_centroids table.
//
// It applies the SAME in-SQL READ access predicate as ChunkStore.SearchByCosine
// when surfacing neighbors, so a relatedness edge never
// leaks the existence of a policy the viewer cannot read.
type CentroidStore struct {
	db *postgres.DB
}

// NewCentroidStore constructs a CentroidStore backed by the given database.
// A nil database is permitted for compile-time wiring and unit tests; methods
// return an error rather than panic.
func NewCentroidStore(db *postgres.DB) *CentroidStore {
	return &CentroidStore{db: db}
}

// UpsertCentroid inserts or replaces the centroid row for a policy, keyed on
// policy_id (one row per policy). Called inline by the policy.published consumer
// after IndexVersion succeeds (see internal/consumer). The denormalized
// category_id/sensitivity/policy_title are required so RelatedPolicies can apply
// its access filter without a join back to core.
func (s *CentroidStore) UpsertCentroid(ctx context.Context, c Centroid) error {
	if s.db == nil {
		return fmt.Errorf("store: pool is nil")
	}
	if len(c.Embedding) == 0 {
		return fmt.Errorf("store: centroid embedding is empty for policy %q", c.PolicyID)
	}
	sensitivity := c.Sensitivity
	if sensitivity == "" {
		sensitivity = airules.SensitivityStandard
	}
	_, err := s.db.Pool().Exec(ctx, `
		INSERT INTO ai_policy_centroids
			(policy_id, version_id, version_no, centroid, chunk_count,
			 category_id, sensitivity, policy_title, corpus_version, computed_at)
		VALUES ($1,$2,$3,$4::vector,$5,$6,$7,$8,$9, now())
		ON CONFLICT (policy_id)
		DO UPDATE SET
			version_id     = EXCLUDED.version_id,
			version_no     = EXCLUDED.version_no,
			centroid       = EXCLUDED.centroid,
			chunk_count    = EXCLUDED.chunk_count,
			category_id       = EXCLUDED.category_id,
			sensitivity    = EXCLUDED.sensitivity,
			policy_title   = EXCLUDED.policy_title,
			corpus_version = EXCLUDED.corpus_version,
			computed_at    = now()
	`,
		c.PolicyID, c.VersionID, c.VersionNo, PgVectorLiteral(c.Embedding), c.ChunkCount,
		c.CategoryID, sensitivity, c.PolicyTitle, c.CorpusVersion,
	)
	return err
}

// BackfillCentroids computes and upserts a centroid for every policy already
// present in ai_chunks, for the one-shot corpus backfill:
// the existing corpus was indexed before centroids existed, so this seeds
// ai_policy_centroids without re-publishing every policy. It returns the number
// of centroid rows written.
//
// For each policy it uses its LATEST version's chunks (max version_no — the
// currently-published one; re-index upserts new-version rows without deleting
// prior ones, so a policy can have several version_ids in ai_chunks). The
// centroid is pgvector's avg(embedding) over that version's chunks — the same
// mean the inline publish-time path computes — and category_id/sensitivity/
// policy_title are the version's denormalized values. Idempotent: ON CONFLICT
// refreshes an existing row, so it is safe to re-run.
func (s *CentroidStore) BackfillCentroids(ctx context.Context) (int, error) {
	if s.db == nil {
		return 0, fmt.Errorf("store: pool is nil")
	}
	tag, err := s.db.Pool().Exec(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (policy_id)
			       policy_id, version_id, version_no, category_id, sensitivity, policy_title
			FROM ai_chunks
			ORDER BY policy_id, version_no DESC
		)
		INSERT INTO ai_policy_centroids
			(policy_id, version_id, version_no, centroid, chunk_count,
			 category_id, sensitivity, policy_title, corpus_version, computed_at)
		SELECT l.policy_id, l.version_id, l.version_no,
		       avg(c.embedding), count(*),
		       l.category_id, l.sensitivity, l.policy_title, 0, now()
		FROM latest l
		JOIN ai_chunks c ON c.version_id = l.version_id
		GROUP BY l.policy_id, l.version_id, l.version_no,
		         l.category_id, l.sensitivity, l.policy_title
		ON CONFLICT (policy_id) DO UPDATE SET
			version_id     = EXCLUDED.version_id,
			version_no     = EXCLUDED.version_no,
			centroid       = EXCLUDED.centroid,
			chunk_count    = EXCLUDED.chunk_count,
			category_id       = EXCLUDED.category_id,
			sensitivity    = EXCLUDED.sensitivity,
			policy_title   = EXCLUDED.policy_title,
			computed_at    = now()
	`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// DeleteByPolicyID removes a policy's centroid row. Called when a policy
// version is unpublished/archived (its chunks are removed too — see
// ChunkStore.DeleteByVersionID) so a no-longer-published policy stops being
// surfaced as a related policy.
func (s *CentroidStore) DeleteByPolicyID(ctx context.Context, policyID string) error {
	if s.db == nil {
		return fmt.Errorf("store: pool is nil")
	}
	_, err := s.db.Pool().Exec(ctx,
		`DELETE FROM ai_policy_centroids WHERE policy_id = $1`, policyID)
	return err
}

// RelatedPolicies returns the topN related policies for policyID, most-related
// first, access-filtered by the caller's AccessFilter.
//
// It PREFERS the precomputed suggestion list (ai_related_policies, maintained
// by the RELATED_REEVAL job) so the read is a plain indexed SELECT:
// once re-eval has populated suggestions for policyID it returns those,
// access-filtered by joining each suggested policy's centroid row for its
// category_id/sensitivity. When there is NO precomputed suggestion for policyID
// (cold cache — never re-evaluated, or its suggestions were all deleted), it
// falls back to a live centroid KNN (NeighborsByCentroid) so a policy is never
// left with no related surface. The access predicate is identical on both
// paths, so switching source never changes what a given caller may see.
//
// A precomputed list that exists but access-filters down to zero rows returns
// empty (it does NOT fall back to live KNN) — the suggestions were computed,
// this caller simply may not read them, and falling back could surface a
// neighbour the precomputed set had legitimately dropped.
func (s *CentroidStore) RelatedPolicies(
	ctx context.Context,
	policyID string,
	filter AccessFilter,
	topN int,
) ([]RelatedPolicy, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: pool is nil")
	}
	if policyID == "" {
		return nil, fmt.Errorf("store: policyID is required")
	}
	topN = clampTopN(topN)

	// Prefer precomputed suggestions when any exist for this policy. A failure
	// of the existence probe is not fatal — fall through to live KNN.
	if has, err := s.hasPrecomputedSuggestions(ctx, policyID); err == nil && has {
		return s.relatedFromPrecomputed(ctx, policyID, filter, topN)
	}
	return s.NeighborsByCentroid(ctx, policyID, filter, topN)
}

// hasPrecomputedSuggestions reports whether ai_related_policies has at least
// one 'suggested' row for policyID — the signal that the precomputed read path
// is warm for this policy.
func (s *CentroidStore) hasPrecomputedSuggestions(ctx context.Context, policyID string) (bool, error) {
	var one int
	err := s.db.Pool().QueryRow(ctx,
		`SELECT 1 FROM ai_related_policies WHERE policy_id = $1 AND status = 'suggested' LIMIT 1`,
		policyID).Scan(&one)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// relatedFromPrecomputed serves the read from the precomputed suggestion list,
// joining ai_policy_centroids for each suggested policy's denormalized
// category_id/sensitivity/title so the SAME in-SQL access predicate as
// ChunkStore.SearchByCosine / NeighborsByCentroid applies. Ordered by the
// stored score (higher = more related); the join also drops any suggestion
// whose target lost its centroid (e.g. unpublished after being suggested), so
// a no-longer-published policy is never surfaced even before re-eval prunes it.
func (s *CentroidStore) relatedFromPrecomputed(
	ctx context.Context,
	policyID string,
	filter AccessFilter,
	topN int,
) ([]RelatedPolicy, error) {
	rows, err := s.db.Pool().Query(ctx, `
		SELECT c.policy_id, c.policy_title, c.category_id, c.version_no, r.centroid_dist AS distance
		FROM ai_related_policies r
		JOIN ai_policy_centroids c ON c.policy_id = r.related_id
		WHERE r.policy_id = $1
		  AND r.status = 'suggested'
		  AND ($3
		       OR (c.category_id = ANY($2) AND (c.sensitivity = 'standard' OR $4)))
		ORDER BY r.score DESC
		LIMIT $5
	`,
		policyID,
		filter.CategoryIDs,
		filter.AllCategories,
		filter.IncludeSensitive,
		topN,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []RelatedPolicy
	for rows.Next() {
		var r RelatedPolicy
		if err := rows.Scan(&r.PolicyID, &r.PolicyTitle, &r.CategoryID, &r.VersionNo, &r.Distance); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// NeighborsByCentroid returns the topN policies whose centroid is nearest
// (cosine) to policyID's centroid, EXCLUDING policyID itself, most-related
// first, access-filtered by the caller's AccessFilter. This is the LIVE
// geometry: the cold-cache fallback for RelatedPolicies, and the source the
// RELATED_REEVAL job (internal/reeval) recomputes suggestions from (passing an
// all-access filter, since access is applied at read time).
//
// The self centroid is resolved in the same query (a CROSS JOIN against the
// policy's own row); if policyID has no centroid (never published / not yet
// backfilled), the join yields no rows and the result is empty — a missing
// anchor is not an error.
//
// The WHERE predicate is the EXACT SAME READ access model as
// ChunkStore.SearchByCosine, evaluated against each CANDIDATE neighbor's
// copied category and sensitivity (see AccessFilter).
//
// canonical rule: see internal/airules.ChunkReadable — the same predicate the
// 'standard' literal below spells out (SQL cannot import the Go constant).
func (s *CentroidStore) NeighborsByCentroid(
	ctx context.Context,
	policyID string,
	filter AccessFilter,
	topN int,
) ([]RelatedPolicy, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: pool is nil")
	}
	if policyID == "" {
		return nil, fmt.Errorf("store: policyID is required")
	}
	topN = clampTopN(topN)

	rows, err := s.db.Pool().Query(ctx, `
		SELECT c.policy_id, c.policy_title, c.category_id, c.version_no,
		       (c.centroid <=> self.centroid) AS distance
		FROM ai_policy_centroids c
		CROSS JOIN (
			SELECT centroid FROM ai_policy_centroids WHERE policy_id = $1
		) AS self
		WHERE c.policy_id <> $1
		  AND ($3
		       OR (c.category_id = ANY($2) AND (c.sensitivity = 'standard' OR $4)))
		ORDER BY c.centroid <=> self.centroid
		LIMIT $5
	`,
		policyID,
		filter.CategoryIDs,
		filter.AllCategories,
		filter.IncludeSensitive,
		topN,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []RelatedPolicy
	for rows.Next() {
		var r RelatedPolicy
		if err := rows.Scan(&r.PolicyID, &r.PolicyTitle, &r.CategoryID, &r.VersionNo, &r.Distance); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// clampTopN applies the RelatedPolicies top-N default (topN<=0) and ceiling.
func clampTopN(topN int) int {
	if topN <= 0 {
		return airules.RelatedPoliciesTopNDefault
	}
	if topN > airules.RelatedPoliciesTopNCeiling {
		return airules.RelatedPoliciesTopNCeiling
	}
	return topN
}
