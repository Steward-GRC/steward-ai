// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestRecordJobMetrics_doesNotPanic: recording never panics, whatever meter
// provider is registered, including startedAt=nil (a job that went terminal
// before markRunning).
func TestRecordJobMetrics_doesNotPanic(t *testing.T) {
	recordJobAttempt(context.Background(), "DRAFT")
	recordJobTerminal(context.Background(), "DRAFT", "succeeded", nil, time.Now())
}

// TestRecordJobMetrics_recordsOperationPhaseAndDuration: the lifecycle
// instruments carry the operation and phase labels and the duration, read
// through a manual reader installed as the global provider (the instruments
// created at package load forward to it).
func TestRecordJobMetrics_recordsOperationPhaseAndDuration(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	started := metav1.NewTime(time.Now().Add(-5 * time.Second))
	finished := started.Add(5 * time.Second)

	recordJobAttempt(context.Background(), "REVIEW")
	recordJobAttempt(context.Background(), "REVIEW")
	recordJobTerminal(context.Background(), "REVIEW", "succeeded", &started, finished)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	var sawAttempts, sawJobs, sawDuration bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "ai_job_attempts_total":
				sawAttempts = true
				sum := m.Data.(metricdata.Sum[int64])
				if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 2 {
					t.Fatalf("ai_job_attempts_total: unexpected data points %+v", sum.DataPoints)
				}
				op, _ := sum.DataPoints[0].Attributes.Value("operation")
				if op.AsString() != "REVIEW" {
					t.Fatalf("expected operation=REVIEW, got %+v", sum.DataPoints[0].Attributes)
				}
			case "ai_jobs_total":
				sawJobs = true
				sum := m.Data.(metricdata.Sum[int64])
				if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
					t.Fatalf("ai_jobs_total: unexpected data points %+v", sum.DataPoints)
				}
				phase, _ := sum.DataPoints[0].Attributes.Value("phase")
				if phase.AsString() != "succeeded" {
					t.Fatalf("expected phase=succeeded, got %+v", sum.DataPoints[0].Attributes)
				}
			case "ai_job_duration_seconds":
				sawDuration = true
				hist := m.Data.(metricdata.Histogram[float64])
				if len(hist.DataPoints) != 1 {
					t.Fatalf("ai_job_duration_seconds: expected 1 data point, got %+v", hist.DataPoints)
				}
				if got := hist.DataPoints[0].Sum; got < 4.9 || got > 5.1 {
					t.Fatalf("expected ~5s duration, got %v", got)
				}
			}
		}
	}
	if !sawAttempts || !sawJobs || !sawDuration {
		t.Fatalf("missing metrics: attempts=%v jobs=%v duration=%v", sawAttempts, sawJobs, sawDuration)
	}
}
