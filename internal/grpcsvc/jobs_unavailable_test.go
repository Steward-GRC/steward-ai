// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"errors"
	"testing"
	"time"

	log "github.com/Bugs5382/go-log"
	"google.golang.org/grpc/codes"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/fixture"
)

// withoutKubernetes is a server whose jobs have no Kubernetes API.
func withoutKubernetes() *AiServiceServer {
	d := newFakes().deps()
	d.Jobs = NewUnavailableJobs(errors.New("kubernetes config: no configuration has been provided"))
	return New(d, WithLogger(log.Nop()), WithClock(func() time.Time { return testNow }))
}

func TestSubmitAIJob_withoutTheKubernetesAPIIsJobsUnavailable(t *testing.T) {
	_, err := withoutKubernetes().SubmitAIJob(erinCtx(), &aiv1.SubmitAIJobRequest{
		CategoryId: fixture.Facilities,
		Scope:      &aiv1.ReadScope{CategoryIds: []string{fixture.Facilities}},
		Operation:  aiv1.JobOperation_JOB_OPERATION_DRAFT,
	})
	requireCode(t, err, errcodes.CodeJobsUnavailable, codes.Unavailable)
}

func TestGetAIJob_withoutTheKubernetesAPIIsJobsUnavailable(t *testing.T) {
	_, err := withoutKubernetes().GetAIJob(erinCtx(), &aiv1.GetAIJobRequest{JobId: "aijob-abc123"})
	requireCode(t, err, errcodes.CodeJobsUnavailable, codes.Unavailable)
}

func TestGetProviderStatus_withoutTheKubernetesAPIIsUnavailableWithAReason(t *testing.T) {
	resp, err := withoutKubernetes().GetProviderStatus(erinCtx(), &aiv1.GetProviderStatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetAvailable() || resp.GetReason() != ReasonJobsUnavailable {
		t.Fatalf("want unavailable with reason %q, got %+v", ReasonJobsUnavailable, resp)
	}
}

func TestGetProviderStatus_jobsUnavailableWinsOverAHealthyProvider(t *testing.T) {
	d := newFakes().deps()
	d.Jobs = NewUnavailableJobs(errors.New("no kubernetes"))
	resp, err := New(d, WithLogger(log.Nop())).GetProviderStatus(erinCtx(), &aiv1.GetProviderStatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetAvailable() {
		t.Fatalf("provider status must not claim available while jobs cannot run: %+v", resp)
	}
}

func TestGetProviderStatus_withJobsReflectsTheProvider(t *testing.T) {
	resp, err := New(newFakes().deps(), WithLogger(log.Nop())).GetProviderStatus(erinCtx(), &aiv1.GetProviderStatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetAvailable() || resp.GetReason() != "" {
		t.Fatalf("want available, got %+v", resp)
	}
}

func TestGetProviderStatus_providerReasonKeptWhenJobsAreUp(t *testing.T) {
	f := newFakes()
	f.llm.available, f.llm.reason = false, "auth_failed"
	resp, err := New(f.deps(), WithLogger(log.Nop())).GetProviderStatus(erinCtx(), &aiv1.GetProviderStatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetAvailable() || resp.GetReason() != "auth_failed" {
		t.Fatalf("want auth_failed, got %+v", resp)
	}
}
