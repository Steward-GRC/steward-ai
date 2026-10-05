// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

func TestSummaryStore_UpsertAndGet_roundTrips(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewSummaryStore(pool)
	ctx := context.Background()

	in := store.Summary{
		VersionID:   "ver-1",
		PolicyID:    "pol-1",
		PolicyTitle: "HR Policy",
		SummaryText: "- All employees must comply.\n- Reviewed annually.",
	}
	if err := repo.UpsertSummary(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := repo.GetSummary(ctx, "ver-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.VersionID != in.VersionID || got.PolicyID != in.PolicyID ||
		got.PolicyTitle != in.PolicyTitle || got.SummaryText != in.SummaryText {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, in)
	}
	if got.GeneratedAt.IsZero() {
		t.Fatal("expected GeneratedAt to be populated")
	}
}

func TestSummaryStore_UpsertSummary_overwritesOnRepublish(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewSummaryStore(pool)
	ctx := context.Background()

	base := store.Summary{
		VersionID:   "ver-2",
		PolicyID:    "pol-2",
		PolicyTitle: "IT Policy",
		SummaryText: "- Original summary.",
	}
	if err := repo.UpsertSummary(ctx, base); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// A regenerate-on-demand call (or a re-publish of the same version)
	// overwrites the row rather than duplicating it.
	updated := base
	updated.SummaryText = "- Updated summary."
	if err := repo.UpsertSummary(ctx, updated); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var count int
	if err := pool.Pool().QueryRow(ctx, `SELECT count(*) FROM ai_summaries WHERE version_id = $1`, base.VersionID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row after upsert, got %d", count)
	}

	got, err := repo.GetSummary(ctx, base.VersionID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SummaryText != "- Updated summary." {
		t.Fatalf("expected updated summary text, got %q", got.SummaryText)
	}
}

func TestSummaryStore_GetSummary_notFound(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewSummaryStore(pool)
	ctx := context.Background()

	if _, err := repo.GetSummary(ctx, "does-not-exist"); err == nil {
		t.Fatal("expected error for missing version_id, got nil")
	}
}
