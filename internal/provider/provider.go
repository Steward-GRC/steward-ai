// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package provider is Steward's own interface to generative and embeddings
// providers. Every adapter (one subpackage per provider) implements it, and
// nothing outside the adapters imports a provider's SDK or wire types.
package provider

import (
	"context"
	"errors"
)

// Kind names a generative provider. The values are stored in ai_config.
type Kind string

// The generative providers Steward has an adapter for.
const (
	Anthropic        Kind = "anthropic"
	OpenAI           Kind = "openai"
	AzureOpenAI      Kind = "azure_openai"
	Gemini           Kind = "gemini"
	Bedrock          Kind = "bedrock"
	OpenAICompatible Kind = "openai_compatible"
)

// Kinds lists every provider kind, in display order.
var Kinds = []Kind{Anthropic, OpenAI, AzureOpenAI, Gemini, Bedrock, OpenAICompatible}

// Role is who wrote a message.
type Role string

// The two message roles.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn of the conversation.
type Message struct {
	Role    Role
	Content string
}

// Request is one completion call.
type Request struct {
	// System is the stable instruction prompt; adapters that can cache it
	// do.
	System   string
	Messages []Message
	// MaxTokens bounds the output; zero uses the adapter's default.
	MaxTokens int
	// Model overrides the configured model for this call; jobs set the
	// model they were submitted with.
	Model string
	// Operation is a low-cardinality label for metrics ("qa", "draft").
	Operation string
	// EnableWebFetch asks the provider to read URLs in the prompt where it
	// has a web fetch tool; adapters without one ignore it.
	EnableWebFetch bool
}

// Response is a completion's text and the tokens it used.
type Response struct {
	Text         string
	InputTokens  int64
	OutputTokens int64
}

// Generator completes a request.
type Generator interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// Embedder returns one vector per input text, in order. Every vector is
// Dimensions wide, the width the schema stores.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Dimensions is the embedding width the schema stores.
const Dimensions = 384

// Settings configure one generative adapter.
type Settings struct {
	Kind Kind
	// Model empty uses the adapter's default model.
	Model string
	// BaseURL is the endpoint for Azure OpenAI and OpenAI-compatible
	// servers, and an override for the others (tests point it at a
	// recorded contract).
	BaseURL string
	// Region is the AWS region for Bedrock.
	Region string
	// Deployment is the Azure OpenAI deployment.
	Deployment string
	// Credential is the key or token. Bedrock without one uses the AWS
	// default credential chain; an OpenAI-compatible server may need none.
	Credential string
}

// The reasons a provider call failed, as GetProviderStatus reports them.
const (
	ReasonAuthFailed    = "auth_failed"
	ReasonProviderError = "provider_error"
	ReasonNotConfigured = "not_configured"
)

// ErrAuth marks a refused credential (HTTP 401 or 403 or the provider's
// equivalent). Adapters wrap it so Reason can classify the failure.
var ErrAuth = errors.New("provider: credential refused")

// ErrNotConfigured means the settings lack something the adapter needs.
var ErrNotConfigured = errors.New("provider: not configured")

// Reason classifies a Complete error for the provider status.
func Reason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrAuth):
		return ReasonAuthFailed
	case errors.Is(err, ErrNotConfigured):
		return ReasonNotConfigured
	default:
		return ReasonProviderError
	}
}
