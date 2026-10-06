// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

//go:build live

package anthropic

import (
	"context"
	"os"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/provider"
)

// TestLive calls the real API. It builds only with -tags live and runs only
// when STEWARD_AI_LIVE_ANTHROPIC_KEY is set; CI does neither.
func TestLive(t *testing.T) {
	key := os.Getenv("STEWARD_AI_LIVE_ANTHROPIC_KEY")
	if key == "" {
		t.Skip("STEWARD_AI_LIVE_ANTHROPIC_KEY not set")
	}
	c, err := New(provider.Settings{Kind: provider.Anthropic, Credential: key})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Complete(context.Background(), provider.Request{
		System:    "Answer in one word.",
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "What colour is a clear daytime sky?"}},
		MaxTokens: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text == "" || resp.InputTokens == 0 || resp.OutputTokens == 0 {
		t.Fatalf("unexpected response %+v", resp)
	}
}
