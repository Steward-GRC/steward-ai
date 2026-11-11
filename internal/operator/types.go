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

// ResultKey is the Valkey key a job's result is written under:
// ai:job-result:<job id>:<OP>. The job id is unique, so two jobs of one
// operation on one version never overwrite each other's result.
func ResultKey(job *v1alpha1.PolicyAIJob) string {
	return fmt.Sprintf("%s:%s:%s", resultKeyPrefix, job.Name, string(job.Spec.Operation))
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
// its operation, so a reader can decode it without looking up the job, and
// with the user who submitted the job, so a reader can refuse anyone else.
type ResultEnvelope struct {
	Operation string `json:"operation"`
	// OwnerUserID is the job's spec.actorUserId, the same user GetAIJob
	// answers. Envelopes written before the field existed decode it as empty.
	OwnerUserID string `json:"owner_user_id"`
	Result      any    `json:"result"`
}

// OwnedBy reports whether userID submitted the job. It fails closed: an
// envelope with no owner is owned by no one, and an empty userID never
// matches.
func (e ResultEnvelope) OwnedBy(userID string) bool {
	return e.OwnerUserID != "" && userID != "" && e.OwnerUserID == userID
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
