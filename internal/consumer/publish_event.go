// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package consumer handles core's lifecycle events for the AI index: a
// published version is chunked, embedded and indexed, with its centroid and,
// while the module is on, its summary; an unpublished or retired document
// leaves the index. Only published content is ever indexed, and only one
// version of a document at a time (airules.GovernancePublishedOnlyIndexing,
// airules.GovernanceLatestVersionOnly).
package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Bugs5382/go-log"

	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/index"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// EventType is core's lifecycle event_type.
type EventType string

// The lifecycle events the indexer acts on, as core publishes them.
const (
	// EventTypePublished carries a published policy version.
	EventTypePublished EventType = "policy.published"
	// EventTypeUnpublished names a policy version to drop. Core's reindex
	// sends it, straight to the AI queue, for each superseded version.
	EventTypeUnpublished EventType = "policy.unpublished"
	// EventTypeRetired names a retired policy by id only.
	EventTypeRetired EventType = "policy.retired"

	// EventTypeProcedurePublished carries a published procedure version.
	EventTypeProcedurePublished EventType = "procedure.published"
	// EventTypeProcedureUnpublished is EventTypeUnpublished for a procedure.
	EventTypeProcedureUnpublished EventType = "procedure.unpublished"
	// EventTypeProcedureRetired names a retired procedure by id only.
	EventTypeProcedureRetired EventType = "procedure.retired"
)

// PublishEvent is the body of every lifecycle event the indexer reads. Core
// sends a version for publish and unpublish, and only policy_id for a retire.
// The version's JSON keys are index.PolicyVersionContent's field names, which
// are core's keys; keys the indexer doesn't use (published_at, EffectiveDate)
// are ignored.
type PublishEvent struct {
	EventType EventType                  `json:"event_type"`
	Version   index.PolicyVersionContent `json:"version"`
	// PolicyID is set by retire events, which carry no version.
	PolicyID string `json:"policy_id,omitempty"`
}

// policyID is the document the event is about, from either shape.
func (e PublishEvent) policyID() string {
	if e.Version.PolicyID != "" {
		return e.Version.PolicyID
	}
	return e.PolicyID
}

// Indexer is the part of *index.Indexer the consumer uses.
type Indexer interface {
	IndexVersion(ctx context.Context, pv index.PolicyVersionContent) (index.IndexResult, error)
	RemoveVersion(ctx context.Context, versionID string) error
	RemovePolicy(ctx context.Context, policyID string) error
}

// CentroidStore is the part of *store.CentroidStore the consumer uses.
type CentroidStore interface {
	UpsertCentroid(ctx context.Context, c store.Centroid) error
	DeleteByPolicyID(ctx context.Context, policyID string) error
}

// CorpusBumper advances the answer cache's corpus version so a reindex
// invalidates cached answers. The cache satisfies it.
type CorpusBumper interface {
	BumpCorpusVersion(ctx context.Context) error
}

// Summarizer writes a policy summary. *generation.Assist satisfies it.
type Summarizer interface {
	Generate(ctx context.Context, req generation.AssistRequest) (generation.AssistResponse, error)
}

// SummaryStore stores a summary. *store.SummaryStore satisfies it.
type SummaryStore interface {
	UpsertSummary(ctx context.Context, s store.Summary) error
}

// ModuleSwitch reports whether the AI module is on. Nothing that calls a
// generative provider runs unless it says yes.
type ModuleSwitch interface {
	Enabled(ctx context.Context) (bool, error)
}

// ReevalTrigger queues a policy for the debounced related-policy
// re-evaluation. *reeval.Debouncer satisfies it.
type ReevalTrigger interface {
	Trigger(ctx context.Context, policyID string) error
}

// PublishEventConsumer handles messages from the indexer's queues. Everything
// after indexing (summary, centroid, re-evaluation) is best effort: a failure
// there is logged and never fails the message
// (airules.GovernanceBestEffortDegradation).
type PublishEventConsumer struct {
	indexer    Indexer
	bumper     CorpusBumper
	summarizer Summarizer
	summaries  SummaryStore
	module     ModuleSwitch
	centroids  CentroidStore
	reeval     ReevalTrigger
	logger     log.Logger
}

// NewPublishEventConsumer returns a consumer over idx with nothing else
// wired.
func NewPublishEventConsumer(idx Indexer) *PublishEventConsumer {
	return &PublishEventConsumer{indexer: idx, logger: log.Nop()}
}

// NewPublishEventConsumerWithBumper also advances the corpus version after
// every index change, so cached answers re-ground on the new content.
func NewPublishEventConsumerWithBumper(idx Indexer, bumper CorpusBumper) *PublishEventConsumer {
	return &PublishEventConsumer{indexer: idx, bumper: bumper, logger: log.Nop()}
}

