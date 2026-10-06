// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Bugs5382/go-log"

	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/index"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

type stubIndexer struct {
	indexed         []index.PolicyVersionContent
	removed         []string
	removedPolicies []string
	chunkCount      int       // ChunkCount returned from IndexVersion on success; 0 is a valid "no chunks" case
	centroid        []float32 // Centroid returned from IndexVersion; nil = "no chunks, no centroid"
	err             error
}

func (s *stubIndexer) IndexVersion(_ context.Context, pv index.PolicyVersionContent) (index.IndexResult, error) {
	if s.err != nil {
		return index.IndexResult{}, s.err
	}
	s.indexed = append(s.indexed, pv)
	return index.IndexResult{ChunkCount: s.chunkCount, Centroid: s.centroid}, nil
}

func (s *stubIndexer) RemoveVersion(_ context.Context, versionID string) error {
	if s.err != nil {
		return s.err
	}
	s.removed = append(s.removed, versionID)
	return nil
}

func (s *stubIndexer) RemovePolicy(_ context.Context, policyID string) error {
	if s.err != nil {
		return s.err
	}
	s.removedPolicies = append(s.removedPolicies, policyID)
	return nil
}

// moduleSwitch is a fake ModuleSwitch.
type moduleSwitch struct {
	on  bool
	err error
}

func (m moduleSwitch) Enabled(context.Context) (bool, error) { return m.on, m.err }

var moduleOn = moduleSwitch{on: true}

// stubCentroidStore is a fake CentroidStore for the publish-time centroid
// path.
type stubCentroidStore struct {
	upserted  []store.Centroid
	deleted   []string
	upsertErr error
	deleteErr error
}

func (s *stubCentroidStore) UpsertCentroid(_ context.Context, c store.Centroid) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.upserted = append(s.upserted, c)
	return nil
}

func (s *stubCentroidStore) DeleteByPolicyID(_ context.Context, policyID string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted = append(s.deleted, policyID)
	return nil
}

func TestHandlePublishEvent_indexesOnPublish(t *testing.T) {
	st := &stubIndexer{chunkCount: 7}
	c := NewPublishEventConsumer(st)

	pv := index.PolicyVersionContent{
		PolicyID:    "pol-1",
		VersionID:   "ver-2",
		VersionNo:   2,
		CategoryID:  "cat-hr",
		Sensitivity: "standard",
		PolicyTitle: "HR Policy",
		Sections: []index.SectionContent{
			{Key: "scope", Text: "All employees."},
		},
	}

	evt := PublishEvent{
		EventType: EventTypePublished,
		Version:   pv,
	}
	body, _ := json.Marshal(evt)

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.indexed) != 1 {
		t.Fatalf("expected 1 indexed version, got %d", len(st.indexed))
	}
	if st.indexed[0].PolicyID != "pol-1" {
		t.Fatalf("indexed PolicyID: got %q", st.indexed[0].PolicyID)
	}
}

// TestHandleProcedureEvent_indexesOnProcedurePublished confirms a
// procedure.published event takes the same index path as policy.published and
// the version's DocumentType reaches the indexer, so it lands on every chunk.
func TestHandleProcedureEvent_indexesOnProcedurePublished(t *testing.T) {
	st := &stubIndexer{chunkCount: 4}
	c := NewPublishEventConsumer(st)

	pv := index.PolicyVersionContent{
		PolicyID:     "prc-1",
		VersionID:    "ver-p2",
		VersionNo:    2,
		CategoryID:   "cat-hr",
		Sensitivity:  "standard",
		PolicyTitle:  "Onboarding Procedure",
		DocumentType: "PROCEDURE",
		Sections: []index.SectionContent{
			{Key: "steps", Text: "Do the onboarding steps."},
		},
	}
	body, _ := json.Marshal(PublishEvent{EventType: EventTypeProcedurePublished, Version: pv})

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.indexed) != 1 {
		t.Fatalf("expected 1 indexed version, got %d", len(st.indexed))
	}
	if st.indexed[0].PolicyID != "prc-1" {
		t.Fatalf("indexed PolicyID: got %q", st.indexed[0].PolicyID)
	}
	if st.indexed[0].DocumentType != "PROCEDURE" {
		t.Fatalf("expected DocumentType PROCEDURE threaded to the indexer, got %q", st.indexed[0].DocumentType)
	}
}

