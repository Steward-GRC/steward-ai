// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

func TestUserQueryLimitStore_GetMissing_notFound(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewUserQueryLimitStore(pool)

	_, found, err := repo.Get(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if found {
		t.Fatal("expected found=false for a user with no override")
	}
}

func TestUserQueryLimitStore_SetGetUpsert_roundTrips(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewUserQueryLimitStore(pool)
	ctx := context.Background()

	if err := repo.Set(ctx, "user-1", 25); err != nil {
		t.Fatalf("set: %v", err)
	}
	limit, found, err := repo.Get(ctx, "user-1")
	if err != nil || !found {
		t.Fatalf("get after set: found=%v err=%v", found, err)
	}
	if limit != 25 {
		t.Fatalf("expected 25, got %d", limit)
	}

	// Upsert overwrites rather than duplicating.
	if err := repo.Set(ctx, "user-1", 10); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	limit, _, _ = repo.Get(ctx, "user-1")
	if limit != 10 {
		t.Fatalf("expected upsert to 10, got %d", limit)
	}
	var count int
	if err := pool.Pool().QueryRow(ctx, `SELECT count(*) FROM ai_user_query_limit WHERE user_id = $1`, "user-1").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one row per user, got %d", count)
	}
}

func TestUserQueryLimitStore_UnlimitedSentinel(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewUserQueryLimitStore(pool)
	ctx := context.Background()

	if err := repo.Set(ctx, "vip", -1); err != nil {
		t.Fatalf("set -1: %v", err)
	}
	limit, found, err := repo.Get(ctx, "vip")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if limit != -1 {
		t.Fatalf("expected unlimited sentinel -1, got %d", limit)
	}
}

func TestUserQueryLimitStore_Delete_revertsToNoOverride(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewUserQueryLimitStore(pool)
	ctx := context.Background()

	if err := repo.Set(ctx, "user-1", 5); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := repo.Delete(ctx, "user-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, found, err := repo.Get(ctx, "user-1")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if found {
		t.Fatal("expected no override after delete")
	}
	// Deleting a non-existent user is a no-op, not an error.
	if err := repo.Delete(ctx, "ghost"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestUserQueryLimitStore_RejectsZero(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewUserQueryLimitStore(pool)
	// The CHECK constraint rejects 0 (callers resolve "0 clears" to Delete).
	if err := repo.Set(context.Background(), "user-1", 0); err == nil {
		t.Fatal("expected the CHECK constraint to reject daily_limit=0")
	}
}
