// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

func TestUsageStore_AddAccumulatesPerMonth(t *testing.T) {
	repo := store.NewUsageStore(newTestDB(t))
	ctx := context.Background()
	oct := time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC)
	nov := time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC)

	for _, u := range []store.Usage{{Requests: 1, InputTokens: 100, OutputTokens: 40}, {Requests: 1, InputTokens: 20, OutputTokens: 5}} {
		if err := repo.Add(ctx, oct, u); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if err := repo.Add(ctx, nov, store.Usage{Requests: 1, InputTokens: 7, OutputTokens: 3}); err != nil {
		t.Fatalf("add nov: %v", err)
	}

	got, err := repo.Month(ctx, oct)
	if err != nil {
		t.Fatalf("month: %v", err)
	}
	if got != (store.Usage{Requests: 2, InputTokens: 120, OutputTokens: 45}) {
		t.Fatalf("october usage = %+v", got)
	}
	got, err = repo.Month(ctx, nov)
	if err != nil {
		t.Fatalf("month: %v", err)
	}
	if got.Requests != 1 {
		t.Fatalf("november usage = %+v", got)
	}
}

func TestUsageStore_EmptyMonthIsZero(t *testing.T) {
	got, err := store.NewUsageStore(newTestDB(t)).Month(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("month: %v", err)
	}
	if got != (store.Usage{}) {
		t.Fatalf("expected zero usage, got %+v", got)
	}
}

func TestMonthStartIsUTC(t *testing.T) {
	east := time.FixedZone("UTC-5", -5*3600)
	got := store.MonthStart(time.Date(2026, 10, 31, 21, 0, 0, 0, east))
	if !got.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("MonthStart = %v, want 2026-11-01 UTC", got)
	}
}
