// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	log "github.com/Bugs5382/go-log"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	"github.com/Steward-GRC/steward-ai/internal/llm"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// nowFunc is replaced in tests.
var nowFunc = time.Now

var (
	// ErrModuleOff fails a job that would call a model while the AI module
	// is off.
	ErrModuleOff = errors.New("operator: the AI module is off, so the job was not run")
	// ErrModuleSwitchUnreadable fails such a job when the switch can't be
	// read, rather than run with the module possibly off.
	ErrModuleSwitchUnreadable = errors.New("operator: the AI module switch could not be read, so the job was not run")
)

// PolicyAIJobReconciler drives a PolicyAIJob from Pending to Succeeded or
// Failed.
//
// Reconcile is idempotent: a job already terminal for its current generation
// is never run again; it only goes through garbage collection.
//
// A failed attempt (the dispatch or either result write) increments
// Status.Attempts and requeues with a backoff until MaxAttempts, then fails
// the job. A refusal from the model client (the module turned off mid-job,
// the monthly limit reached, the settings unreadable) fails it at once.
//
// Before every attempt that may call a model, the module switch is read: off,
// or unreadable, fails the job without dispatching. Jobs that make no model
// call run either way.
type PolicyAIJobReconciler struct {
	client.Client
	Dispatcher *Dispatcher
	Results    ResultWriter
	Events     EventPublisher
	// Durable keeps the result in Postgres beside Valkey; nil keeps Valkey
	// only.
	Durable DurableWriter
	// Enabled is the module switch. Nil counts as off.
	Enabled EnabledChecker
	// MaxAttempts caps retries; <= 0 uses DefaultMaxAttempts.
	MaxAttempts int
	// Verifier confirms a succeeded job's result is durable before GC deletes
	// the job. Nil accepts a set ResultRef, which is only set after the
	// Valkey write.
	Verifier ResultVerifier
	// SucceededTTL is how long a succeeded job is kept after it finished, so
	// a client polling it sees the outcome; the result outlives it. <= 0
	// keeps succeeded jobs.
	SucceededTTL time.Duration
	// FailedTTL is how long a failed job is kept for diagnosis; <= 0 keeps
	// failed jobs.
	FailedTTL time.Duration
	// Log is the logger; nil uses go-log's "steward-ai-operator".
	Log log.Logger
}

func isTerminal(phase v1alpha1.JobPhase) bool {
	return phase == v1alpha1.PhaseSucceeded || phase == v1alpha1.PhaseFailed
}

func (r *PolicyAIJobReconciler) maxAttempts() int {
	if r.MaxAttempts > 0 {
		return r.MaxAttempts
	}
	return DefaultMaxAttempts
}

func (r *PolicyAIJobReconciler) logger(ctx context.Context, job *v1alpha1.PolicyAIJob) log.Logger {
	l := r.Log
	if l == nil {
		l = log.NewLogger("steward-ai-operator")
	}
	return l.Ctx(ctx).With(log.F("job", job.Name), log.F("namespace", job.Namespace), log.F("operation", string(job.Spec.Operation)))
}

// retryBackoff grows linearly, 5s per attempt up to 30s; MaxAttempts already
// bounds the total.
func retryBackoff(attempts int) time.Duration {
	d := min(time.Duration(attempts)*5*time.Second, 30*time.Second)
	if d <= 0 {
		d = 5 * time.Second
	}
	return d
}

// permanent reports whether a dispatch error is a refusal no retry can fix.
func permanent(err error) bool {
	if _, ok := errors.AsType[*llm.MonthlyLimitError](err); ok {
		return true
	}
	return errors.Is(err, llm.ErrDisabled) || errors.Is(err, llm.ErrSettingsUnavailable)
}

