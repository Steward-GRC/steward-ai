// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package bedrock is the provider adapter for Amazon Bedrock's Converse API,
// through aws-sdk-go-v2.
package bedrock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Bugs5382/go-log"
	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/provider"
)

// Client completes requests on Bedrock.
type Client struct {
	client *bedrockruntime.Client
	model  string
	log    log.Logger
}

var _ provider.Generator = (*Client)(nil)

// Option adjusts a Client.
type Option func(*options)

type options struct {
	httpClient  *http.Client
	logger      log.Logger
	maxAttempts int
}

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.httpClient = c } }

// WithLogger sets the logger.
func WithLogger(l log.Logger) Option { return func(o *options) { o.logger = l } }

// WithMaxAttempts sets the SDK's attempts per call, retries included; the
// SDK default applies otherwise.
func WithMaxAttempts(n int) Option { return func(o *options) { o.maxAttempts = n } }

// New builds a client. Region and model are required. A credential of the
// form ACCESS_KEY_ID:SECRET_ACCESS_KEY is used as static keys; with none,
// the AWS default credential chain applies (environment, shared files,
// workload identity, instance role). BaseURL overrides the endpoint.
func New(s provider.Settings, opts ...Option) (*Client, error) {
	if s.Region == "" || s.Model == "" {
		return nil, fmt.Errorf("bedrock: region and model are required: %w", provider.ErrNotConfigured)
	}
	o := options{logger: log.NewLogger("ai")}
	for _, opt := range opts {
		opt(&o)
	}
	load := []func(*config.LoadOptions) error{config.WithRegion(s.Region)}
	if s.Credential != "" {
		id, secret, err := parseCredential(s.Credential)
		if err != nil {
			return nil, err
		}
		load = append(load, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(id, secret, "")))
	}
	if o.httpClient != nil {
		load = append(load, config.WithHTTPClient(o.httpClient))
	}
	if o.maxAttempts > 0 {
		load = append(load, config.WithRetryMaxAttempts(o.maxAttempts))
	}
	// Loading reads only local configuration; credentials resolve on the
	// first call.
	cfg, err := config.LoadDefaultConfig(context.Background(), load...)
	if err != nil {
		return nil, fmt.Errorf("bedrock: load aws config: %w", err)
	}
	client := bedrockruntime.NewFromConfig(cfg, func(bo *bedrockruntime.Options) {
		if s.BaseURL != "" {
			bo.BaseEndpoint = aws.String(s.BaseURL)
		}
	})
	return &Client{client: client, model: s.Model, log: o.logger}, nil
}

func parseCredential(cred string) (id, secret string, err error) {
	id, secret, ok := strings.Cut(cred, ":")
	if !ok || id == "" || secret == "" {
		return "", "", fmt.Errorf("bedrock: credential must be ACCESS_KEY_ID:SECRET_ACCESS_KEY: %w", provider.ErrNotConfigured)
	}
	return id, secret, nil
}

// Complete sends one Converse call. The web fetch flag is ignored.
func (c *Client) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = airules.DefaultMaxTokens
	}
	model := req.Model
	if model == "" {
		model = c.model
	}
	in := &bedrockruntime.ConverseInput{
		ModelId:         aws.String(model),
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(int32(min(maxTokens, math.MaxInt32)))},
	}
	if req.System != "" {
		in.System = []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: req.System}}
	}
	for _, m := range req.Messages {
		role := types.ConversationRoleUser
		if m.Role == provider.RoleAssistant {
			role = types.ConversationRoleAssistant
		}
		in.Messages = append(in.Messages, types.Message{
			Role:    role,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: m.Content}},
		})
	}

	l := c.log.Ctx(ctx)
	start := time.Now()
	out, err := c.client.Converse(ctx, in)
	elapsed := time.Since(start)
	if err != nil {
		err = classify(err)
		l.Warn("bedrock call failed", log.F("model", model), log.F("operation", req.Operation),
			log.F("duration_ms", elapsed.Milliseconds()), log.F("reason", provider.Reason(err)), log.F("error", err.Error()))
		return provider.Response{}, err
	}
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return provider.Response{}, errors.New("bedrock: no message in response")
	}
	var b strings.Builder
	for _, block := range msg.Value.Content {
		if t, ok := block.(*types.ContentBlockMemberText); ok {
			b.WriteString(t.Value)
		}
	}
	if b.Len() == 0 {
		return provider.Response{}, fmt.Errorf("bedrock: no text in response (stop reason %q)", out.StopReason)
	}
	resp := provider.Response{Text: b.String()}
	if u := out.Usage; u != nil {
		resp.InputTokens = int64(aws.ToInt32(u.InputTokens)) + int64(aws.ToInt32(u.CacheReadInputTokens)) + int64(aws.ToInt32(u.CacheWriteInputTokens))
		resp.OutputTokens = int64(aws.ToInt32(u.OutputTokens))
	}
	l.Debug("bedrock call done", log.F("model", model), log.F("operation", req.Operation),
		log.F("duration_ms", elapsed.Milliseconds()), log.F("input_tokens", resp.InputTokens),
		log.F("output_tokens", resp.OutputTokens))
	return resp, nil
}

// classify wraps a refused credential with provider.ErrAuth. Bedrock answers
// a bad or unauthorised key with 403.
func classify(err error) error {
	var re *awshttp.ResponseError
	if errors.As(err, &re) {
		if s := re.HTTPStatusCode(); s == http.StatusUnauthorized || s == http.StatusForbidden {
			return fmt.Errorf("bedrock: %w: %w", provider.ErrAuth, err)
		}
	}
	return fmt.Errorf("bedrock: converse: %w", err)
}
