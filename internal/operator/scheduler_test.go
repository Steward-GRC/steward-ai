// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-ai/internal/operator"
)

// fakeLearnCreator counts CreateRelationshipLearnJob calls (the CR the leader
// ticker and the admin trigger both create).
type fakeLearnCreator struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeLearnCreator) CreateRelationshipLearnJob(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func (f *fakeLearnCreator) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestScheduler_NeedsLeaderElection: only the elected leader ticks, so an HA
// operator creates exactly one nightly job.
func TestScheduler_NeedsLeaderElection(t *testing.T) {
	s := operator.NewScheduler(&fakeLearnCreator{}, time.Hour)
	if !s.NeedLeaderElection() {
		t.Fatal("scheduler must require leader election")
	}
}

// TestScheduler_TicksCreateJobs: on each interval the scheduler creates one
// RELATIONSHIP_LEARN job (the scheduled trigger), and stops cleanly on ctx
// cancel. A short interval keeps the test fast.
func TestScheduler_TicksCreateJobs(t *testing.T) {
	creator := &fakeLearnCreator{}
	s := operator.NewScheduler(creator, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = s.Start(ctx)
		close(done)
	}()

	// Wait until at least two ticks have fired.
	deadline := time.After(2 * time.Second)
	for creator.count() < 2 {
		select {
		case <-deadline:
			t.Fatalf("expected >=2 scheduled jobs, got %d", creator.count())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop on ctx cancel")
	}
}

// TestScheduler_CreateErrorIsBestEffort: a creation failure is swallowed and the
// next tick retries — a missed nightly run must never crash the operator.
func TestScheduler_CreateErrorIsBestEffort(t *testing.T) {
	creator := &fakeLearnCreator{err: errors.New("apiserver down")}
	s := operator.NewScheduler(creator, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start should return nil on ctx timeout, got %v", err)
	}
	if creator.count() < 2 {
		t.Fatalf("expected the scheduler to keep ticking through errors, got %d calls", creator.count())
	}
}
