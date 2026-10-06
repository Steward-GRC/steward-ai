// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"time"

	otel "github.com/Bugs5382/go-otel"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The model-call metrics live in internal/llm, which every call passes
// through. These cover the job lifecycle. All labels are low-cardinality:
// operation, phase and GC action, never an id.
var (
	jobsTotal = otel.NewCounter("ai_jobs_total",
		"PolicyAIJob reconciliations reaching a terminal phase, by operation and phase")
	jobAttemptsTotal = otel.NewCounter("ai_job_attempts_total",
		"PolicyAIJob dispatch attempts, by operation")
	jobDuration = otel.NewHistogram("ai_job_duration_seconds",
		"PolicyAIJob wall-clock duration from first reconcile to terminal phase, by operation", "s")
	jobGCTotal = otel.NewCounter("ai_job_gc_total",
		"PolicyAIJob GC decisions on terminal jobs, by operation, phase and action (deleted, retained, deferred)")
)

func recordJobAttempt(ctx context.Context, operation string) {
	jobAttemptsTotal.Add(ctx, 1, otel.KV("operation", operation))
}

// recordJobTerminal counts a job reaching phase ("succeeded" or "failed") and
// records its duration when it started.
func recordJobTerminal(ctx context.Context, operation, phase string, startedAt *metav1.Time, finishedAt time.Time) {
	jobsTotal.Add(ctx, 1, otel.KV("operation", operation), otel.KV("phase", phase))
	if startedAt != nil {
		jobDuration.Record(ctx, finishedAt.Sub(startedAt.Time).Seconds(), otel.KV("operation", operation))
	}
}

func recordJobGC(ctx context.Context, operation, phase, action string) {
	jobGCTotal.Add(ctx, 1, otel.KV("operation", operation), otel.KV("phase", phase), otel.KV("action", action))
}
