// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package reeval

import (
	"context"
	"time"

	"github.com/Bugs5382/go-log"
)

// Enqueuer is the part of PendingSet the Debouncer uses.
type Enqueuer interface {
	Enqueue(ctx context.Context, policyID string) (bool, error)
}

// JobCreator creates one RELATED_REEVAL job. The server wires an adapter over
// its job resource client, so this package never imports Kubernetes.
type JobCreator interface {
	CreateReevalJob(ctx context.Context) error
}

// Debouncer is the trigger half, called from the lifecycle consumer. Every
// changed policy joins the pending set; the first change after a quiet spell
// also opens the window and schedules one job for when it closes. Later
// changes in the window are drained by that same job.
type Debouncer struct {
	pending Enqueuer
	creator JobCreator
	window  time.Duration
	logger  log.Logger
	// afterFunc schedules fire; tests replace it.
	afterFunc func(time.Duration, func()) *time.Timer
}

// NewDebouncer returns a Debouncer. window should match the PendingSet's; a
// non-positive one uses DefaultWindow.
func NewDebouncer(pending Enqueuer, creator JobCreator, window time.Duration) *Debouncer {
	if window <= 0 {
		window = DefaultWindow
	}
	return &Debouncer{
		pending:   pending,
		creator:   creator,
		window:    window,
		logger:    log.Nop(),
		afterFunc: time.AfterFunc,
	}
}

// WithLogger sets the logger and returns the Debouncer.
func (d *Debouncer) WithLogger(l log.Logger) *Debouncer {
	d.logger = l
	return d
}

// Trigger queues a changed policy. It returns the enqueue error for the
// caller to log and drop, and schedules the job only when this call opened
// the window.
func (d *Debouncer) Trigger(ctx context.Context, policyID string) error {
	opened, err := d.pending.Enqueue(ctx, policyID)
	if err != nil {
		return err
	}
	if opened {
		l := d.logger.Ctx(ctx)
		l.Debug("reeval: window opened", log.F("policy_id", policyID), log.F("window", d.window.String()))
		d.afterFunc(d.window, d.fire)
	}
	return nil
}

// fire creates the job when the window closes, on its own context since the
// triggering request is long gone. A failure is logged: the set stays queued
// and the next publish's window drains it.
func (d *Debouncer) fire() {
	ctx := context.Background()
	if err := d.creator.CreateReevalJob(ctx); err != nil {
		d.logger.Warn("reeval: create RELATED_REEVAL job failed; the next publish retries", log.F("error", err.Error()))
		return
	}
	d.logger.Info("reeval: RELATED_REEVAL job created")
}