// Reconcile implements reconcile.Reconciler.
func (r *PolicyAIJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var job v1alpha1.PolicyAIJob
	if err := r.Get(ctx, req.NamespacedName, &job); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	l := r.logger(ctx, &job)

	if isTerminal(job.Status.Phase) && job.Status.ObservedGeneration == job.Generation {
		return r.reconcileGC(ctx, &job)
	}

	attempt := job.Status.Attempts + 1
	if r.Dispatcher.NeedsModel(&job) {
		if err := r.moduleOn(ctx); err != nil {
			l.Warn("job not run: the AI module is off or its switch is unreadable",
				log.F("attempt", attempt), log.F("error", err.Error()))
			return r.finalize(ctx, &job, r.markFailed(ctx, &job, err))
		}
	}

	l.Info("job running", log.F("phase", string(v1alpha1.PhaseRunning)), log.F("attempt", attempt), log.F("max_attempts", r.maxAttempts()))
	if err := r.markRunning(ctx, &job); err != nil {
		return ctrl.Result{}, err
	}

	recordJobAttempt(ctx, string(job.Spec.Operation))
	start := nowFunc()
	result, err := r.Dispatcher.Dispatch(ctx, &job)
	if err != nil {
		l.Warn("job dispatch failed", log.F("attempt", attempt), log.F("duration_ms", nowFunc().Sub(start).Milliseconds()), log.F("error", err.Error()))
		if permanent(err) {
			job.Status.Attempts++
			return r.finalize(ctx, &job, r.markFailed(ctx, &job, err))
		}
		return r.handleAttemptFailure(ctx, &job, err)
	}
	l.Debug("job dispatched", log.F("attempt", attempt), log.F("duration_ms", nowFunc().Sub(start).Milliseconds()))

	resultKey := ResultKey(&job)
	if err := r.Results.WriteResult(ctx, resultKey, ResultEnvelope{Operation: string(job.Spec.Operation), Result: result}, ResultTTL); err != nil {
		l.Error(err, "job result not written to valkey", log.F("attempt", attempt))
		return r.handleAttemptFailure(ctx, &job, err)
	}

	if r.Durable != nil {
		resultJSON, err := json.Marshal(result)
		if err != nil {
			l.Error(err, "job result not encoded for postgres", log.F("attempt", attempt))
			return r.handleAttemptFailure(ctx, &job, err)
		}
		if err := r.Durable.Save(ctx, store.JobResult{
			JobID:       job.Name,
			Operation:   string(job.Spec.Operation),
			PolicyID:    job.Spec.PolicyID,
			VersionID:   job.Spec.VersionID,
			ActorUserID: job.Spec.ActorUserID,
			ResultJSON:  string(resultJSON),
		}); err != nil {
			l.Error(err, "job result not written to postgres", log.F("attempt", attempt))
			return r.handleAttemptFailure(ctx, &job, err)
		}
	}

	return r.finalize(ctx, &job, r.markSucceeded(ctx, &job, resultKey))
}

func (r *PolicyAIJobReconciler) moduleOn(ctx context.Context) error {
	if r.Enabled == nil {
		return ErrModuleOff
	}
	on, err := r.Enabled.Enabled(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrModuleSwitchUnreadable, err)
	}
	if !on {
		return ErrModuleOff
	}
	return nil
}

// finalize hands a job that just became terminal to the GC, which returns
// the RequeueAfter that eventually deletes it. A failed status write is
// returned as is.
func (r *PolicyAIJobReconciler) finalize(ctx context.Context, job *v1alpha1.PolicyAIJob, markErr error) (ctrl.Result, error) {
	if markErr != nil {
		return ctrl.Result{}, markErr
	}
	return r.reconcileGC(ctx, job)
}

