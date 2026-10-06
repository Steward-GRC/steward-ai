// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	log "github.com/Bugs5382/go-log"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/aiconfig"
	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// OrgContextMaxChars bounds the organisation context, which is sent with
// every prompt.
const OrgContextMaxChars = 4000

// The gateway decides who may change the settings; this service checks no
// role. Every change is audited and logged, never with the credential.

// GetProviderStatus reports the cached provider status. It never calls the
// provider.
func (s *AiServiceServer) GetProviderStatus(_ context.Context, _ *aiv1.GetProviderStatusRequest) (*aiv1.GetProviderStatusResponse, error) {
	available, reason := s.d.LLM.ProviderStatus()
	return &aiv1.GetProviderStatusResponse{Available: available, Reason: reason}, nil
}

// GetAIEnabled reports the module switch.
func (s *AiServiceServer) GetAIEnabled(ctx context.Context, _ *aiv1.GetAIEnabledRequest) (*aiv1.GetAIEnabledResponse, error) {
	st, err := s.readSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &aiv1.GetAIEnabledResponse{Enabled: st.Enabled}, nil
}

// SetAIEnabled turns the module on or off. Turning it on needs the current
// data notice accepted; aiconfig checks it again against Postgres.
func (s *AiServiceServer) SetAIEnabled(ctx context.Context, req *aiv1.SetAIEnabledRequest) (*aiv1.SetAIEnabledResponse, error) {
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	l := s.log.Ctx(ctx).With(log.F("enabled", req.GetEnabled()))
	if req.GetEnabled() {
		st, err := s.readSettings(ctx)
		if err != nil {
			return nil, err
		}
		if !st.NoticeCurrent() {
			l.Warn("ai module not turned on: data notice not accepted")
			return nil, errcodes.New(ctx, errcodes.CodeDataNoticeRequired)
		}
	}
	if err := s.d.Settings.SetEnabled(ctx, req.GetEnabled()); err != nil {
		if errors.Is(err, aiconfig.ErrNoticeRequired) {
			l.Warn("ai module not turned on: data notice not accepted")
			return nil, errcodes.New(ctx, errcodes.CodeDataNoticeRequired)
		}
		return nil, s.writeFailed(ctx, err, "set_ai_enabled")
	}
	l.Warn("ai module switch changed")
	s.emit(ctx, AuditRecord{Operation: "set_ai_enabled", Actor: actor, Query: fmt.Sprintf("enabled=%t", req.GetEnabled())})
	return &aiv1.SetAIEnabledResponse{Enabled: req.GetEnabled()}, nil
}

// GetAIConfig reads the settings. The credential shows only as set, with its
// last four characters.
func (s *AiServiceServer) GetAIConfig(ctx context.Context, _ *aiv1.GetAIConfigRequest) (*aiv1.GetAIConfigResponse, error) {
	if _, err := s.requireActor(ctx); err != nil {
		return nil, err
	}
	cfg, err := s.currentConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &aiv1.GetAIConfigResponse{Config: cfg}, nil
}

// SetProviderConfig sets the provider and its non-secret settings. Jobs
// already submitted keep the model they were submitted with.
func (s *AiServiceServer) SetProviderConfig(ctx context.Context, req *aiv1.SetProviderConfigRequest) (*aiv1.SetProviderConfigResponse, error) {
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	kind, ok := protoToKind[req.GetProvider()]
	if !ok {
		return nil, errcodes.Invalid(ctx, "provider", "unknown provider")
	}
	p := store.ProviderSettings{
		Provider: string(kind), Model: req.GetModel(), BaseURL: req.GetBaseUrl(),
		Region: req.GetRegion(), Deployment: req.GetDeployment(),
	}
	if err := s.d.Settings.SetProviderSettings(ctx, p); err != nil {
		if errors.Is(err, aiconfig.ErrUnknownProvider) {
			return nil, errcodes.Invalid(ctx, "provider", "unknown provider")
		}
		return nil, s.writeFailed(ctx, err, "set_provider_config")
	}
	s.log.Ctx(ctx).Warn("ai provider settings changed", log.F("provider", p.Provider), log.F("model", p.Model))
	s.emit(ctx, AuditRecord{
		Operation: "set_provider_config", Actor: actor,
		Query: fmt.Sprintf("provider=%s model=%s base_url=%s region=%s deployment=%s",
			p.Provider, p.Model, p.BaseURL, p.Region, p.Deployment),
	})
	cfg, err := s.currentConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &aiv1.SetProviderConfigResponse{Config: cfg}, nil
}

