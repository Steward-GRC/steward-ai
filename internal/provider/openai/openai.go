// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package openai is the provider adapter for the Chat Completions API as
// OpenAI, Azure OpenAI and OpenAI-compatible servers serve it, over net/http.
// It also embeds text through the embeddings endpoint.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Bugs5382/go-log"

	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/internal/httpjson"
)

// DefaultBaseURL is OpenAI's API root.
const DefaultBaseURL = "https://api.openai.com/v1"

// AzureAPIVersion is the Azure OpenAI data-plane inference version sent on
// every Azure call, the latest generally available dated version.
const AzureAPIVersion = "2024-10-21"

// Client completes requests on a Chat Completions endpoint.
type Client struct {
	kind     provider.Kind
	endpoint string
	headers  map[string]string
	model    string
	http     *http.Client
	log      log.Logger
}

var _ provider.Generator = (*Client)(nil)

// Option adjusts a Client or an Embedder.
type Option func(*config)

type config struct {
	httpClient *http.Client
	logger     log.Logger
}

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithLogger sets the logger.
func WithLogger(l log.Logger) Option { return func(o *config) { o.logger = l } }

func newConfig(opts []Option) config {
	cfg := config{httpClient: &http.Client{}, logger: log.NewLogger("ai")}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// New builds a client for s.Kind: provider.OpenAI, provider.AzureOpenAI or
// provider.OpenAICompatible.
//
// OpenAI needs a model and a key (Bearer); BaseURL overrides DefaultBaseURL.
// Azure needs BaseURL (the resource endpoint), Deployment and a key, sent in
// the api-key header; the deployment picks the model. An OpenAI-compatible
// server needs BaseURL and a model; a key, when set, is sent as Bearer.
func New(s provider.Settings, opts ...Option) (*Client, error) {
	cfg := newConfig(opts)
	c := &Client{kind: s.Kind, model: s.Model, http: cfg.httpClient, log: cfg.logger, headers: map[string]string{}}
	base := strings.TrimRight(s.BaseURL, "/")
	switch s.Kind {
	case provider.OpenAI:
		if s.Model == "" || s.Credential == "" {
			return nil, fmt.Errorf("openai: model and key are required: %w", provider.ErrNotConfigured)
		}
		if base == "" {
			base = DefaultBaseURL
		}
		c.endpoint = base + "/chat/completions"
		c.headers["Authorization"] = "Bearer " + s.Credential
	case provider.AzureOpenAI:
		if base == "" || s.Deployment == "" || s.Credential == "" {
			return nil, fmt.Errorf("azure openai: base URL, deployment and key are required: %w", provider.ErrNotConfigured)
		}
		c.endpoint = base + "/openai/deployments/" + url.PathEscape(s.Deployment) +
			"/chat/completions?api-version=" + url.QueryEscape(AzureAPIVersion)
		c.headers["api-key"] = s.Credential
	case provider.OpenAICompatible:
		if base == "" || s.Model == "" {
			return nil, fmt.Errorf("openai-compatible: base URL and model are required: %w", provider.ErrNotConfigured)
		}
		c.endpoint = base + "/chat/completions"
		if s.Credential != "" {
			c.headers["Authorization"] = "Bearer " + s.Credential
		}
	default:
		return nil, fmt.Errorf("openai: kind %q: %w", s.Kind, provider.ErrNotConfigured)
	}
	return c, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model               string        `json:"model,omitempty"`
	Messages            []chatMessage `json:"messages"`
	MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
	MaxTokens           int           `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
			Refusal *string `json:"refusal"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete sends one chat completion. The web fetch flag is ignored: Chat
// Completions has no fetch tool.
func (c *Client) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = airules.DefaultMaxTokens
	}
	body := chatRequest{Messages: make([]chatMessage, 0, len(req.Messages)+1)}
	if req.System != "" {
		body.Messages = append(body.Messages, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, chatMessage{Role: string(m.Role), Content: m.Content})
	}
	model := req.Model
	if model == "" {
		model = c.model
	}
	switch c.kind {
	case provider.AzureOpenAI:
		// The deployment names the model; reasoning deployments reject
		// max_tokens.
		body.MaxCompletionTokens = maxTokens
	case provider.OpenAI:
		body.Model = model
		body.MaxCompletionTokens = maxTokens
	default:
		// Compatible servers accept max_tokens more widely than its
		// replacement.
		body.Model = model
		body.MaxTokens = maxTokens
	}

	l := c.log.Ctx(ctx)
	start := time.Now()
	var out chatResponse
	err := httpjson.Post(ctx, c.http, c.endpoint, c.headers, body, &out)
	elapsed := time.Since(start)
	if err != nil {
		err = classify(string(c.kind), err)
		l.Warn("chat completion failed", log.F("kind", string(c.kind)), log.F("model", model),
			log.F("operation", req.Operation), log.F("duration_ms", elapsed.Milliseconds()),
			log.F("reason", provider.Reason(err)), log.F("error", err.Error()))
		return provider.Response{}, err
	}
	if len(out.Choices) == 0 {
		return provider.Response{}, fmt.Errorf("%s: no choices in response", c.kind)
	}
	msg := out.Choices[0].Message
	if msg.Content == nil || *msg.Content == "" {
		if msg.Refusal != nil && *msg.Refusal != "" {
			return provider.Response{}, fmt.Errorf("%s: model refused: %s", c.kind, *msg.Refusal)
		}
		return provider.Response{}, fmt.Errorf("%s: no text in response (finish reason %q)", c.kind, out.Choices[0].FinishReason)
	}
	resp := provider.Response{
		Text:         *msg.Content,
		InputTokens:  out.Usage.PromptTokens,
		OutputTokens: out.Usage.CompletionTokens,
	}
	l.Debug("chat completion done", log.F("kind", string(c.kind)), log.F("model", model),
		log.F("operation", req.Operation), log.F("duration_ms", elapsed.Milliseconds()),
		log.F("input_tokens", resp.InputTokens), log.F("output_tokens", resp.OutputTokens))
	return resp, nil
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// classify wraps a refused credential with provider.ErrAuth and keeps the
// provider's own message rather than the raw body.
func classify(prefix string, err error) error {
	var se *httpjson.StatusError
	if !errors.As(err, &se) {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	msg := strings.TrimSpace(string(se.Body))
	var ae apiError
	if json.Unmarshal(se.Body, &ae) == nil && ae.Error.Message != "" {
		msg = ae.Error.Message
	}
	if se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden {
		return fmt.Errorf("%s: status %d: %s: %w", prefix, se.Status, msg, provider.ErrAuth)
	}
	return fmt.Errorf("%s: status %d: %s", prefix, se.Status, msg)
}
