// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package llm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/aiconfig"
	"github.com/Steward-GRC/steward-ai/internal/llm"
)

type countingEmbedder struct{ calls int }

func (e *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	return make([][]float32, len(texts)), nil
}

type switchOnly struct {
	enabled bool
	err     error
}

func (s switchOnly) Settings(context.Context) (aiconfig.Settings, error) {
	return aiconfig.Settings{Enabled: s.enabled}, s.err
}

// A remote embeddings provider is a call out, so it follows the module
// switch; the built-in server doesn't.
func TestGateEmbedder(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		remote  bool
		sw      switchOnly
		wantErr error
	}{
		{"built-in while off", false, switchOnly{enabled: false}, nil},
		{"remote while on", true, switchOnly{enabled: true}, nil},
		{"remote while off", true, switchOnly{enabled: false}, llm.ErrDisabled},
		{"remote with unreadable settings", true, switchOnly{err: errors.New("down")}, llm.ErrSettingsUnavailable},
	}
	for _, tc := range cases {
		inner := &countingEmbedder{}
		_, err := llm.GateEmbedder(inner, tc.sw, tc.remote).Embed(ctx, []string{"a"})
		if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.wantErr)
		}
		if wantCalls := map[bool]int{true: 0, false: 1}[tc.wantErr != nil]; inner.calls != wantCalls {
			t.Fatalf("%s: embedder called %d times, want %d", tc.name, inner.calls, wantCalls)
		}
	}
}
