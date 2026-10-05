// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"

	postgres "github.com/Bugs5382/go-postgres"
)

// JobResult is a durable record of one completed async AI job's generated
// content — the Postgres-side "memory" the operator writes alongside the
// Redis hot-tier result, so a completed job's content survives Redis TTL
// eviction and is there to review/resume even after the browser session
// that triggered it is long gone.
type JobResult struct {
	JobID       string
	Operation   string // "DRAFT" | "REVIEW" | ... (ai/api/v1alpha1.JobOperation)
	PolicyID    string // may be empty (e.g. DRAFT runs ahead of policy creation)
	VersionID   string // may be empty
	ActorUserID string
	ResultJSON  string // the generation payload's JSON, same shape as the Redis envelope's "result"
}

// JobResultStore reads and writes rows in the ai_job_results table.
type JobResultStore struct {
	db *postgres.DB
}

// NewJobResultStore constructs a JobResultStore backed by the given database. A
// nil database is permitted for compile-time wiring and unit tests; Save will
// return an error rather than panic.
func NewJobResultStore(db *postgres.DB) *JobResultStore {
	return &JobResultStore{db: db}
}

// Save upserts one job's durable result record, keyed by job_id — a retried
// reconcile of the same job (e.g. after a requeue) overwrites rather than
// duplicating.
func (s *JobResultStore) Save(ctx context.Context, r JobResult) error {
	if s.db == nil {
		return fmt.Errorf("store: job result store has no pool configured")
	}
	const q = `
INSERT INTO ai_job_results (job_id, operation, policy_id, version_id, actor_user_id, result_json)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (job_id) DO UPDATE SET
    operation      = EXCLUDED.operation,
    policy_id      = EXCLUDED.policy_id,
    version_id     = EXCLUDED.version_id,
    actor_user_id  = EXCLUDED.actor_user_id,
    result_json    = EXCLUDED.result_json,
    updated_at     = now()`
	if _, err := s.db.Pool().Exec(ctx, q, r.JobID, r.Operation, r.PolicyID, r.VersionID, r.ActorUserID, r.ResultJSON); err != nil {
		return fmt.Errorf("store: save job result: %w", err)
	}
	return nil
}

// ResultExists reports whether a durable result row exists for jobID — the
// operator GC's "is this job's content still fetchable via aiJobResultContent
// (durable tier)?" guard, checked before deleting a Succeeded job's CRD so
// its reviewable history is never lost. A nil database returns an error rather
// than panicking, matching Save; the caller (GC) fails safe on an error by
// retaining the CRD.
func (s *JobResultStore) ResultExists(ctx context.Context, jobID string) (bool, error) {
	if s.db == nil {
		return false, fmt.Errorf("store: job result store has no pool configured")
	}
	const q = `SELECT EXISTS (SELECT 1 FROM ai_job_results WHERE job_id = $1)`
	var exists bool
	if err := s.db.Pool().QueryRow(ctx, q, jobID).Scan(&exists); err != nil {
		return false, fmt.Errorf("store: check job result exists: %w", err)
	}
	return exists, nil
}
