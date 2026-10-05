// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"math"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
)

// ln2 is math.Ln2, inlined so the exponential time-decay factor can be spelled
// directly into SQL: decay(age) = exp(-ln2 * age / half_life), i.e. weight
// halves every half_life. Kept as a literal (not a Go import) because it is
// interpolated into the aggregation query, not computed in Go.
const ln2 = 0.6931471805599453

// PairKey canonicalizes an unordered policy pair into a single map/dedupe key
// (policyA<policyB joined by a NUL, which cannot appear in a policy id). The
// undirected edge substrate keys every pair the same way — see
// ai_policy_relationship's (policy_a<policy_b) primary key.
func PairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

// CoRetrievalPair is one co-retrieved (co-cited) policy pair aggregated from the
// durable Q&A history (ai_qa_cache.citations_json). PolicyA<PolicyB is
// canonical. UsageRaw is the TIME-DECAYED sum of ask_count over every Q&A row
// that co-cited the pair (recent asks weigh more — the substrate "stays
// current"); it is normalized into ai_policy_relationship.usage_weight by the
// learn engine. CoRetrievalN is the raw count of co-citing rows, stored verbatim
// for explainability.
type CoRetrievalPair struct {
	PolicyA      string
	PolicyB      string
	UsageRaw     float64
	CoRetrievalN int
}

// CandidatePair is one centroid-near policy pair (from bounded per-policy KNN,
// never the full N²) with its centroid similarity (1 - cosine_distance,
// higher = more similar). PolicyA<PolicyB is canonical.
type CandidatePair struct {
	PolicyA     string
	PolicyB     string
	CentroidSim float64
}

// Pair is a bare canonical (PolicyA<PolicyB) policy pair.
type Pair struct {
	PolicyA string
	PolicyB string
}

// RelationshipEdge is one fully-computed row of the ai_policy_relationship
// weighted-edge substrate: the four component signals plus the materialized
// blended score. PolicyA<PolicyB is canonical.
type RelationshipEdge struct {
	PolicyA        string
	PolicyB        string
	CentroidSim    float64
	UsageWeight    float64
	CrossrefWeight float64
	EntityWeight   float64
	BlendedScore   float64
	CoRetrievalN   int
}

// RelationshipStore reads and writes the ai_policy_relationship substrate and
// runs the pure-Postgres aggregations the RELATIONSHIP_LEARN job blends.
// It is the WRITE side of the substrate; the read/convergence side
// (BlendedScoresFor) is consumed by the related re-evaluation so a matured edge's
// blended_score supersedes the raw centroid distance.
type RelationshipStore struct {
	db *postgres.DB
}

// NewRelationshipStore constructs a RelationshipStore backed by the given database.
// A nil database is permitted for compile-time wiring and unit tests; methods return
// an error rather than panic, matching CentroidStore/RelatedStore.
func NewRelationshipStore(db *postgres.DB) *RelationshipStore {
	return &RelationshipStore{db: db}
}

