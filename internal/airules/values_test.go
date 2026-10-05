// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package airules

import "testing"

// TestRetrievalTopKDefault_is50 pins the default candidate pool: retrieval fetches 50
// candidate chunks (before access-filtering) when no site-admin override is
// configured. A regression to the old value of 10 would silently narrow the
// hybrid retrieval + re-rank candidate pool.
func TestRetrievalTopKDefault_is50(t *testing.T) {
	if RetrievalTopKDefault != 50 {
		t.Fatalf("expected RetrievalTopKDefault=50, got %d", RetrievalTopKDefault)
	}
}

// TestRetrievalTopKCeiling_bounds pins the clamp ceiling SetAIRetrievalConfig
// enforces, and that it is above the default (so the default is always a legal
// value).
func TestRetrievalTopKCeiling_bounds(t *testing.T) {
	if RetrievalTopKCeiling < RetrievalTopKDefault {
		t.Fatalf("ceiling %d must be >= default %d", RetrievalTopKCeiling, RetrievalTopKDefault)
	}
	if RetrievalTopKCeiling != 200 {
		t.Fatalf("expected RetrievalTopKCeiling=200, got %d", RetrievalTopKCeiling)
	}
}
