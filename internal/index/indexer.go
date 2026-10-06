// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package index chunks the text of a published version, embeds the chunks and
// upserts them into the chunk store so retrieval can answer questions over
// them. Every chunk carries the version's category and sensitivity, so the
// read filter needs no join back to the source.
package index

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// SectionContent is one section's plain text.
type SectionContent struct {
	Key  string
	Text string
}

// PolicyVersionContent is a published version to index, with the access
// metadata every chunk carries.
type PolicyVersionContent struct {
	PolicyID    string
	VersionID   string
	VersionNo   int
	CategoryID  string
	Sensitivity string // airules.SensitivityStandard or SensitivitySensitive
	PolicyTitle string
	// DocumentType is airules.DocumentTypePolicy or DocumentTypeProcedure;
	// empty reads as a policy.
	DocumentType string
	Sections     []SectionContent
}

// ChunkStore is the part of *store.ChunkStore the Indexer uses.
type ChunkStore interface {
	UpsertChunk(ctx context.Context, c store.Chunk) error
	DeleteByVersionID(ctx context.Context, versionID string) error
	DeleteByPolicyID(ctx context.Context, policyID string) error
}

// Indexer chunks, embeds and stores version text.
type Indexer struct {
	embedder     provider.Embedder
	store        ChunkStore
	chunkSize    int // approximate words per chunk
	chunkOverlap int // approximate word overlap between chunks
}

// New returns an Indexer. A non-positive chunkSize uses
// airules.ChunkSizeDefault and a negative chunkOverlap uses
// airules.ChunkOverlapDefault.
func New(embedder provider.Embedder, store ChunkStore, chunkSize, chunkOverlap int) *Indexer {
	if chunkSize <= 0 {
		chunkSize = airules.ChunkSizeDefault
	}
	if chunkOverlap < 0 {
		chunkOverlap = airules.ChunkOverlapDefault
	}
	return &Indexer{embedder: embedder, store: store, chunkSize: chunkSize, chunkOverlap: chunkOverlap}
}

// IndexResult is what IndexVersion produced: the chunk count and the
// version's centroid, the mean of every chunk embedding. The centroid comes
// from the embeddings already in hand, so the store is never re-read for it.
type IndexResult struct {
	ChunkCount int
	// Centroid is nil when the version produced no chunks.
	Centroid []float32
}

// IndexVersion chunks and embeds every section and upserts the chunks. It is
// idempotent: the upsert is keyed on version, section and chunk index.
func (idx *Indexer) IndexVersion(ctx context.Context, pv PolicyVersionContent) (IndexResult, error) {
	total := 0
	var sum []float32
	for _, sec := range pv.Sections {
		chunks := chunkText(sec.Text, idx.chunkSize, idx.chunkOverlap)
		if len(chunks) == 0 {
			continue
		}
		embeddings, err := idx.embedder.Embed(ctx, chunks)
		if err != nil {
			return IndexResult{ChunkCount: total}, err
		}
		if len(embeddings) != len(chunks) {
			return IndexResult{ChunkCount: total}, fmt.Errorf("index: embedder returned %d vectors for %d chunks", len(embeddings), len(chunks))
		}
		for i, text := range chunks {
			c := store.Chunk{
				PolicyID:     pv.PolicyID,
				VersionID:    pv.VersionID,
				VersionNo:    pv.VersionNo,
				SectionKey:   sec.Key,
				ChunkIndex:   i,
				ContentText:  text,
				Embedding:    embeddings[i],
				CategoryID:   pv.CategoryID,
				Sensitivity:  pv.Sensitivity,
				PolicyTitle:  pv.PolicyTitle,
				DocumentType: airules.NormalizeDocumentType(pv.DocumentType),
			}
			if err := idx.store.UpsertChunk(ctx, c); err != nil {
				return IndexResult{ChunkCount: total}, err
			}
			sum = accumulate(sum, embeddings[i])
			total++
		}
	}
	return IndexResult{ChunkCount: total, Centroid: meanOf(sum, total)}, nil
}

// accumulate adds vec into sum, allocating sum on first use. A vector of a
// different width only adds the overlap, so a stray one can't panic.
func accumulate(sum, vec []float32) []float32 {
	if len(sum) == 0 {
		sum = make([]float32, len(vec))
	}
	n := min(len(vec), len(sum))
	for i := range n {
		sum[i] += vec[i]
	}
	return sum
}

// meanOf divides sum by count, or returns nil when there were no chunks.
func meanOf(sum []float32, count int) []float32 {
	if count == 0 || len(sum) == 0 {
		return nil
	}
	mean := make([]float32, len(sum))
	inv := float32(1) / float32(count)
	for i, v := range sum {
		mean[i] = v * inv
	}
	return mean
}

// RemoveVersion deletes one version's chunks, on unpublish.
func (idx *Indexer) RemoveVersion(ctx context.Context, versionID string) error {
	return idx.store.DeleteByVersionID(ctx, versionID)
}

// RemovePolicy deletes every chunk of a policy, on retire, when only the
// policy id is known.
func (idx *Indexer) RemovePolicy(ctx context.Context, policyID string) error {
	return idx.store.DeleteByPolicyID(ctx, policyID)
}

// chunkText splits text into overlapping chunks of about size words.
func chunkText(text string, size, overlap int) []string {
	words := strings.FieldsFunc(text, unicode.IsSpace)
	if len(words) == 0 {
		return nil
	}
	var chunks []string
	step := size - overlap
	if step <= 0 {
		step = 1
	}
	for start := 0; start < len(words); start += step {
		end := min(start+size, len(words))
		chunks = append(chunks, strings.Join(words[start:end], " "))
		if end == len(words) {
			break
		}
	}
	return chunks
}
