// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/operator"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

type fakeSummaryCache struct {
	mu sync.Mutex
	m  map[string]generation.SummaryJobResult
}

func (f *fakeSummaryCache) Get(_ context.Context, key string) (generation.SummaryJobResult, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[key]
	return v, ok, nil
}

func (f *fakeSummaryCache) Put(_ context.Context, key string, v generation.SummaryJobResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]generation.SummaryJobResult{}
	}
	f.m[key] = v
	return nil
}

type fakeSummaryStore struct {
	mu   sync.Mutex
	rows map[string]store.Summary
}

func (f *fakeSummaryStore) GetSummary(_ context.Context, id string) (store.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.rows[id]; ok {
		return s, nil
	}
	return store.Summary{}, pgx.ErrNoRows
}

func (f *fakeSummaryStore) UpsertSummary(_ context.Context, s store.Summary) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows == nil {
		f.rows = map[string]store.Summary{}
	}
	f.rows[s.VersionID] = s
	return nil
}

type fakeAssist struct {
	mu    sync.Mutex
	text  string
	err   error
	calls int
}

func (f *fakeAssist) Generate(context.Context, generation.AssistRequest) (generation.AssistResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return generation.AssistResponse{Suggestion: f.text}, f.err
}

func summaryJob(t *testing.T, in generation.SummaryJobInput) *v1alpha1.PolicyAIJob {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationSummarize, ActorUserID: "user-1", Input: runtime.RawExtension{Raw: raw},
	}}
}

func changesDispatcher(llm *stubLLM, c *fakeSummaryCache) *operator.Dispatcher {
	return newTestDispatcher(llm).WithSummaries(generation.NewChangeSummarizer(llm), c, nil, nil)
}

func changesInput(regenerate bool) generation.SummaryJobInput {
	return generation.SummaryJobInput{Scope: generation.SummaryScopeChanges, FromVersionID: "v1", ToVersionID: "v2", Diff: "-a\n+b", Regenerate: regenerate}
}