// TestHandleProcedureEvent_removesOnProcedureRetired confirms procedure.retired
// removes the procedure's chunks, mirroring policy.unpublished.
func TestHandleProcedureEvent_removesOnProcedureRetired(t *testing.T) {
	st := &stubIndexer{}
	c := NewPublishEventConsumer(st)

	body, _ := json.Marshal(PublishEvent{
		EventType: EventTypeProcedureRetired,
		Version:   index.PolicyVersionContent{VersionID: "ver-p-old"},
	})

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.removed) != 1 || st.removed[0] != "ver-p-old" {
		t.Fatalf("expected ver-p-old to be removed, got %v", st.removed)
	}
	if len(st.indexed) != 0 {
		t.Fatalf("expected no indexing on retire, got %d", len(st.indexed))
	}
}

// TestHandleProcedureEvent_wireEnvelopeUnmarshals confirms the wire envelope
// (event_type and version, with the "DocumentType" key) decodes into the
// consumer's types and drives the procedure index path.
func TestHandleProcedureEvent_wireEnvelopeUnmarshals(t *testing.T) {
	st := &stubIndexer{chunkCount: 1}
	c := NewPublishEventConsumer(st)

	raw := `{"event_type":"procedure.published","version":{"PolicyID":"prc-9","VersionID":"ver-9","VersionNo":1,"CategoryID":"cat-workplace","Sensitivity":"standard","PolicyTitle":"Proc","DocumentType":"PROCEDURE","Sections":[{"Key":"s","Text":"steps here"}]}}`

	if err := c.Handle(context.Background(), []byte(raw)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.indexed) != 1 {
		t.Fatalf("expected 1 indexed version, got %d", len(st.indexed))
	}
	got := st.indexed[0]
	if got.PolicyID != "prc-9" || got.DocumentType != "PROCEDURE" {
		t.Fatalf("unexpected decoded version: %+v", got)
	}
	if len(got.Sections) != 1 || got.Sections[0].Text != "steps here" {
		t.Fatalf("expected sections decoded, got %+v", got.Sections)
	}
}

func TestHandlePublishEvent_removesOnUnpublish(t *testing.T) {
	st := &stubIndexer{}
	c := NewPublishEventConsumer(st)

	evt := PublishEvent{
		EventType: EventTypeUnpublished,
		Version:   index.PolicyVersionContent{VersionID: "ver-old"},
	}
	body, _ := json.Marshal(evt)

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.removed) != 1 || st.removed[0] != "ver-old" {
		t.Fatalf("expected ver-old to be removed, got %v", st.removed)
	}
}

func TestHandlePublishEvent_unknownTypeIsNoOp(t *testing.T) {
	st := &stubIndexer{}
	c := NewPublishEventConsumer(st)
	evt := PublishEvent{EventType: "unrecognized"}
	body, _ := json.Marshal(evt)
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error for unknown event type: %v", err)
	}
}

// TestHandlePublishEvent_indexFailurePropagates confirms an IndexVersion
// error (such as the embed batch timing out on a large section) still reaches
// the caller, which applies DecideDelivery, even though Handle also logs it.
func TestHandlePublishEvent_indexFailurePropagates(t *testing.T) {
	wantErr := errors.New("embed: tei request: context deadline exceeded")
	st := &stubIndexer{err: wantErr}
	c := NewPublishEventConsumer(st)

	evt := PublishEvent{
		EventType: EventTypePublished,
		Version:   index.PolicyVersionContent{PolicyID: "pol-1", VersionID: "ver-2"},
	}
	body, _ := json.Marshal(evt)

	err := c.Handle(context.Background(), body)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped/underlying error %v, got %v", wantErr, err)
	}
}

