// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package anthropic is the provider adapter for the Anthropic Messages API,
// through the official Go SDK. It authenticates with an API key only.
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Bugs5382/go-log"
	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/provider"
)

// DefaultModel is used when neither the settings nor the request name one.
const DefaultModel = "claude-sonnet-4-6"

// webFetchMaxUses caps the fetches one call may make, so a prompt full of
// URLs can't fan out without bound.
const webFetchMaxUses = 5

// webFetchLegacyModelPrefixes are the models without the current
// web_fetch_20260209 tool; they get web_fetch_20250910. Anything else,
// including an unknown model, gets the current tool.
var webFetchLegacyModelPrefixes = []string{
	"claude-haiku",
	"claude-3-",
	"claude-sonnet-4-5",
	"claude-sonnet-4-0",
	"claude-sonnet-4-20",
	"claude-opus-4-5",
	"claude-opus-4-1",
	"claude-opus-4-0",
	"claude-opus-4-20",
}

// Client completes requests on the Anthropic Messages API.
type Client struct {
	client sdk.Client
	model  string
	log    log.Logger
}

var _ provider.Generator = (*Client)(nil)

// Option adjusts a Client.
type Option func(*config)

type config struct {
	httpClient *http.Client
	logger     log.Logger
	maxRetries *int
}

// WithHTTPClient sets the HTTP client the SDK uses.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithLogger sets the logger.
func WithLogger(l log.Logger) Option { return func(o *config) { o.logger = l } }

// WithMaxRetries sets how often the SDK retries a 429, 5xx or connection
// failure; the SDK default applies otherwise.
func WithMaxRetries(n int) Option { return func(o *config) { o.maxRetries = &n } }

// New builds a client from s. The credential is an API key, sent as
// x-api-key; environment credentials are never read.
func New(s provider.Settings, opts ...Option) (*Client, error) {
	if s.Credential == "" {
		return nil, fmt.Errorf("anthropic: no API key: %w", provider.ErrNotConfigured)
	}
	cfg := config{logger: log.NewLogger("ai")}
	for _, o := range opts {
		o(&cfg)
	}
	ro := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(s.Credential),
	}
	if s.BaseURL != "" {
		ro = append(ro, option.WithBaseURL(s.BaseURL))
	}
	if cfg.httpClient != nil {
		ro = append(ro, option.WithHTTPClient(cfg.httpClient))
	}
	if cfg.maxRetries != nil {
		ro = append(ro, option.WithMaxRetries(*cfg.maxRetries))
	}
	model := s.Model
	if model == "" {
		model = DefaultModel
	}
	return &Client{client: sdk.NewClient(ro...), model: model, log: cfg.logger}, nil
}

// Complete sends one request and returns the reply text and token usage.
//
// The system block carries an ephemeral cache mark: it is the same for every
// call of an operation, while the retrieved text in the user turn changes
// per call and is left uncached.
func (c *Client) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	maxTokens := int64(req.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = airules.DefaultMaxTokens
	}
	model := req.Model
	if model == "" {
		model = c.model
	}

	msgs := make([]sdk.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case provider.RoleUser:
			msgs = append(msgs, sdk.NewUserMessage(sdk.NewTextBlock(m.Content)))
		case provider.RoleAssistant:
			msgs = append(msgs, sdk.NewAssistantMessage(sdk.NewTextBlock(m.Content)))
		}
	}

	params := sdk.MessageNewParams{
		Model:     sdk.Model(model),
		MaxTokens: maxTokens,
		System: []sdk.TextBlockParam{{
			Text:         req.System,
			CacheControl: sdk.NewCacheControlEphemeralParam(),
		}},
		Messages: msgs,
	}
	if req.EnableWebFetch {
		params.Tools = []sdk.ToolUnionParam{webFetchToolFor(model)}
	}

	l := c.log.Ctx(ctx)
	start := time.Now()
	resp, err := c.stream(ctx, params)
	if err != nil && len(params.Tools) > 0 && isWebFetchToolRejection(err) {
		l.Warn("anthropic rejected the web_fetch tool; retrying without it",
			log.F("model", model), log.F("error", err.Error()))
		params.Tools = nil
		resp, err = c.stream(ctx, params)
	}
	elapsed := time.Since(start)
	if err != nil {
		err = classify(err)
		l.Warn("anthropic call failed", log.F("model", model), log.F("operation", req.Operation),
			log.F("duration_ms", elapsed.Milliseconds()), log.F("reason", provider.Reason(err)), log.F("error", err.Error()))
		return provider.Response{}, err
	}

	text, ok := replyText(resp.Content)
	if !ok {
		return provider.Response{}, errors.New("anthropic: no text block in response")
	}
	u := resp.Usage
	out := provider.Response{
		Text:         text,
		InputTokens:  u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens,
		OutputTokens: u.OutputTokens,
	}
	l.Debug("anthropic call done", log.F("model", model), log.F("operation", req.Operation),
		log.F("duration_ms", elapsed.Milliseconds()), log.F("input_tokens", out.InputTokens),
		log.F("output_tokens", out.OutputTokens), log.F("cache_read_tokens", u.CacheReadInputTokens))
	return out, nil
}