func TestDispatch_changeSummaryFreshThenCached(t *testing.T) {
	llm := &stubLLM{response: "- Changes a to b."}
	d := changesDispatcher(llm, &fakeSummaryCache{})

	got, err := d.Dispatch(context.Background(), summaryJob(t, changesInput(false)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fresh := got.(generation.SummaryJobResult)
	if fresh.Cached || len(fresh.Bullets) != 1 || fresh.GeneratedAt == "" {
		t.Fatalf("fresh result wrong: %+v", fresh)
	}

	got, err = d.Dispatch(context.Background(), summaryJob(t, changesInput(false)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	again := got.(generation.SummaryJobResult)
	if !again.Cached || again.GeneratedAt != fresh.GeneratedAt || again.Bullets[0] != fresh.Bullets[0] {
		t.Fatalf("repeat should be the stored summary: fresh %+v again %+v", fresh, again)
	}
	if llm.callCount() != 1 {
		t.Fatalf("model calls = %d, want 1", llm.callCount())
	}
}

func TestDispatch_changeSummaryRegenerateReplacesTheStoredOne(t *testing.T) {
	llm := &stubLLM{response: "- First."}
	c := &fakeSummaryCache{}
	d := changesDispatcher(llm, c)
	if _, err := d.Dispatch(context.Background(), summaryJob(t, changesInput(false))); err != nil {
		t.Fatal(err)
	}
	llm.mu.Lock()
	llm.response = "- Second."
	llm.mu.Unlock()

	got, err := d.Dispatch(context.Background(), summaryJob(t, changesInput(true)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r := got.(generation.SummaryJobResult); r.Cached || r.Bullets[0] != "Second." {
		t.Fatalf("regenerate should call the model and not be cached: %+v", r)
	}
	got, _ = d.Dispatch(context.Background(), summaryJob(t, changesInput(false)))
	if r := got.(generation.SummaryJobResult); !r.Cached || r.Bullets[0] != "Second." {
		t.Fatalf("the regenerated summary should now be the stored one: %+v", r)
	}
}

func TestDispatch_changeSummaryFailureKeepsTheStoredSummary(t *testing.T) {
	llm := &stubLLM{response: "- First."}
	c := &fakeSummaryCache{}
	d := changesDispatcher(llm, c)
	if _, err := d.Dispatch(context.Background(), summaryJob(t, changesInput(false))); err != nil {
		t.Fatal(err)
	}
	llm.mu.Lock()
	llm.err = errors.New("provider down")
	llm.mu.Unlock()
	if _, err := d.Dispatch(context.Background(), summaryJob(t, changesInput(true))); err == nil {
		t.Fatal("expected the regenerate to fail")
	}
	llm.mu.Lock()
	llm.err = nil
	llm.mu.Unlock()
	got, err := d.Dispatch(context.Background(), summaryJob(t, changesInput(false)))
	if err != nil {
		t.Fatal(err)
	}
	if r := got.(generation.SummaryJobResult); !r.Cached || r.Bullets[0] != "First." {
		t.Fatalf("stored summary lost after a failed regenerate: %+v", r)
	}
}

func TestDispatch_versionSummaryStoredThenRegenerated(t *testing.T) {
	rows := &fakeSummaryStore{rows: map[string]store.Summary{"v1": {VersionID: "v1", SummaryText: "- Old.", PolicyID: "p1"}}}
	assist := &fakeAssist{text: "- New."}
	d := newTestDispatcher(&stubLLM{}).WithSummaries(nil, nil, assist, rows)
	in := generation.SummaryJobInput{Scope: generation.SummaryScopeVersion, VersionID: "v1", PolicyID: "p1", PolicyTitle: "T", Text: "body"}

	got, err := d.Dispatch(context.Background(), summaryJob(t, in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r := got.(generation.SummaryJobResult); !r.Cached || r.SummaryText != "- Old." || assist.calls != 0 {
		t.Fatalf("stored summary should be returned without a model call: %+v calls=%d", r, assist.calls)
	}

	in.Regenerate = true
	got, err = d.Dispatch(context.Background(), summaryJob(t, in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r := got.(generation.SummaryJobResult); r.Cached || r.SummaryText != "- New." || rows.rows["v1"].SummaryText != "- New." {
		t.Fatalf("regenerate should replace the stored summary: %+v row=%+v", r, rows.rows["v1"])
	}
}

func TestDispatch_versionSummaryWithNoStoredRowGenerates(t *testing.T) {
	rows := &fakeSummaryStore{}
	assist := &fakeAssist{text: "- Made."}
	d := newTestDispatcher(&stubLLM{}).WithSummaries(nil, nil, assist, rows)
	got, err := d.Dispatch(context.Background(), summaryJob(t, generation.SummaryJobInput{Scope: generation.SummaryScopeVersion, VersionID: "v9", Text: "body"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r := got.(generation.SummaryJobResult); r.Cached || r.SummaryText != "- Made." || rows.rows["v9"].SummaryText != "- Made." {
		t.Fatalf("a miss should generate and store: %+v", r)
	}
}

func TestDispatch_versionSummaryFailureKeepsTheRow(t *testing.T) {
	rows := &fakeSummaryStore{rows: map[string]store.Summary{"v1": {VersionID: "v1", SummaryText: "- Old."}}}
	assist := &fakeAssist{err: errors.New("provider down")}
	d := newTestDispatcher(&stubLLM{}).WithSummaries(nil, nil, assist, rows)
	_, err := d.Dispatch(context.Background(), summaryJob(t, generation.SummaryJobInput{Scope: generation.SummaryScopeVersion, VersionID: "v1", Text: "body", Regenerate: true}))
	if err == nil || rows.rows["v1"].SummaryText != "- Old." {
		t.Fatalf("a failed regenerate must keep the stored summary: err=%v row=%+v", err, rows.rows["v1"])
	}
}

func TestDispatch_summarizeWithoutAScopeIsStillNotImplemented(t *testing.T) {
	d := changesDispatcher(&stubLLM{}, &fakeSummaryCache{})
	_, err := d.Dispatch(context.Background(), &v1alpha1.PolicyAIJob{Spec: v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationSummarize}})
	if !errors.Is(err, operator.ErrOperationNotImplemented) {
		t.Fatalf("got %v", err)
	}
}

func TestDispatch_summaryScopeNotWiredIsNotImplemented(t *testing.T) {
	d := newTestDispatcher(&stubLLM{})
	_, err := d.Dispatch(context.Background(), summaryJob(t, changesInput(false)))
	if !errors.Is(err, operator.ErrOperationNotImplemented) {
		t.Fatalf("got %v", err)
	}
}
