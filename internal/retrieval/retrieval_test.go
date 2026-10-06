// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package retrieval

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

type stubEmbedder struct{}

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, 384)
	}
	return out, nil
}

var _ provider.Embedder = &stubEmbedder{}

type stubSearcher struct {
	results []store.SearchResult

	// gotFilter and gotText record what Retrieval passed. The read rule
	// itself runs in SQL and is covered by the store's integration tests.
	gotFilter store.AccessFilter
	gotText   string
	gotTopK   int
}

func (s *stubSearcher) SearchHybrid(_ context.Context, _ []float32, queryText string, topK int, filter store.AccessFilter) ([]store.SearchResult, error) {
	s.gotFilter = filter
	s.gotText = queryText
	s.gotTopK = topK
	return s.results, nil
}

func TestRetrieve_returnsFilteredResults(t *testing.T) {
	// The stub stands in for the store after its in-query filter; Retrieve
	// must pass the results through untouched.
	searcher := &stubSearcher{
		results: []store.SearchResult{
			{Chunk: store.Chunk{PolicyID: "p1", CategoryID: "hr", Sensitivity: "standard"}, Distance: 0.1},
			{Chunk: store.Chunk{PolicyID: "p2", CategoryID: "hr", Sensitivity: "sensitive"}, Distance: 0.2},
		},
	}
	r := New(&stubEmbedder{}, searcher, 5)

	req := Request{
		Question:         "What is the leave policy?",
		CategoryIDs:      []string{"hr"},
		IncludeSensitive: true,
		TopK:             5,
	}
	results, err := r.Retrieve(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

// TestRetrieve_emptyScopeStillQueries checks Retrieve adds no gate of
// its own for an empty category list: it always asks the store, whose SQL
// decides what an empty scope reads.
func TestRetrieve_emptyScopeStillQueries(t *testing.T) {
	searcher := &stubSearcher{results: []store.SearchResult{
		{Chunk: store.Chunk{PolicyID: "p1", Sensitivity: "standard"}, Distance: 0.1},
	}}
	r := New(&stubEmbedder{}, searcher, 5)
	req := Request{
		Question:         "anything",
		CategoryIDs:      []string{}, // an empty scope still asks the store
		IncludeSensitive: false,
		TopK:             5,
	}
	results, err := r.Retrieve(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected the store's results to pass through unfiltered by Retrieval, got %d", len(results))
	}
	if len(searcher.gotFilter.CategoryIDs) != 0 {
		t.Fatalf("expected empty CategoryIDs threaded through, got %v", searcher.gotFilter.CategoryIDs)
	}
	if searcher.gotFilter.AllCategories {
		t.Fatal("expected AllCategories=false threaded through for a scoped request")
	}
}

// TestRetrieveWithEmbedding_threadsAllCategories checks AllCategories reaches the
// store's AccessFilter unchanged; the SQL then reads every category.
func TestRetrieveWithEmbedding_threadsAllCategories(t *testing.T) {
	searcher := &stubSearcher{}
	r := New(&stubEmbedder{}, searcher, 5)

	if _, err := r.RetrieveWithEmbedding(context.Background(), make([]float32, 384), Request{
		Question:         "anything",
		CategoryIDs:      nil,
		IncludeSensitive: false,
		AllCategories:    true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !searcher.gotFilter.AllCategories {
		t.Fatal("expected AllCategories=true threaded through to the store's AccessFilter")
	}
}

// TestRetrieveWithEmbedding_scopedUnchanged checks a scoped request
// passes its categories and sensitivity flag through unmodified.
func TestRetrieveWithEmbedding_scopedUnchanged(t *testing.T) {
	searcher := &stubSearcher{}
	r := New(&stubEmbedder{}, searcher, 5)

	if _, err := r.RetrieveWithEmbedding(context.Background(), make([]float32, 384), Request{
		Question:         "anything",
		CategoryIDs:      []string{"hr", "fin"},
		IncludeSensitive: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if searcher.gotFilter.AllCategories {
		t.Fatal("expected AllCategories=false when the request didn't set it")
	}
	if !searcher.gotFilter.IncludeSensitive {
		t.Fatal("expected IncludeSensitive threaded through unchanged")
	}
	if len(searcher.gotFilter.CategoryIDs) != 2 {
		t.Fatalf("expected CategoryIDs threaded through unchanged, got %v", searcher.gotFilter.CategoryIDs)
	}
}

// TestRetrieveWithEmbedding_passesQuestionAndDefaultTopK checks the question
// text reaches the keyword half of the hybrid search and a zero TopK uses the
// default.
func TestRetrieveWithEmbedding_passesQuestionAndDefaultTopK(t *testing.T) {
	searcher := &stubSearcher{}
	r := New(&stubEmbedder{}, searcher, 7)

	if _, err := r.RetrieveWithEmbedding(context.Background(), make([]float32, 384), Request{
		Question: "desk booking rules",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if searcher.gotText != "desk booking rules" {
		t.Fatalf("query text: got %q", searcher.gotText)
	}
	if searcher.gotTopK != 7 {
		t.Fatalf("topK: got %d want 7", searcher.gotTopK)
	}
}
