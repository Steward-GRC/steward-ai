// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package reeval re-evaluates the suggested related policies, with no
// provider call, when the corpus moves on publish or unpublish.
//
// It has two halves, one per binary. The server's Debouncer collects changed
// policy ids in a Valkey set behind a short window and creates one
// RELATED_REEVAL job when the window opens. The operator's Engine drains the
// set and, for each changed policy and its centroid neighbours, recomputes the
// suggested set and writes it as a diff: new suggestions are added, stale
// ones removed, and accepted or dismissed ones kept.
//
// All of it is best effort (airules.GovernanceBestEffortDegradation): a
// failure never blocks a publish, and a partly failed pass still applies what
// it computed.
package reeval

import (
	"context"
	"errors"
	"time"

	"github.com/Bugs5382/go-redis"
)

const (
	// PendingSetKey is the Valkey set of changed policy ids awaiting
	// re-evaluation.
	PendingSetKey = "ai:reeval:pending"
	// windowKey marks an open debounce window. Set with a TTL, so changes
	// inside the window join the job already scheduled.
	windowKey = "ai:reeval:window"
	// corpusVersionKey is the answer cache's corpus version, read here only
	// to stamp each suggestion. It must match the cache's key.
	corpusVersionKey = "ai:corpus:version"
)

// PendingSet is the Valkey debounce queue: the server enqueues, the operator
// drains.
type PendingSet struct {
	rdb    redis.UniversalClient
	window time.Duration
}

// NewPendingSet returns a PendingSet over rdb with the debounce window; a
// non-positive window uses DefaultWindow.
func NewPendingSet(rdb redis.UniversalClient, window time.Duration) *PendingSet {
	if window <= 0 {
		window = DefaultWindow
	}
	return &PendingSet{rdb: rdb, window: window}
}

// Window returns the debounce window.
func (p *PendingSet) Window() time.Duration { return p.window }

// Enqueue adds policyID to the set and reports whether this call opened a new
// window. Exactly one call in a burst opens it, and that caller schedules the
// one job.
func (p *PendingSet) Enqueue(ctx context.Context, policyID string) (bool, error) {
	if err := p.rdb.SAdd(ctx, PendingSetKey, policyID).Err(); err != nil {
		return false, err
	}
	return p.rdb.SetNX(ctx, windowKey, "1", p.window).Result()
}

// drainScript returns and clears the set in one step, so an id added
// between a read and a delete can't be lost.
const drainScript = `local m = redis.call('SMEMBERS', KEYS[1]); redis.call('DEL', KEYS[1]); return m`

// Drain returns and clears every queued id. An empty set gives nil.
func (p *PendingSet) Drain(ctx context.Context) ([]string, error) {
	ids, err := p.rdb.Eval(ctx, drainScript, []string{PendingSetKey}).StringSlice()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// CorpusVersion reads the corpus version, 0 when unset.
func (p *PendingSet) CorpusVersion(ctx context.Context) (int64, error) {
	v, err := p.rdb.Get(ctx, corpusVersionKey).Int64()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}