// WithLogger sets the logger and returns the consumer.
func (c *PublishEventConsumer) WithLogger(logger log.Logger) *PublishEventConsumer {
	c.logger = logger
	return c
}

// WithSummary turns on publish-time summaries: one is written per published
// version and stored, so reading a policy never regenerates it. module gates
// it; a nil module, a module that is off, or one that can't be read skips
// the summary.
func (c *PublishEventConsumer) WithSummary(assist Summarizer, summaries SummaryStore, module ModuleSwitch) *PublishEventConsumer {
	c.summarizer = assist
	c.summaries = summaries
	c.module = module
	return c
}

// WithCentroids turns on centroid upkeep: the published version's centroid
// is stored, and a removed document's centroid is deleted.
func (c *PublishEventConsumer) WithCentroids(centroids CentroidStore) *PublishEventConsumer {
	c.centroids = centroids
	return c
}

// WithReeval queues every changed policy for related-policy re-evaluation,
// after its centroid is updated so the re-evaluation reads the new geometry.
func (c *PublishEventConsumer) WithReeval(t ReevalTrigger) *PublishEventConsumer {
	c.reeval = t
	return c
}

// Handle processes one message body. It is idempotent: indexing upserts and
// removal deletes, so a redelivery is safe. Unknown event types are ignored.
func (c *PublishEventConsumer) Handle(ctx context.Context, body []byte) error {
	var evt PublishEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("consumer: unmarshal publish event: %w", err)
	}
	l := c.logger.Ctx(ctx)

	l.Info("received lifecycle event",
		log.F("event_type", string(evt.EventType)),
		log.F("policy_id", evt.policyID()),
		log.F("version_id", evt.Version.VersionID))

	switch evt.EventType {
	case EventTypePublished, EventTypeProcedurePublished:
		return c.handlePublished(ctx, evt.Version)
	case EventTypeUnpublished, EventTypeProcedureUnpublished,
		EventTypeRetired, EventTypeProcedureRetired:
		return c.handleRemoved(ctx, evt)
	default:
		l.Debug("ignored lifecycle event", log.F("event_type", string(evt.EventType)))
		return nil
	}
}

func (c *PublishEventConsumer) handlePublished(ctx context.Context, pv index.PolicyVersionContent) error {
	l := c.logger.Ctx(ctx)
	res, err := c.indexer.IndexVersion(ctx, pv)
	if err != nil {
		l.Error(err, "index version failed",
			log.F("policy_id", pv.PolicyID), log.F("version_id", pv.VersionID))
		return err
	}
	l.Info("indexed version",
		log.F("policy_id", pv.PolicyID),
		log.F("version_id", pv.VersionID),
		log.F("chunk_count", res.ChunkCount))
	c.generateAndStoreSummary(ctx, pv)
	c.upsertCentroid(ctx, pv, res)
	c.triggerReeval(ctx, pv.PolicyID)
	return c.bumpCorpusVersion(ctx)
}

// handleRemoved drops a version when the event names one, and otherwise
// every chunk of the policy; a retire names only the policy.
func (c *PublishEventConsumer) handleRemoved(ctx context.Context, evt PublishEvent) error {
	l := c.logger.Ctx(ctx)
	policyID := evt.policyID()
	versionID := evt.Version.VersionID

	switch {
	case versionID != "":
		if err := c.indexer.RemoveVersion(ctx, versionID); err != nil {
			l.Error(err, "remove version failed",
				log.F("policy_id", policyID), log.F("version_id", versionID))
			return err
		}
		l.Info("removed version from the index",
			log.F("policy_id", policyID), log.F("version_id", versionID))
	case policyID != "":
		if err := c.indexer.RemovePolicy(ctx, policyID); err != nil {
			l.Error(err, "remove policy failed", log.F("policy_id", policyID))
			return err
		}
		l.Info("removed policy from the index", log.F("policy_id", policyID))
	default:
		l.Warn("removal event names no version or policy; nothing to remove",
			log.F("event_type", string(evt.EventType)))
		return nil
	}

	c.removeCentroid(ctx, policyID)
	c.triggerReeval(ctx, policyID)
	return c.bumpCorpusVersion(ctx)
}

func (c *PublishEventConsumer) triggerReeval(ctx context.Context, policyID string) {
	if c.reeval == nil || policyID == "" {
		return
	}
	if err := c.reeval.Trigger(ctx, policyID); err != nil {
		l := c.logger.Ctx(ctx)
		l.Warn("queue related-policy re-evaluation failed; skipped",
			log.F("policy_id", policyID), log.F("error", err.Error()))
	}
}

