// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package operator is the PolicyAIJob controller. It dispatches a Pending job
// by operation, writes the result to Valkey and Postgres, publishes a
// completion event and records the outcome on the job's status. A job that
// would call a model fails without running while the AI module is off.
package operator

import (
	"context"
	"fmt"
	"time"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

const resultKeyPrefix = "ai:job-result"

// ResultTTL is how long a result stays in Valkey. The durable copy in
// Postgres outlives it.
const ResultTTL = 24 * time.Hour

// ResultKey is the Valkey key a job's result is written under. A job on an
// existing version is keyed by policy, version and operation, so the latest
// result for that version and operation is found without the job name. A job
// with no version yet (a new draft) falls back to the job's own name, so
// concurrent drafts never share a key.
func ResultKey(job *v1alpha1.PolicyAIJob) string {
	op := string(job.Spec.Operation)
	if job.Spec.PolicyID != "" && job.Spec.VersionID != "" {
		return fmt.Sprintf("%s:%s:%s:%s", resultKeyPrefix, job.Spec.PolicyID, job.Spec.VersionID, op)
	}
	return fmt.Sprintf("%s:%s:%s", resultKeyPrefix, job.Name, op)
}

// CompletionEvent is published once a job reaches Succeeded or Failed.
type CompletionEvent struct {
	JobID       string    `json:"job_id"`
	Operation   string    `json:"operation"`
	ActorUserID string    `json:"actor_user_id,omitempty"`
	PolicyID    string    `json:"policy_id,omitempty"`
	VersionID   string    `json:"version_id,omitempty"`
	CategoryID  string    `json:"category_id,omitempty"`
	Phase       string    `json:"phase"`
	ResultRef   string    `json:"result_ref,omitempty"`
	Error       string    `json:"error,omitempty"`
	FinishedAt  time.Time `json:"finished_at"`
}

// ResultWriter stores a job's result in the hot tier.
type ResultWriter interface {
	WriteResult(ctx context.Context, key string, result any, ttl time.Duration) error
}

// ResultEnvelope is what is written under ResultKey: the result tagged with
// its operation, so a reader can decode it without looking up the job.
type ResultEnvelope struct {
	Operation string `json:"operation"`
	Result    any    `json:"result"`
}

// EventPublisher publishes a completion event.
type EventPublisher interface {
	PublishJobCompletion(ctx context.Context, ev CompletionEvent) error
}

// DurableWriter stores a succeeded job's result in Postgres.
// *store.JobResultStore satisfies it.
type DurableWriter interface {
	Save(ctx context.Context, r store.JobResult) error
}

// EnabledChecker is the AI module's switch. *aiconfig.Store satisfies it.
type EnabledChecker interface {
	Enabled(ctx context.Context) (bool, error)
}

// ResultVerifier reports whether a job's result is stored durably, which the
// garbage collector requires before it deletes a succeeded job.
// *store.JobResultStore satisfies it.
type ResultVerifier interface {
	ResultExists(ctx context.Context, jobID string) (bool, error)
}

// DefaultMaxAttempts is used when the reconciler's MaxAttempts is unset.
const DefaultMaxAttempts = 3

// gcRecheckInterval is how soon a succeeded job whose result is not yet
// confirmed durable is looked at again.
const gcRecheckInterval = 30 * time.Second
