// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
)

// Summary is a durable, publish-time-generated summary of one policy
// version — the read-cheap counterpart to the on-demand
// generation.Assist(AssistOperationSummarize) call. Written once by the
// policy.published consumer (see internal/consumer.PublishEventConsumer);
// a later view of the policy reads this row instead of re-invoking the LLM.
type Summary struct {
	VersionID   string
	PolicyID    string
	PolicyTitle string
	SummaryText string
	GeneratedAt time.Time
}

// SummaryStore reads and writes rows in the ai_summaries table.
type SummaryStore struct {
	db *postgres.DB
}

// NewSummaryStore constructs a SummaryStore backed by the given database. A nil
// pool is permitted for compile-time wiring and unit tests; methods will
// return an error rather than panic, matching ChunkStore/JobResultStore.
func NewSummaryStore(db *postgres.DB) *SummaryStore {
	return &SummaryStore{db: db}
}

// UpsertSummary inserts or overwrites the summary for a version, keyed on
// version_id. A later regenerate-on-demand step (not yet built) will call
// this same method to replace a stale summary for the same version.
func (s *SummaryStore) UpsertSummary(ctx context.Context, sum Summary) error {
	if s.db == nil {
		return fmt.Errorf("store: summary store has no pool configured")
	}
	const q = `
INSERT INTO ai_summaries (version_id, policy_id, policy_title, summary_text)
VALUES ($1, $2, $3, $4)
ON CONFLICT (version_id) DO UPDATE SET
    policy_id     = EXCLUDED.policy_id,
    policy_title  = EXCLUDED.policy_title,
    summary_text  = EXCLUDED.summary_text,
    generated_at  = now()`
	if _, err := s.db.Pool().Exec(ctx, q, sum.VersionID, sum.PolicyID, sum.PolicyTitle, sum.SummaryText); err != nil {
		return fmt.Errorf("store: upsert summary: %w", err)
	}
	return nil
}

// GetSummary reads the stored summary for a version. This is for the later
// read path (view-time lookup instead of regeneration) — not yet wired to
// any RPC, but added now so that follow-up is a plain read against an
// existing store method.
func (s *SummaryStore) GetSummary(ctx context.Context, versionID string) (Summary, error) {
	if s.db == nil {
		return Summary{}, fmt.Errorf("store: summary store has no pool configured")
	}
	var sum Summary
	const q = `SELECT version_id, policy_id, policy_title, summary_text, generated_at
FROM ai_summaries WHERE version_id = $1`
	if err := s.db.Pool().QueryRow(ctx, q, versionID).Scan(
		&sum.VersionID, &sum.PolicyID, &sum.PolicyTitle, &sum.SummaryText, &sum.GeneratedAt,
	); err != nil {
		return Summary{}, fmt.Errorf("store: get summary: %w", err)
	}
	return sum, nil
}