func (c *PublishEventConsumer) bumpCorpusVersion(ctx context.Context) error {
	if c.bumper == nil {
		return nil
	}
	return c.bumper.BumpCorpusVersion(ctx)
}

// summariesOn reports whether a summary may be written now. A failed read of
// the switch counts as off.
func (c *PublishEventConsumer) summariesOn(ctx context.Context, pv index.PolicyVersionContent) bool {
	if c.summarizer == nil || c.summaries == nil || c.module == nil {
		return false
	}
	on, err := c.module.Enabled(ctx)
	l := c.logger.Ctx(ctx)
	if err != nil {
		l.Warn("read AI module switch failed; summary skipped",
			log.F("policy_id", pv.PolicyID), log.F("version_id", pv.VersionID), log.F("error", err.Error()))
		return false
	}
	if !on {
		l.Debug("AI module is off; summary skipped",
			log.F("policy_id", pv.PolicyID), log.F("version_id", pv.VersionID))
	}
	return on
}

// generateAndStoreSummary writes and stores the version's summary. It never
// fails the message: indexing has already succeeded, and requeueing a good
// publish for a summary would be worse than going without one.
func (c *PublishEventConsumer) generateAndStoreSummary(ctx context.Context, pv index.PolicyVersionContent) {
	if !c.summariesOn(ctx, pv) {
		return
	}
	l := c.logger.Ctx(ctx)

	resp, err := c.summarizer.Generate(ctx, generation.AssistRequest{
		PolicyID:        pv.PolicyID,
		VersionID:       pv.VersionID,
		EditableContent: buildSummaryDocument(pv),
		Operation:       generation.AssistOperationSummarize,
	})
	if err != nil {
		l.Warn("generate summary failed; skipped",
			log.F("policy_id", pv.PolicyID), log.F("version_id", pv.VersionID), log.F("error", err.Error()))
		return
	}

	sum := store.Summary{
		VersionID:   pv.VersionID,
		PolicyID:    pv.PolicyID,
		PolicyTitle: pv.PolicyTitle,
		SummaryText: resp.Suggestion,
	}
	if err := c.summaries.UpsertSummary(ctx, sum); err != nil {
		l.Warn("store summary failed; skipped",
			log.F("policy_id", pv.PolicyID), log.F("version_id", pv.VersionID), log.F("error", err.Error()))
		return
	}
	l.Info("stored summary", log.F("policy_id", pv.PolicyID), log.F("version_id", pv.VersionID))
}

// upsertCentroid stores the version's centroid, the mean IndexVersion already
// computed. A version with no chunks has none. Best effort, like the summary.
func (c *PublishEventConsumer) upsertCentroid(ctx context.Context, pv index.PolicyVersionContent, res index.IndexResult) {
	if c.centroids == nil || len(res.Centroid) == 0 {
		return
	}
	l := c.logger.Ctx(ctx)
	if err := c.centroids.UpsertCentroid(ctx, store.Centroid{
		PolicyID:    pv.PolicyID,
		VersionID:   pv.VersionID,
		VersionNo:   pv.VersionNo,
		Embedding:   res.Centroid,
		ChunkCount:  res.ChunkCount,
		CategoryID:  pv.CategoryID,
		Sensitivity: pv.Sensitivity,
		PolicyTitle: pv.PolicyTitle,
	}); err != nil {
		l.Warn("upsert centroid failed; skipped",
			log.F("policy_id", pv.PolicyID), log.F("version_id", pv.VersionID), log.F("error", err.Error()))
		return
	}
	l.Info("upserted centroid",
		log.F("policy_id", pv.PolicyID),
		log.F("version_id", pv.VersionID),
		log.F("chunk_count", res.ChunkCount))
}

// removeCentroid deletes the policy's centroid so it stops showing as
// related. Best effort: the chunks are already gone.
func (c *PublishEventConsumer) removeCentroid(ctx context.Context, policyID string) {
	if c.centroids == nil || policyID == "" {
		return
	}
	if err := c.centroids.DeleteByPolicyID(ctx, policyID); err != nil {
		l := c.logger.Ctx(ctx)
		l.Warn("delete centroid failed; skipped",
			log.F("policy_id", policyID), log.F("error", err.Error()))
	}
}

// buildSummaryDocument is the title and each non-empty section's text,
// separated by blank lines.
func buildSummaryDocument(pv index.PolicyVersionContent) string {
	parts := make([]string, 0, len(pv.Sections)+1)
	if strings.TrimSpace(pv.PolicyTitle) != "" {
		parts = append(parts, pv.PolicyTitle)
	}
	for _, sec := range pv.Sections {
		if strings.TrimSpace(sec.Text) == "" {
			continue
		}
		parts = append(parts, sec.Text)
	}
	return strings.Join(parts, "\n\n")
}
