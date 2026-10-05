// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package errcodes_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Bugs5382/go-apperr/apperrgrpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Steward-GRC/steward-ai/internal/errcodes"
)

// The original's four codes keep their numbers and symbols, so the gateway's
// handling of them carries over.
func TestOriginalCodesKeepTheirNumbers(t *testing.T) {
	cases := map[int]string{
		3001: "AI_QUOTA_EXCEEDED",
		3002: "AI_CATEGORY_FORBIDDEN",
		3003: "AI_RETRIEVAL_UNAVAILABLE",
		3004: "AI_GENERATION_FAILED",
	}
	for code, symbol := range cases {
		e, ok := errcodes.Registry().Describe(code)
		require.True(t, ok, symbol)
		require.Equal(t, symbol, e.Symbol)
	}
}

func TestQuotaExceededRoundTrip(t *testing.T) {
	st := status.Convert(errcodes.New(context.Background(), errcodes.CodeQuotaExceeded,
		"limit", "50", "used", "51", "reset_at", "2026-10-06T00:00:00Z"))
	require.Equal(t, codes.FailedPrecondition, st.Code())
	require.Equal(t, "You've reached your daily AI limit of 50. It resets at 2026-10-06T00:00:00Z.", st.Message())
	info, ok := apperrgrpc.FromStatus(st)
	require.True(t, ok)
	require.Equal(t, "AI_QUOTA_EXCEEDED", info.Symbol)
	require.Equal(t, 3001, info.Code)
	require.Equal(t, "ai", info.Domain)
	require.Equal(t, "51", info.Metadata["used"])
}

func TestRefusalCodesKeepTheirStatusAndSymbol(t *testing.T) {
	cases := []struct {
		code   int
		symbol string
		grpc   codes.Code
	}{
		{errcodes.CodeQuotaExceeded, "AI_QUOTA_EXCEEDED", codes.FailedPrecondition},
		{errcodes.CodeCategoryForbidden, "AI_CATEGORY_FORBIDDEN", codes.PermissionDenied},
		{errcodes.CodeRetrievalUnavailable, "AI_RETRIEVAL_UNAVAILABLE", codes.Unavailable},
		{errcodes.CodeGenerationFailed, "AI_GENERATION_FAILED", codes.Unavailable},
		{errcodes.CodeDisabled, "AI_DISABLED", codes.FailedPrecondition},
		{errcodes.CodeDataNoticeRequired, "AI_DATA_NOTICE_REQUIRED", codes.FailedPrecondition},
		{errcodes.CodeMonthlyLimitReached, "AI_MONTHLY_LIMIT_REACHED", codes.FailedPrecondition},
		{errcodes.CodeActorRequired, "AI_ACTOR_REQUIRED", codes.Unauthenticated},
		{errcodes.CodeRequestInvalid, "AI_REQUEST_INVALID", codes.InvalidArgument},
		{errcodes.CodeJobNotFound, "AI_JOB_NOT_FOUND", codes.NotFound},
		{errcodes.CodeProviderNotConfigured, "AI_PROVIDER_NOT_CONFIGURED", codes.FailedPrecondition},
		{errcodes.CodeSettingsUnavailable, "AI_SETTINGS_UNAVAILABLE", codes.Unavailable},
	}
	for _, c := range cases {
		st := status.Convert(errcodes.New(context.Background(), c.code))
		require.Equal(t, c.grpc, st.Code(), c.symbol)
		info, ok := apperrgrpc.FromStatus(st)
		require.True(t, ok, c.symbol)
		require.Equal(t, c.symbol, info.Symbol)
		require.Equal(t, c.code, info.Code)
		require.Equal(t, errcodes.Domain, info.Domain)
	}
}

// A server fault's cause never reaches the caller.
func TestFaultsAreNotUserSafe(t *testing.T) {
	for _, code := range []int{errcodes.CodeRetrievalUnavailable, errcodes.CodeGenerationFailed, errcodes.CodeRequestInvalid} {
		st := status.Convert(errcodes.New(context.Background(), code, "op", "embed_question"))
		require.Contains(t, st.Message(), "Internal Error")
	}
}

func TestUserSafeMessages(t *testing.T) {
	cases := map[int]string{
		errcodes.CodeDisabled:              "The AI module is turned off.",
		errcodes.CodeDataNoticeRequired:    "Accept the AI data notice before turning the AI module on.",
		errcodes.CodeCategoryForbidden:     "You don't have access to that category.",
		errcodes.CodeJobNotFound:           "That AI job doesn't exist.",
		errcodes.CodeProviderNotConfigured: "No AI provider is set up yet.",
	}
	for code, want := range cases {
		require.Equal(t, want, status.Convert(errcodes.New(context.Background(), code)).Message())
	}
	st := status.Convert(errcodes.New(context.Background(), errcodes.CodeMonthlyLimitReached,
		"limit", "1000", "reset_at", "2026-11-01T00:00:00Z"))
	require.Equal(t, "The organisation's monthly AI limit of 1000 requests is reached. It resets at 2026-11-01T00:00:00Z.", st.Message())
}

func TestUncodedErrorsFallBackToInternal(t *testing.T) {
	info, _ := apperrgrpc.FromError(errcodes.Error(context.Background(), errors.New("boom")))
	require.Equal(t, errcodes.CodeInternal, info.Code)
	require.Equal(t, "INTERNAL", info.Symbol)
}

func TestRegistryBandAndDomain(t *testing.T) {
	require.NotEmpty(t, errcodes.Entries())
	for _, e := range errcodes.Entries() {
		require.GreaterOrEqual(t, e.Code, 3000, "code %d must be in band 3", e.Code)
		require.Less(t, e.Code, 4000, "code %d must be in band 3", e.Code)
	}
}

// docs/error-codes.md is generated from the registry; refresh it with
// UPDATE_DOCS=1 go test ./internal/errcodes.
func TestErrorCodesDocIsCurrent(t *testing.T) {
	const path = "../../docs/error-codes.md"
	want := errcodes.Doc()
	if os.Getenv("UPDATE_DOCS") == "1" {
		require.NoError(t, os.WriteFile(path, []byte(want), 0o600))
	}
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, want, string(got), "docs/error-codes.md is stale; run UPDATE_DOCS=1 go test ./internal/errcodes")
}
