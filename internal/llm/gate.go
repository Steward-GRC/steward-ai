// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"fmt"

	"github.com/Steward-GRC/steward-ai/internal/aiconfig"
	"github.com/Steward-GRC/steward-ai/internal/provider"
)

// Switch reads the module switch; aiconfig.Store satisfies it.
type Switch interface {
	Settings(ctx context.Context) (aiconfig.Settings, error)
}

// GateEmbedder returns emb unchanged for the built-in embeddings server.
// For a remote one (remote true), every call first checks the module switch:
// sending document text out is a call out, so it stops while the module is
// off, and a settings read failure refuses rather than guess.
func GateEmbedder(emb provider.Embedder, sw Switch, remote bool) provider.Embedder {
	if !remote {
		return emb
	}
	return gatedEmbedder{emb: emb, sw: sw}
}

type gatedEmbedder struct {
	emb provider.Embedder
	sw  Switch
}

func (g gatedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	s, err := g.sw.Settings(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
	}
	if !s.Enabled {
		return nil, ErrDisabled
	}
	return g.emb.Embed(ctx, texts)
}