// SetProviderCredential stores the provider credential, encrypted; empty
// clears it. The value is never returned, logged or audited.
func (s *AiServiceServer) SetProviderCredential(ctx context.Context, req *aiv1.SetProviderCredentialRequest) (*aiv1.SetProviderCredentialResponse, error) {
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	cred := req.GetCredential()
	if err := s.d.Settings.SetCredential(ctx, cred); err != nil {
		return nil, s.writeFailed(ctx, err, "set_provider_credential")
	}
	set := cred != ""
	s.log.Ctx(ctx).Warn("ai provider credential changed", log.F("credential_set", set))
	s.emit(ctx, AuditRecord{Operation: "set_provider_credential", Actor: actor, Query: fmt.Sprintf("credential_set=%t", set)})
	return &aiv1.SetProviderCredentialResponse{CredentialSet: set, CredentialLast4: store.Last4(cred)}, nil
}

// TestProvider makes one small call to the configured provider. While the
// module is off it is refused like any other intake call.
func (s *AiServiceServer) TestProvider(ctx context.Context, _ *aiv1.TestProviderRequest) (*aiv1.TestProviderResponse, error) {
	const rpc = "test_provider"
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.enabledSettings(ctx, rpc)
	if err != nil {
		return nil, err
	}
	if err := s.checkMonthlyLimit(ctx, st, rpc); err != nil {
		return nil, err
	}
	res, err := s.d.LLM.Test(ctx)
	if err != nil {
		return nil, s.providerError(ctx, err)
	}
	ok := res.Reason == ""
	s.log.Ctx(ctx).Info("ai provider tested", log.F("ok", ok), log.F("reason", res.Reason),
		log.F("latency_ms", res.Latency.Milliseconds()))
	s.emit(ctx, AuditRecord{Operation: rpc, Actor: actor, Query: fmt.Sprintf("ok=%t reason=%s", ok, res.Reason)})
	return &aiv1.TestProviderResponse{Ok: ok, Reason: res.Reason, LatencyMs: res.Latency.Milliseconds()}, nil
}

// AcceptDataNotice records the acceptance of the current data notice. The
// acceptance names the person who made it: during act-as, the
// administrator.
func (s *AiServiceServer) AcceptDataNotice(ctx context.Context, req *aiv1.AcceptDataNoticeRequest) (*aiv1.AcceptDataNoticeResponse, error) {
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetNoticeVersion() != airules.DataNoticeVersion {
		return nil, errcodes.Invalid(ctx, "notice_version", "not the current data notice")
	}
	if err := s.d.Settings.AcceptNotice(ctx, req.GetNoticeVersion(), actor.RealUser(), s.now()); err != nil {
		if errors.Is(err, aiconfig.ErrNoticeVersion) {
			return nil, errcodes.Invalid(ctx, "notice_version", "not the current data notice")
		}
		return nil, s.writeFailed(ctx, err, "accept_data_notice")
	}
	s.log.Ctx(ctx).Warn("ai data notice accepted", log.F("version", req.GetNoticeVersion()))
	s.emit(ctx, AuditRecord{Operation: "accept_data_notice", Actor: actor, Query: "version=" + req.GetNoticeVersion()})
	st, err := s.readSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &aiv1.AcceptDataNoticeResponse{DataNotice: dataNotice(st.Notice)}, nil
}

