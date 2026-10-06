// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/aiconfig"
	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/fixture"
	"github.com/Steward-GRC/steward-ai/internal/llm"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// intakeCalls are the four calls that can reach a provider.
func intakeCalls(svc *AiServiceServer) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"SearchAndAnswer": func(ctx context.Context) error {
			_, err := svc.SearchAndAnswer(ctx, &aiv1.SearchAndAnswerRequest{Question: "q", Scope: scope(fixture.Workplace)})
			return err
		},
		"AuthoringAssist": func(ctx context.Context) error {
			_, err := svc.AuthoringAssist(ctx, &aiv1.AuthoringAssistRequest{SectionKey: "intro", Operation: aiv1.AssistOperation_ASSIST_OPERATION_DRAFT})
			return err
		},
		"SubmitAIJob": func(ctx context.Context) error {
			_, err := svc.SubmitAIJob(ctx, &aiv1.SubmitAIJobRequest{Operation: aiv1.JobOperation_JOB_OPERATION_DRAFT})
			return err
		},
		"TestProvider": func(ctx context.Context) error {
			_, err := svc.TestProvider(ctx, &aiv1.TestProviderRequest{})
			return err
		},
	}
}

func TestIntake_disabledRefusesEveryCall(t *testing.T) {
	f := newFakes()
	f.settings.st.Enabled = false
	for name, call := range intakeCalls(f.server()) {
		t.Run(name, func(t *testing.T) {
			requireCode(t, call(erinCtx()), errcodes.CodeDisabled, codes.FailedPrecondition)
		})
	}
	if f.qa.calls+f.llm.tested+len(f.jobs.created) != 0 || f.assist.got.SectionKey != "" {
		t.Fatal("nothing may reach a provider or create a job while the module is off")
	}
	if f.quota.calls != 0 {
		t.Fatal("a refused call is not counted")
	}
}

// Off is off: a settings read failure refuses rather than runs with the
// module possibly off.
func TestIntake_settingsReadFailureRefuses(t *testing.T) {
	f := newFakes()
	f.settings.readErr = errBoom
	for name, call := range intakeCalls(f.server()) {
		t.Run(name, func(t *testing.T) {
			requireCode(t, call(erinCtx()), errcodes.CodeSettingsUnavailable, codes.Unavailable)
		})
	}
	if f.qa.calls+f.llm.tested+len(f.jobs.created) != 0 {
		t.Fatal("nothing may reach a provider while the settings are unreadable")
	}
}

func TestIntake_monthlyLimitReachedRefuses(t *testing.T) {
	f := newFakes()
	f.settings.st.MonthlyLimit = 100
	f.usage.u = store.Usage{Requests: 100}
	for name, call := range intakeCalls(f.server()) {
		t.Run(name, func(t *testing.T) {
			info := requireCode(t, call(erinCtx()), errcodes.CodeMonthlyLimitReached, codes.FailedPrecondition)
			if info.Metadata["limit"] != "100" || info.Metadata["reset_at"] != "2026-11-01T00:00:00Z" {
				t.Fatalf("expected limit and the first of next month, got %v", info.Metadata)
			}
		})
	}
	if f.qa.calls+f.llm.tested+len(f.jobs.created) != 0 {
		t.Fatal("nothing may reach a provider once the month's limit is reached")
	}
}

func TestIntake_underMonthlyLimitAllowed(t *testing.T) {
	f := newFakes()
	f.settings.st.MonthlyLimit = 100
	f.usage.u = store.Usage{Requests: 99}
	for name, call := range intakeCalls(f.server()) {
		t.Run(name, func(t *testing.T) {
			if err := call(erinCtx()); err != nil {
				t.Fatalf("expected the call allowed under the limit, got %v", err)
			}
		})
	}
}

