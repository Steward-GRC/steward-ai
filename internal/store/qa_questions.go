// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"

	postgres "github.com/Bugs5382/go-postgres"
)

// defaultTopQuestionsLimit is used when a caller passes a non-positive
// limit; maxTopQuestionsLimit caps the other end so a misbehaving/malicious
// caller can't force an unbounded scan.
const (
	defaultTopQuestionsLimit = 6
	maxTopQuestionsLimit     = 20
)

// QAQuestionStore reads aggregate ask-count data off the ai_qa_cache table
// (owned/written by internal/cache.QACache — see its package doc) to back
// the "most-asked questions" suggestion feature. It is read-only: nothing in
// this package increments ask_count, since that happens as a side effect of
// a real question-and-answer round trip (see QACache.Lookup/Put), not a
// stand-alone store write.
type QAQuestionStore struct {
	db *postgres.DB
}

// NewQAQuestionStore constructs a QAQuestionStore backed by the given database.
// A nil database is permitted for compile-time wiring and unit tests; methods
// will return an error rather than panic, matching ChunkStore/SummaryStore.
func NewQAQuestionStore(db *postgres.DB) *QAQuestionStore {
	return &QAQuestionStore{db: db}
}

// TopQuestions returns the limit most-asked questions that returned a real,
// universally-readable answer — filtered to rows where a source was actually
// found (no_authorized_source = false), none of the sources were sensitive
// (has_sensitive_source = false), and every cited policy is still indexed
// and readable under filter, so a suggested question never reveals a policy
// the caller can't read.
//
// Ranking is by SUM(ask_count) (a question can have been cached as several
// near-duplicate-but-not-identical rows via the semantic cache tier; their
// counts are combined) then MAX(created_at) as a tiebreak, so among equally
// popular questions the most recently seen one sorts first.
//
// limit is clamped to [1, maxTopQuestionsLimit]; a non-positive value uses
// defaultTopQuestionsLimit.
func (s *QAQuestionStore) TopQuestions(ctx context.Context, filter AccessFilter, limit int) ([]string, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: qa question store has no pool configured")
	}
	switch {
	case limit <= 0:
		limit = defaultTopQuestionsLimit
	case limit > maxTopQuestionsLimit:
		limit = maxTopQuestionsLimit
	}

	const q = `
SELECT question_text
FROM ai_qa_cache q
WHERE no_authorized_source = false AND has_sensitive_source = false
  AND NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(q.citations_json) AS elem
      LEFT JOIN ai_policy_centroids c ON c.policy_id = elem->>'policyId'
      WHERE c.policy_id IS NULL
         OR NOT ($2 OR (c.category_id = ANY($3) AND (c.sensitivity = 'standard' OR $4)))
  )
GROUP BY question_text
ORDER BY SUM(ask_count) DESC, MAX(created_at) DESC
LIMIT $1`
	rows, err := s.db.Pool().Query(ctx, q, limit, filter.AllCategories, filter.CategoryIDs, filter.IncludeSensitive)
	if err != nil {
		return nil, fmt.Errorf("store: top questions: %w", err)
	}
	defer rows.Close()

	questions := make([]string, 0, limit)
	for rows.Next() {
		var question string
		if err := rows.Scan(&question); err != nil {
			return nil, fmt.Errorf("store: top questions scan: %w", err)
		}
		questions = append(questions, question)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: top questions: %w", err)
	}
	return questions, nil
}
