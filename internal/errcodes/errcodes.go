// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package errcodes holds the ai service's coded errors (band 3) and turns
// them into gRPC statuses through go-apperr.
package errcodes

import (
	"context"
	"sync"

	apperr "github.com/Bugs5382/go-apperr"
	"github.com/Bugs5382/go-apperr/apperrgrpc"
	log "github.com/Bugs5382/go-log"
)

// Domain is the ErrorInfo domain every ai error carries.
const Domain = "ai"

// The ai service's codes. 3001 to 3004 keep the original's numbers.
const (
	CodeInternal              = 3000
	CodeQuotaExceeded         = 3001
	CodeCategoryForbidden     = 3002
	CodeRetrievalUnavailable  = 3003
	CodeGenerationFailed      = 3004
	CodeDisabled              = 3005
	CodeDataNoticeRequired    = 3006
	CodeMonthlyLimitReached   = 3007
	CodeActorRequired         = 3008
	CodeRequestInvalid        = 3009
	CodeJobNotFound           = 3010
	CodeProviderNotConfigured = 3011
	CodeSettingsUnavailable   = 3012
)

// Entries returns the registry entries. go-apperr has no resource-exhausted
// category, so the two limits are failed preconditions: retrying doesn't help
// until the window resets.
func Entries() []apperr.Entry {
	return []apperr.Entry{
		{Code: CodeInternal, Symbol: "INTERNAL", Category: apperr.CategoryInternal,
			Title: "ai", Cause: "an uncoded failure inside the ai service"},
		{Code: CodeQuotaExceeded, Symbol: "AI_QUOTA_EXCEEDED", Category: apperr.CategoryFailedPrecondition,
			Title: "quota", Cause: "the person reached their daily query limit (limit, used and reset_at in the metadata)",
			UserSafe: true, Message: "You've reached your daily AI limit of {limit}. It resets at {reset_at}."},
		{Code: CodeCategoryForbidden, Symbol: "AI_CATEGORY_FORBIDDEN", Category: apperr.CategoryPermissionDenied,
			Title: "scope", Cause: "the request narrowed the search to a category its read scope doesn't hold (category_id in the metadata)",
			UserSafe: true, Message: "You don't have access to that category."},
		{Code: CodeRetrievalUnavailable, Symbol: "AI_RETRIEVAL_UNAVAILABLE", Category: apperr.CategoryUnavailable,
			Title: "retrieval", Cause: "embedding the question or the vector search failed (op in the metadata); the cause is in the debug log"},
		{Code: CodeGenerationFailed, Symbol: "AI_GENERATION_FAILED", Category: apperr.CategoryUnavailable,
			Title: "generation", Cause: "the provider call failed; the cause is in the debug log"},
		{Code: CodeDisabled, Symbol: "AI_DISABLED", Category: apperr.CategoryFailedPrecondition,
			Title: "module", Cause: "the AI module is off, so no AI call is accepted",
			UserSafe: true, Message: "The AI module is turned off."},
		{Code: CodeDataNoticeRequired, Symbol: "AI_DATA_NOTICE_REQUIRED", Category: apperr.CategoryFailedPrecondition,
			Title: "module", Cause: "the module can't be turned on before an administrator accepts the current data notice",
			UserSafe: true, Message: "Accept the AI data notice before turning the AI module on."},
		{Code: CodeMonthlyLimitReached, Symbol: "AI_MONTHLY_LIMIT_REACHED", Category: apperr.CategoryFailedPrecondition,
			Title: "usage", Cause: "the organisation's monthly request limit is reached (limit and reset_at in the metadata)",
			UserSafe: true, Message: "The organisation's monthly AI limit of {limit} requests is reached. It resets at {reset_at}."},
		{Code: CodeActorRequired, Symbol: "AI_ACTOR_REQUIRED", Category: apperr.CategoryUnauthenticated,
			Title: "request", Cause: "the call carries no go-grpc-actor actor, so there is nobody to answer or attribute it to"},
		{Code: CodeRequestInvalid, Symbol: "AI_REQUEST_INVALID", Category: apperr.CategoryInvalid,
			Title: "request", Cause: "a required field is missing or a value is out of range (field and reason in the metadata); the gateway validates first, so this is a client bug"},
		{Code: CodeJobNotFound, Symbol: "AI_JOB_NOT_FOUND", Category: apperr.CategoryNotFound,
			Title: "job", Cause: "no job has this id, or it was submitted by someone else",
			UserSafe: true, Message: "That AI job doesn't exist."},
		{Code: CodeProviderNotConfigured, Symbol: "AI_PROVIDER_NOT_CONFIGURED", Category: apperr.CategoryFailedPrecondition,
			Title: "provider", Cause: "no provider is chosen, or the chosen one needs a credential, endpoint or region that isn't set",
			UserSafe: true, Message: "No AI provider is set up yet."},
		{Code: CodeSettingsUnavailable, Symbol: "AI_SETTINGS_UNAVAILABLE", Category: apperr.CategoryUnavailable,
			Title: "settings", Cause: "the module's settings couldn't be read, so the call is refused rather than run with the module possibly off",
			UserSafe: true, Message: "AI is unavailable right now. Try again in a moment."},
	}
}

var (
	regOnce sync.Once
	reg     *apperr.Registry
)

// Registry returns the service registry. Coded errors are logged through
// go-log with the trace of the request they failed.
func Registry() *apperr.Registry {
	regOnce.Do(func() {
		r, err := apperr.NewRegistry(Entries(), apperr.WithService(3), apperr.WithCodeDigits(4),
			apperr.WithLogger(logSink{log.NewLogger("ai")}))
		if err != nil {
			panic(err)
		}
		reg = r
	})
	return reg
}

// Error turns err into the gRPC error a handler returns.
func Error(ctx context.Context, err error) error {
	return apperrgrpc.Error(ctx, Registry(), err, CodeInternal, Domain)
}

// New returns the gRPC error for code with the given metadata pairs, in
// key, value order.
func New(ctx context.Context, code int, kv ...string) error {
	pairs := make([]apperr.MetaPair, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		pairs = append(pairs, apperr.Meta(kv[i], kv[i+1]))
	}
	entry, _ := Registry().Describe(code)
	return Error(ctx, apperr.WithMeta(apperr.Coded(code, refusal(entry.Symbol)), pairs...))
}

// Invalid is AI_REQUEST_INVALID naming the field and why.
func Invalid(ctx context.Context, field, reason string) error {
	return New(ctx, CodeRequestInvalid, "field", field, "reason", reason)
}

// Doc is the Markdown body of docs/error-codes.md.
func Doc() string {
	return "# Error codes\n\nEvery coded gRPC error from the ai service carries an `ErrorInfo` with the symbol as\n" +
		"its reason, the domain `" + Domain + "` and the code in `codeNum`. Only user-safe messages reach\n" +
		"the caller; every other code is sent as `Code N: Internal Error`.\n\n" + Registry().Markdown()
}

type refusal string

func (r refusal) Error() string { return "ai: " + string(r) }

type logSink struct{ l log.Logger }

func (s logSink) LogCoded(ctx context.Context, code int, err error) {
	s.l.Ctx(ctx).Debug("coded error", log.F("code", code), log.F("error", err.Error()))
}