// TestWithLogger_chains confirms WithLogger returns the same consumer, so it
// chains onto either constructor.
func TestWithLogger_chains(t *testing.T) {
	st := &stubIndexer{}
	c := NewPublishEventConsumer(st)
	got := c.WithLogger(log.Nop())
	if got != c {
		t.Fatal("expected WithLogger to return the same *PublishEventConsumer")
	}
}

// stubSummarizer is a fake Summarizer for the publish-time summary path.
type stubSummarizer struct {
	calls []generation.AssistRequest
	resp  generation.AssistResponse
	err   error
}

func (s *stubSummarizer) Generate(_ context.Context, req generation.AssistRequest) (generation.AssistResponse, error) {
	s.calls = append(s.calls, req)
	if s.err != nil {
		return generation.AssistResponse{}, s.err
	}
	return s.resp, nil
}

// stubSummaryStore is a fake SummaryStore for the publish-time summary path.
type stubSummaryStore struct {
	upserted []store.Summary
	err      error
}

func (s *stubSummaryStore) UpsertSummary(_ context.Context, sum store.Summary) error {
	if s.err != nil {
		return s.err
	}
	s.upserted = append(s.upserted, sum)
	return nil
}

func TestHandlePublishEvent_generatesAndStoresSummary(t *testing.T) {
	idx := &stubIndexer{chunkCount: 3}
	summarizer := &stubSummarizer{resp: generation.AssistResponse{
		Suggestion:  "- Point one.\n- Point two.",
		OperationID: "summarize",
	}}
	summaries := &stubSummaryStore{}
	c := NewPublishEventConsumer(idx).WithSummary(summarizer, summaries, moduleOn)

	pv := index.PolicyVersionContent{
		PolicyID:    "pol-1",
		VersionID:   "ver-2",
		VersionNo:   2,
		PolicyTitle: "HR Policy",
		Sections: []index.SectionContent{
			{Key: "scope", Text: "All employees."},
			{Key: "purpose", Text: "Explains onboarding."},
		},
	}
	evt := PublishEvent{EventType: EventTypePublished, Version: pv}
	body, _ := json.Marshal(evt)

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(idx.indexed) != 1 {
		t.Fatalf("expected the version to still be indexed, got %d", len(idx.indexed))
	}

	if len(summarizer.calls) != 1 {
		t.Fatalf("expected exactly 1 summarize call, got %d", len(summarizer.calls))
	}
	gotReq := summarizer.calls[0]
	if gotReq.Operation != generation.AssistOperationSummarize {
		t.Fatalf("expected AssistOperationSummarize, got %v", gotReq.Operation)
	}
	if gotReq.PolicyID != "pol-1" || gotReq.VersionID != "ver-2" {
		t.Fatalf("expected policy/version ids threaded through, got %+v", gotReq)
	}
	if gotReq.EditableContent == "" {
		t.Fatal("expected a non-empty document built from the version's sections")
	}

	if len(summaries.upserted) != 1 {
		t.Fatalf("expected exactly 1 stored summary, got %d", len(summaries.upserted))
	}
	got := summaries.upserted[0]
	if got.VersionID != "ver-2" || got.PolicyID != "pol-1" || got.PolicyTitle != "HR Policy" {
		t.Fatalf("unexpected stored summary metadata: %+v", got)
	}
	if got.SummaryText != "- Point one.\n- Point two." {
		t.Fatalf("expected the summarizer's suggestion to be stored verbatim, got %q", got.SummaryText)
	}
}

// TestHandlePublishEvent_summaryGenerationFailureDoesNotFailHandle is the
// best-effort guarantee: indexing has already succeeded when the summary
// runs, so a provider failure there must not fail, and so requeue, the whole
// message.
func TestHandlePublishEvent_summaryGenerationFailureDoesNotFailHandle(t *testing.T) {
	idx := &stubIndexer{chunkCount: 1}
	wantErr := errors.New("provider: rate limited")
	summarizer := &stubSummarizer{err: wantErr}
	summaries := &stubSummaryStore{}
	c := NewPublishEventConsumer(idx).WithSummary(summarizer, summaries, moduleOn)

	evt := PublishEvent{
		EventType: EventTypePublished,
		Version:   index.PolicyVersionContent{PolicyID: "pol-1", VersionID: "ver-1"},
	}
	body, _ := json.Marshal(evt)

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("expected Handle to succeed (best-effort summary) despite the summarizer error, got: %v", err)
	}
	if len(idx.indexed) != 1 {
		t.Fatal("expected indexing to have still succeeded")
	}
	if len(summaries.upserted) != 0 {
		t.Fatal("expected no summary to be stored when generation failed")
	}
}