// CoRetrievalPairs aggregates the co-retrieval usage signal from the durable
// Q&A history: for every ai_qa_cache row it forms the
// unordered pairs of DISTINCT policies co-cited in that answer, and sums each
// pair's ask_count TIME-DECAYED by the row's age (half-life = halfLife) so older
// questions fade. Returns one row per co-cited pair with the decayed UsageRaw
// and the raw CoRetrievalN (count of co-citing rows). Pure Postgres — no LLM.
//
// This scans only rows that actually co-cite (a policy cited alone produces no
// pair), so it never materializes the full N²; the candidate set is exactly the
// pairs usage has already connected.
func (s *RelationshipStore) CoRetrievalPairs(ctx context.Context, halfLife time.Duration) ([]CoRetrievalPair, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: pool is nil")
	}
	halfLifeSeconds := halfLife.Seconds()
	if halfLifeSeconds <= 0 {
		halfLifeSeconds = (30 * 24 * time.Hour).Seconds()
	}

	const q = `
WITH cited AS (
    SELECT q.id, q.ask_count, q.created_at, (elem->>'policyId') AS policy_id
    FROM ai_qa_cache q
    CROSS JOIN LATERAL jsonb_array_elements(q.citations_json) AS elem
    WHERE COALESCE(elem->>'policyId', '') <> ''
),
distinct_cited AS (
    SELECT DISTINCT id, ask_count, created_at, policy_id FROM cited
),
pairs AS (
    SELECT a.id, a.ask_count, a.created_at,
           a.policy_id AS policy_a, b.policy_id AS policy_b
    FROM distinct_cited a
    JOIN distinct_cited b ON a.id = b.id AND a.policy_id < b.policy_id
)
SELECT policy_a, policy_b,
       COUNT(*) AS co_retrieval_n,
       SUM(ask_count * exp(-($1::double precision) * EXTRACT(EPOCH FROM (now() - created_at)) / ($2::double precision))) AS usage_raw
FROM pairs
GROUP BY policy_a, policy_b`

	rows, err := s.db.Pool().Query(ctx, q, ln2, halfLifeSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CoRetrievalPair
	for rows.Next() {
		var p CoRetrievalPair
		if err := rows.Scan(&p.PolicyA, &p.PolicyB, &p.CoRetrievalN, &p.UsageRaw); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CentroidCandidatePairs returns the centroid-near candidate pairs: for each
// policy, its topK nearest centroid neighbours. The per-policy LATERAL LIMIT bounds the scan
// to N*topK rows before the GROUP BY canonicalizes/dedupes each symmetric pair,
// so the cost is linear in the corpus, not quadratic. CentroidSim is
// 1 - cosine_distance (higher = more similar).
func (s *RelationshipStore) CentroidCandidatePairs(ctx context.Context, topK int) ([]CandidatePair, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: pool is nil")
	}
	if topK <= 0 {
		topK = 10
	}

	const q = `
SELECT LEAST(c.policy_id, n.policy_id)    AS policy_a,
       GREATEST(c.policy_id, n.policy_id) AS policy_b,
       MIN(c.centroid <=> n.centroid)     AS dist
FROM ai_policy_centroids c
CROSS JOIN LATERAL (
    SELECT n2.policy_id, n2.centroid
    FROM ai_policy_centroids n2
    WHERE n2.policy_id <> c.policy_id
    ORDER BY n2.centroid <=> c.centroid
    LIMIT $1
) n
GROUP BY LEAST(c.policy_id, n.policy_id), GREATEST(c.policy_id, n.policy_id)`

	rows, err := s.db.Pool().Query(ctx, q, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CandidatePair
	for rows.Next() {
		var (
			p    CandidatePair
			dist float64
		)
		if err := rows.Scan(&p.PolicyA, &p.PolicyB, &dist); err != nil {
			return nil, err
		}
		p.CentroidSim = 1 - dist
		out = append(out, p)
	}
	return out, rows.Err()
}

// CrossrefPairs returns the explicit cross-reference candidate pairs: policies an author has explicitly linked. Until core.policy_relations
// is reachable in this workspace, the authoritative in-service proxy is the set
// of 'accepted' ai_related_policies rows — a suggestion an author promoted to a
// real core link (see internal/store/related.go). Canonicalized and deduped
// (either direction), self-links dropped.
func (s *RelationshipStore) CrossrefPairs(ctx context.Context) ([]Pair, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: pool is nil")
	}
	const q = `
SELECT DISTINCT LEAST(policy_id, related_id)    AS policy_a,
                GREATEST(policy_id, related_id) AS policy_b
FROM ai_related_policies
WHERE status = 'accepted' AND policy_id <> related_id`

	rows, err := s.db.Pool().Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Pair
	for rows.Next() {
		var p Pair
		if err := rows.Scan(&p.PolicyA, &p.PolicyB); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CentroidSimForPairs computes centroid similarity (1 - cosine_distance) for an
// EXPLICIT set of pairs — the candidate pairs that came from co-retrieval or
// cross-refs but are not among the centroid-KNN candidates, so the learn engine
// can still give them the always-present centroid base. Pairs whose either
// policy has no centroid row (never published / not backfilled) simply do not
// appear in the result (the engine treats a missing sim as 0). Bounded by the
// caller's pair count, never N².
func (s *RelationshipStore) CentroidSimForPairs(ctx context.Context, pairs []Pair) (map[string]float64, error) {
	out := make(map[string]float64, len(pairs))
	if len(pairs) == 0 {
		return out, nil
	}
	if s.db == nil {
		return nil, fmt.Errorf("store: pool is nil")
	}
	as := make([]string, len(pairs))
	bs := make([]string, len(pairs))
	for i, p := range pairs {
		as[i], bs[i] = p.PolicyA, p.PolicyB
	}

	const q = `
SELECT p.a, p.b, (ca.centroid <=> cb.centroid) AS dist
FROM unnest($1::text[], $2::text[]) AS p(a, b)
JOIN ai_policy_centroids ca ON ca.policy_id = p.a
JOIN ai_policy_centroids cb ON cb.policy_id = p.b`

	rows, err := s.db.Pool().Query(ctx, q, as, bs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var a, b string
		var dist float64
		if err := rows.Scan(&a, &b, &dist); err != nil {
			return nil, err
		}
		out[PairKey(a, b)] = 1 - dist
	}
	return out, rows.Err()
}

// PolicyCategories returns each policy's category id (from
// ai_policy_centroids), the input for the entity-weight term: two policies in
// the same category share an "entity" signal. A cheap full scan of a
// one-row-per-policy table.
func (s *RelationshipStore) PolicyCategories(ctx context.Context) (map[string]string, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: pool is nil")
	}
	rows, err := s.db.Pool().Query(ctx, `SELECT policy_id, category_id FROM ai_policy_centroids`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var id, grp string
		if err := rows.Scan(&id, &grp); err != nil {
			return nil, err
		}
		out[id] = grp
	}
	return out, rows.Err()
}

// UpsertEdges writes the fully-computed edges to ai_policy_relationship in one
// statement (INSERT … SELECT unnest … ON CONFLICT DO UPDATE), refreshing every
// component and the blended score and stamping updated_at. Idempotent: re-running
// the learn job overwrites the prior computation. Returns the number of rows
// written.
func (s *RelationshipStore) UpsertEdges(ctx context.Context, edges []RelationshipEdge) (int, error) {
	if s.db == nil {
		return 0, fmt.Errorf("store: pool is nil")
	}
	if len(edges) == 0 {
		return 0, nil
	}
	n := len(edges)
	as := make([]string, n)
	bs := make([]string, n)
	cs := make([]float32, n)
	uw := make([]float32, n)
	xw := make([]float32, n)
	ew := make([]float32, n)
	bl := make([]float32, n)
	cn := make([]int32, n)
	for i, e := range edges {
		as[i], bs[i] = e.PolicyA, e.PolicyB
		cs[i] = float32(e.CentroidSim)
		uw[i] = float32(e.UsageWeight)
		xw[i] = float32(e.CrossrefWeight)
		ew[i] = float32(e.EntityWeight)
		bl[i] = float32(e.BlendedScore)
		cn[i] = clampInt32(e.CoRetrievalN)
	}

	const q = `
INSERT INTO ai_policy_relationship AS r
    (policy_a, policy_b, centroid_sim, usage_weight, crossref_weight, entity_weight, blended_score, co_retrieval_n)
SELECT a, b, cs, uw, xw, ew, bs, cn
FROM unnest($1::text[], $2::text[], $3::real[], $4::real[], $5::real[], $6::real[], $7::real[], $8::int[])
     AS t(a, b, cs, uw, xw, ew, bs, cn)
ON CONFLICT (policy_a, policy_b) DO UPDATE SET
    centroid_sim    = EXCLUDED.centroid_sim,
    usage_weight    = EXCLUDED.usage_weight,
    crossref_weight = EXCLUDED.crossref_weight,
    entity_weight   = EXCLUDED.entity_weight,
    blended_score   = EXCLUDED.blended_score,
    co_retrieval_n  = EXCLUDED.co_retrieval_n,
    updated_at      = now()`

	tag, err := s.db.Pool().Exec(ctx, q, as, bs, cs, uw, xw, ew, bl, cn)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// BlendedScoresFor is the convergence read: given a policy and
// a set of candidate related ids, it returns the learned blended_score for every
// pair that already has a substrate edge, keyed by the OTHER policy id. The
// re-eval engine prefers this matured score over the raw centroid distance and
// falls back to centroid geometry for pairs with no edge yet — so centroid
// bootstraps a new policy and the substrate takes over as usage accrues. The
// edge is undirected, so it matches policyID on either side.
func (s *RelationshipStore) BlendedScoresFor(ctx context.Context, policyID string, relatedIDs []string) (map[string]float64, error) {
	out := make(map[string]float64, len(relatedIDs))
	if len(relatedIDs) == 0 {
		return out, nil
	}
	if s.db == nil {
		return nil, fmt.Errorf("store: pool is nil")
	}
	const q = `
SELECT CASE WHEN policy_a = $1 THEN policy_b ELSE policy_a END AS other, blended_score
FROM ai_policy_relationship
WHERE (policy_a = $1 AND policy_b = ANY($2))
   OR (policy_b = $1 AND policy_a = ANY($2))`

	rows, err := s.db.Pool().Query(ctx, q, policyID, relatedIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var other string
		var score float64
		if err := rows.Scan(&other, &score); err != nil {
			return nil, err
		}
		out[other] = score
	}
	return out, rows.Err()
}

// clampInt32 narrows a count to the int4 column, clamped to [0, MaxInt32].
func clampInt32(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	}
	return int32(n) // #nosec G115 -- clamped to the int32 range above
}
