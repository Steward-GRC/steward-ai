// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"context"

	otel "github.com/Bugs5382/go-otel"
)

// qaCacheLookups counts Lookup calls by result: hot_hit, durable_hit or
// miss. It is created on the global meter, which forwards to the provider
// go-otel installs at startup.
var qaCacheLookups = otel.NewCounter("ai_qa_cache_lookups_total", "Q&A cache lookups, by result")

// recordCacheLookup takes a fixed result label, never a question or an id.
func recordCacheLookup(ctx context.Context, result string) {
	qaCacheLookups.Add(ctx, 1, otel.KV("result", result))
}
