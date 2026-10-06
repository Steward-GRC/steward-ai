// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package gemini is the provider adapter for the Gemini API's generateContent
// method, over net/http.
package gemini

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

// DefaultBaseURL is the Gemini API root, version included.
const DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// Client completes requests on the Gemini API.
type Client struct {
	base  string
	key   string
	model string
	http  *http.Client
	log   log.Logger
}

var _ provider.Generator = (*Client)(nil)

// Option adjusts a Client.
type Option func(*config)

type config struct {
	httpClient *http.Client
	logger     log.Logger
}

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(o *config) { o.httpClient = c } }

// WithLogger sets the logger.
func WithLogger(l log.Logger) Option { return func(o *config) { o.logger = l } }

// New builds a client. The model and key are required; BaseURL overrides
// DefaultBaseURL.
func New(s provider.Settings, opts ...Option) (*Client, error) {
	if s.Model == "" || s.Credential == "" {
		return nil, fmt.Errorf("gemini: model and key are required: %w", provider.ErrNotConfigured)
	}
	cfg := config{httpClient: &http.Client{}, logger: log.NewLogger("ai")}
	for _, o := range opts {
		o(&cfg)
	}
	base := strings.TrimRight(s.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{base: base, key: s.Credential, model: s.Model, http: cfg.httpClient, log: cfg.logger}, nil
}

type part struct {
	Text    string `json:"text"`
	Thought bool   `json:"thought,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type generationConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens"`
}

type generateRequest struct {
	SystemInstruction *content         `json:"systemInstruction,omitempty"`
	Contents          []content        `json:"contents"`
	GenerationConfig  generationConfig `json:"generationConfig"`
}

type generateResponse struct {
	Candidates []struct {
		Content      content `json:"content"`
		FinishReason string  `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount     int64 `json:"promptTokenCount"`
		CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int64 `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
}

// Complete sends one generateContent call. The web fetch flag is ignored.
func (c *Client) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = airules.DefaultMaxTokens
	}
	model := req.Model
	if model == "" {
		model = c.model
	}
	body := generateRequest{GenerationConfig: generationConfig{MaxOutputTokens: maxTokens}}
	if req.System != "" {
		body.SystemInstruction = &content{Parts: []part{{Text: req.System}}}
	}
	for _, m := range req.Messages {
		role := "user"
		if m.Role == provider.RoleAssistant {
			role = "model"
		}
		body.Contents = append(body.Contents, content{Role: role, Parts: []part{{Text: m.Content}}})
	}

	l := c.log.Ctx(ctx)
	start := time.Now()
	endpoint := c.base + "/models/" + url.PathEscape(model) + ":generateContent"
	var out generateResponse
	err := httpjson.Post(ctx, c.http, endpoint, map[string]string{"x-goog-api-key": c.key}, body, &out)
	elapsed := time.Since(start)
	if err != nil {
		err = classify(err)
		l.Warn("gemini call failed", log.F("model", model), log.F("operation", req.Operation),
			log.F("duration_ms", elapsed.Milliseconds()), log.F("reason", provider.Reason(err)), log.F("error", err.Error()))
		return provider.Response{}, err
	}
	if len(out.Candidates) == 0 {
		return provider.Response{}, fmt.Errorf("gemini: no candidates (block reason %q)", out.PromptFeedback.BlockReason)
	}
	var b strings.Builder
	for _, p := range out.Candidates[0].Content.Parts {
		if !p.Thought {
			b.WriteString(p.Text)
		}
	}
	if b.Len() == 0 {
		return provider.Response{}, fmt.Errorf("gemini: no text in response (finish reason %q)", out.Candidates[0].FinishReason)
	}
	u := out.UsageMetadata
	// Thinking tokens are billed as output but counted apart from the
	// candidates.
	resp := provider.Response{
		Text:         b.String(),
		InputTokens:  u.PromptTokenCount,
		OutputTokens: u.CandidatesTokenCount + u.ThoughtsTokenCount,
	}
	l.Debug("gemini call done", log.F("model", model), log.F("operation", req.Operation),
		log.F("duration_ms", elapsed.Milliseconds()), log.F("input_tokens", resp.InputTokens),
		log.F("output_tokens", resp.OutputTokens))
	return resp, nil
}

type apiError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

// classify wraps a refused key with provider.ErrAuth. Gemini answers a bad
// key with 400 and reason API_KEY_INVALID, not 401.
func classify(err error) error {
	var se *httpjson.StatusError
	if !errors.As(err, &se) {
		return fmt.Errorf("gemini: %w", err)
	}
	msg := strings.TrimSpace(string(se.Body))
	auth := se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden
	var ae apiError
	if json.Unmarshal(se.Body, &ae) == nil && ae.Error.Message != "" {
		msg = ae.Error.Message
		for _, d := range ae.Error.Details {
			if d.Reason == "API_KEY_INVALID" {
				auth = true
			}
		}
	}
	if auth {
		return fmt.Errorf("gemini: status %d: %s: %w", se.Status, msg, provider.ErrAuth)
	}
	return fmt.Errorf("gemini: status %d: %s", se.Status, msg)
}
