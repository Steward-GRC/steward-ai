// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	"github.com/Steward-GRC/steward-ai/internal/operator"
)

// jobDeleted reports whether the job's CRD has been garbage-collected (Get
// returns NotFound). Any other error fails the test.
func jobDeleted(t *testing.T, ctx context.Context, c client.Client, job *v1alpha1.PolicyAIJob) bool {
	t.Helper()
	var out v1alpha1.PolicyAIJob
	err := c.Get(ctx, types.NamespacedName{Namespace: job.Namespace, Name: job.Name}, &out)
	if err == nil {
		return false
	}
	if apierrors.IsNotFound(err) {
		return true
	}
	t.Fatalf("get job: %v", err)
	return false
}

// driveToSucceeded reconciles a fresh DRAFT job to terminal Succeeded with GC
// switched OFF (SucceededTTL=0), so the CRD is retained and the test can then
// flip the GC knobs and reconcile again in isolation.
func driveToSucceeded(t *testing.T, ctx context.Context, c client.Client, r *operator.PolicyAIJobReconciler) *v1alpha1.PolicyAIJob {
	t.Helper()
	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationDraft,
		Input:     runtime.RawExtension{Raw: draftInputJSON(t)},
	})
	reconcile(t, ctx, r, job)
	got := getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseSucceeded {
		t.Fatalf("setup: expected Succeeded, got %q (error=%q)", got.Status.Phase, got.Status.Error)
	}
	if got.Status.ResultRef == "" {
		t.Fatal("setup: expected ResultRef set on success")
	}
	return got
}

// TestReconcileGC_succeededPersisted_deletesCRD is the core GC guarantee:
// once a job is Succeeded AND its result is confirmed durably persisted
// (still fetchable), its CRD is garbage-collected after the
// short SucceededTTL.
func TestReconcileGC_succeededPersisted_deletesCRD(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	rw := &fakeResultWriter{}
	r := newReconcilerFull(c, &stubLLM{response: draftLLMResponse}, rw, &fakeEventPublisher{}, nil, nil, 0)
	got := driveToSucceeded(t, ctx, c, r)

	// Turn GC on with a durable verifier that confirms the result is persisted.
	verifier := &fakeResultVerifier{exists: true}
	r.Verifier = verifier
	r.SucceededTTL = time.Nanosecond // any elapsed time since FinishedAt is past-TTL

	reconcile(t, ctx, r, got)

	if !jobDeleted(t, ctx, c, got) {
		t.Fatal("expected Succeeded CRD to be GC-deleted once result is persisted")
	}
	if verifier.callCount() == 0 {
		t.Fatal("expected GC to CONFIRM result persisted (verifier consulted) before deleting")
	}
}

// TestReconcileGC_succeededNotYetPersisted_retainsCRD is the CRITICAL safety
// guarantee: a Succeeded job whose result is NOT yet durably persisted is
// NEVER GC'd — deletion is deferred until the content is fetchable, so
// UI/history is never lost.
func TestReconcileGC_succeededNotYetPersisted_retainsCRD(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	r := newReconcilerFull(c, &stubLLM{response: draftLLMResponse}, &fakeResultWriter{}, &fakeEventPublisher{}, nil, nil, 0)
	got := driveToSucceeded(t, ctx, c, r)

	// Verifier reports the durable result is NOT there yet.
	verifier := &fakeResultVerifier{exists: false}
	r.Verifier = verifier
	r.SucceededTTL = time.Nanosecond // TTL is past — only persistence gates the delete

	reconcile(t, ctx, r, got)

	if jobDeleted(t, ctx, c, got) {
		t.Fatal("expected Succeeded CRD to be RETAINED while its result is not yet persisted")
	}
	still := getJob(t, ctx, c, got)
	if still.Status.Phase != v1alpha1.PhaseSucceeded {
		t.Fatalf("expected retained job to still be Succeeded, got %q", still.Status.Phase)
	}
	if verifier.callCount() == 0 {
		t.Fatal("expected GC to consult the persistence verifier")
	}
}

// TestReconcileGC_succeededWithinTTL_retainsCRD verifies the short grace
// window: a just-Succeeded, persisted job is kept until it ages past
// SucceededTTL, so a client still polling GetAIJob can observe the terminal
// phase and grab the result ref before the CRD disappears.
func TestReconcileGC_succeededWithinTTL_retainsCRD(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	r := newReconcilerFull(c, &stubLLM{response: draftLLMResponse}, &fakeResultWriter{}, &fakeEventPublisher{}, nil, nil, 0)
	got := driveToSucceeded(t, ctx, c, r)

	r.Verifier = &fakeResultVerifier{exists: true}
	r.SucceededTTL = time.Hour // well in the future — not yet eligible

	reconcile(t, ctx, r, got)

	if jobDeleted(t, ctx, c, got) {
		t.Fatal("expected Succeeded CRD to be retained within its TTL grace window")
	}
}

