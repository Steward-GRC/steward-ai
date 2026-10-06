// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package llm is the only path from the ai service to a generative
// provider. Every call reads the module's settings first and is refused
// while the module is off, once the month's request limit is reached, or
// when the settings can't be read. It then builds (or reuses) the adapter
// for the configured provider, adds the organisation context, makes the
// call, and records the month's usage and the provider's status.
package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	log "github.com/Bugs5382/go-log"

	"github.com/Steward-GRC/steward-ai/internal/aiconfig"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/stub"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

var (
	// ErrDisabled refuses a call while the module is off.
	ErrDisabled = errors.New("llm: the AI module is off")
	// ErrSettingsUnavailable refuses a call when the settings or the usage
	// meter can't be read, rather than run with the module possibly off or
	// over its limit.
	ErrSettingsUnavailable = errors.New("llm: AI settings unavailable")
)

// MonthlyLimitError refuses a call once the month's requests reach the
// organisation's limit.
type MonthlyLimitError struct {
	Limit   int64
	ResetAt time.Time
}

func (e *MonthlyLimitError) Error() string {
	return fmt.Sprintf("llm: monthly limit of %d requests reached; resets at %s", e.Limit, e.ResetAt.Format(time.RFC3339))
}

// Config is the settings source; aiconfig.Store satisfies it.
type Config interface {
	Settings(ctx context.Context) (aiconfig.Settings, error)
	Credential(ctx context.Context) (credential string, has bool, err error)
}

// Usage is the monthly usage meter; store.UsageStore satisfies it.
type Usage interface {
	Month(ctx context.Context, at time.Time) (store.Usage, error)
	Add(ctx context.Context, at time.Time, u store.Usage) error
}

// Factory builds the adapter for one set of provider settings.
type Factory func(provider.Settings) (provider.Generator, error)

// Client implements provider.Generator over the configured provider.
type Client struct {
	cfg     Config
	usage   Usage
	factory Factory
	log     log.Logger
	now     func() time.Time
	stub    bool

	adapterMu   sync.Mutex
	adapterKey  string
	adapterImpl provider.Generator

	statusMu   sync.RWMutex
	lastReason string
}

var _ provider.Generator = (*Client)(nil)

// Option configures a Client.
type Option func(*Client)

// WithLogger sets the logger; the default is go-log's "ai" logger.
func WithLogger(l log.Logger) Option { return func(c *Client) { c.log = l } }

// WithClock replaces time.Now.
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// WithStub answers every call with a labelled, deterministic placeholder
// instead of a provider, for local runs only. The module switch and the
// monthly limit still apply.
func WithStub() Option { return func(c *Client) { c.stub = true } }

// New returns a Client reading cfg, metering into usage and building
// adapters with factory.
func New(cfg Config, usage Usage, factory Factory, opts ...Option) *Client {
	c := &Client{cfg: cfg, usage: usage, factory: factory, log: log.NewLogger("ai"), now: time.Now}
	for _, o := range opts {
		o(c)
	}
	return c
}

// refusal is an error returned before any provider is reached.
type refusal struct{ err error }

func (r refusal) Error() string { return r.err.Error() }
func (r refusal) Unwrap() error { return r.err }

// Complete runs one completion through the configured provider.
func (c *Client) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	resp, err := c.complete(ctx, req)
	if r, ok := errors.AsType[refusal](err); ok {
		return provider.Response{}, r.err
	}
	return resp, err
}

func (c *Client) complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	l := c.log.Ctx(ctx).With(log.F("operation", req.Operation))

	settings, err := c.cfg.Settings(ctx)
	if err != nil {
		l.Error(err, "ai call refused: settings unreadable")
		return provider.Response{}, refusal{fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)}
	}
	if !settings.Enabled {
		l.Info("ai call refused: module off")
		return provider.Response{}, refusal{ErrDisabled}
	}
	if err := c.checkMonthlyLimit(ctx, l, settings.MonthlyLimit); err != nil {
		return provider.Response{}, err
	}

	gen, kind, err := c.adapter(ctx, l, settings)
	if err != nil {
		return provider.Response{}, err
	}

	if req.Model == "" {
		req.Model = settings.Provider.Model
	}
	if settings.OrgContext != "" {
		if req.System == "" {
			req.System = settings.OrgContext
		} else {
			req.System = settings.OrgContext + "\n\n" + req.System
		}
	}

	l = l.With(log.F("provider", kind), log.F("model", req.Model))
	l.Debug("provider call")
	start := c.now()
	resp, err := gen.Complete(ctx, req)
	elapsed := c.now().Sub(start)
	outcome := outcomeSuccess
	if err != nil {
		outcome = provider.Reason(err)
	}
	recordGenerationCall(ctx, req.Operation, outcome)
	recordGenerationLatency(ctx, req.Operation, outcome, elapsed.Seconds())
	if err != nil {
		c.setReason(outcome)
		l.Warn("provider call failed", log.F("outcome", outcome), log.F("duration_ms", elapsed.Milliseconds()), log.F("error", err.Error()))
		return provider.Response{}, fmt.Errorf("llm: %s complete: %w", kind, err)
	}
	c.setReason("")
	l.Info("provider call", log.F("outcome", outcome), log.F("duration_ms", elapsed.Milliseconds()),
		log.F("input_tokens", resp.InputTokens), log.F("output_tokens", resp.OutputTokens))

	// The answer is already paid for; a meter write failure must not lose it.
	if err := c.usage.Add(ctx, c.now(), store.Usage{Requests: 1, InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens}); err != nil {
		l.Error(err, "usage not recorded")
	}
	return resp, nil
}

