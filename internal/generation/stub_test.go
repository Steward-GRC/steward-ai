// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"context"

	"github.com/Steward-GRC/steward-ai/internal/provider"
)

// stubLLMClient is a provider.Generator that returns a canned response and
// records the last request, so tests can assert on what was sent.
type stubLLMClient struct {
	response string
	lastReq  provider.Request
}

func (s *stubLLMClient) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	s.lastReq = req
	return provider.Response{Text: s.response}, nil
}

var _ provider.Generator = (*stubLLMClient)(nil)
