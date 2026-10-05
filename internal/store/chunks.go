// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package store holds the ai service's Postgres repositories. Every read that
// returns document content applies the caller's read scope inside the SQL, so
// nothing outside the scope reaches the service, the model or the answer.
package store

import (
	"context"
	"fmt"
	"sort"
	"strings"

	postgres "github.com/Bugs5382/go-postgres"
)

// rrfK is the reciprocal-rank-fusion smoothing constant. 60 is the widely-used
// default: large enough that the fused score is dominated by whether a chunk
// appears near the top of EITHER ranked list (vector or keyword) rather than
// by its exact position, so a strong keyword match and a strong vector match
// are weighted comparably.
const rrfK = 60

// Chunk is a single indexed unit from a published policy section.
type Chunk struct {
	ID          string
	PolicyID    string
	VersionID   string
	VersionNo   int
	SectionKey  string
	ChunkIndex  int
	ContentText string
	Embedding   []float32
	CategoryID  string
	Sensitivity string // airules.SensitivityStandard | airules.SensitivitySensitive
	PolicyTitle string
	// DocumentType is the kind of source this chunk came from —
	// airules.DocumentTypePolicy | airules.DocumentTypeProcedure —
	// denormalized here so retrieval results/citations can label a source
	// PRC-… vs POL-…. Empty is persisted as POLICY (see
	// UpsertChunk's COALESCE and the ai_chunks.document_type column default).
	DocumentType string
}

// SearchResult pairs a Chunk with its cosine distance to the query embedding.
// Lower Distance means more similar.
type SearchResult struct {
	Chunk
	Distance float32
}

// AccessFilter is the read scope a query runs under. A row is readable when
// AllCategories is set, or its category is in CategoryIDs and it is standard
// or IncludeSensitive is set. airules.ChunkReadable states the same rule in
// Go; the SQL predicates here must stay in step with it.
type AccessFilter struct {
	CategoryIDs      []string
	IncludeSensitive bool
	AllCategories    bool
}

// ChunkStore reads and writes chunks in the ai_chunks table.
type ChunkStore struct {
	db *postgres.DB
}

// NewChunkStore constructs a ChunkStore backed by the given database.
// A nil database is permitted for compile-time wiring and unit tests; methods
// will return an error rather than panic.
func NewChunkStore(db *postgres.DB) *ChunkStore {
	return &ChunkStore{db: db}
}

// UpsertChunk inserts or updates a chunk keyed on (version_id, section_key, chunk_index).
// The denormalized category_id, sensitivity, and policy_title are required so that
// SearchByCosine can apply access filters without a join.
func (s *ChunkStore) UpsertChunk(ctx context.Context, c Chunk) error {
	if s.db == nil {
		return fmt.Errorf("store: pool is nil")
	}
	_, err := s.db.Pool().Exec(ctx, `
		INSERT INTO ai_chunks
			(policy_id, version_id, version_no, section_key, chunk_index,
			 content_text, embedding, category_id, sensitivity, policy_title, document_type)
		VALUES ($1,$2,$3,$4,$5,$6,$7::vector,$8,$9,$10,COALESCE(NULLIF($11,''),'POLICY'))
		ON CONFLICT (version_id, section_key, chunk_index)
		DO UPDATE SET
			content_text  = EXCLUDED.content_text,
			embedding     = EXCLUDED.embedding,
			document_type = EXCLUDED.document_type,
			indexed_at    = now()
	`,
		c.PolicyID, c.VersionID, c.VersionNo, c.SectionKey, c.ChunkIndex,
		c.ContentText, pgvecLiteral(c.Embedding), c.CategoryID, c.Sensitivity, c.PolicyTitle,
		c.DocumentType,
	)
	return err
}