// stream calls the streaming endpoint and accumulates the events into one
// message. The SDK refuses a non-streaming call whose estimated generation
// time passes ten minutes, which a drafted section at its token budget does;
// streaming is bounded by ctx alone. Don't add a request timeout here, it
// would bring that ceiling back.
func (c *Client) stream(ctx context.Context, params sdk.MessageNewParams) (*sdk.Message, error) {
	s := c.client.Messages.NewStreaming(ctx, params)
	defer func() { _ = s.Close() }()
	var acc sdk.Message
	for s.Next() {
		if err := acc.Accumulate(s.Current()); err != nil {
			return nil, fmt.Errorf("accumulate stream: %w", err)
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return &acc, nil
}

// replyText joins the text blocks after the last tool block. With web fetch
// on, text before a fetch is the model announcing it, not the answer;
// citations split one answer across several text blocks.
func replyText(blocks []sdk.ContentBlockUnion) (string, bool) {
	var b strings.Builder
	found := false
	for _, block := range blocks {
		if block.Type != "text" {
			b.Reset()
			found = false
			continue
		}
		b.WriteString(block.Text)
		found = true
	}
	return b.String(), found
}

func webFetchToolFor(model string) sdk.ToolUnionParam {
	for _, prefix := range webFetchLegacyModelPrefixes {
		if strings.HasPrefix(model, prefix) {
			return sdk.ToolUnionParam{OfWebFetchTool20250910: &sdk.WebFetchTool20250910Param{
				MaxUses: sdk.Int(webFetchMaxUses),
			}}
		}
	}
	return sdk.ToolUnionParam{OfWebFetchTool20260209: &sdk.WebFetchTool20260209Param{
		MaxUses: sdk.Int(webFetchMaxUses),
	}}
}

// isWebFetchToolRejection reports a 400 or 403 whose body names the web fetch
// tool. Some keys and organisations aren't allowed server tools; drafting
// should go on without the tool rather than fail. The match is narrow so an
// unrelated error is never hidden by a retry; the SDK has no typed error for
// it, so it reads the body.
func isWebFetchToolRejection(err error) bool {
	var aerr *sdk.Error
	if !errors.As(err, &aerr) {
		return false
	}
	if aerr.StatusCode != http.StatusBadRequest && aerr.StatusCode != http.StatusForbidden {
		return false
	}
	body := strings.ToLower(aerr.RawJSON())
	return strings.Contains(body, "web_fetch") || strings.Contains(body, "web fetch")
}

func classify(err error) error {
	var aerr *sdk.Error
	if errors.As(err, &aerr) && (aerr.StatusCode == http.StatusUnauthorized || aerr.StatusCode == http.StatusForbidden) {
		return fmt.Errorf("anthropic: %w: %w", provider.ErrAuth, err)
	}
	return fmt.Errorf("anthropic: complete: %w", err)
}
