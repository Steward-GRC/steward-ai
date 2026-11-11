// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/fixture"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

func getMyUsageLimit(t *testing.T, f *fakes) *aiv1.GetMyUsageLimitResponse {
	t.Helper()
	resp, err := f.server().GetMyUsageLimit(erinCtx(), &aiv1.GetMyUsageLimitRequest{})
	require.NoError(t, err)
	return resp
}

func TestGetMyUsageLimit_underBothLimits(t *testing.T) {
	f := newFakes()
	f.quota.used = 10
	f.settings.st.MonthlyLimit = 1000
	f.usage.u = store.Usage{Requests: 12}

	resp := getMyUsageLimit(t, f)

	require.EqualValues(t, airules.QueryQuotaDefault, resp.DailyLimit)
	require.EqualValues(t, 10, resp.DailyUsed)
	require.Equal(t, "2026-10-06T00:00:00Z", resp.DailyResetsAt)
	require.Equal(t, "2026-11-01T00:00:00Z", resp.MonthlyResetsAt)
	require.False(t, resp.DailyLimitReached)
	require.False(t, resp.MonthlyLimitReached)
	require.False(t, resp.LimitReached)
	require.Zero(t, f.quota.calls, "reading the limit never counts a query")
}

func TestGetMyUsageLimit_dailyLimitReached(t *testing.T) {
	f := newFakes()
	f.quota.used = int64(airules.QueryQuotaDefault) // the next query would be refused

	resp := getMyUsageLimit(t, f)

	require.True(t, resp.DailyLimitReached)
	require.False(t, resp.MonthlyLimitReached)
	require.True(t, resp.LimitReached)
}

func TestGetMyUsageLimit_oneBelowTheDailyLimitIsNotReached(t *testing.T) {
	f := newFakes()
	f.quota.used = int64(airules.QueryQuotaDefault) - 1

	resp := getMyUsageLimit(t, f)

	require.False(t, resp.DailyLimitReached)
	require.False(t, resp.LimitReached)
}

func TestGetMyUsageLimit_dailyLimitClearsWhenTheCountResets(t *testing.T) {
	f := newFakes()
	f.quota.used = int64(airules.QueryQuotaDefault)
	require.True(t, getMyUsageLimit(t, f).LimitReached)

	f.quota.used = 0 // the next UTC day: the counter key has expired
	require.False(t, getMyUsageLimit(t, f).LimitReached)
}

func TestGetMyUsageLimit_monthlyLimitReached(t *testing.T) {
	f := newFakes()
	f.settings.st.MonthlyLimit = 1000
	f.usage.u = store.Usage{Requests: 1000}

	resp := getMyUsageLimit(t, f)

	require.True(t, resp.MonthlyLimitReached)
	require.False(t, resp.DailyLimitReached)
	require.True(t, resp.LimitReached)
}

func TestGetMyUsageLimit_noMonthlyLimitIsNeverReached(t *testing.T) {
	f := newFakes()
	f.settings.st.MonthlyLimit = 0
	f.usage.u = store.Usage{Requests: 1_000_000}

	resp := getMyUsageLimit(t, f)

	require.False(t, resp.MonthlyLimitReached)
	require.False(t, resp.LimitReached)
}

func TestGetMyUsageLimit_usesThePersonsOverride(t *testing.T) {
	f := newFakes()
	f.limits.limits[fixture.Erin] = 5
	f.quota.used = 5

	resp := getMyUsageLimit(t, f)

	require.EqualValues(t, 5, resp.DailyLimit)
	require.True(t, resp.DailyLimitReached)
}

func TestGetMyUsageLimit_unlimitedOverrideIsNeverReached(t *testing.T) {
	f := newFakes()
	f.limits.limits[fixture.Erin] = airules.UnlimitedQueryQuota
	f.quota.used = 100000

	resp := getMyUsageLimit(t, f)

	require.Zero(t, resp.DailyLimit, "zero means unlimited")
	require.False(t, resp.DailyLimitReached)
	require.False(t, resp.LimitReached)
}

func TestGetMyUsageLimit_unlimitedStillReportsAReachedMonthlyLimit(t *testing.T) {
	f := newFakes()
	f.limits.limits[fixture.Erin] = airules.UnlimitedQueryQuota
	f.settings.st.MonthlyLimit = 10
	f.usage.u = store.Usage{Requests: 10}

	require.True(t, getMyUsageLimit(t, f).LimitReached)
}

func TestGetMyUsageLimit_readsOnlyTheActor(t *testing.T) {
	f := newFakes()
	f.quota.usedBy = map[string]int64{
		fixture.Erin:  3,
		fixture.Alice: int64(airules.QueryQuotaDefault) + 10, // someone else, over their limit
	}

	resp := getMyUsageLimit(t, f)

	require.EqualValues(t, 3, resp.DailyUsed)
	require.False(t, resp.LimitReached, "another person's usage never reaches the caller")
	require.Equal(t, []string{fixture.Erin}, f.quota.peeks)
}

func TestGetMyUsageLimit_counterFailureFailsOpen(t *testing.T) {
	f := newFakes()
	f.quota.err = errBoom

	resp := getMyUsageLimit(t, f)

	require.False(t, resp.DailyLimitReached, "the intake gate fails open on a counter error, so does the read")
	require.Zero(t, resp.DailyUsed)
}

func TestGetMyUsageLimit_overrideReadFailureUsesTheDefault(t *testing.T) {
	f := newFakes()
	f.limits.getErr = errBoom

	resp := getMyUsageLimit(t, f)

	require.EqualValues(t, airules.QueryQuotaDefault, resp.DailyLimit)
}

func TestGetMyUsageLimit_meterFailureRefuses(t *testing.T) {
	f := newFakes()
	f.settings.st.MonthlyLimit = 1000
	f.usage.err = errBoom

	_, err := f.server().GetMyUsageLimit(erinCtx(), &aiv1.GetMyUsageLimitRequest{})

	requireCode(t, err, errcodes.CodeSettingsUnavailable, codes.Unavailable)
}

func TestGetMyUsageLimit_settingsFailureRefuses(t *testing.T) {
	f := newFakes()
	f.settings.readErr = errBoom

	_, err := f.server().GetMyUsageLimit(erinCtx(), &aiv1.GetMyUsageLimitRequest{})

	requireCode(t, err, errcodes.CodeSettingsUnavailable, codes.Unavailable)
}

func TestGetMyUsageLimit_requiresAnActor(t *testing.T) {
	f := newFakes()

	_, err := f.server().GetMyUsageLimit(context.Background(), &aiv1.GetMyUsageLimitRequest{})

	requireCode(t, err, errcodes.CodeActorRequired, codes.Unauthenticated)
}