// DeleteByVersionID removes all chunks for a given version. This is called
// when a policy version is superseded, unpublished, or archived so stale
// content cannot be retrieved.
func (s *ChunkStore) DeleteByVersionID(ctx context.Context, versionID string) error {
	if s.db == nil {
		return fmt.Errorf("store: pool is nil")
	}
	_, err := s.db.Pool().Exec(ctx,
		`DELETE FROM ai_chunks WHERE version_id = $1`, versionID)
	return err
}

// SearchByCosine returns the topK nearest chunks to queryEmbedding, filtered by
// the caller's AccessFilter. Filtering is applied in-query so blocked rows never
// reach the caller or the model.
//
// The WHERE predicate is the read rule (see AccessFilter). pgx encodes
// []string{} as an empty Postgres text array, so "category_id = ANY($2)"
// matches zero rows when CategoryIDs is empty: an empty scope reads nothing
// unless AllCategories is set.
//
// canonical rule: see internal/airules.ChunkReadable — this SQL predicate
// and that Go function must be kept in lockstep; the sensitivity value
// literal below ('standard') is internal/airules.SensitivityStandard's raw
// string (SQL cannot import a Go constant, so it is spelled out here).
func (s *ChunkStore) SearchByCosine(
	ctx context.Context,
	queryEmbedding []float32,
	topK int,
	filter AccessFilter,
) ([]SearchResult, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: pool is nil")
	}

	rows, err := s.db.Pool().Query(ctx, `
		SELECT id, policy_id, version_id, version_no, section_key, chunk_index,
		       content_text, category_id, sensitivity, policy_title, document_type,
		       (embedding <=> $1::vector) AS distance
		FROM ai_chunks
		WHERE $4
		   OR (category_id = ANY($2) AND (sensitivity = 'standard' OR $5))
		ORDER BY embedding <=> $1::vector
		LIMIT $3
	`,
		pgvecLiteral(queryEmbedding),
		filter.CategoryIDs,
		topK,
		filter.AllCategories,
		filter.IncludeSensitive,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(
			&r.ID, &r.PolicyID, &r.VersionID, &r.VersionNo, &r.SectionKey,
			&r.ChunkIndex, &r.ContentText, &r.CategoryID, &r.Sensitivity,
			&r.PolicyTitle, &r.DocumentType, &r.Distance,
		); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchHybrid returns the topK best chunks for a query using a HYBRID of
// vector similarity (cosine distance over the embedding) and keyword relevance
// (Postgres full-text ts_rank over content_text), fused with reciprocal rank
// fusion (RRF), then re-ranked in Go by exact distinct-term overlap.
//
// It applies the EXACT SAME access predicate as SearchByCosine in-query, so blocked rows never reach
// the caller or the model — the two methods must stay in lockstep on that
// predicate. queryText is the raw user question; an empty/stopword-only query
// degenerates gracefully to pure vector ranking (every ts_rank is 0, so RRF is
// driven by the vector list). SearchByCosine is retained unchanged for callers
// that want pure vector search.
//
// Two stages: (1) SQL candidate generation + RRF fuse (below), returning up to
// topK fused candidates; (2) a Go re-rank (reRankByKeyword) that promotes
// candidates sharing the most distinct query terms with their content,
// stable-preserving the fused order on ties. Stage 2 refines the corpus-
// statistics-based ts_rank with an exact term-presence signal over just the
// returned pool.
func (s *ChunkStore) SearchHybrid(
	ctx context.Context,
	queryEmbedding []float32,
	queryText string,
	topK int,
	filter AccessFilter,
) ([]SearchResult, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: pool is nil")
	}

	rows, err := s.db.Pool().Query(ctx, `
		WITH filtered AS (
			SELECT id, policy_id, version_id, version_no, section_key, chunk_index,
			       content_text, category_id, sensitivity, policy_title, document_type,
			       (embedding <=> $1::vector) AS distance,
			       ts_rank(to_tsvector('english', content_text),
			               websearch_to_tsquery('english', $6)) AS kw_rank
			FROM ai_chunks
			WHERE $4
			   OR (category_id = ANY($2) AND (sensitivity = 'standard' OR $5))
		),
		ranked AS (
			SELECT *,
			       rank() OVER (ORDER BY distance ASC)          AS vec_rank,
			       rank() OVER (ORDER BY kw_rank DESC, distance ASC) AS kw_pos
			FROM filtered
		)
		SELECT id, policy_id, version_id, version_no, section_key, chunk_index,
		       content_text, category_id, sensitivity, policy_title, document_type, distance
		FROM ranked
		ORDER BY (1.0 / ($7 + vec_rank) + 1.0 / ($7 + kw_pos)) DESC, distance ASC
		LIMIT $3
	`,
		pgvecLiteral(queryEmbedding),
		filter.CategoryIDs,
		topK,
		filter.AllCategories,
		filter.IncludeSensitive,
		queryText,
		rrfK,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(
			&r.ID, &r.PolicyID, &r.VersionID, &r.VersionNo, &r.SectionKey,
			&r.ChunkIndex, &r.ContentText, &r.CategoryID, &r.Sensitivity,
			&r.PolicyTitle, &r.DocumentType, &r.Distance,
		); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	reRankByKeyword(results, queryText)
	return results, nil
}

// reRankByKeyword re-orders the fused candidate pool in place: candidates
// sharing more distinct query terms with their content_text move ahead, and
// the incoming (RRF-fused) order is preserved on ties (stable sort). A query
// with no usable terms leaves the fused order untouched. This is the re-rank stage layered over the SQL fuse — an exact term-presence refinement
// of the corpus-statistics ts_rank, operating only over the already-returned
// pool.
func reRankByKeyword(results []SearchResult, queryText string) {
	terms := tokenSet(queryText)
	if len(terms) == 0 || len(results) < 2 {
		return
	}
	overlap := make([]int, len(results))
	for i := range results {
		ct := tokenSet(results[i].ContentText)
		n := 0
		for t := range terms {
			if ct[t] {
				n++
			}
		}
		overlap[i] = n
	}
	idx := make([]int, len(results))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return overlap[idx[a]] > overlap[idx[b]] })
	reordered := make([]SearchResult, len(results))
	for newPos, oldPos := range idx {
		reordered[newPos] = results[oldPos]
	}
	copy(results, reordered)
}