// The usage meter failing closes the limit, as the llm client does.
func TestIntake_usageReadFailureRefusesWhenLimited(t *testing.T) {
	f := newFakes()
	f.settings.st.MonthlyLimit = 100
	f.usage.err = errBoom
	err := intakeCalls(f.server())["SearchAndAnswer"](erinCtx())
	requireCode(t, err, errcodes.CodeSettingsUnavailable, codes.Unavailable)
}

// The llm client's own refusals keep their codes when generation returns
// them.
func TestProviderErrorsMapToTheirCodes(t *testing.T) {
	resetAt := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		err  error
		code int
		grpc codes.Code
	}{
		{"disabled", fmt.Errorf("qa: complete: %w", llm.ErrDisabled), errcodes.CodeDisabled, codes.FailedPrecondition},
		{"settings", fmt.Errorf("qa: complete: %w", llm.ErrSettingsUnavailable), errcodes.CodeSettingsUnavailable, codes.Unavailable},
		{"monthly", fmt.Errorf("qa: complete: %w", &llm.MonthlyLimitError{Limit: 10, ResetAt: resetAt}), errcodes.CodeMonthlyLimitReached, codes.FailedPrecondition},
		{"not configured", fmt.Errorf("qa: complete: %w", provider.ErrNotConfigured), errcodes.CodeProviderNotConfigured, codes.FailedPrecondition},
		{"provider", errors.New("qa: complete: upstream 500 secret-detail"), errcodes.CodeGenerationFailed, codes.Unavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakes()
			f.retr.results = []store.SearchResult{{Chunk: store.Chunk{PolicyID: "p"}}}
			f.qa.err = c.err
			_, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{Question: "q", Scope: scope(fixture.Workplace)})
			info := requireCode(t, err, c.code, c.grpc)
			if strings.Contains(status.Convert(err).Message(), "secret-detail") {
				t.Fatal("a provider cause must never reach the caller")
			}
			if c.name == "monthly" && (info.Metadata["limit"] != "10" || info.Metadata["reset_at"] != "2026-11-01T00:00:00Z") {
				t.Fatalf("expected the client's limit and reset, got %v", info.Metadata)
			}

			f.assist.err = c.err
			_, err = f.server().AuthoringAssist(erinCtx(), &aiv1.AuthoringAssistRequest{SectionKey: "s", Operation: aiv1.AssistOperation_ASSIST_OPERATION_DRAFT})
			requireCode(t, err, c.code, c.grpc)
		})
	}
}

func TestEveryCallButStatusAndSwitchNeedsAnActor(t *testing.T) {
	open := map[string]bool{"GetProviderStatus": true, "GetAIEnabled": true}
	svc := newFakes().server()
	v := reflect.ValueOf(svc)
	for _, m := range aiv1.AiService_ServiceDesc.Methods {
		t.Run(m.MethodName, func(t *testing.T) {
			method := v.MethodByName(m.MethodName)
			req := reflect.New(method.Type().In(1).Elem())
			out := method.Call([]reflect.Value{reflect.ValueOf(context.Background()), req})
			err, _ := out[1].Interface().(error)
			if open[m.MethodName] {
				if err != nil {
					t.Fatalf("%s must answer without an actor, got %v", m.MethodName, err)
				}
				return
			}
			requireCode(t, err, errcodes.CodeActorRequired, codes.Unauthenticated)
		})
	}
}

func TestSetAIEnabled_requiresTheCurrentNotice(t *testing.T) {
	for name, notice := range map[string]store.DataNotice{
		"none":     {},
		"previous": {Version: "2025-01-01", AcceptedBy: fixture.Alice},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakes()
			f.settings.st.Enabled = false
			f.settings.st.Notice = notice
			_, err := f.server().SetAIEnabled(actAsCtx(), &aiv1.SetAIEnabledRequest{Enabled: true})
			requireCode(t, err, errcodes.CodeDataNoticeRequired, codes.FailedPrecondition)
			if f.settings.setEnabled != nil {
				t.Fatal("the switch must not be written")
			}
			if len(f.audit.recs) != 0 {
				t.Fatal("a refused change is not audited as made")
			}
		})
	}
}

