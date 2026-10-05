// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package airules

// RabbitMQ names. The Publish* names are core's "jobs" exchange and the
// queues ai indexes from; the OperatorJobs* names are ai's own exchange for
// job completion events.
const (
	// PublishExchange is core's lifecycle exchange.
	PublishExchange = "jobs"
	// PublishQueueName is ai's policy indexing queue. Core's reindex
	// publishes to it directly through the default exchange.
	PublishQueueName = "ai.policy.publish"
	// PublishRoutingKey carries policy publish and unpublish events.
	PublishRoutingKey = "policy.published"

	// ProcedurePublishQueueName is ai's procedure indexing queue, bound to
	// both procedure routing keys. Core's reindex publishes to it directly.
	ProcedurePublishQueueName = "ai.procedure.publish"
	// ProcedurePublishRoutingKey carries procedure publish events.
	ProcedurePublishRoutingKey = "procedure.published"
	// ProcedureRetireRoutingKey carries procedure retire events.
	ProcedureRetireRoutingKey = "procedure.retired"

	// OperatorJobsCompletionExchange is the exchange job completion events
	// go to.
	OperatorJobsCompletionExchange = "ai.jobs"
	// OperatorJobSucceededRoutingKey is used when a job succeeds.
	OperatorJobSucceededRoutingKey = "ai.job.succeeded"
	// OperatorJobFailedRoutingKey is used when a job fails.
	OperatorJobFailedRoutingKey = "ai.job.failed"
)
