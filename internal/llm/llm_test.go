// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package llm_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	log "github.com/Bugs5382/go-log"

	"github.com/Steward-GRC/steward-ai/internal/aiconfig"
	"github.com/Steward-GRC/steward-ai/internal/llm"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

const testCredential = "test-key-1"

type fakeConfig struct {
	settings aiconfig.Settings
	cred     string
	err      error
	credErr  error
}

func (f *fakeConfig) Settings(context.Context) (aiconfig.Settings, error) {
	return f.settings, f.err
}

func (f *fakeConfig) Credential(context.Context) (string, bool, error) {
	if f.credErr != nil {
		return "", false, f.credErr
	}
	return f.cred, f.cred != "", nil
}

type fakeUsage struct {
	mu       sync.Mutex
	month    store.Usage
	monthErr error
	added    []store.Usage
	addErr   error
}

func (f *fakeUsage) Month(context.Context, time.Time) (store.Usage, error) {
	return f.month, f.monthErr
}

func (f *fakeUsage) Add(_ context.Context, _ time.Time, u store.Usage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, u)
	return f.addErr
}

type fakeGen struct {
	resp provider.Response
	err  error
	reqs []provider.Request
}

func (g *fakeGen) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	g.reqs = append(g.reqs, req)
	return g.resp, g.err
}

type fakeFactory struct {
	gen   *fakeGen
	err   error
	built []provider.Settings
}

func (f *fakeFactory) build(s provider.Settings) (provider.Generator, error) {
	f.built = append(f.built, s)
	if f.err != nil {
		return nil, f.err
	}
	return f.gen, nil
}

func onSettings() aiconfig.Settings {
	return aiconfig.Settings{
		Enabled:       true,
		Provider:      store.ProviderSettings{Provider: string(provider.OpenAI), Model: "example-model"},
		CredentialSet: true,
	}
}

type harness struct {
	cfg     *fakeConfig
	usage   *fakeUsage
	gen     *fakeGen
	factory *fakeFactory
	client  *llm.Client
	logs    *bytes.Buffer
	now     time.Time
}

func newHarness(t *testing.T, opts ...llm.Option) *harness {
	t.Helper()
	h := &harness{
		cfg:   &fakeConfig{settings: onSettings(), cred: testCredential},
		usage: &fakeUsage{},
		gen:   &fakeGen{resp: provider.Response{Text: "answer", InputTokens: 10, OutputTokens: 5}},
		logs:  &bytes.Buffer{},
		now:   time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
	}
	h.factory = &fakeFactory{gen: h.gen}
	logger := log.NewLoggerWithOptions("ai", log.WithOutput(h.logs), log.WithDefaultLevel(log.LevelTrace))
	all := append([]llm.Option{llm.WithLogger(logger), llm.WithClock(func() time.Time { return h.now })}, opts...)
	h.client = llm.New(h.cfg, h.usage, h.factory.build, all...)
	return h
}

func ask(model string) provider.Request {
	return provider.Request{
		System:    "You answer questions.",
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "What is the leave policy?"}},
		Model:     model,
		Operation: "qa",
	}
}

// Stub mode is a deliberate placeholder, not a failure.
func TestClient_StubMode_AlwaysAvailable(t *testing.T) {
	h := newHarness(t, llm.WithStub())
	available, reason := h.client.ProviderStatus()
	if !available || reason != "" {
		t.Fatalf("expected stub mode always available, got available=%v reason=%q", available, reason)
	}
}

