// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package retrieval embeds a question and returns the chunks the caller's read
// scope may see, nearest first. The scope is applied inside the SQL, so an
// unreadable chunk never reaches Go or the model.
package retrieval

import (
	"context"
	"fmt"

	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// Request is one retrieval. The scope fields map onto store.AccessFilter: a
// chunk is readable when AllCategories is set, or its category is in
// CategoryIDs and it is standard or IncludeSensitive is set.
type Request struct {
	Question         string
	CategoryIDs      []string
	IncludeSensitive bool
	AllCategories    bool
	// TopK is the most chunks to return; zero uses the default.
	TopK int
}

// Filter is the request's read scope.
func (r Request) Filter() store.AccessFilter {
	return store.AccessFilter{
		CategoryIDs:      r.CategoryIDs,
		IncludeSensitive: r.IncludeSensitive,
		AllCategories:    r.AllCategories,
	}
}

// Searcher is the search Retrieval needs. *store.ChunkStore satisfies it.
type Searcher interface {
	SearchHybrid(ctx context.Context, queryEmbedding []float32, queryText string, topK int, filter store.AccessFilter) ([]store.SearchResult, error)
}

// Retrieval embeds questions and runs the hybrid search.
type Retrieval struct {
	embedder provider.Embedder
	store    Searcher
	topK     int
}

// New returns a Retrieval. A non-positive defaultTopK uses
// airules.RetrievalTopKDefault.
func New(embedder provider.Embedder, store Searcher, defaultTopK int) *Retrieval {
	if defaultTopK <= 0 {
		defaultTopK = airules.RetrievalTopKDefault
	}
	return &Retrieval{embedder: embedder, store: store, topK: defaultTopK}
}

// Retrieve embeds the question and returns the readable chunks for it.
func (r *Retrieval) Retrieve(ctx context.Context, req Request) ([]store.SearchResult, error) {
	queryVec, err := r.EmbedQuestion(ctx, req.Question)
	if err != nil {
		return nil, err
	}
	return r.RetrieveWithEmbedding(ctx, queryVec, req)
}

// EmbedQuestion embeds one question, for a caller that also needs the vector
// (the answer cache's semantic lookup) and passes it to RetrieveWithEmbedding.
func (r *Retrieval) EmbedQuestion(ctx context.Context, question string) ([]float32, error) {
	vecs, err := r.embedder.Embed(ctx, []string{question})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("retrieval: embedder returned %d vectors for one question", len(vecs))
	}
	return vecs[0], nil
}

// RetrieveWithEmbedding is Retrieve with the question already embedded. The
// question text still drives the keyword half of the hybrid search.
func (r *Retrieval) RetrieveWithEmbedding(ctx context.Context, queryVec []float32, req Request) ([]store.SearchResult, error) {
	topK := req.TopK
	if topK <= 0 {
		topK = r.topK
	}
	return r.store.SearchHybrid(ctx, queryVec, req.Question, topK, req.Filter())
}
