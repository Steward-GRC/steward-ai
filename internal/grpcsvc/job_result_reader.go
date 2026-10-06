// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"errors"
	"fmt"

	redis "github.com/Bugs5382/go-redis"
)

// RedisJobResultReader reads the result envelope the operator writes to
// Valkey under PolicyAIJob.status.resultRef.
type RedisJobResultReader struct{ rdb redis.UniversalClient }

// NewRedisJobResultReader wraps a connected client.
func NewRedisJobResultReader(rdb redis.UniversalClient) *RedisJobResultReader {
	return &RedisJobResultReader{rdb: rdb}
}

// ReadResult returns the raw JSON under key; found is false with a nil error
// when the key is absent, for example evicted past the result TTL.
func (r *RedisJobResultReader) ReadResult(ctx context.Context, key string) ([]byte, bool, error) {
	raw, err := r.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("grpcsvc: read job result %q: %w", key, err)
	}
	return raw, true, nil
}
