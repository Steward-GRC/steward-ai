// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package reeval

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeEnqueuer records enqueues and returns a scripted opened/err per call.
type fakeEnqueuer struct {
	mu      sync.Mutex
	ids     []string
	openOn  int // 1-based call index that returns opened=true (0 = never)
	calls   int
	failErr error
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, policyID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.ids = append(f.ids, policyID)
	if f.failErr != nil {
		return false, f.failErr
	}
	return f.calls == f.openOn, nil
}

// countingCreator counts how many RELATED_REEVAL jobs were created.
type countingCreator struct {
	mu    sync.Mutex
	count int
	err   error
}

func (c *countingCreator) CreateReevalJob(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	return c.err
}

func (c *countingCreator) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// TestDebouncer_CoalescesBurstIntoOneJob: a burst of Triggers where only the
// first opened the window schedules exactly ONE job (fired when the window
// callback runs).
func TestDebouncer_CoalescesBurstIntoOneJob(t *testing.T) {
	ctx := context.Background()
	enq := &fakeEnqueuer{openOn: 1}
	creator := &countingCreator{}

	var captured []func()
	d := NewDebouncer(enq, creator, time.Minute)
	d.afterFunc = func(_ time.Duration, f func()) *time.Timer {
		captured = append(captured, f) // capture, don't run
		return nil
	}

	for _, id := range []string{"p1", "p2", "p3"} {
		if err := d.Trigger(ctx, id); err != nil {
			t.Fatalf("Trigger(%s): %v", id, err)
		}
	}

	if len(captured) != 1 {
		t.Fatalf("expected exactly one scheduled window callback, got %d", len(captured))
	}
	if creator.n() != 0 {
		t.Fatalf("creator must not run until the window fires; got %d", creator.n())
	}
	captured[0]() // window elapses
	if creator.n() != 1 {
		t.Fatalf("expected exactly one job created on window fire, got %d", creator.n())
	}
	if len(enq.ids) != 3 {
		t.Fatalf("all three changes should have been enqueued, got %v", enq.ids)
	}
}

// TestDebouncer_NoScheduleWhenWindowAlreadyOpen: if no call opens the window
// (one already open), nothing is scheduled.
func TestDebouncer_NoScheduleWhenWindowAlreadyOpen(t *testing.T) {
	enq := &fakeEnqueuer{openOn: 0} // never opens
	creator := &countingCreator{}
	scheduled := 0
	d := NewDebouncer(enq, creator, time.Minute)
	d.afterFunc = func(_ time.Duration, _ func()) *time.Timer { scheduled++; return nil }

	if err := d.Trigger(context.Background(), "p1"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if scheduled != 0 {
		t.Fatalf("expected no schedule when window already open, got %d", scheduled)
	}
}

// TestDebouncer_TriggerReturnsEnqueueError: an enqueue failure propagates so the
// consumer can log-and-swallow it (best-effort), and nothing is scheduled.
func TestDebouncer_TriggerReturnsEnqueueError(t *testing.T) {
	wantErr := errors.New("valkey down")
	enq := &fakeEnqueuer{failErr: wantErr}
	scheduled := 0
	d := NewDebouncer(enq, &countingCreator{}, time.Minute)
	d.afterFunc = func(_ time.Duration, _ func()) *time.Timer { scheduled++; return nil }

	if err := d.Trigger(context.Background(), "p1"); !errors.Is(err, wantErr) {
		t.Fatalf("Trigger error: got %v want %v", err, wantErr)
	}
	if scheduled != 0 {
		t.Fatalf("no schedule expected on enqueue error, got %d", scheduled)
	}
}

// TestDebouncer_FireSwallowsCreatorError: fire() must not panic/propagate a
// creator error (best-effort).
func TestDebouncer_FireSwallowsCreatorError(t *testing.T) {
	creator := &countingCreator{err: errors.New("kubernetes unavailable")}
	d := NewDebouncer(&fakeEnqueuer{openOn: 1}, creator, time.Minute)
	var fired func()
	d.afterFunc = func(_ time.Duration, f func()) *time.Timer { fired = f; return nil }

	if err := d.Trigger(context.Background(), "p1"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	fired() // must not panic despite creator error
	if creator.n() != 1 {
		t.Fatalf("creator should have been attempted once, got %d", creator.n())
	}
}