// handleAttemptFailure counts one failed attempt and requeues, or fails the
// job once MaxAttempts is reached.
func (r *PolicyAIJobReconciler) handleAttemptFailure(ctx context.Context, job *v1alpha1.PolicyAIJob, cause error) (ctrl.Result, error) {
	l := r.logger(ctx, job)
	job.Status.Attempts++
	limit := r.maxAttempts()
	if job.Status.Attempts >= limit {
		l.Error(cause, "job failed: attempts exhausted", log.F("attempts", job.Status.Attempts), log.F("max_attempts", limit))
		return r.finalize(ctx, job, r.markFailed(ctx, job, fmt.Errorf("attempts exhausted (%d/%d): %w", job.Status.Attempts, limit, cause)))
	}
	job.Status.Error = cause.Error()
	if err := r.Status().Update(ctx, job); err != nil {
		return ctrl.Result{}, err
	}
	backoff := retryBackoff(job.Status.Attempts)
	l.Warn("job attempt failed; retrying", log.F("phase", string(job.Status.Phase)), log.F("attempts", job.Status.Attempts),
		log.F("max_attempts", limit), log.F("backoff", backoff.String()), log.F("error", cause.Error()))
	return ctrl.Result{RequeueAfter: backoff}, nil
}

func (r *PolicyAIJobReconciler) markRunning(ctx context.Context, job *v1alpha1.PolicyAIJob) error {
	if job.Status.Phase == v1alpha1.PhaseRunning && job.Status.ObservedGeneration == job.Generation {
		return nil
	}
	now := metav1.NewTime(nowFunc())
	job.Status.Phase = v1alpha1.PhaseRunning
	job.Status.StartedAt = &now
	job.Status.Error = ""
	job.Status.ObservedGeneration = job.Generation
	return r.Status().Update(ctx, job)
}

func (r *PolicyAIJobReconciler) markSucceeded(ctx context.Context, job *v1alpha1.PolicyAIJob, resultKey string) error {
	finished := metav1.NewTime(nowFunc())
	job.Status.Phase = v1alpha1.PhaseSucceeded
	job.Status.ResultRef = resultKey
	job.Status.Error = ""
	job.Status.FinishedAt = &finished
	job.Status.ObservedGeneration = job.Generation
	if err := r.Status().Update(ctx, job); err != nil {
		return err
	}
	r.logger(ctx, job).Info("job succeeded", log.F("phase", string(v1alpha1.PhaseSucceeded)), log.F("attempt", job.Status.Attempts+1),
		log.F("duration_ms", duration(job.Status.StartedAt, finished.Time).Milliseconds()), log.F("result_ref", resultKey))
	recordJobTerminal(ctx, string(job.Spec.Operation), "succeeded", job.Status.StartedAt, finished.Time)
	r.publishCompletion(ctx, job, v1alpha1.PhaseSucceeded, resultKey, "")
	return nil
}

func (r *PolicyAIJobReconciler) markFailed(ctx context.Context, job *v1alpha1.PolicyAIJob, cause error) error {
	finished := metav1.NewTime(nowFunc())
	job.Status.Phase = v1alpha1.PhaseFailed
	job.Status.Error = cause.Error()
	job.Status.FinishedAt = &finished
	job.Status.ObservedGeneration = job.Generation
	if err := r.Status().Update(ctx, job); err != nil {
		return errors.Join(cause, err)
	}
	r.logger(ctx, job).Warn("job failed", log.F("phase", string(v1alpha1.PhaseFailed)), log.F("attempts", job.Status.Attempts),
		log.F("duration_ms", duration(job.Status.StartedAt, finished.Time).Milliseconds()), log.F("error", cause.Error()))
	recordJobTerminal(ctx, string(job.Spec.Operation), "failed", job.Status.StartedAt, finished.Time)
	r.publishCompletion(ctx, job, v1alpha1.PhaseFailed, "", cause.Error())
	return nil
}

func duration(startedAt *metav1.Time, finished time.Time) time.Duration {
	if startedAt == nil {
		return 0
	}
	return finished.Sub(startedAt.Time)
}

