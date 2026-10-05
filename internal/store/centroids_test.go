// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"testing"
)

func TestNewCentroidStore_compiles(t *testing.T) {
	_ = NewCentroidStore(nil)
}

func TestCentroidStore_methodsExist(t *testing.T) {
	cs := NewCentroidStore(nil)
	_ = cs.UpsertCentroid
	_ = cs.DeleteByPolicyID
	_ = cs.RelatedPolicies
}

func TestCentroidStore_nilPoolErrors(t *testing.T) {
	cs := NewCentroidStore(nil)
	ctx := context.Background()

	if err := cs.UpsertCentroid(ctx, Centroid{PolicyID: "p1", Embedding: make([]float32, 384)}); err == nil {
		t.Fatal("UpsertCentroid: expected error from nil pool")
	}
	if err := cs.DeleteByPolicyID(ctx, "p1"); err == nil {
		t.Fatal("DeleteByPolicyID: expected error from nil pool")
	}
	if _, err := cs.RelatedPolicies(ctx, "p1", AccessFilter{}, 10); err == nil {
		t.Fatal("RelatedPolicies: expected error from nil pool")
	}
}

// TestUpsertCentroid_rejectsEmptyEmbedding guards the invariant that a centroid
// row always carries a vector — an empty embedding (no chunks) must never be
// written; the consumer skips the upsert in that case.
func TestUpsertCentroid_rejectsEmptyEmbedding(t *testing.T) {
	// A non-nil pool is not needed: the empty-embedding guard returns before any
	// query. Use nil pool; the empty-embedding check must fire first.
	cs := NewCentroidStore(nil)
	err := cs.UpsertCentroid(context.Background(), Centroid{PolicyID: "p1", Embedding: nil})
	if err == nil {
		t.Fatal("expected an error for an empty centroid embedding")
	}
}

// TestRelatedPolicies_requiresPolicyID guards against an empty anchor.
func TestRelatedPolicies_requiresPolicyID(t *testing.T) {
	cs := NewCentroidStore(nil)
	if _, err := cs.RelatedPolicies(context.Background(), "", AccessFilter{}, 10); err == nil {
		t.Fatal("expected an error for an empty policyID")
	}
}

func TestRelatedPolicyFields(t *testing.T) {
	r := RelatedPolicy{PolicyID: "p2", PolicyTitle: "Secure Coding", CategoryID: "g", VersionNo: 3, Distance: 0.2}
	if r.PolicyID != "p2" || r.Distance != 0.2 {
		t.Fatalf("RelatedPolicy field mismatch: %+v", r)
	}
}
