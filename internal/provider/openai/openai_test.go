// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package openai_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/internal/contract"
	"github.com/Steward-GRC/steward-ai/internal/provider/openai"
)

func TestContracts_chat(t *testing.T) {
	for _, dir := range []string{"openai", "azure_openai", "openai_compatible"} {
		for _, c := range contract.LoadDir(t, "testdata/contracts/"+dir) {
			t.Run(dir+"/"+c.Name, func(t *testing.T) {
				srv := c.Serve(t)
				s := c.Call.Settings
				client, err := openai.New(provider.Settings{
					Kind: provider.Kind(s.Kind), Model: s.Model, Deployment: s.Deployment,
					Credential: s.Credential, BaseURL: srv.URL,
				})
				if err != nil {
					t.Fatal(err)
				}
				req := provider.Request{System: c.Call.System, MaxTokens: c.Call.MaxTokens, Model: c.Call.Model}
				for _, m := range c.Call.Messages {
					req.Messages = append(req.Messages, provider.Message{Role: provider.Role(m.Role), Content: m.Content})
				}
				resp, err := client.Complete(context.Background(), req)
				if !checkError(t, c.Expect.Error, err) {
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
}

func TestContracts_embeddings(t *testing.T) {
	for _, c := range contract.LoadDir(t, "testdata/contracts/openai_embeddings") {
		t.Run(c.Name, func(t *testing.T) {
			srv := c.Serve(t)
			e, err := openai.NewEmbedder(srv.URL, c.Call.Settings.Model, c.Call.Settings.Credential)
			if err != nil {
				t.Fatal(err)
			}
			vecs, err := e.Embed(context.Background(), c.Call.Inputs)
			if !checkError(t, c.Expect.Error, err) {
				return
			}
			if len(vecs) != c.Expect.Vectors {
				t.Fatalf("%d vectors, want %d", len(vecs), c.Expect.Vectors)
			}
			for i, v := range vecs {
				if len(v) != c.Expect.Dimensions {
					t.Errorf("vector %d width %d, want %d", i, len(v), c.Expect.Dimensions)
				}
			}
		})
	}
}

// checkError reports whether the call succeeded as the contract expects and
// the result should be checked further.
func checkError(t *testing.T, want string, err error) bool {
	t.Helper()
	switch want {
	case "":
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return true
	case "auth":
		if !errors.Is(err, provider.ErrAuth) {
			t.Fatalf("want ErrAuth, got %v", err)
		}
	case "provider":
		if err == nil || provider.Reason(err) != provider.ReasonProviderError {
			t.Fatalf("want a provider error, got %v", err)
		}
	default:
		t.Fatalf("unknown expected error %q", want)
	}
	return false
}

// The embeddings contract puts the vectors back in input order.
func TestEmbed_ordersByIndex(t *testing.T) {
	c := contract.Load(t, "testdata/contracts/openai_embeddings/success.json")
	srv := c.Serve(t)
	e, err := openai.NewEmbedder(srv.URL, c.Call.Settings.Model, c.Call.Settings.Credential)
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := e.Embed(context.Background(), c.Call.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	// index 0 was generated with seed 1: element 0 is 1/97 - 0.5.
	if got, want := vecs[0][0], float32(0.0103-0.5); got > want+0.001 || got < want-0.001 {
		t.Fatalf("vector 0 starts %v, want about %v", got, want)
	}
}

func TestNew_requiredSettings(t *testing.T) {
	cases := []struct {
		name string
		s    provider.Settings
	}{
		{"openai without model", provider.Settings{Kind: provider.OpenAI, Credential: "test-key-1"}},
		{"openai without key", provider.Settings{Kind: provider.OpenAI, Model: "gpt-4.1"}},
		{"azure without base URL", provider.Settings{Kind: provider.AzureOpenAI, Deployment: "chat", Credential: "test-key-1"}},
		{"azure without deployment", provider.Settings{Kind: provider.AzureOpenAI, BaseURL: "https://example.org", Credential: "test-key-1"}},
		{"azure without key", provider.Settings{Kind: provider.AzureOpenAI, BaseURL: "https://example.org", Deployment: "chat"}},
		{"compatible without base URL", provider.Settings{Kind: provider.OpenAICompatible, Model: "m"}},
		{"compatible without model", provider.Settings{Kind: provider.OpenAICompatible, BaseURL: "https://example.org"}},
		{"another kind", provider.Settings{Kind: provider.Gemini, Model: "m", Credential: "test-key-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := openai.New(tc.s); !errors.Is(err, provider.ErrNotConfigured) {
				t.Fatalf("want ErrNotConfigured, got %v", err)
			}
		})
	}
	if _, err := openai.New(provider.Settings{Kind: provider.OpenAICompatible, Model: "m", BaseURL: "https://example.org"}); err != nil {
		t.Fatalf("compatible without a key should build: %v", err)
	}
	if _, err := openai.NewEmbedder("", "", "test-key-1"); !errors.Is(err, provider.ErrNotConfigured) {
		t.Fatalf("embedder without model: want ErrNotConfigured, got %v", err)
	}
}