// aiconfig re-checks the notice against Postgres; its refusal maps the same.
func TestSetAIEnabled_storeNoticeRefusalMaps(t *testing.T) {
	f := newFakes()
	f.settings.enabledErr = aiconfig.ErrNoticeRequired
	_, err := f.server().SetAIEnabled(actAsCtx(), &aiv1.SetAIEnabledRequest{Enabled: true})
	requireCode(t, err, errcodes.CodeDataNoticeRequired, codes.FailedPrecondition)
}

// Turning the module off never needs the notice.
func TestSetAIEnabled_offNeedsNoNotice(t *testing.T) {
	f := newFakes()
	f.settings.st.Notice = store.DataNotice{}
	if _, err := f.server().SetAIEnabled(actAsCtx(), &aiv1.SetAIEnabledRequest{Enabled: false}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAcceptDataNotice_currentVersion(t *testing.T) {
	f := newFakes()
	f.settings.st.Notice = store.DataNotice{}
	resp, err := f.server().AcceptDataNotice(actAsCtx(), &aiv1.AcceptDataNoticeRequest{NoticeVersion: airules.DataNoticeVersion})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	a := f.settings.accepted
	if a == nil || a.version != airules.DataNoticeVersion || !a.at.Equal(testNow) {
		t.Fatalf("expected the current notice accepted now, got %+v", a)
	}
	if a.actor != fixture.Alice {
		t.Fatalf("during act-as the acceptance names the administrator, got %q", a.actor)
	}
	n := resp.GetDataNotice()
	if n.CurrentVersion != airules.DataNoticeVersion || n.AcceptedVersion != airules.DataNoticeVersion || n.AcceptedAt != "2026-10-05T14:30:00Z" {
		t.Fatalf("unexpected notice: %+v", n)
	}
	if ops := f.audit.ops(); len(ops) != 1 || ops[0] != "accept_data_notice" {
		t.Fatalf("expected an accept_data_notice audit, got %v", ops)
	}
}

func TestAcceptDataNotice_otherVersionInvalid(t *testing.T) {
	f := newFakes()
	_, err := f.server().AcceptDataNotice(actAsCtx(), &aiv1.AcceptDataNoticeRequest{NoticeVersion: "2025-01-01"})
	info := requireCode(t, err, errcodes.CodeRequestInvalid, codes.InvalidArgument)
	if info.Metadata["field"] != "notice_version" {
		t.Fatalf("expected field=notice_version, got %v", info.Metadata)
	}
	if f.settings.accepted != nil {
		t.Fatal("nothing may be recorded")
	}
}

// Another person's job reads as not found, and an act-as administrator
// reads the jobs of the person acted as.
func TestGetAIJob_onlyTheSubmitterSeesIt(t *testing.T) {
	f := newFakes()
	f.jobs.jobs = map[string]*v1alpha1.PolicyAIJob{
		"aijob-bob":  {Spec: v1alpha1.PolicyAIJobSpec{Operation: v1alpha1.OperationDraft, ActorUserID: fixture.Bob}},
		"aijob-erin": erinJob("aijob-erin", v1alpha1.OperationDraft, v1alpha1.PolicyAIJobStatus{Phase: v1alpha1.PhaseRunning}),
	}
	svc := f.server()
	_, err := svc.GetAIJob(erinCtx(), &aiv1.GetAIJobRequest{JobId: "aijob-bob"})
	requireCode(t, err, errcodes.CodeJobNotFound, codes.NotFound)
	if _, err := svc.GetAIJob(actAsCtx(), &aiv1.GetAIJobRequest{JobId: "aijob-erin"}); err != nil {
		t.Fatalf("expected the act-as read of the subject's job, got %v", err)
	}
}

func TestSubmitAIJob_actAsKeepsTheSubjectAsOwner(t *testing.T) {
	f := newFakes()
	if _, err := f.server().SubmitAIJob(actAsCtx(), &aiv1.SubmitAIJobRequest{Operation: aiv1.JobOperation_JOB_OPERATION_REVIEW}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	spec := f.jobs.created[0]
	if spec.ActorUserID != fixture.Erin || spec.ImpersonatorUserID != fixture.Alice {
		t.Fatalf("expected owner Erin, impersonator Alice, got %+v", spec)
	}
	if f.quota.lastUID != fixture.Erin {
		t.Fatalf("the quota counts against the person acted as, got %q", f.quota.lastUID)
	}
	rec := f.audit.recs[0]
	if rec.Actor.Subject != fixture.Erin || rec.Actor.Impersonator != fixture.Alice {
		t.Fatalf("the audit record carries both, got %+v", rec.Actor)
	}
}

func TestSubmitAIJob_defaultModelFailureRefuses(t *testing.T) {
	f := newFakes()
	f.llm.modelErr = fmt.Errorf("x: %w", llm.ErrSettingsUnavailable)
	_, err := f.server().SubmitAIJob(erinCtx(), &aiv1.SubmitAIJobRequest{Operation: aiv1.JobOperation_JOB_OPERATION_DRAFT})
	requireCode(t, err, errcodes.CodeSettingsUnavailable, codes.Unavailable)
	if len(f.jobs.created) != 0 {
		t.Fatal("no job without a model snapshot")
	}
}

func TestGetAIEnabled_readFailureRefuses(t *testing.T) {
	f := newFakes()
	f.settings.readErr = errBoom
	_, err := f.server().GetAIEnabled(context.Background(), &aiv1.GetAIEnabledRequest{})
	requireCode(t, err, errcodes.CodeSettingsUnavailable, codes.Unavailable)
}

func TestSetProviderConfig_storesAndReturnsTheConfig(t *testing.T) {
	f := newFakes()
	resp, err := f.server().SetProviderConfig(actAsCtx(), &aiv1.SetProviderConfigRequest{
		Provider: aiv1.Provider_PROVIDER_BEDROCK, Model: "model-d", Region: "eu-west-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := f.settings.setProvider
	if p == nil || p.Provider != "bedrock" || p.Model != "model-d" || p.Region != "eu-west-1" {
		t.Fatalf("expected the bedrock settings stored, got %+v", p)
	}
	c := resp.GetConfig()
	if c.Provider != aiv1.Provider_PROVIDER_BEDROCK || c.Model != "model-d" || c.Region != "eu-west-1" {
		t.Fatalf("unexpected config: %+v", c)
	}
	if ops := f.audit.ops(); len(ops) != 1 || ops[0] != "set_provider_config" {
		t.Fatalf("expected a set_provider_config audit, got %v", ops)
	}
}

func TestSetProviderConfig_everyProviderRoundTrips(t *testing.T) {
	for _, k := range provider.Kinds {
		p, ok := kindToProto[k]
		if !ok {
			t.Fatalf("provider kind %q has no proto value", k)
		}
		if protoToKind[p] != k {
			t.Fatalf("provider %v does not map back to %q", p, k)
		}
	}
	if len(kindToProto) != len(aiv1.Provider_name)-1 {
		t.Fatalf("expected every Provider value but UNSPECIFIED mapped, got %d", len(kindToProto))
	}
}

func TestSetProviderConfig_unknownProviderInvalid(t *testing.T) {
	f := newFakes()
	_, err := f.server().SetProviderConfig(actAsCtx(), &aiv1.SetProviderConfigRequest{Provider: aiv1.Provider_PROVIDER_UNSPECIFIED})
	info := requireCode(t, err, errcodes.CodeRequestInvalid, codes.InvalidArgument)
	if info.Metadata["field"] != "provider" || f.settings.setProvider != nil {
		t.Fatalf("expected field=provider and nothing stored, got %v", info.Metadata)
	}
}

func TestSetProviderCredential_storesAndNeverEchoes(t *testing.T) {
	const cred = "test-key-1"
	f := newFakes()
	svc := f.server()
	resp, err := svc.SetProviderCredential(actAsCtx(), &aiv1.SetProviderCredentialRequest{Credential: cred})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.CredentialSet || resp.CredentialLast4 != "ey-1" {
		t.Fatalf("expected set with last4 ey-1, got %+v", resp)
	}
	if f.settings.setCredential == nil || *f.settings.setCredential != cred {
		t.Fatal("expected the credential to reach the store")
	}
	if strings.Contains(fmt.Sprint(f.audit.recs), cred) {
		t.Fatal("the credential must never be audited")
	}
	cfg, err := svc.GetAIConfig(actAsCtx(), &aiv1.GetAIConfigRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(cfg.String(), cred) || !cfg.GetConfig().CredentialSet {
		t.Fatalf("GetAIConfig shows only that a credential is set, got %v", cfg)
	}

	resp, err = svc.SetProviderCredential(actAsCtx(), &aiv1.SetProviderCredentialRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.CredentialSet || resp.CredentialLast4 != "" || *f.settings.setCredential != "" {
		t.Fatalf("an empty credential clears it, got %+v", resp)
	}
}

func TestTestProvider_reportsTheOutcome(t *testing.T) {
	f := newFakes()
	f.llm.test = llm.TestResult{Latency: 420 * time.Millisecond} // scrub:allow=fqdn
	resp, err := f.server().TestProvider(actAsCtx(), &aiv1.TestProviderRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Ok || resp.Reason != "" || resp.LatencyMs != 420 {
		t.Fatalf("unexpected result: %+v", resp)
	}

	f.llm.test = llm.TestResult{Latency: time.Second, Reason: provider.ReasonAuthFailed} // scrub:allow=fqdn
	resp, err = f.server().TestProvider(actAsCtx(), &aiv1.TestProviderRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Ok || resp.Reason != provider.ReasonAuthFailed {
		t.Fatalf("expected a failed test with its reason, got %+v", resp)
	}
}

func TestTestProvider_clientRefusalMaps(t *testing.T) {
	f := newFakes()
	f.llm.testErr = llm.ErrDisabled
	_, err := f.server().TestProvider(actAsCtx(), &aiv1.TestProviderRequest{})
	requireCode(t, err, errcodes.CodeDisabled, codes.FailedPrecondition)
}

func TestSetMonthlyLimit(t *testing.T) {
	f := newFakes()
	resp, err := f.server().SetMonthlyLimit(actAsCtx(), &aiv1.SetMonthlyLimitRequest{Limit: 5000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Limit != 5000 || f.settings.st.MonthlyLimit != 5000 {
		t.Fatalf("expected 5000 stored and echoed, got %d/%d", resp.Limit, f.settings.st.MonthlyLimit)
	}
	_, err = f.server().SetMonthlyLimit(actAsCtx(), &aiv1.SetMonthlyLimitRequest{Limit: -1})
	requireCode(t, err, errcodes.CodeRequestInvalid, codes.InvalidArgument)
	if f.settings.st.MonthlyLimit != 5000 {
		t.Fatal("a negative limit is not stored")
	}
}

func TestGetUsage(t *testing.T) {
	f := newFakes()
	f.settings.st.MonthlyLimit = 1000
	f.usage.u = store.Usage{Requests: 12, InputTokens: 3400, OutputTokens: 560}
	resp, err := f.server().GetUsage(actAsCtx(), &aiv1.GetUsageRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Month != "2026-10" || resp.Requests != 12 || resp.InputTokens != 3400 || resp.OutputTokens != 560 ||
		resp.MonthlyLimit != 1000 || resp.ResetsAt != "2026-11-01T00:00:00Z" {
		t.Fatalf("unexpected usage: %+v", resp)
	}
}

func TestGetUsage_meterFailureRefuses(t *testing.T) {
	f := newFakes()
	f.usage.err = errBoom
	_, err := f.server().GetUsage(actAsCtx(), &aiv1.GetUsageRequest{})
	requireCode(t, err, errcodes.CodeSettingsUnavailable, codes.Unavailable)
}

func TestSetOrgContext(t *testing.T) {
	f := newFakes()
	resp, err := f.server().SetOrgContext(actAsCtx(), &aiv1.SetOrgContextRequest{OrgContext: "Example Organisation runs three offices."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.OrgContext != "Example Organisation runs three offices." || f.settings.st.OrgContext != resp.OrgContext {
		t.Fatalf("expected the context stored and echoed, got %q", resp.OrgContext)
	}

	atCap := strings.Repeat("é", OrgContextMaxChars)
	if _, err := f.server().SetOrgContext(actAsCtx(), &aiv1.SetOrgContextRequest{OrgContext: atCap}); err != nil {
		t.Fatalf("the cap counts characters, not bytes: %v", err)
	}
	_, err = f.server().SetOrgContext(actAsCtx(), &aiv1.SetOrgContextRequest{OrgContext: atCap + "x"})
	info := requireCode(t, err, errcodes.CodeRequestInvalid, codes.InvalidArgument)
	if info.Metadata["field"] != "org_context" {
		t.Fatalf("expected field=org_context, got %v", info.Metadata)
	}
}

func TestSettingsWritesAreAudited(t *testing.T) {
	f := newFakes()
	svc := f.server()
	ctx := actAsCtx()
	calls := []func() error{
		func() error { _, err := svc.SetAIEnabled(ctx, &aiv1.SetAIEnabledRequest{Enabled: true}); return err },
		func() error {
			_, err := svc.SetProviderConfig(ctx, &aiv1.SetProviderConfigRequest{Provider: aiv1.Provider_PROVIDER_ANTHROPIC})
			return err
		},
		func() error {
			_, err := svc.SetProviderCredential(ctx, &aiv1.SetProviderCredentialRequest{Credential: "test-key-2"})
			return err
		},
		func() error {
			_, err := svc.AcceptDataNotice(ctx, &aiv1.AcceptDataNoticeRequest{NoticeVersion: airules.DataNoticeVersion})
			return err
		},
		func() error { _, err := svc.SetMonthlyLimit(ctx, &aiv1.SetMonthlyLimitRequest{Limit: 1}); return err },
		func() error {
			_, err := svc.SetOrgContext(ctx, &aiv1.SetOrgContextRequest{OrgContext: "x"})
			return err
		},
		func() error {
			_, err := svc.SetAIRetrievalConfig(ctx, &aiv1.SetAIRetrievalConfigRequest{TopK: 5})
			return err
		},
		func() error {
			_, err := svc.SetUserAiQueryLimit(ctx, &aiv1.SetUserAiQueryLimitRequest{TargetUserId: fixture.Bob, Limit: 3})
			return err
		},
	}
	for _, c := range calls {
		if err := c(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	want := []string{"set_ai_enabled", "set_provider_config", "set_provider_credential", "accept_data_notice",
		"set_monthly_limit", "set_org_context", "set_ai_retrieval_config", "set_user_ai_query_limit"}
	if got := f.audit.ops(); !reflect.DeepEqual(got, want) {
		t.Fatalf("audited ops: got %v, want %v", got, want)
	}
	for _, r := range f.audit.recs {
		if r.Actor.Impersonator != fixture.Alice {
			t.Fatalf("%s: expected the act-as administrator on the record, got %+v", r.Operation, r.Actor)
		}
	}
}

func TestSettingsWriteFailureIsInternal(t *testing.T) {
	f := newFakes()
	f.settings.writeErr = errBoom
	_, err := f.server().SetOrgContext(actAsCtx(), &aiv1.SetOrgContextRequest{OrgContext: "x"})
	requireCode(t, err, errcodes.CodeInternal, codes.Internal)
	if len(f.audit.recs) != 0 {
		t.Fatal("a failed write is not audited as made")
	}
}