// TestHandlePublishEvent_summaryStoreFailureDoesNotFailHandle mirrors the
// generation-failure case for the storage half of the best-effort path: a
// DB error persisting the summary must not fail the message either.
func TestHandlePublishEvent_summaryStoreFailureDoesNotFailHandle(t *testing.T) {
	idx := &stubIndexer{chunkCount: 1}
	summarizer := &stubSummarizer{resp: generation.AssistResponse{Suggestion: "- Bullet."}}
	summaries := &stubSummaryStore{err: errors.New("pg: connection reset")}
	c := NewPublishEventConsumer(idx).WithSummary(summarizer, summaries, moduleOn)

	evt := PublishEvent{
		EventType: EventTypePublished,
		Version:   index.PolicyVersionContent{PolicyID: "pol-1", VersionID: "ver-1"},
	}
	body, _ := json.Marshal(evt)

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("expected Handle to succeed (best-effort summary) despite the store error, got: %v", err)
	}
	if len(idx.indexed) != 1 {
		t.Fatal("expected indexing to have still succeeded")
	}
}

// TestHandlePublishEvent_nilSummarizerSkipsCleanly confirms a consumer built
// without WithSummary does no summary work and still succeeds.
func TestHandlePublishEvent_nilSummarizerSkipsCleanly(t *testing.T) {
	idx := &stubIndexer{chunkCount: 1}
	c := NewPublishEventConsumer(idx) // no WithSummary call

	evt := PublishEvent{
		EventType: EventTypePublished,
		Version:   index.PolicyVersionContent{PolicyID: "pol-1", VersionID: "ver-1"},
	}
	body, _ := json.Marshal(evt)

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx.indexed) != 1 {
		t.Fatal("expected indexing to have still succeeded")
	}
}