// SetMonthlyLimit sets the organisation's monthly request limit; zero
// removes it.
func (s *AiServiceServer) SetMonthlyLimit(ctx context.Context, req *aiv1.SetMonthlyLimitRequest) (*aiv1.SetMonthlyLimitResponse, error) {
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetLimit() < 0 {
		return nil, errcodes.Invalid(ctx, "limit", "must be zero or more")
	}
	if err := s.d.Settings.SetMonthlyLimit(ctx, req.GetLimit()); err != nil {
		if errors.Is(err, aiconfig.ErrInvalidMonthlyLimit) {
			return nil, errcodes.Invalid(ctx, "limit", "must be zero or more")
		}
		return nil, s.writeFailed(ctx, err, "set_monthly_limit")
	}
	s.log.Ctx(ctx).Warn("ai monthly limit changed", log.F("limit", req.GetLimit()))
	s.emit(ctx, AuditRecord{Operation: "set_monthly_limit", Actor: actor, Query: fmt.Sprintf("limit=%d", req.GetLimit())})
	return &aiv1.SetMonthlyLimitResponse{Limit: req.GetLimit()}, nil
}

// GetUsage reads the usage meter for the current month (UTC).
func (s *AiServiceServer) GetUsage(ctx context.Context, _ *aiv1.GetUsageRequest) (*aiv1.GetUsageResponse, error) {
	if _, err := s.requireActor(ctx); err != nil {
		return nil, err
	}
	st, err := s.readSettings(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	u, err := s.d.Usage.Month(ctx, now)
	if err != nil {
		s.log.Ctx(ctx).Error(err, "ai usage unreadable")
		return nil, errcodes.New(ctx, errcodes.CodeSettingsUnavailable)
	}
	return &aiv1.GetUsageResponse{
		Month:        store.MonthStart(now).Format("2006-01"),
		Requests:     u.Requests,
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		MonthlyLimit: st.MonthlyLimit,
		ResetsAt:     nextMonth(now).Format(time.RFC3339),
	}, nil
}

// SetOrgContext sets the organisation context sent with every prompt; empty
// sends none.
func (s *AiServiceServer) SetOrgContext(ctx context.Context, req *aiv1.SetOrgContextRequest) (*aiv1.SetOrgContextResponse, error) {
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	oc := req.GetOrgContext()
	if n := utf8.RuneCountInString(oc); n > OrgContextMaxChars {
		return nil, errcodes.Invalid(ctx, "org_context", fmt.Sprintf("longer than %d characters", OrgContextMaxChars))
	}
	if err := s.d.Settings.SetOrgContext(ctx, oc); err != nil {
		return nil, s.writeFailed(ctx, err, "set_org_context")
	}
	s.log.Ctx(ctx).Warn("ai organisation context changed", log.F("chars", utf8.RuneCountInString(oc)))
	s.emit(ctx, AuditRecord{Operation: "set_org_context", Actor: actor, Query: oc})
	return &aiv1.SetOrgContextResponse{OrgContext: oc}, nil
}

// SetAIRetrievalConfig sets the retrieval candidate count. Zero or less
// clears the override; a value above airules.RetrievalTopKCeiling is
// clamped, so a typo can't make retrieval unbounded. The response is the
// count now in effect.
func (s *AiServiceServer) SetAIRetrievalConfig(ctx context.Context, req *aiv1.SetAIRetrievalConfigRequest) (*aiv1.SetAIRetrievalConfigResponse, error) {
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	toStore := min(max(int(req.GetTopK()), 0), airules.RetrievalTopKCeiling)
	if err := s.d.Settings.SetTopK(ctx, toStore); err != nil {
		return nil, s.writeFailed(ctx, err, "set_ai_retrieval_config")
	}
	effective := effectiveTopK(toStore)
	s.log.Ctx(ctx).Warn("ai retrieval top_k changed", log.F("top_k", effective))
	s.emit(ctx, AuditRecord{Operation: "set_ai_retrieval_config", Actor: actor, Query: fmt.Sprintf("top_k=%d", effective)})
	return &aiv1.SetAIRetrievalConfigResponse{TopK: toInt32(effective)}, nil
}

// SetUserAiQueryLimit sets one person's daily limit: 1 or more sets it, 0
// clears the override and airules.UnlimitedQueryQuota lifts it. The response
// is the state now in effect.
func (s *AiServiceServer) SetUserAiQueryLimit(ctx context.Context, req *aiv1.SetUserAiQueryLimitRequest) (*aiv1.SetUserAiQueryLimitResponse, error) {
	actor, err := s.requireActor(ctx)
	if err != nil {
		return nil, err
	}
	target := req.GetTargetUserId()
	if target == "" {
		return nil, errcodes.Invalid(ctx, "target_user_id", "required")
	}
	limit := int(req.GetLimit())
	if limit < airules.UnlimitedQueryQuota {
		return nil, errcodes.Invalid(ctx, "limit", "must be -1 (unlimited), 0 (clear) or 1 or more")
	}

	resp := &aiv1.SetUserAiQueryLimitResponse{}
	if limit == 0 {
		if err := s.d.UserLimits.Delete(ctx, target); err != nil {
			return nil, s.writeFailed(ctx, err, "set_user_ai_query_limit")
		}
		resp.IsDefault = true
		resp.Unlimited = s.d.DefaultQueryLimit == airules.UnlimitedQueryQuota
		if !resp.Unlimited {
			resp.EffectiveLimit = toInt32(s.d.DefaultQueryLimit)
		}
	} else {
		if err := s.d.UserLimits.Set(ctx, target, limit); err != nil {
			return nil, s.writeFailed(ctx, err, "set_user_ai_query_limit")
		}
		resp.Unlimited = limit == airules.UnlimitedQueryQuota
		if !resp.Unlimited {
			resp.EffectiveLimit = toInt32(limit)
		}
	}
	s.log.Ctx(ctx).Warn("ai per-person query limit changed", log.F("target", target), log.F("limit", limit))
	s.emit(ctx, AuditRecord{Operation: "set_user_ai_query_limit", Actor: actor, Query: fmt.Sprintf("target=%s limit=%d", target, limit)})
	return resp, nil
}

func (s *AiServiceServer) writeFailed(ctx context.Context, err error, op string) error {
	s.log.Ctx(ctx).Error(err, "ai settings write failed", log.F("op", op))
	return errcodes.Error(ctx, err)
}

// currentConfig reads the settings as the AIConfig message.
func (s *AiServiceServer) currentConfig(ctx context.Context) (*aiv1.AIConfig, error) {
	st, err := s.readSettings(ctx)
	if err != nil {
		return nil, err
	}
	model := st.Provider.Model
	if model == "" {
		if model, err = s.d.LLM.DefaultModel(ctx); err != nil {
			return nil, s.providerError(ctx, err)
		}
	}
	return &aiv1.AIConfig{
		Enabled:            st.Enabled,
		Provider:           kindToProto[provider.Kind(st.Provider.Provider)],
		Model:              model,
		BaseUrl:            st.Provider.BaseURL,
		Region:             st.Provider.Region,
		Deployment:         st.Provider.Deployment,
		CredentialSet:      st.CredentialSet,
		CredentialLast4:    st.CredentialLast4,
		EmbeddingsProvider: s.d.Embeddings.Provider,
		EmbeddingsModel:    s.d.Embeddings.Model,
		TopK:               toInt32(effectiveTopK(st.TopK)),
		MonthlyLimit:       st.MonthlyLimit,
		DataNotice:         dataNotice(st.Notice),
		OrgContext:         st.OrgContext,
	}, nil
}

func dataNotice(n store.DataNotice) *aiv1.DataNotice {
	out := &aiv1.DataNotice{CurrentVersion: airules.DataNoticeVersion, AcceptedVersion: n.Version, AcceptedBy: n.AcceptedBy}
	if n.AcceptedAt != nil {
		out.AcceptedAt = n.AcceptedAt.UTC().Format(time.RFC3339)
	}
	return out
}

var kindToProto = map[provider.Kind]aiv1.Provider{
	provider.Anthropic:        aiv1.Provider_PROVIDER_ANTHROPIC,
	provider.OpenAI:           aiv1.Provider_PROVIDER_OPENAI,
	provider.AzureOpenAI:      aiv1.Provider_PROVIDER_AZURE_OPENAI,
	provider.Gemini:           aiv1.Provider_PROVIDER_GEMINI,
	provider.Bedrock:          aiv1.Provider_PROVIDER_BEDROCK,
	provider.OpenAICompatible: aiv1.Provider_PROVIDER_OPENAI_COMPATIBLE,
}

var protoToKind = func() map[aiv1.Provider]provider.Kind {
	m := make(map[aiv1.Provider]provider.Kind, len(kindToProto))
	for k, p := range kindToProto {
		m[p] = k
	}
	return m
}()
