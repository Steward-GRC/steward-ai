// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/fixture"
)

func ask(f *fakes) error {
	_, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{Question: "q", Scope: scope(fixture.Workplace)})
	return err
}

func TestSearchAndAnswer_underQuota_allowed(t *testing.T) {
	f := newFakes()
	f.quota.used = 50 // exactly at the default limit: still allowed
	if err := ask(f); err != nil {
		t.Fatalf("expected the request allowed at used==limit, got %v", err)
	}
	if f.quota.calls != 1 || f.quota.lastUID != fixture.Erin {
		t.Fatalf("expected Erin counted exactly once, got %d calls for %q", f.quota.calls, f.quota.lastUID)
	}
}

func TestSearchAndAnswer_overQuota_blockedWithTypedError(t *testing.T) {
	f := newFakes()
	f.quota.used = 51
	err := ask(f)
	info := requireCode(t, err, errcodes.CodeQuotaExceeded, codes.FailedPrecondition)
	if info.Symbol != "AI_QUOTA_EXCEEDED" {
		t.Fatalf("expected reason AI_QUOTA_EXCEEDED, got %q", info.Symbol)
	}
	if info.Metadata["limit"] != fmt.Sprint(airules.QueryQuotaDefault) || info.Metadata["used"] != "51" {
		t.Fatalf("expected limit and used metadata, got %v", info.Metadata)
	}
	if info.Metadata["reset_at"] != "2026-10-06T00:00:00Z" {
		t.Fatalf("expected the next UTC midnight, got %q", info.Metadata["reset_at"])
	}
	if _, perr := time.Parse(time.RFC3339, info.Metadata["reset_at"]); perr != nil {
		t.Fatalf("reset_at is not RFC3339: %v", perr)
	}
	want := "You've reached your daily AI limit of 50. It resets at 2026-10-06T00:00:00Z."
	if got := status.Convert(err).Message(); got != want {
		t.Fatalf("expected the interpolated user-safe message, got %q", got)
	}
	if f.qa.calls != 0 {
		t.Fatal("no model work once over quota")
	}
}

// Nobody is exempt by role: a scope that reads every category is counted
// and blocked like anyone else's.
func TestCheckQuota_noRoleExemption(t *testing.T) {
	f := newFakes()
	f.quota.used = 9999
	_, err := f.server().SearchAndAnswer(actAsCtx(), &aiv1.SearchAndAnswerRequest{
		Question: "admin question", Scope: &aiv1.ReadScope{AllCategories: true, IncludeSensitive: true},
	})
	requireCode(t, err, errcodes.CodeQuotaExceeded, codes.FailedPrecondition)
	if f.quota.calls != 1 {
		t.Fatalf("expected the call counted, got %d", f.quota.calls)
	}
}

func TestCheckQuota_perUserOverrideBeatsDefault_blocksEarlier(t *testing.T) {
	f := newFakes()
	f.limits.limits[fixture.Erin] = 10
	f.quota.used = 11
	requireCode(t, ask(f), errcodes.CodeQuotaExceeded, codes.FailedPrecondition)

	g := newFakes()
	g.limits.limits[fixture.Bob] = 10
	g.quota.used = 11
	if err := ask(g); err != nil {
		t.Fatalf("a person on the default (50) is allowed at used=11, got %v", err)
	}
}

func TestCheckQuota_perUserUnlimitedOverride_neverBlocked(t *testing.T) {
	f := newFakes()
	f.limits.limits[fixture.Erin] = airules.UnlimitedQueryQuota
	f.quota.used = 100000
	if err := ask(f); err != nil {
		t.Fatalf("an unlimited person is never blocked, got %v", err)
	}
	if f.quota.calls != 0 {
		t.Fatalf("an unlimited person is never counted, got %d", f.quota.calls)
	}
}

