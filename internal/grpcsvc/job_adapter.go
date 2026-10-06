// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
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
