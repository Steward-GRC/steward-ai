// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package build picks the provider adapter for a configuration.
package build

import (
	"fmt"
	"net/http"

	"github.com/Bugs5382/go-log"

	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/anthropic"
	"github.com/Steward-GRC/steward-ai/internal/provider/bedrock"
	"github.com/Steward-GRC/steward-ai/internal/provider/gemini"
	"github.com/Steward-GRC/steward-ai/internal/provider/openai"
	"github.com/Steward-GRC/steward-ai/internal/provider/stub"
	"github.com/Steward-GRC/steward-ai/internal/provider/tei"
)

// The embeddings backends NewEmbedder accepts.
const (
	EmbedTEI    = "tei"
	EmbedOpenAI = "openai"
	EmbedStub   = "stub"
)

// Option adjusts the adapter New builds.
type Option func(*options)

type options struct {
	httpClient *http.Client
	logger     log.Logger
}

// WithHTTPClient sets the HTTP client the adapter uses.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.httpClient = c } }

// WithLogger sets the adapter's logger.
func WithLogger(l log.Logger) Option { return func(o *options) { o.logger = l } }

// New builds the generative adapter for s.Kind. An empty or unknown kind, or
// settings the adapter can't run with, is provider.ErrNotConfigured.
func New(s provider.Settings, opts ...Option) (provider.Generator, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	switch s.Kind {
	case provider.Anthropic:
		var ao []anthropic.Option
		if o.httpClient != nil {
			ao = append(ao, anthropic.WithHTTPClient(o.httpClient))
		}
		if o.logger != nil {
			ao = append(ao, anthropic.WithLogger(o.logger))
		}
		return anthropic.New(s, ao...)
	case provider.OpenAI, provider.AzureOpenAI, provider.OpenAICompatible:
		var oo []openai.Option
		if o.httpClient != nil {
			oo = append(oo, openai.WithHTTPClient(o.httpClient))
		}
		if o.logger != nil {
			oo = append(oo, openai.WithLogger(o.logger))
		}
		return openai.New(s, oo...)
	case provider.Gemini:
		var gopts []gemini.Option
		if o.httpClient != nil {
			gopts = append(gopts, gemini.WithHTTPClient(o.httpClient))
		}
		if o.logger != nil {
			gopts = append(gopts, gemini.WithLogger(o.logger))
		}
		return gemini.New(s, gopts...)
	case provider.Bedrock:
		var bo []bedrock.Option
		if o.httpClient != nil {
			bo = append(bo, bedrock.WithHTTPClient(o.httpClient))
		}
		if o.logger != nil {
			bo = append(bo, bedrock.WithLogger(o.logger))
		}
		return bedrock.New(s, bo...)
	case "":
		return nil, fmt.Errorf("provider: no kind set: %w", provider.ErrNotConfigured)
	default:
		return nil, fmt.Errorf("provider: unknown kind %q: %w", s.Kind, provider.ErrNotConfigured)
	}
}

// NewEmbedder builds the embeddings backend kind. "tei" posts to endpoint,
// the server's full /embed URL, sending credential as Bearer when set.
// "openai" is any OpenAI or OpenAI-compatible embeddings endpoint: endpoint
// is its base URL (empty for OpenAI itself), model is required. "stub" is the
// local stand-in and takes no settings.
func NewEmbedder(kind, endpoint, model, credential string) (provider.Embedder, error) {
	switch kind {
	case EmbedTEI:
		if endpoint == "" {
			return nil, fmt.Errorf("tei: endpoint is required: %w", provider.ErrNotConfigured)
		}
		var opts []tei.Option
		if credential != "" {
			opts = append(opts, tei.WithAPIKey(credential))
		}
		return tei.New(endpoint, provider.Dimensions, opts...), nil
	case EmbedOpenAI:
		return openai.NewEmbedder(endpoint, model, credential)
	case EmbedStub:
		return stub.Embedder{}, nil
	default:
		return nil, fmt.Errorf("embeddings: unknown backend %q: %w", kind, provider.ErrNotConfigured)
	}
}