// TestReconcileGC_succeededGCDisabled_retainsCRD verifies SucceededTTL<=0
// disables Succeeded GC entirely (the default for unit reconcilers), so
// existing behaviour is preserved and nothing is deleted.
func TestReconcileGC_succeededGCDisabled_retainsCRD(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	r := newReconcilerFull(c, &stubLLM{response: draftLLMResponse}, &fakeResultWriter{}, &fakeEventPublisher{}, nil, nil, 0)
	got := driveToSucceeded(t, ctx, c, r)

	// SucceededTTL stays 0 (disabled); a re-reconcile must not delete.
	reconcile(t, ctx, r, got)

	if jobDeleted(t, ctx, c, got) {
		t.Fatal("expected Succeeded CRD retained when Succeeded GC is disabled (TTL<=0)")
	}
}

// TestReconcileGC_failedRetainedForDebugging verifies a Failed job's CRD is
// retained (not GC'd) while within its long FailedTTL, so failures stay
// diagnosable.
func TestReconcileGC_failedRetainedForDebugging(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	// MaxAttempts=1 → first failed attempt is permanent Failed.
	r := newReconcilerFull(c, &stubLLM{err: context.DeadlineExceeded}, &fakeResultWriter{}, &fakeEventPublisher{}, nil, nil, 1)
	r.FailedTTL = time.Hour // long retention — not yet eligible

	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationDraft,
		Input:     runtime.RawExtension{Raw: draftInputJSON(t)},
	})
	reconcile(t, ctx, r, job)
	got := getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("expected Failed, got %q", got.Status.Phase)
	}

	// Re-reconcile the terminal Failed job: it must be retained.
	reconcile(t, ctx, r, got)
	if jobDeleted(t, ctx, c, got) {
		t.Fatal("expected Failed CRD to be retained for debugging within FailedTTL")
	}
}

// TestReconcileGC_failedForeverWhenTTLDisabled verifies FailedTTL<=0 retains
// Failed CRDs forever (never GC'd), the default for a deployment that would
// rather keep every failure than risk collecting one.
func TestReconcileGC_failedForeverWhenTTLDisabled(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	r := newReconcilerFull(c, &stubLLM{err: context.DeadlineExceeded}, &fakeResultWriter{}, &fakeEventPublisher{}, nil, nil, 1)
	// FailedTTL stays 0 (retain forever).

	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationDraft,
		Input:     runtime.RawExtension{Raw: draftInputJSON(t)},
	})
	reconcile(t, ctx, r, job)
	got := getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("expected Failed, got %q", got.Status.Phase)
	}

	reconcile(t, ctx, r, got)
	if jobDeleted(t, ctx, c, got) {
		t.Fatal("expected Failed CRD retained forever when FailedTTL<=0")
	}
}

// TestReconcileGC_failedAgedOutPastCap_deletesCRD verifies the unbounded-growth
// cap: a Failed job's CRD IS collected once it ages past the (long) FailedTTL.
func TestReconcileGC_failedAgedOutPastCap_deletesCRD(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	// Drive to Failed with the cap OFF (FailedTTL=0) so the failing reconcile
	// retains the CRD and the test can observe the terminal phase first.
	r := newReconcilerFull(c, &stubLLM{err: context.DeadlineExceeded}, &fakeResultWriter{}, &fakeEventPublisher{}, nil, nil, 1)

	job := createJob(t, ctx, c, v1alpha1.PolicyAIJobSpec{
		Operation: v1alpha1.OperationDraft,
		Input:     runtime.RawExtension{Raw: draftInputJSON(t)},
	})
	reconcile(t, ctx, r, job)
	got := getJob(t, ctx, c, job)
	if got.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("expected Failed, got %q", got.Status.Phase)
	}

	// Now impose a cap already in the past: the next resync collects it.
	r.FailedTTL = time.Nanosecond // any elapsed time since FinishedAt is past the cap
	reconcile(t, ctx, r, got)
	if !jobDeleted(t, ctx, c, got) {
		t.Fatal("expected aged-out Failed CRD to be GC-deleted past the retention cap")
	}
}

// TestReconcileGC_succeededNoVerifier_usesResultRef verifies the Redis-only
// path: with no durable Verifier wired, a Succeeded job with a set ResultRef
// counts as persisted (the Redis write is proven), so it is GC-eligible.
func TestReconcileGC_succeededNoVerifier_usesResultRef(t *testing.T) {
	c, stop := startEnv(t)
	defer stop()
	ctx := context.Background()

	r := newReconcilerFull(c, &stubLLM{response: draftLLMResponse}, &fakeResultWriter{}, &fakeEventPublisher{}, nil, nil, 0)
	got := driveToSucceeded(t, ctx, c, r)

	r.Verifier = nil // Redis-only: ResultRef presence is the persistence proof
	r.SucceededTTL = time.Nanosecond

	reconcile(t, ctx, r, got)

	if !jobDeleted(t, ctx, c, got) {
		t.Fatal("expected Succeeded CRD GC-deleted via ResultRef when no durable verifier is wired")
	}
}