// TestBuildSummaryDocument_joinsTitleAndSections confirms the document
// passed to the summarizer concatenates the policy title and each section's
// text, skipping any section with empty/whitespace-only text.
func TestBuildSummaryDocument_joinsTitleAndSections(t *testing.T) {
	pv := index.PolicyVersionContent{
		PolicyTitle: "HR Policy",
		Sections: []index.SectionContent{
			{Key: "scope", Text: "All employees."},
			{Key: "empty", Text: "   "},
			{Key: "purpose", Text: "Explains onboarding."},
		},
	}
	got := buildSummaryDocument(pv)
	want := "HR Policy\n\nAll employees.\n\nExplains onboarding."
	if got != want {
		t.Fatalf("buildSummaryDocument mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestHandlePublishEvent_upsertsCentroidOnPublish confirms the centroid the
// indexer returned is upserted with the version's denormalized metadata.
func TestHandlePublishEvent_upsertsCentroidOnPublish(t *testing.T) {
	idx := &stubIndexer{chunkCount: 3, centroid: []float32{0.1, 0.2, 0.3}}
	centroids := &stubCentroidStore{}
	c := NewPublishEventConsumer(idx).WithCentroids(centroids)

	pv := index.PolicyVersionContent{
		PolicyID: "pol-1", VersionID: "ver-2", VersionNo: 2,
		CategoryID: "cat-hr", Sensitivity: "sensitive", PolicyTitle: "HR Policy",
		Sections: []index.SectionContent{{Key: "scope", Text: "All employees."}},
	}
	body, _ := json.Marshal(PublishEvent{EventType: EventTypePublished, Version: pv})

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(centroids.upserted) != 1 {
		t.Fatalf("expected 1 centroid upsert, got %d", len(centroids.upserted))
	}
	got := centroids.upserted[0]
	if got.PolicyID != "pol-1" || got.VersionID != "ver-2" || got.VersionNo != 2 {
		t.Fatalf("unexpected centroid identity: %+v", got)
	}
	if got.CategoryID != "cat-hr" || got.Sensitivity != "sensitive" || got.PolicyTitle != "HR Policy" {
		t.Fatalf("expected denormalized metadata threaded onto the centroid, got %+v", got)
	}
	if got.ChunkCount != 3 || len(got.Embedding) != 3 {
		t.Fatalf("expected chunk count 3 and the returned centroid vector, got count=%d dim=%d", got.ChunkCount, len(got.Embedding))
	}
}

// TestHandlePublishEvent_deletesCentroidOnUnpublish confirms the policy's
// centroid is removed on unpublish/archive so it stops being surfaced.
func TestHandlePublishEvent_deletesCentroidOnUnpublish(t *testing.T) {
	idx := &stubIndexer{}
	centroids := &stubCentroidStore{}
	c := NewPublishEventConsumer(idx).WithCentroids(centroids)

	pv := index.PolicyVersionContent{PolicyID: "pol-9", VersionID: "ver-old"}
	body, _ := json.Marshal(PublishEvent{EventType: EventTypeUnpublished, Version: pv})

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(centroids.deleted) != 1 || centroids.deleted[0] != "pol-9" {
		t.Fatalf("expected pol-9's centroid to be deleted, got %v", centroids.deleted)
	}
}

// TestHandlePublishEvent_noCentroidWhenNoChunks confirms an empty-content
// version (nil centroid from the indexer) upserts nothing — there is no
// centroid to store — while indexing itself still succeeds.
func TestHandlePublishEvent_noCentroidWhenNoChunks(t *testing.T) {
	idx := &stubIndexer{chunkCount: 0, centroid: nil}
	centroids := &stubCentroidStore{}
	c := NewPublishEventConsumer(idx).WithCentroids(centroids)

	pv := index.PolicyVersionContent{PolicyID: "pol-1", VersionID: "ver-1"}
	body, _ := json.Marshal(PublishEvent{EventType: EventTypePublished, Version: pv})

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(centroids.upserted) != 0 {
		t.Fatalf("expected no centroid upsert for zero-chunk version, got %d", len(centroids.upserted))
	}
}

// TestHandlePublishEvent_centroidFailureDoesNotFailHandle is the best-effort
// guarantee for the centroid path (matching the summary path): an upsert error
// must not fail the message, since indexing already succeeded.
func TestHandlePublishEvent_centroidFailureDoesNotFailHandle(t *testing.T) {
	idx := &stubIndexer{chunkCount: 2, centroid: []float32{1, 2}}
	centroids := &stubCentroidStore{upsertErr: errors.New("pg: connection reset")}
	c := NewPublishEventConsumer(idx).WithCentroids(centroids)

	pv := index.PolicyVersionContent{PolicyID: "pol-1", VersionID: "ver-1"}
	body, _ := json.Marshal(PublishEvent{EventType: EventTypePublished, Version: pv})

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("expected Handle to succeed (best-effort centroid) despite the upsert error, got: %v", err)
	}
	if len(idx.indexed) != 1 {
		t.Fatal("expected indexing to have still succeeded")
	}
}

// TestHandlePublishEvent_nilCentroidStoreSkipsCleanly confirms a consumer built
// without WithCentroids does no centroid work and still succeeds.
func TestHandlePublishEvent_nilCentroidStoreSkipsCleanly(t *testing.T) {
	idx := &stubIndexer{chunkCount: 1, centroid: []float32{1}}
	c := NewPublishEventConsumer(idx) // no WithCentroids call

	pv := index.PolicyVersionContent{PolicyID: "pol-1", VersionID: "ver-1"}
	body, _ := json.Marshal(PublishEvent{EventType: EventTypePublished, Version: pv})

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx.indexed) != 1 {
		t.Fatal("expected indexing to have still succeeded")
	}
}

// TestWithCentroids_chains confirms WithCentroids returns the same consumer
// (fluent), matching WithSummary/WithLogger.
func TestWithCentroids_chains(t *testing.T) {
	c := NewPublishEventConsumer(&stubIndexer{})
	if got := c.WithCentroids(&stubCentroidStore{}); got != c {
		t.Fatal("expected WithCentroids to return the same *PublishEventConsumer")
	}
}

// stubReevalTrigger records the policies enqueued for related-link re-eval.
type stubReevalTrigger struct {
	triggered []string
	err       error
}

func (s *stubReevalTrigger) Trigger(_ context.Context, policyID string) error {
	if s.err != nil {
		return s.err
	}
	s.triggered = append(s.triggered, policyID)
	return nil
}

// TestHandlePublishEvent_triggersReevalOnPublish confirms the changed policy is
// queued for related-policy re-evaluation on publish, after indexing.
func TestHandlePublishEvent_triggersReevalOnPublish(t *testing.T) {
	idx := &stubIndexer{chunkCount: 3, centroid: []float32{0.1}}
	trig := &stubReevalTrigger{}
	c := NewPublishEventConsumer(idx).WithCentroids(&stubCentroidStore{}).WithReeval(trig)

	pv := index.PolicyVersionContent{PolicyID: "pol-1", VersionID: "ver-2"}
	body, _ := json.Marshal(PublishEvent{EventType: EventTypePublished, Version: pv})

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(trig.triggered) != 1 || trig.triggered[0] != "pol-1" {
		t.Fatalf("expected pol-1 enqueued for re-eval, got %v", trig.triggered)
	}
}

// TestHandlePublishEvent_triggersReevalOnUnpublish confirms unpublish also
// enqueues the policy (its dropping out changes neighbours' suggestions too).
func TestHandlePublishEvent_triggersReevalOnUnpublish(t *testing.T) {
	trig := &stubReevalTrigger{}
	c := NewPublishEventConsumer(&stubIndexer{}).WithReeval(trig)

	pv := index.PolicyVersionContent{PolicyID: "pol-9", VersionID: "ver-old"}
	body, _ := json.Marshal(PublishEvent{EventType: EventTypeUnpublished, Version: pv})

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(trig.triggered) != 1 || trig.triggered[0] != "pol-9" {
		t.Fatalf("expected pol-9 enqueued for re-eval on unpublish, got %v", trig.triggered)
	}
}

// TestHandlePublishEvent_reevalFailureDoesNotFailHandle is the best-effort
// guarantee for the re-eval trigger: a trigger error (Valkey down) must not
// fail the message, since indexing already succeeded.
func TestHandlePublishEvent_reevalFailureDoesNotFailHandle(t *testing.T) {
	idx := &stubIndexer{chunkCount: 1, centroid: []float32{1}}
	trig := &stubReevalTrigger{err: errors.New("valkey down")}
	c := NewPublishEventConsumer(idx).WithReeval(trig)

	pv := index.PolicyVersionContent{PolicyID: "pol-1", VersionID: "ver-1"}
	body, _ := json.Marshal(PublishEvent{EventType: EventTypePublished, Version: pv})

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("expected Handle to succeed (best-effort re-eval) despite the trigger error, got: %v", err)
	}
	if len(idx.indexed) != 1 {
		t.Fatal("expected indexing to have still succeeded")
	}
}

// TestHandlePublishEvent_nilReevalTriggerSkipsCleanly confirms a consumer built
// without WithReeval does no re-eval work and still succeeds.
func TestHandlePublishEvent_nilReevalTriggerSkipsCleanly(t *testing.T) {
	idx := &stubIndexer{chunkCount: 1, centroid: []float32{1}}
	c := NewPublishEventConsumer(idx) // no WithReeval

	pv := index.PolicyVersionContent{PolicyID: "pol-1", VersionID: "ver-1"}
	body, _ := json.Marshal(PublishEvent{EventType: EventTypePublished, Version: pv})

	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestWithReeval_chains confirms WithReeval returns the same consumer (fluent).
func TestWithReeval_chains(t *testing.T) {
	c := NewPublishEventConsumer(&stubIndexer{})
	if got := c.WithReeval(&stubReevalTrigger{}); got != c {
		t.Fatal("expected WithReeval to return the same *PublishEventConsumer")
	}
}
