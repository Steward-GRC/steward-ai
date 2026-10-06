// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"

	otel "github.com/Bugs5382/go-otel"
)

const outcomeSuccess = "success"

// Every provider call, from the server's sync operations and the operator's
// jobs alike, passes through Client, so these two cover both binaries; the
// resource's service name tells them apart. They are created on the global
// meter, which forwards to the provider go-otel installs at startup.
var (
	generationCalls = otel.NewCounter("ai_generation_calls_total",
		"Generation calls, by operation and outcome")
	generationLatency = otel.NewHistogram("ai_generation_latency_seconds",
		"Generation call latency, by operation and outcome", "s")
)

// operation and outcome are low-cardinality labels: a handful of operation
// names, and "success" or a provider Reason. Never an id.
func recordGenerationCall(ctx context.Context, operation, outcome string) {
	generationCalls.Add(ctx, 1, otel.KV("operation", operation), otel.KV("outcome", outcome))
}

func recordGenerationLatency(ctx context.Context, operation, outcome string, seconds float64) {
	generationLatency.Record(ctx, seconds, otel.KV("operation", operation), otel.KV("outcome", outcome))
}
