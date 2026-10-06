// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

func TestChunkDeleteByPolicyID_removesEveryVersionOfOnePolicy(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewChunkStore(pool)
	ctx := context.Background()

	rows := []store.Chunk{
		{PolicyID: "prc-1", VersionID: "ver-1", VersionNo: 1, SectionKey: "steps", ChunkIndex: 0},
		{PolicyID: "prc-1", VersionID: "ver-2", VersionNo: 2, SectionKey: "steps", ChunkIndex: 0},
		{PolicyID: "prc-1", VersionID: "ver-2", VersionNo: 2, SectionKey: "steps", ChunkIndex: 1},
		{PolicyID: "pol-2", VersionID: "ver-9", VersionNo: 1, SectionKey: "scope", ChunkIndex: 0},
	}
	for i, c := range rows {
		c.ContentText = "text"
		c.Embedding = makeEmbedding(i)
		c.CategoryID = "cat-workplace"
		c.Sensitivity = "standard"
		c.PolicyTitle = "Title"
		if err := repo.UpsertChunk(ctx, c); err != nil {
			t.Fatalf("UpsertChunk: %v", err)
		}
	}

	if err := repo.DeleteByPolicyID(ctx, "prc-1"); err != nil {
		t.Fatalf("DeleteByPolicyID: %v", err)
	}

	count := func(policyID string) int {
		var n int
		if err := pool.Pool().QueryRow(ctx,
			`SELECT count(*) FROM ai_chunks WHERE policy_id = $1`, policyID).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	if n := count("prc-1"); n != 0 {
		t.Fatalf("prc-1 chunks left: %d", n)
	}
	if n := count("pol-2"); n != 1 {
		t.Fatalf("pol-2 chunks: got %d want 1", n)
	}
}

func TestChunkDeleteByPolicyID_nilPool(t *testing.T) {
	if err := store.NewChunkStore(nil).DeleteByPolicyID(context.Background(), "p"); err == nil {
		t.Fatal("expected an error with no pool")
	}
}
