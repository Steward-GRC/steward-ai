// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	redis "github.com/Bugs5382/go-redis"

	"github.com/Steward-GRC/steward-ai/internal/generation"
)

// ValkeyResultWriter is the production ResultWriter.
type ValkeyResultWriter struct {
	rdb redis.UniversalClient
}

// NewValkeyResultWriter writes through rdb.
func NewValkeyResultWriter(rdb redis.UniversalClient) *ValkeyResultWriter {
	return &ValkeyResultWriter{rdb: rdb}
}

// WriteResult stores result as JSON under key for ttl.
func (w *ValkeyResultWriter) WriteResult(ctx context.Context, key string, result any, ttl time.Duration) error {
	b, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("operator: marshal result: %w", err)
	}
	if err := w.rdb.Set(ctx, key, b, ttl).Err(); err != nil {
		return fmt.Errorf("operator: write result: %w", err)
	}
	return nil
}

// summaryTTL is how long a stored change summary is kept; versions never
// change, so this only bounds the cache.
const summaryTTL = 30 * 24 * time.Hour

// ValkeySummaryCache stores change summaries in Valkey.
type ValkeySummaryCache struct {
	rdb redis.UniversalClient
}

// NewValkeySummaryCache stores through rdb.
func NewValkeySummaryCache(rdb redis.UniversalClient) *ValkeySummaryCache {
	return &ValkeySummaryCache{rdb: rdb}
}

// Get reads the summary under key; found is false for a miss.
func (c *ValkeySummaryCache) Get(ctx context.Context, key string) (generation.SummaryJobResult, bool, error) {
	b, err := c.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return generation.SummaryJobResult{}, false, nil
	}
	if err != nil {
		return generation.SummaryJobResult{}, false, fmt.Errorf("operator: read summary: %w", err)
	}
	var v generation.SummaryJobResult
	if err := json.Unmarshal(b, &v); err != nil {
		return generation.SummaryJobResult{}, false, fmt.Errorf("operator: decode summary: %w", err)
	}
	return v, true, nil
}

// Put stores v under key.
func (c *ValkeySummaryCache) Put(ctx context.Context, key string, v generation.SummaryJobResult) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("operator: marshal summary: %w", err)
	}
	if err := c.rdb.Set(ctx, key, b, summaryTTL).Err(); err != nil {
		return fmt.Errorf("operator: write summary: %w", err)
	}
	return nil
}
