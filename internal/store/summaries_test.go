// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"testing"
)

// NewSummaryStore compile-check: verifies the constructor signature exists
// and a nil pool is accepted (real pool injected in integration tests).
func TestNewSummaryStore_compiles(t *testing.T) {
	_ = NewSummaryStore(nil)
}

// TestSummaryStore_nilPool documents the nil-pool guard shared by every
// store in this package (ChunkStore, JobResultStore, AIConfigStore): a store
// constructed without a pool (e.g. compile-time wiring where DATABASE_DSN is
// unset) returns an error rather than panicking.
func TestSummaryStore_nilPool(t *testing.T) {
	s := NewSummaryStore(nil)
	ctx := context.Background()

	if err := s.UpsertSummary(ctx, Summary{VersionID: "ver-1"}); err == nil {
		t.Fatal("expected error from nil pool on UpsertSummary; got none")
	}
	if _, err := s.GetSummary(ctx, "ver-1"); err == nil {
		t.Fatal("expected error from nil pool on GetSummary; got none")
	}
}

// UpsertSummary/GetSummary round-trip and overwrite-on-republish behavior
// are covered by summaries_integration_test.go against a real Postgres
// (pgvector image, since the shared migrations set requires the vector
// extension) — see testhelper_test.go's newTestDB.
