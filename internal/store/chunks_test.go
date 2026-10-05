// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"testing"
)

func TestChunkFields(t *testing.T) {
	c := Chunk{
		PolicyID:    "pol-1",
		VersionID:   "ver-1",
		VersionNo:   2,
		SectionKey:  "section_scope",
		ChunkIndex:  0,
		ContentText: "All employees must comply.",
		Embedding:   make([]float32, 384),
		CategoryID:  "hr",
		Sensitivity: "standard",
		PolicyTitle: "HR Policy",
	}
	if c.PolicyID != "pol-1" {
		t.Fatalf("PolicyID mismatch")
	}
	if len(c.Embedding) != 384 {
		t.Fatalf("embedding dimension: got %d", len(c.Embedding))
	}
}

func TestSearchResultFields(t *testing.T) {
	sr := SearchResult{
		Chunk:    Chunk{PolicyID: "pol-1", SectionKey: "scope", DocumentType: "PROCEDURE"},
		Distance: 0.12,
	}
	if sr.Distance != 0.12 {
		t.Fatalf("Distance mismatch")
	}
	// DocumentType is surfaced on retrieval results so a
	// caller can label the source PRC-… vs POL-… .
	if sr.DocumentType != "PROCEDURE" {
		t.Fatalf("expected DocumentType surfaced on SearchResult, got %q", sr.DocumentType)
	}
}

// NewChunkStore compile-check: verifies the constructor signature exists.
func TestNewChunkStore_compiles(t *testing.T) {
	// nil is acceptable in a unit test; real pool injected in integration tests.
	_ = NewChunkStore(nil)
}

// UpsertChunk, DeleteByVersionID, SearchByCosine are tested via integration tests
// in CI (see ci/integration_test.go) where a real pgvector DB is available.
// Here we only verify the method set compiles and the type is exported.
func TestChunkStore_methodsExist(t *testing.T) {
	cs := NewChunkStore(nil)
	_ = cs.UpsertChunk
	_ = cs.DeleteByVersionID
	_ = cs.SearchByCosine
	_ = cs.SearchHybrid
}

func TestSearchHybrid_nilPoolErrors(t *testing.T) {
	cs := NewChunkStore(nil)
	if _, err := cs.SearchHybrid(context.Background(), make([]float32, 384), "q", 10, AccessFilter{}); err == nil {
		t.Fatal("expected error from nil pool")
	}
}

// TestReRankByKeyword_promotesTermOverlapStably verifies the re-rank stage
// moves the higher term-overlap candidate ahead of a zero-overlap one while
// preserving the incoming (fused) order among equal-overlap candidates.
func TestReRankByKeyword_promotesTermOverlapStably(t *testing.T) {
	results := []SearchResult{
		{Chunk: Chunk{ID: "a", ContentText: "parking and facilities rules"}},         // 0 overlap
		{Chunk: Chunk{ID: "b", ContentText: "vacation leave accrual for employees"}}, // 2 overlap
		{Chunk: Chunk{ID: "c", ContentText: "remote work eligibility guidance"}},     // 0 overlap
	}
	reRankByKeyword(results, "vacation leave")
	if results[0].ID != "b" {
		t.Fatalf("expected the keyword-overlapping chunk 'b' first, got %q", results[0].ID)
	}
	// a and c both have zero overlap: their original relative order (a before c) is kept.
	if results[1].ID != "a" || results[2].ID != "c" {
		t.Fatalf("expected stable order among zero-overlap candidates (a,c), got %q,%q", results[1].ID, results[2].ID)
	}
}

// TestReRankByKeyword_noQueryTermsIsNoOp verifies an empty/stopword-only query
// leaves the fused order untouched.
func TestReRankByKeyword_noQueryTermsIsNoOp(t *testing.T) {
	results := []SearchResult{
		{Chunk: Chunk{ID: "a", ContentText: "first"}},
		{Chunk: Chunk{ID: "b", ContentText: "second"}},
	}
	reRankByKeyword(results, "  ")
	if results[0].ID != "a" || results[1].ID != "b" {
		t.Fatalf("expected no reordering with no query terms, got %q,%q", results[0].ID, results[1].ID)
	}
}

// TestSearchByCosine_emptyGroups: an empty scope is an empty Postgres array,
// so "category_id = ANY($2)" is false for every row and nothing leaks. The
// nil-database guard returns before any query; the live behaviour is in
// chunks_integration_test.go.
func TestSearchByCosine_emptyGroups(t *testing.T) {
	// This is a compile-time + documentation test. The nil pool guard returns an error
	// before any query executes, confirming the access filter path is always reached.
	cs := NewChunkStore(nil)
	results, err := cs.SearchByCosine(context.Background(), make([]float32, 384), 10, AccessFilter{
		CategoryIDs:      []string{},
		IncludeSensitive: false,
	})
	if err == nil {
		t.Fatal("expected error from nil pool; got none")
	}
	if results != nil {
		t.Fatal("expected nil results when pool is nil")
	}
}