func TestCheckQuota_unlimitedDefault_neverCounted(t *testing.T) {
	f := newFakes()
	d := f.deps()
	d.DefaultQueryLimit = airules.UnlimitedQueryQuota
	if _, err := New(d, WithClock(func() time.Time { return testNow })).SearchAndAnswer(erinCtx(),
		&aiv1.SearchAndAnswerRequest{Question: "q", Scope: scope(fixture.Workplace)}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.quota.calls != 0 {
		t.Fatal("an unlimited default counts nobody")
	}
}

func TestCheckQuota_counterFailure_failsOpen(t *testing.T) {
	f := newFakes()
	f.quota.err = fmt.Errorf("valkey down")
	if err := ask(f); err != nil {
		t.Fatalf("a counter failure must fail open, got %v", err)
	}
}

func TestCheckQuota_overrideReadFailure_usesDefault(t *testing.T) {
	f := newFakes()
	f.limits.getErr = fmt.Errorf("postgres down")
	f.quota.used = 51
	requireCode(t, ask(f), errcodes.CodeQuotaExceeded, codes.FailedPrecondition)
}

func TestAuthoringAssist_overQuota_blocked(t *testing.T) {
	f := newFakes()
	f.quota.used = 51
	_, err := f.server().AuthoringAssist(erinCtx(), &aiv1.AuthoringAssistRequest{
		SectionKey: "intro", Operation: aiv1.AssistOperation_ASSIST_OPERATION_DRAFT,
	})
	requireCode(t, err, errcodes.CodeQuotaExceeded, codes.FailedPrecondition)
}

func TestSubmitAIJob_overQuota_blockedAndNoJobCreated(t *testing.T) {
	f := newFakes()
	f.quota.used = 51
	_, err := f.server().SubmitAIJob(erinCtx(), &aiv1.SubmitAIJobRequest{Operation: aiv1.JobOperation_JOB_OPERATION_DRAFT})
	requireCode(t, err, errcodes.CodeQuotaExceeded, codes.FailedPrecondition)
	if len(f.jobs.created) != 0 {
		t.Fatalf("expected no job created when quota exceeded, got %d", len(f.jobs.created))
	}
}

func TestSetUserAiQueryLimit_setExplicit(t *testing.T) {
	f := newFakes()
	resp, err := f.server().SetUserAiQueryLimit(actAsCtx(), &aiv1.SetUserAiQueryLimitRequest{TargetUserId: fixture.Bob, Limit: 25})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.EffectiveLimit != 25 || resp.Unlimited || resp.IsDefault {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if f.limits.limits[fixture.Bob] != 25 {
		t.Fatalf("expected override persisted as 25, got %d", f.limits.limits[fixture.Bob])
	}
}

func TestSetUserAiQueryLimit_clearRevertsToDefault(t *testing.T) {
	f := newFakes()
	f.limits.limits[fixture.Bob] = 5
	resp, err := f.server().SetUserAiQueryLimit(actAsCtx(), &aiv1.SetUserAiQueryLimitRequest{TargetUserId: fixture.Bob, Limit: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsDefault || resp.EffectiveLimit != int32(airules.QueryQuotaDefault) {
		t.Fatalf("expected clear to default, got %+v", resp)
	}
	if _, ok := f.limits.limits[fixture.Bob]; ok {
		t.Fatal("expected the override deleted on clear")
	}
}

func TestSetUserAiQueryLimit_unlimited(t *testing.T) {
	f := newFakes()
	resp, err := f.server().SetUserAiQueryLimit(actAsCtx(), &aiv1.SetUserAiQueryLimitRequest{
		TargetUserId: fixture.Grace, Limit: airules.UnlimitedQueryQuota,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Unlimited {
		t.Fatalf("expected unlimited=true, got %+v", resp)
	}
	if f.limits.limits[fixture.Grace] != airules.UnlimitedQueryQuota {
		t.Fatalf("expected -1 persisted, got %d", f.limits.limits[fixture.Grace])
	}
}

func TestSetUserAiQueryLimit_validation(t *testing.T) {
	f := newFakes()
	_, err := f.server().SetUserAiQueryLimit(actAsCtx(), &aiv1.SetUserAiQueryLimitRequest{Limit: 10})
	requireCode(t, err, errcodes.CodeRequestInvalid, codes.InvalidArgument)
	_, err = f.server().SetUserAiQueryLimit(actAsCtx(), &aiv1.SetUserAiQueryLimitRequest{TargetUserId: fixture.Bob, Limit: -5})
	requireCode(t, err, errcodes.CodeRequestInvalid, codes.InvalidArgument)
}
