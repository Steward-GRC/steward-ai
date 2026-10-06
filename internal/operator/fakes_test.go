// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"sync"
	"time"

	"github.com/Steward-GRC/steward-ai/internal/operator"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/retrieval"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// stubRetriever records the request it got and returns fixed chunks.
type stubRetriever struct {
	mu      sync.Mutex
	results []store.SearchResult
	err     error
	gotReq  retrieval.Request
	calls   int
}

func (s *stubRetriever) Retrieve(_ context.Context, req retrieval.Request) ([]store.SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.gotReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.results, nil
}

func (s *stubRetriever) snapshot() (int, retrieval.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.gotReq
}

// stubLLM is a provider.Generator answering every call with one response or
// one error, and counting the calls.
type stubLLM struct {
	mu       sync.Mutex
	response string
	err      error
	calls    int
}

func (s *stubLLM) Complete(_ context.Context, _ provider.Request) (provider.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return provider.Response{}, s.err
	}
	return provider.Response{Text: s.response}, nil
}

func (s *stubLLM) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// fakeResultWriter records the last result written.
type fakeResultWriter struct {
	mu      sync.Mutex
	writes  int
	lastKey string
	lastVal any
	lastTTL time.Duration
	err     error
}

func (f *fakeResultWriter) WriteResult(_ context.Context, key string, result any, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.writes++
	f.lastKey = key
	f.lastVal = result
	f.lastTTL = ttl
	return nil
}

func (f *fakeResultWriter) snapshot() (int, string, any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes, f.lastKey, f.lastVal
}

// fakeEventPublisher records every completion event.
type fakeEventPublisher struct {
	mu     sync.Mutex
	events []operator.CompletionEvent
	err    error
}

func (f *fakeEventPublisher) PublishJobCompletion(_ context.Context, ev operator.CompletionEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeEventPublisher) snapshot() []operator.CompletionEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]operator.CompletionEvent, len(f.events))
	copy(out, f.events)
	return out
}

// fakeDurableWriter records every durable save.
type fakeDurableWriter struct {
	mu    sync.Mutex
	saves []store.JobResult
	err   error
}

func (f *fakeDurableWriter) Save(_ context.Context, r store.JobResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.saves = append(f.saves, r)
	return nil
}

func (f *fakeDurableWriter) snapshot() []store.JobResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.JobResult, len(f.saves))
	copy(out, f.saves)
	return out
}

// fakeEnabledChecker is a fixed module switch.
type fakeEnabledChecker struct {
	mu  sync.Mutex
	on  bool
	err error
}

func (f *fakeEnabledChecker) Enabled(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.on, f.err
}

func (f *fakeEnabledChecker) set(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.on = on
}

// fakeResultVerifier is a fixed "result durable?" answer that counts how
// often it was asked.
type fakeResultVerifier struct {
	mu     sync.Mutex
	exists bool
	err    error
	calls  int
}

func (f *fakeResultVerifier) ResultExists(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.exists, f.err
}

func (f *fakeResultVerifier) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
