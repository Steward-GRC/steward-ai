// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
)

// JobAdapter creates and reads PolicyAIJob resources. It is the service's
// only Kubernetes access; the operator does the work.
type JobAdapter struct {
	client    client.Client
	namespace string
}

// NewJobAdapter wraps c, scoped to namespace.
func NewJobAdapter(c client.Client, namespace string) *JobAdapter {
	return &JobAdapter{client: c, namespace: namespace}
}

// CreateJob creates a PolicyAIJob with a generated name and returns it.
func (a *JobAdapter) CreateJob(ctx context.Context, spec v1alpha1.PolicyAIJobSpec) (*v1alpha1.PolicyAIJob, error) {
	job := &v1alpha1.PolicyAIJob{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "aijob-", Namespace: a.namespace},
		Spec:       spec,
	}
	if err := a.client.Create(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}

// CreateRelationshipLearnJob creates a RELATIONSHIP_LEARN job, the same one
// the operator schedules nightly, and returns its name. It carries no input
// and is charged to nobody's quota.
func (a *JobAdapter) CreateRelationshipLearnJob(ctx context.Context) (string, error) {
	job, err := a.CreateJob(ctx, v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationRelationshipLearn})
	if err != nil {
		return "", err
	}
	return job.Name, nil
}

// GetJob reads a PolicyAIJob by name.
func (a *JobAdapter) GetJob(ctx context.Context, jobID string) (*v1alpha1.PolicyAIJob, error) {
	var job v1alpha1.PolicyAIJob
	if err := a.client.Get(ctx, client.ObjectKey{Namespace: a.namespace, Name: jobID}, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// pingName is a job name the service never generates, so reading it is
// always a not-found.
const pingName = "readiness-probe"

// Ping reads a job that doesn't exist: a not-found means the API server, the
// PolicyAIJob resource and the service's get access are all there.
func (a *JobAdapter) Ping(ctx context.Context) error {
	var job v1alpha1.PolicyAIJob
	err := a.client.Get(ctx, client.ObjectKey{Namespace: a.namespace, Name: pingName}, &job)
	if err == nil || apierrors.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("policyaijobs in %s: %w", a.namespace, err)
}

// UnavailableJobs stands in for the job client when the service has no
// Kubernetes API: every job call is refused with AI_JOBS_UNAVAILABLE.
type UnavailableJobs struct{ reason error }

// NewUnavailableJobs refuses every job call; reason is why there is no API.
func NewUnavailableJobs(reason error) UnavailableJobs { return UnavailableJobs{reason: reason} }

// CreateJob refuses.
func (u UnavailableJobs) CreateJob(context.Context, v1alpha1.PolicyAIJobSpec) (*v1alpha1.PolicyAIJob, error) {
	return nil, errcodes.JobsUnavailable(u.reason)
}

// GetJob refuses.
func (u UnavailableJobs) GetJob(context.Context, string) (*v1alpha1.PolicyAIJob, error) {
	return nil, errcodes.JobsUnavailable(u.reason)
}
