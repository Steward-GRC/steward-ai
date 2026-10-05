// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

func TestJobResultStore_Save_insertsAndReadsBack(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewJobResultStore(pool)
	ctx := context.Background()

	r := store.JobResult{
		JobID:       "aijob-abc123",
		Operation:   "DRAFT",
		ActorUserID: "user-1",
		ResultJSON:  `{"sections":[{"sectionKey":"purpose","content":"Body."}]}`,
	}
	if err := repo.Save(ctx, r); err != nil {
		t.Fatalf("save: %v", err)
	}

	var gotOp, gotResult string
	row := pool.Pool().QueryRow(ctx, `SELECT operation, result_json::text FROM ai_job_results WHERE job_id = $1`, r.JobID)
	if err := row.Scan(&gotOp, &gotResult); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if gotOp != "DRAFT" {
		t.Fatalf("expected operation DRAFT, got %q", gotOp)
	}
}

func TestJobResultStore_Save_upsertsOnRetry(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewJobResultStore(pool)
	ctx := context.Background()

	base := store.JobResult{
		JobID:       "aijob-retry",
		Operation:   "REVIEW",
		PolicyID:    "pol-1",
		VersionID:   "ver-1",
		ActorUserID: "user-2",
		ResultJSON:  `{"findings":[]}`,
	}
	if err := repo.Save(ctx, base); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// A retried reconcile of the same job overwrites, not duplicates.
	updated := base
	updated.ResultJSON = `{"findings":[{"sectionKey":"purpose","severity":"blocker","finding":"x"}]}`
	if err := repo.Save(ctx, updated); err != nil {
		t.Fatalf("second save: %v", err)
	}

	var count int
	if err := pool.Pool().QueryRow(ctx, `SELECT count(*) FROM ai_job_results WHERE job_id = $1`, base.JobID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row after upsert, got %d", count)
	}

	var gotResult string
	if err := pool.Pool().QueryRow(ctx, `SELECT result_json::text FROM ai_job_results WHERE job_id = $1`, base.JobID).Scan(&gotResult); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if gotResult == `{"findings":[]}` {
		t.Fatal("expected the row to reflect the updated result, not the original")
	}
}
