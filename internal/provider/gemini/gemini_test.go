// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package gemini_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/gemini"
	"github.com/Steward-GRC/steward-ai/internal/provider/internal/contract"
)

func TestContracts(t *testing.T) {
	for _, c := range contract.LoadDir(t, "testdata/contracts/gemini") {
		t.Run(c.Name, func(t *testing.T) {
			srv := c.Serve(t)
			client, err := gemini.New(provider.Settings{
				Kind: provider.Gemini, Model: c.Call.Settings.Model,
				Credential: c.Call.Settings.Credential, BaseURL: srv.URL,
			})
			if err != nil {
				t.Fatal(err)
			}
			req := provider.Request{System: c.Call.System, MaxTokens: c.Call.MaxTokens, Model: c.Call.Model}
			for _, m := range c.Call.Messages {
				req.Messages = append(req.Messages, provider.Message{Role: provider.Role(m.Role), Content: m.Content})
			}
			resp, err := client.Complete(context.Background(), req)
			switch c.Expect.Error {
			case "":
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			case "auth":
				if !errors.Is(err, provider.ErrAuth) {
					t.Fatalf("want ErrAuth, got %v", err)
				}
				return
			case "provider":
				if err == nil || provider.Reason(err) != provider.ReasonProviderError {
					t.Fatalf("want a provider error, got %v", err)
				}
				return
			}
			if resp.Text != c.Expect.Text {
				t.Errorf("text %q, want %q", resp.Text, c.Expect.Text)
			}
			if resp.InputTokens != c.Expect.InputTokens || resp.OutputTokens != c.Expect.OutputTokens {
				t.Errorf("tokens in=%d out=%d, want in=%d out=%d", resp.InputTokens, resp.OutputTokens, c.Expect.InputTokens, c.Expect.OutputTokens)
			}
		})
	}
}

func TestNew_requiredSettings(t *testing.T) {
	for name, s := range map[string]provider.Settings{
		"no model": {Kind: provider.Gemini, Credential: "test-key-1"},
		"no key":   {Kind: provider.Gemini, Model: "gemini-2.5-flash"},
	} {
		if _, err := gemini.New(s); !errors.Is(err, provider.ErrNotConfigured) {
			t.Errorf("%s: want ErrNotConfigured, got %v", name, err)
		}
	}
}