// The stub answers a review-format prompt with a finding block, so a stub
// review job has a finding to show.
func TestStubComplete_reviewFormat_emitsAFindingBlock(t *testing.T) {
	h := newHarness(t, llm.WithStub())
	resp, err := h.client.Complete(context.Background(), provider.Request{
		System:   `Report each finding as <finding section="SECTION_KEY_OR_EMPTY" severity="...">.`,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: `<section key="purpose" title="Purpose">Body.</section>`}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Count(resp.Text, "<finding ") != 1 || !strings.Contains(resp.Text, `<finding section="purpose"`) {
		t.Fatalf("expected 1 stub finding on section \"purpose\", got %q", resp.Text)
	}
	if len(h.factory.built) != 0 {
		t.Fatal("stub mode must not build a provider adapter")
	}
}

func TestStubComplete_draft_namesTheSection(t *testing.T) {
	h := newHarness(t, llm.WithStub())
	resp, err := h.client.Complete(context.Background(), provider.Request{
		Operation: "draft",
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: `<current_section key="scope">`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, `"scope"`) || !strings.Contains(resp.Text, "STUB") {
		t.Fatalf("expected a labelled stub draft for section scope, got %q", resp.Text)
	}
}

func TestStub_stillRefusedWhileOff(t *testing.T) {
	h := newHarness(t, llm.WithStub())
	h.cfg.settings.Enabled = false
	if _, err := h.client.Complete(context.Background(), ask("")); !errors.Is(err, llm.ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

// A classified failure flips availability; the next success restores it.
func TestClient_ProviderStatus_RoundTrips(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if available, reason := h.client.ProviderStatus(); !available || reason != "" {
		t.Fatalf("expected healthy before any recorded error, got available=%v reason=%q", available, reason)
	}

	h.gen.err = fmt.Errorf("status 401: %w", provider.ErrAuth)
	if _, err := h.client.Complete(ctx, ask("")); err == nil {
		t.Fatal("expected the provider error")
	}
	available, reason := h.client.ProviderStatus()
	if available || reason != provider.ReasonAuthFailed {
		t.Fatalf("expected unavailable with auth_failed, got available=%v reason=%q", available, reason)
	}

	h.gen.err = errors.New("overloaded")
	_, _ = h.client.Complete(ctx, ask(""))
	if _, reason := h.client.ProviderStatus(); reason != provider.ReasonProviderError {
		t.Fatalf("expected provider_error, got %q", reason)
	}

	h.gen.err = nil
	if _, err := h.client.Complete(ctx, ask("")); err != nil {
		t.Fatal(err)
	}
	if available, reason := h.client.ProviderStatus(); !available || reason != "" {
		t.Fatalf("expected healthy again after a success, got available=%v reason=%q", available, reason)
	}
}

// Model precedence: the request's model (a job's snapshot) wins, then the
// configured model, then the adapter's own default (an empty model).
func TestClient_modelPrecedence(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	if _, err := h.client.Complete(ctx, ask("snapshot")); err != nil {
		t.Fatal(err)
	}
	if got := h.gen.reqs[0].Model; got != "snapshot" {
		t.Fatalf("expected the request's model to win, got %q", got)
	}

	if _, err := h.client.Complete(ctx, ask("")); err != nil {
		t.Fatal(err)
	}
	if got := h.gen.reqs[1].Model; got != "example-model" {
		t.Fatalf("expected the configured model, got %q", got)
	}
	if got, _ := h.client.DefaultModel(ctx); got != "example-model" {
		t.Fatalf("DefaultModel: got %q", got)
	}

	h.cfg.settings.Provider.Model = ""
	if _, err := h.client.Complete(ctx, ask("")); err != nil {
		t.Fatal(err)
	}
	if got := h.gen.reqs[2].Model; got != "" {
		t.Fatalf("expected an empty model for the adapter default, got %q", got)
	}
}

// A settings read failure refuses the call; it never falls back to a
// default and runs.
func TestClient_settingsErrorRefuses(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.cfg.err = errors.New("postgres down")

	if _, err := h.client.Complete(ctx, ask("")); !errors.Is(err, llm.ErrSettingsUnavailable) {
		t.Fatalf("expected ErrSettingsUnavailable, got %v", err)
	}
	if _, err := h.client.DefaultModel(ctx); !errors.Is(err, llm.ErrSettingsUnavailable) {
		t.Fatalf("DefaultModel: expected ErrSettingsUnavailable, got %v", err)
	}
	if len(h.gen.reqs) != 0 {
		t.Fatal("no provider call may happen when settings are unreadable")
	}

	h.cfg.err = nil
	h.cfg.credErr = errors.New("settings key mismatch")
	if _, err := h.client.Complete(ctx, ask("")); !errors.Is(err, llm.ErrSettingsUnavailable) {
		t.Fatalf("credential read failure: expected ErrSettingsUnavailable, got %v", err)
	}
}

func TestClient_refusedWhileOff(t *testing.T) {
	h := newHarness(t)
	h.cfg.settings.Enabled = false
	if _, err := h.client.Complete(context.Background(), ask("")); !errors.Is(err, llm.ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
	if len(h.factory.built) != 0 || len(h.gen.reqs) != 0 {
		t.Fatal("nothing may reach a provider while the module is off")
	}
}

// The stored credential and settings reach the adapter; the adapter is
// reused while they stay the same and rebuilt when they change.
func TestClient_credentialSource(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.cfg.settings.Provider.BaseURL = "https://ai.example.org"

	for range 2 {
		if _, err := h.client.Complete(ctx, ask("")); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.factory.built) != 1 {
		t.Fatalf("expected one adapter for unchanged settings, got %d builds", len(h.factory.built))
	}
	want := provider.Settings{Kind: provider.OpenAI, Model: "example-model", BaseURL: "https://ai.example.org", Credential: testCredential}
	if h.factory.built[0] != want {
		t.Fatalf("adapter settings: got kind=%s base=%s credential-matches=%v",
			h.factory.built[0].Kind, h.factory.built[0].BaseURL, h.factory.built[0].Credential == testCredential)
	}

	h.cfg.cred = "test-key-2"
	if _, err := h.client.Complete(ctx, ask("")); err != nil {
		t.Fatal(err)
	}
	if len(h.factory.built) != 2 || h.factory.built[1].Credential != "test-key-2" {
		t.Fatal("a rotated credential must rebuild the adapter")
	}

	h.cfg.settings.Provider.Region = "example-region-1"
	if _, err := h.client.Complete(ctx, ask("")); err != nil {
		t.Fatal(err)
	}
	if len(h.factory.built) != 3 {
		t.Fatal("changed settings must rebuild the adapter")
	}
}

func TestClient_noProviderChosen(t *testing.T) {
	h := newHarness(t)
	h.cfg.settings.Provider.Provider = ""
	_, err := h.client.Complete(context.Background(), ask(""))
	if !errors.Is(err, provider.ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	if len(h.factory.built) != 0 {
		t.Fatal("no adapter may be built without a provider")
	}
	if available, reason := h.client.ProviderStatus(); available || reason != provider.ReasonNotConfigured {
		t.Fatalf("expected not_configured, got available=%v reason=%q", available, reason)
	}
}

func TestClient_factoryErrorIsClassified(t *testing.T) {
	h := newHarness(t)
	h.factory.err = fmt.Errorf("no endpoint: %w", provider.ErrNotConfigured)
	if _, err := h.client.Complete(context.Background(), ask("")); !errors.Is(err, provider.ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	if _, reason := h.client.ProviderStatus(); reason != provider.ReasonNotConfigured {
		t.Fatalf("expected not_configured, got %q", reason)
	}
}

// The organisation context leads the system prompt as its own paragraph,
// and an empty one changes nothing.
func TestClient_orgContext(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	if _, err := h.client.Complete(ctx, ask("")); err != nil {
		t.Fatal(err)
	}
	if got := h.gen.reqs[0].System; got != "You answer questions." {
		t.Fatalf("empty org context must leave the prompt alone, got %q", got)
	}

	h.cfg.settings.OrgContext = "These policies belong to Example Organisation."
	if _, err := h.client.Complete(ctx, ask("")); err != nil {
		t.Fatal(err)
	}
	if got := h.gen.reqs[1].System; got != "These policies belong to Example Organisation.\n\nYou answer questions." {
		t.Fatalf("org context not prepended: %q", got)
	}

	req := ask("")
	req.System = ""
	if _, err := h.client.Complete(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := h.gen.reqs[2].System; got != "These policies belong to Example Organisation." {
		t.Fatalf("org context alone: got %q", got)
	}
}

func TestClient_monthlyLimit(t *testing.T) {
	ctx := context.Background()

	t.Run("reached", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.settings.MonthlyLimit = 100
		h.usage.month = store.Usage{Requests: 100}
		_, err := h.client.Complete(ctx, ask(""))
		limitErr, ok := errors.AsType[*llm.MonthlyLimitError](err)
		if !ok {
			t.Fatalf("expected a MonthlyLimitError, got %v", err)
		}
		if limitErr.Limit != 100 || !limitErr.ResetAt.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("got limit=%d reset=%v", limitErr.Limit, limitErr.ResetAt)
		}
		if len(h.gen.reqs) != 0 {
			t.Fatal("no provider call past the limit")
		}
	})

	t.Run("below", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.settings.MonthlyLimit = 100
		h.usage.month = store.Usage{Requests: 99}
		if _, err := h.client.Complete(ctx, ask("")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("zero is none", func(t *testing.T) {
		h := newHarness(t)
		h.usage.month = store.Usage{Requests: 1_000_000}
		h.usage.monthErr = errors.New("never read")
		if _, err := h.client.Complete(ctx, ask("")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unreadable usage refuses", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.settings.MonthlyLimit = 100
		h.usage.monthErr = errors.New("postgres down")
		if _, err := h.client.Complete(ctx, ask("")); !errors.Is(err, llm.ErrSettingsUnavailable) {
			t.Fatalf("expected ErrSettingsUnavailable, got %v", err)
		}
	})
}

func TestClient_recordsUsage(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	if _, err := h.client.Complete(ctx, ask("")); err != nil {
		t.Fatal(err)
	}
	if len(h.usage.added) != 1 || h.usage.added[0] != (store.Usage{Requests: 1, InputTokens: 10, OutputTokens: 5}) {
		t.Fatalf("usage: got %+v", h.usage.added)
	}

	h.gen.err = errors.New("overloaded")
	_, _ = h.client.Complete(ctx, ask(""))
	if len(h.usage.added) != 1 {
		t.Fatal("a failed call must not be metered")
	}

	h.gen.err = nil
	h.usage.addErr = errors.New("postgres down")
	if _, err := h.client.Complete(ctx, ask("")); err != nil {
		t.Fatalf("a meter write failure must not fail the answer: %v", err)
	}
}

func TestClient_Test(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		h := newHarness(t)
		res, err := h.client.Test(ctx)
		if err != nil || res.Reason != "" {
			t.Fatalf("got res=%+v err=%v", res, err)
		}
		if len(h.gen.reqs) != 1 || h.gen.reqs[0].MaxTokens == 0 || h.gen.reqs[0].MaxTokens > 32 {
			t.Fatalf("expected one tiny call, got %+v", h.gen.reqs)
		}
	})

	t.Run("provider failure is a reason", func(t *testing.T) {
		h := newHarness(t)
		h.gen.err = fmt.Errorf("status 403: %w", provider.ErrAuth)
		res, err := h.client.Test(ctx)
		if err != nil || res.Reason != provider.ReasonAuthFailed {
			t.Fatalf("got res=%+v err=%v", res, err)
		}
	})

	t.Run("no provider is a reason", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.settings.Provider.Provider = ""
		res, err := h.client.Test(ctx)
		if err != nil || res.Reason != provider.ReasonNotConfigured {
			t.Fatalf("got res=%+v err=%v", res, err)
		}
	})

	t.Run("refused while off", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.settings.Enabled = false
		if _, err := h.client.Test(ctx); !errors.Is(err, llm.ErrDisabled) {
			t.Fatalf("expected ErrDisabled, got %v", err)
		}
	})
}

// Refusals and calls are logged with their target, never with the
// credential or the prompt.
func TestClient_logsWithoutSecretsOrPrompts(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	_, _ = h.client.Complete(ctx, ask(""))
	h.gen.err = fmt.Errorf("status 401: %w", provider.ErrAuth)
	_, _ = h.client.Complete(ctx, ask(""))
	h.cfg.settings.Enabled = false
	_, _ = h.client.Complete(ctx, ask(""))

	out := h.logs.String()
	for _, banned := range []string{testCredential, "What is the leave policy?", "You answer questions."} {
		if strings.Contains(out, banned) {
			t.Fatalf("log output contains %q", banned)
		}
	}
	for _, want := range []string{`"provider":"openai"`, `"model":"example-model"`, `"duration_ms"`, `"outcome":"success"`, `"outcome":"auth_failed"`, "module off"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log output lacks %s:\n%s", want, out)
		}
	}
}
