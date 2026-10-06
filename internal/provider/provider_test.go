// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package provider_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/provider"
)

func TestReason(t *testing.T) {
	cases := map[error]string{
		nil:                                    "",
		fmt.Errorf("x: %w", provider.ErrAuth):  provider.ReasonAuthFailed,
		provider.ErrNotConfigured:              provider.ReasonNotConfigured,
		errors.New("connection reset by peer"): provider.ReasonProviderError,
	}
	for err, want := range cases {
		if got := provider.Reason(err); got != want {
			t.Errorf("Reason(%v) = %q, want %q", err, got, want)
		}
	}
}