// publishCompletion is best effort: the job's status is the record, and a
// client polling it still sees the outcome.
func (r *PolicyAIJobReconciler) publishCompletion(ctx context.Context, job *v1alpha1.PolicyAIJob, phase v1alpha1.JobPhase, resultRef, errMsg string) {
	ev := CompletionEvent{
		JobID:       job.Name,
		Operation:   string(job.Spec.Operation),
		ActorUserID: job.Spec.ActorUserID,
		PolicyID:    job.Spec.PolicyID,
		VersionID:   job.Spec.VersionID,
		CategoryID:  job.Spec.CategoryID,
		Phase:       string(phase),
		ResultRef:   resultRef,
		Error:       errMsg,
		FinishedAt:  nowFunc(),
	}
	if err := r.Events.PublishJobCompletion(ctx, ev); err != nil {
		r.logger(ctx, job).Warn("job completion event not published", log.F("phase", string(phase)), log.F("error", err.Error()))
	}
}

// reconcileGC deletes a terminal job once it is eligible, or requeues it for
// a later look. It is the only place a job is deleted.
//
// A succeeded job goes once it is older than SucceededTTL and its result is
// confirmed durable; until then deletion waits, so a result is never lost.
// A failed job is kept for FailedTTL so it can be diagnosed.
func (r *PolicyAIJobReconciler) reconcileGC(ctx context.Context, job *v1alpha1.PolicyAIJob) (ctrl.Result, error) {
	l := r.logger(ctx, job)
	op := string(job.Spec.Operation)

	if job.Status.FinishedAt == nil {
		return ctrl.Result{RequeueAfter: gcRecheckInterval}, nil
	}
	age := nowFunc().Sub(job.Status.FinishedAt.Time)

	switch job.Status.Phase {
	case v1alpha1.PhaseSucceeded:
		if r.SucceededTTL <= 0 {
			return ctrl.Result{}, nil
		}
		persisted, err := r.resultPersisted(ctx, job)
		if err != nil {
			l.Warn("job GC deferred: could not confirm the result is durable", log.F("error", err.Error()))
			recordJobGC(ctx, op, "succeeded", "deferred")
			return ctrl.Result{RequeueAfter: gcRecheckInterval}, nil
		}
		if !persisted {
			l.Info("job GC deferred: the result is not durable yet")
			recordJobGC(ctx, op, "succeeded", "deferred")
			return ctrl.Result{RequeueAfter: gcRecheckInterval}, nil
		}
		if age < r.SucceededTTL {
			recordJobGC(ctx, op, "succeeded", "retained")
			return ctrl.Result{RequeueAfter: r.SucceededTTL - age}, nil
		}
		l.Info("job GC: deleting a succeeded job", log.F("result_ref", job.Status.ResultRef), log.F("age", age.String()))
		return r.deleteJob(ctx, job, "succeeded")

	case v1alpha1.PhaseFailed:
		if r.FailedTTL <= 0 {
			l.Debug("job GC: keeping a failed job (no retention limit)")
			recordJobGC(ctx, op, "failed", "retained")
			return ctrl.Result{}, nil
		}
		if age < r.FailedTTL {
			l.Debug("job GC: keeping a failed job for diagnosis", log.F("age", age.String()), log.F("failed_ttl", r.FailedTTL.String()))
			recordJobGC(ctx, op, "failed", "retained")
			return ctrl.Result{RequeueAfter: r.FailedTTL - age}, nil
		}
		l.Info("job GC: deleting a failed job past its retention", log.F("age", age.String()))
		return r.deleteJob(ctx, job, "failed")

	default:
		return ctrl.Result{}, nil
	}
}

func (r *PolicyAIJobReconciler) resultPersisted(ctx context.Context, job *v1alpha1.PolicyAIJob) (bool, error) {
	if job.Status.ResultRef == "" {
		return false, nil
	}
	if r.Verifier == nil {
		return true, nil
	}
	return r.Verifier.ResultExists(ctx, job.Name)
}

func (r *PolicyAIJobReconciler) deleteJob(ctx context.Context, job *v1alpha1.PolicyAIJob, phase string) (ctrl.Result, error) {
	if err := r.Delete(ctx, job); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	recordJobGC(ctx, string(job.Spec.Operation), phase, "deleted")
	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler with mgr.
func (r *PolicyAIJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PolicyAIJob{}).
		Complete(r)
}
