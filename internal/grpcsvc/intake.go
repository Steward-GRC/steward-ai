// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	grpcactor "github.com/Bugs5382/go-grpc-actor"
	log "github.com/Bugs5382/go-log"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/aiconfig"
	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/llm"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/quota"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// requireActor returns the person the gateway forwarded.
func (s *AiServiceServer) requireActor(ctx context.Context) (grpcactor.Actor, error) {
	a, err := grpcactor.Require(ctx, errcodes.CodeActorRequired)
	if err != nil {
		s.log.Ctx(ctx).Warn("ai call refused: no actor")
		return grpcactor.Actor{}, errcodes.Error(ctx, err)
	}
	return a, nil
}

// readSettings refuses the call when the settings can't be read: off is off,
// and an unreadable switch may be off.
func (s *AiServiceServer) readSettings(ctx context.Context) (aiconfig.Settings, error) {
	st, err := s.d.Settings.Settings(ctx)
	if err != nil {
		s.log.Ctx(ctx).Error(err, "ai settings unreadable; call refused")
		return aiconfig.Settings{}, errcodes.New(ctx, errcodes.CodeSettingsUnavailable)
	}
	return st, nil
}

// enabledSettings is the first intake check of every call that can reach a
// provider: the settings, and the module switch.
func (s *AiServiceServer) enabledSettings(ctx context.Context, rpc string) (aiconfig.Settings, error) {
	st, err := s.readSettings(ctx)
	if err != nil {
		return aiconfig.Settings{}, err
	}
	if !st.Enabled {
		s.log.Ctx(ctx).Info("ai intake refused: module off", log.F("rpc", rpc))
		return aiconfig.Settings{}, errcodes.New(ctx, errcodes.CodeDisabled)
	}
	return st, nil
}

// checkCategory refuses narrowing a search to a category outside the scope.
func (s *AiServiceServer) checkCategory(ctx context.Context, categoryID string, scope *aiv1.ReadScope) error {
	if categoryID == "" || scope.GetAllCategories() || slices.Contains(scope.GetCategoryIds(), categoryID) {
		return nil
	}
	s.log.Ctx(ctx).Warn("ai category read scope denied", log.F("category_id", categoryID))
	return errcodes.New(ctx, errcodes.CodeCategoryForbidden, "category_id", categoryID)
}

// checkQuota counts one query against the person's daily limit. Nobody is
// exempt by role; an override of airules.UnlimitedQueryQuota is never
// counted. A counter failure fails open.
func (s *AiServiceServer) checkQuota(ctx context.Context, userID, rpc string) error {
	l := s.log.Ctx(ctx).With(log.F("rpc", rpc))
	limit, unlimited := s.effectiveQueryLimit(ctx, userID)
	if unlimited {
		recordQuotaDecision(ctx, rpc, quotaExempt)
		return nil
	}
	now := s.now()
	used, err := s.d.Quota.Consume(ctx, userID, now)
	if err != nil {
		l.Warn("ai query quota counter failed; failing open", log.F("error", err.Error()))
		recordQuotaDecision(ctx, rpc, quotaAllowed)
		return nil
	}
	if used > int64(limit) {
		resetAt := quota.ResetAt(now)
		l.Warn("ai query quota exceeded", log.F("limit", limit), log.F("used", used), log.F("reset_at", resetAt))
		recordQuotaDecision(ctx, rpc, quotaBlocked)
		return errcodes.New(ctx, errcodes.CodeQuotaExceeded,
			"limit", strconv.Itoa(limit),
			"used", strconv.FormatInt(used, 10),
			"reset_at", resetAt.UTC().Format(time.RFC3339))
	}
	recordQuotaDecision(ctx, rpc, quotaAllowed)
	return nil
}

// effectiveQueryLimit is the person's override, else the default. An
// override read failure degrades to the default.
func (s *AiServiceServer) effectiveQueryLimit(ctx context.Context, userID string) (limit int, unlimited bool) {
	override, found, err := s.d.UserLimits.Get(ctx, userID)
	if err != nil {
		s.log.Ctx(ctx).Warn("user query limit read failed; using the default", log.F("error", err.Error()))
		override, found = 0, false
	}
	return airules.EffectiveQueryQuota(override, found, s.d.DefaultQueryLimit)
}

// checkMonthlyLimit refuses once the month's provider requests reach the
// organisation's limit. It fails closed, as the llm client does: the limit
// caps spend.
func (s *AiServiceServer) checkMonthlyLimit(ctx context.Context, st aiconfig.Settings, rpc string) error {
	if st.MonthlyLimit <= 0 {
		return nil
	}
	now := s.now()
	u, err := s.d.Usage.Month(ctx, now)
	if err != nil {
		s.log.Ctx(ctx).Error(err, "ai usage unreadable; call refused", log.F("rpc", rpc))
		return errcodes.New(ctx, errcodes.CodeSettingsUnavailable)
	}
	if u.Requests >= st.MonthlyLimit {
		resetAt := nextMonth(now)
		s.log.Ctx(ctx).Info("ai intake refused: monthly limit reached", log.F("rpc", rpc),
			log.F("limit", st.MonthlyLimit), log.F("requests", u.Requests))
		return monthlyLimitError(ctx, st.MonthlyLimit, resetAt)
	}
	return nil
}

func nextMonth(now time.Time) time.Time { return store.MonthStart(now).AddDate(0, 1, 0) }

func monthlyLimitError(ctx context.Context, limit int64, resetAt time.Time) error {
	return errcodes.New(ctx, errcodes.CodeMonthlyLimitReached,
		"limit", strconv.FormatInt(limit, 10), "reset_at", resetAt.UTC().Format(time.RFC3339))
}

// providerError maps a failure from the provider path. The refusals the llm
// client makes keep their own codes; anything else is a generation failure,
// with the cause in the debug log only.
func (s *AiServiceServer) providerError(ctx context.Context, err error) error {
	if mle, ok := errors.AsType[*llm.MonthlyLimitError](err); ok {
		return monthlyLimitError(ctx, mle.Limit, mle.ResetAt)
	}
	switch {
	case errors.Is(err, llm.ErrDisabled):
		return errcodes.New(ctx, errcodes.CodeDisabled)
	case errors.Is(err, llm.ErrSettingsUnavailable):
		return errcodes.New(ctx, errcodes.CodeSettingsUnavailable)
	case errors.Is(err, provider.ErrNotConfigured):
		return errcodes.New(ctx, errcodes.CodeProviderNotConfigured)
	}
	s.log.Ctx(ctx).Debug("ai generation failed", log.F("error", err.Error()))
	return errcodes.New(ctx, errcodes.CodeGenerationFailed)
}

// retrievalUnavailable maps an embedding or vector-search failure; op names
// the step so the code can be matched to the debug log line.
func (s *AiServiceServer) retrievalUnavailable(ctx context.Context, op string, cause error) error {
	s.log.Ctx(ctx).Debug("ai retrieval failed", log.F("op", op), log.F("error", cause.Error()))
	return errcodes.New(ctx, errcodes.CodeRetrievalUnavailable, "op", op)
}

// filter is the read scope as the store's access filter.
func filter(scope *aiv1.ReadScope) store.AccessFilter {
	return store.AccessFilter{
		CategoryIDs:      scope.GetCategoryIds(),
		IncludeSensitive: scope.GetIncludeSensitive(),
		AllCategories:    scope.GetAllCategories(),
	}
}