// tokenSet lowercases s and returns the set of distinct alphanumeric tokens of
// length >= 3 (matching the re-rank granularity; short function words add
// noise without discriminating power). Kept package-local so store has no
// dependency on generation's identical helper.
func tokenSet(s string) map[string]bool {
	set := make(map[string]bool)
	var b strings.Builder
	flush := func() {
		if b.Len() >= 3 {
			set[b.String()] = true
		}
		b.Reset()
	}
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return set
}

// pgvecLiteral converts a float32 slice to the pgvector wire literal '[f,f,f,...]'.
// Kept as a thin alias so existing call sites in this file are unaffected;
// see PgVectorLiteral for the exported version other packages (internal/cache) use.
func pgvecLiteral(v []float32) string { return PgVectorLiteral(v) }

// PgVectorLiteral converts a float32 slice to the pgvector wire literal
// '[f,f,f,...]', exported so other AI-service packages that also write/query
// vector columns (internal/cache's Postgres-tier semantic Q&A cache) can
// reuse the same encoding rather than duplicating it.
//
// We use the textual literal form (cast via ::vector in SQL) rather than depending
// on the upstream pgvector-go binary encoder. This keeps the module's dependency
// surface minimal and matches the pattern used elsewhere in the platform; pgvector
// parses the literal once per query, which is negligible next to the cosine search.
func PgVectorLiteral(v []float32) string {
	b := make([]byte, 0, len(v)*12)
	b = append(b, '[')
	for i, f := range v {
		if i > 0 {
			b = append(b, ',')
		}
		b = fmt.Appendf(b, "%g", f)
	}
	b = append(b, ']')
	return string(b)
}
