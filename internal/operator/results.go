// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	redis "github.com/Bugs5382/go-redis"
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
