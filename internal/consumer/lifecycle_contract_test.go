// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"errors"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/generation"
)

// These bodies are byte-for-byte what core's lifecycle emitter writes.

func TestHandle_corePublishedBodyIndexes(t *testing.T) {
	idx := &stubIndexer{chunkCount: 2, centroid: []float32{0.5}}
	centroids := &stubCentroidStore{}
	c := NewPublishEventConsumer(idx).WithCentroids(centroids)

	raw := `{"event_type":"policy.published","published_at":"2026-10-05T14:00:00Z","version":{` +
		`"PolicyID":"pol-desk","VersionID":"ver-3","VersionNo":3,"CategoryID":"cat-facilities",` +
		`"Sensitivity":"standard","PolicyTitle":"Desk Booking Policy","EffectiveDate":"2026-11-01T00:00:00Z",` +
		`"DocumentType":"POLICY","Sections":[{"Key":"scope","Text":"Desks are booked a day ahead."}]}}`

	if err := c.Handle(context.Background(), []byte(raw)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(idx.indexed) != 1 {
		t.Fatalf("expected 1 indexed version, got %d", len(idx.indexed))
	}
	got := idx.indexed[0]
	if got.PolicyID != "pol-desk" || got.VersionID != "ver-3" || got.VersionNo != 3 ||
		got.CategoryID != "cat-facilities" || got.Sensitivity != "standard" ||
		got.PolicyTitle != "Desk Booking Policy" || got.DocumentType != "POLICY" {
		t.Fatalf("decoded version: %+v", got)
	}
	if len(got.Sections) != 1 || got.Sections[0].Key != "scope" {
		t.Fatalf("decoded sections: %+v", got.Sections)
	}
	if len(centroids.upserted) != 1 || centroids.upserted[0].CategoryID != "cat-facilities" {
		t.Fatalf("centroid: %+v", centroids.upserted)
	}
}

func TestHandle_coreReindexRemoveDropsTheVersion(t *testing.T) {
	for _, evtType := range []string{"policy.unpublished", "procedure.unpublished"} {
		t.Run(evtType, func(t *testing.T) {
			idx := &stubIndexer{}
			centroids := &stubCentroidStore{}
			trig := &stubReevalTrigger{}
			c := NewPublishEventConsumer(idx).WithCentroids(centroids).WithReeval(trig)

			raw := `{"event_type":"` + evtType + `","published_at":"0001-01-01T00:00:00Z","version":` +
				`{"PolicyID":"prc-1","VersionID":"ver-1","VersionNo":0,"CategoryID":"","Sensitivity":"",` +
				`"PolicyTitle":"","Sections":null}}`
			if err := c.Handle(context.Background(), []byte(raw)); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if len(idx.removed) != 1 || idx.removed[0] != "ver-1" {
				t.Fatalf("removed versions: %v", idx.removed)
			}
			if len(idx.removedPolicies) != 0 {
				t.Fatalf("a version removal must not drop the whole policy, got %v", idx.removedPolicies)
			}
			if len(centroids.deleted) != 1 || centroids.deleted[0] != "prc-1" {
				t.Fatalf("centroid delete: %v", centroids.deleted)
			}
			if len(trig.triggered) != 1 || trig.triggered[0] != "prc-1" {
				t.Fatalf("re-eval: %v", trig.triggered)
			}
		})
	}
}

func TestHandle_coreRetiredBodyDropsThePolicy(t *testing.T) {
	for _, evtType := range []string{"procedure.retired", "policy.retired"} {
		t.Run(evtType, func(t *testing.T) {
			idx := &stubIndexer{}
			centroids := &stubCentroidStore{}
			trig := &stubReevalTrigger{}
			bumper := &countingBumper{}
			c := NewPublishEventConsumerWithBumper(idx, bumper).WithCentroids(centroids).WithReeval(trig)

			raw := `{"event_type":"` + evtType + `","retired_at":"2026-10-05T15:00:00Z",` +
				`"policy_id":"prc-7","number":"PRC-FACILITIES-000007","title":"Desk Cleaning Procedure"}`
			if err := c.Handle(context.Background(), []byte(raw)); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if len(idx.removedPolicies) != 1 || idx.removedPolicies[0] != "prc-7" {
				t.Fatalf("removed policies: %v", idx.removedPolicies)
			}
			if len(idx.removed) != 0 {
				t.Fatalf("no version id was given, got version removals %v", idx.removed)
			}
			if len(centroids.deleted) != 1 || centroids.deleted[0] != "prc-7" {
				t.Fatalf("centroid delete: %v", centroids.deleted)
			}
			if len(trig.triggered) != 1 || trig.triggered[0] != "prc-7" {
				t.Fatalf("re-eval: %v", trig.triggered)
			}
			if bumper.n != 1 {
				t.Fatalf("corpus bumps: got %d want 1", bumper.n)
			}
		})
	}
}

func TestHandle_retireRemovalFailurePropagates(t *testing.T) {
	wantErr := errors.New("pg: connection reset")
	c := NewPublishEventConsumer(&stubIndexer{err: wantErr})
	err := c.Handle(context.Background(), []byte(`{"event_type":"procedure.retired","policy_id":"prc-7"}`))
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestHandle_removalNamingNothingIsANoOp(t *testing.T) {
	idx := &stubIndexer{}
	centroids := &stubCentroidStore{}
	c := NewPublishEventConsumer(idx).WithCentroids(centroids)
	if err := c.Handle(context.Background(), []byte(`{"event_type":"procedure.retired"}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(idx.removed)+len(idx.removedPolicies)+len(centroids.deleted) != 0 {
		t.Fatalf("nothing should be removed: %+v %+v", idx, centroids)
	}
}

func TestHandle_malformedBodyFails(t *testing.T) {
	c := NewPublishEventConsumer(&stubIndexer{})
	if err := c.Handle(context.Background(), []byte(`{not json`)); err == nil {
		t.Fatal("expected an unmarshal error")
	}
}

// TestHandle_summarySkippedUnlessModuleOn checks no summary (no provider call)
// is made while the module is off, when its switch can't be read, or when no
// switch is wired, and that indexing goes ahead in every case.
func TestHandle_summarySkippedUnlessModuleOn(t *testing.T) {
	cases := map[string]ModuleSwitch{
		"off":       moduleSwitch{on: false},
		"read fail": moduleSwitch{on: true, err: errors.New("valkey down")},
		"no switch": nil,
	}
	for name, sw := range cases {
		t.Run(name, func(t *testing.T) {
			idx := &stubIndexer{chunkCount: 1}
			summarizer := &stubSummarizer{resp: generation.AssistResponse{Suggestion: "- Bullet."}}
			summaries := &stubSummaryStore{}
			c := NewPublishEventConsumer(idx).WithSummary(summarizer, summaries, sw)

			raw := `{"event_type":"policy.published","version":{"PolicyID":"pol-1","VersionID":"ver-1",` +
				`"Sections":[{"Key":"s","Text":"text"}]}}`
			if err := c.Handle(context.Background(), []byte(raw)); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if len(idx.indexed) != 1 {
				t.Fatal("indexing must go ahead with the module off")
			}
			if len(summarizer.calls) != 0 || len(summaries.upserted) != 0 {
				t.Fatalf("no summary may be written: calls=%d stored=%d", len(summarizer.calls), len(summaries.upserted))
			}
		})
	}
}

type countingBumper struct{ n int }

func (b *countingBumper) BumpCorpusVersion(context.Context) error {
	b.n++
	return nil
}
