// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"time"

	log "github.com/Bugs5382/go-log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
)

// RelationshipLearnJobCreator creates one RELATIONSHIP_LEARN job.
type RelationshipLearnJobCreator interface {
	CreateRelationshipLearnJob(ctx context.Context) error
}

// Scheduler creates a RELATIONSHIP_LEARN job every interval. It runs on the
// elected leader only, so several replicas still create one job per interval;
// the reconciler runs it like any other job.
type Scheduler struct {
	creator  RelationshipLearnJobCreator
	interval time.Duration
	log      log.Logger
}

// NewScheduler returns a Scheduler; interval <= 0 means daily.
func NewScheduler(creator RelationshipLearnJobCreator, interval time.Duration) *Scheduler {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &Scheduler{creator: creator, interval: interval, log: log.NewLogger("steward-ai-operator")}
}

// WithLogger sets the logger.
func (s *Scheduler) WithLogger(l log.Logger) *Scheduler {
	s.log = l
	return s
}

// NeedLeaderElection keeps the scheduler to the leader.
func (s *Scheduler) NeedLeaderElection() bool { return true }

// Start ticks until ctx ends. It does not fire on start, so a restart or a
// leadership change doesn't add an off-schedule run. A failed create is
// logged and left to the next tick.
func (s *Scheduler) Start(ctx context.Context) error {
	l := s.log.Ctx(ctx)
	l.Info("relationship-learn scheduler started", log.F("interval", s.interval.String()))
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			l.Info("relationship-learn scheduler stopped")
			return nil
		case <-ticker.C:
			if err := s.creator.CreateRelationshipLearnJob(ctx); err != nil {
				l.Warn("relationship-learn job not created; the next tick retries", log.F("error", err.Error()))
				continue
			}
			l.Info("relationship-learn job created")
		}
	}
}

// ClientJobCreator creates RELATIONSHIP_LEARN jobs through a Kubernetes
// client, in Namespace.
type ClientJobCreator struct {
	Client    client.Client
	Namespace string
}

// CreateRelationshipLearnJob creates the job. It carries no input: the whole
// corpus is the work.
func (c ClientJobCreator) CreateRelationshipLearnJob(ctx context.Context) error {
	return c.Client.Create(ctx, &v1alpha1.PolicyAIJob{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "aijob-", Namespace: c.Namespace},
		Spec:       v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationRelationshipLearn},
	})
}