// checkMonthlyLimit fails closed: a limit that can't be checked is refused,
// since it exists to cap spend.
func (c *Client) checkMonthlyLimit(ctx context.Context, l log.Logger, limit int64) error {
	if limit <= 0 {
		return nil
	}
	now := c.now()
	u, err := c.usage.Month(ctx, now)
	if err != nil {
		l.Error(err, "ai call refused: usage unreadable")
		return refusal{fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)}
	}
	if u.Requests >= limit {
		resetAt := store.MonthStart(now).AddDate(0, 1, 0)
		l.Info("ai call refused: monthly limit reached", log.F("limit", limit), log.F("requests", u.Requests), log.F("reset_at", resetAt))
		return refusal{&MonthlyLimitError{Limit: limit, ResetAt: resetAt}}
	}
	return nil
}

// adapter returns the generator for the current settings, building it only
// when they change. The cache key is a hash, so the credential is never a
// map key or held outside the adapter.
func (c *Client) adapter(ctx context.Context, l log.Logger, s aiconfig.Settings) (provider.Generator, string, error) {
	if c.stub {
		return stub.Generator{}, "stub", nil
	}
	kind := provider.Kind(s.Provider.Provider)
	if kind == "" {
		c.setReason(provider.ReasonNotConfigured)
		l.Warn("ai call refused: no provider chosen")
		return nil, "", fmt.Errorf("llm: no provider chosen: %w", provider.ErrNotConfigured)
	}
	cred, _, err := c.cfg.Credential(ctx)
	if err != nil {
		l.Error(err, "ai call refused: credential unreadable", log.F("provider", kind))
		return nil, "", refusal{fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)}
	}
	ps := provider.Settings{
		Kind: kind, Model: s.Provider.Model, BaseURL: s.Provider.BaseURL,
		Region: s.Provider.Region, Deployment: s.Provider.Deployment, Credential: cred,
	}
	key := fingerprint(ps)

	c.adapterMu.Lock()
	defer c.adapterMu.Unlock()
	if c.adapterImpl != nil && c.adapterKey == key {
		return c.adapterImpl, string(kind), nil
	}
	gen, err := c.factory(ps)
	if err != nil {
		reason := provider.Reason(err)
		c.setReason(reason)
		l.Warn("provider adapter not built", log.F("provider", kind), log.F("outcome", reason), log.F("error", err.Error()))
		return nil, "", fmt.Errorf("llm: build %s adapter: %w", kind, err)
	}
	l.Debug("provider adapter built", log.F("provider", kind))
	c.adapterKey, c.adapterImpl = key, gen
	return gen, string(kind), nil
}

func fingerprint(s provider.Settings) string {
	cred := sha256.Sum256([]byte(s.Credential))
	sum := sha256.Sum256([]byte(string(s.Kind) + "\x00" + s.Model + "\x00" + s.BaseURL + "\x00" +
		s.Region + "\x00" + s.Deployment + "\x00" + hex.EncodeToString(cred[:])))
	return hex.EncodeToString(sum[:])
}

func (c *Client) setReason(reason string) {
	c.statusMu.Lock()
	c.lastReason = reason
	c.statusMu.Unlock()
}

// ProviderStatus reports whether the provider is usable: available until a
// classified failure, and again after the next success. reason is one of
// provider's Reason values. The stub is always available.
func (c *Client) ProviderStatus() (available bool, reason string) {
	if c.stub {
		return true, ""
	}
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.lastReason == "", c.lastReason
}

// DefaultModel is the model a call without one uses: the configured model,
// or "" for the adapter's own default. Jobs snapshot it at submit.
func (c *Client) DefaultModel(ctx context.Context) (string, error) {
	s, err := c.cfg.Settings(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
	}
	return s.Provider.Model, nil
}

// TestResult is the outcome of Test: the call's latency, and a provider
// Reason when it failed.
type TestResult struct {
	Latency time.Duration
	Reason  string
}

// Test makes one tiny call to the configured provider. A refusal (module
// off, limit reached, settings unreadable) is returned as the error; a
// provider failure is reported in the result's Reason.
func (c *Client) Test(ctx context.Context) (TestResult, error) {
	start := c.now()
	_, err := c.complete(ctx, provider.Request{
		System:    "Reply with the single word OK.",
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "OK?"}},
		MaxTokens: testMaxTokens,
		Operation: "test",
	})
	res := TestResult{Latency: c.now().Sub(start), Reason: provider.Reason(err)}
	if r, ok := errors.AsType[refusal](err); ok {
		return TestResult{}, r.err
	}
	return res, nil
}

const testMaxTokens = 16
