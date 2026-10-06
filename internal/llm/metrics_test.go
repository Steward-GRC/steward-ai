// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Before a real meter provider is installed the instruments are no-ops and
// must not panic.
func TestRecordGenerationCall_noopMeterDoesNotPanic(t *testing.T) {
	recordGenerationCall(context.Background(), "draft", "success")
	recordGenerationLatency(context.Background(), "draft", "success", 0.42)
}

// The instruments, created at package load on the global meter, forward to
// the provider installed later.
func TestRecordGenerationCall_recordsOperationAndOutcome(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	recordGenerationCall(context.Background(), "qa", "success")
	recordGenerationCall(context.Background(), "qa", "auth_failed")
	recordGenerationLatency(context.Background(), "qa", "success", 1.5)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	foundCallsMetric := false
	foundLatencyMetric := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "ai_generation_calls_total":
				foundCallsMetric = true
				sum, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("ai_generation_calls_total: unexpected data type %T", m.Data)
				}
				if len(sum.DataPoints) != 2 {
					t.Fatalf("expected 2 data points (success + auth_failed), got %d", len(sum.DataPoints))
				}
				for _, dp := range sum.DataPoints {
					op, ok := dp.Attributes.Value("operation")
					if !ok || op.AsString() != "qa" {
						t.Fatalf("expected operation=qa attribute, got %+v", dp.Attributes)
					}
					if dp.Value != 1 {
						t.Fatalf("expected count 1 per outcome, got %d", dp.Value)
					}
				}
			case "ai_generation_latency_seconds":
				foundLatencyMetric = true
				hist, ok := m.Data.(metricdata.Histogram[float64])
				if !ok {
					t.Fatalf("ai_generation_latency_seconds: unexpected data type %T", m.Data)
				}
				if len(hist.DataPoints) != 1 || hist.DataPoints[0].Count != 1 {
					t.Fatalf("expected 1 histogram data point with count 1, got %+v", hist.DataPoints)
				}
				outcome, ok := hist.DataPoints[0].Attributes.Value("outcome")
				if !ok || outcome.AsString() != "success" {
					t.Fatalf("expected outcome=success attribute, got %+v", hist.DataPoints[0].Attributes)
				}
				op, ok := hist.DataPoints[0].Attributes.Value("operation")
				if !ok || op.AsString() != "qa" {
					t.Fatalf("expected operation=qa attribute, got %+v", hist.DataPoints[0].Attributes)
				}
			}
		}
	}
	if !foundCallsMetric {
		t.Fatal("ai_generation_calls_total not found in collected metrics")
	}
	if !foundLatencyMetric {
		t.Fatal("ai_generation_latency_seconds not found in collected metrics")
	}
}
