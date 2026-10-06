// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package build_test

import (
	"errors"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/anthropic"
	"github.com/Steward-GRC/steward-ai/internal/provider/bedrock"
	"github.com/Steward-GRC/steward-ai/internal/provider/build"
	"github.com/Steward-GRC/steward-ai/internal/provider/gemini"
	"github.com/Steward-GRC/steward-ai/internal/provider/openai"
	"github.com/Steward-GRC/steward-ai/internal/provider/stub"
	"github.com/Steward-GRC/steward-ai/internal/provider/tei"
)

func TestNew_picksAdapterByKind(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/credentials")
	key := "test-key-1"
	cases := []struct {
		s    provider.Settings
		want func(provider.Generator) bool
	}{
		{provider.Settings{Kind: provider.Anthropic, Credential: key},
			func(g provider.Generator) bool { _, ok := g.(*anthropic.Client); return ok }},
		{provider.Settings{Kind: provider.OpenAI, Model: "gpt-4.1", Credential: key},
			func(g provider.Generator) bool { _, ok := g.(*openai.Client); return ok }},
		{provider.Settings{Kind: provider.AzureOpenAI, BaseURL: "https://example.org", Deployment: "chat", Credential: key},
			func(g provider.Generator) bool { _, ok := g.(*openai.Client); return ok }},
		{provider.Settings{Kind: provider.OpenAICompatible, BaseURL: "https://example.net/v1", Model: "m"},
			func(g provider.Generator) bool { _, ok := g.(*openai.Client); return ok }},
		{provider.Settings{Kind: provider.Gemini, Model: "gemini-2.5-flash", Credential: key},
			func(g provider.Generator) bool { _, ok := g.(*gemini.Client); return ok }},
		{provider.Settings{Kind: provider.Bedrock, Model: "amazon.nova-pro-v1:0", Region: "us-east-1", Credential: "placeholder-access-key-0001:test-secret-1"},
			func(g provider.Generator) bool { _, ok := g.(*bedrock.Client); return ok }},
	}
	if len(cases) != len(provider.Kinds) {
		t.Fatalf("%d cases for %d kinds", len(cases), len(provider.Kinds))
	}
	for _, tc := range cases {
		t.Run(string(tc.s.Kind), func(t *testing.T) {
			g, err := build.New(tc.s)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.want(g) {
				t.Fatalf("wrong adapter %T", g)
			}
		})
	}
}

func TestNew_notConfigured(t *testing.T) {
	for _, s := range []provider.Settings{
		{},
		{Kind: "watsonx", Model: "m", Credential: "test-key-1"},
		{Kind: provider.Anthropic},
	} {
		if _, err := build.New(s); !errors.Is(err, provider.ErrNotConfigured) || provider.Reason(err) != provider.ReasonNotConfigured {
			t.Errorf("%+v: want ErrNotConfigured, got %v", s, err)
		}
	}
}

func TestNewEmbedder(t *testing.T) {
	e, err := build.NewEmbedder(build.EmbedTEI, "http://embeddings.example.org/embed", "", "")
	if _, ok := e.(*tei.Embedder); err != nil || !ok {
		t.Fatalf("tei: %T %v", e, err)
	}
	e, err = build.NewEmbedder(build.EmbedOpenAI, "https://example.net/v1", "bge-small-en-v1.5", "")
	if _, ok := e.(*openai.Embedder); err != nil || !ok {
		t.Fatalf("openai: %T %v", e, err)
	}
	e, err = build.NewEmbedder(build.EmbedStub, "", "", "")
	if _, ok := e.(stub.Embedder); err != nil || !ok {
		t.Fatalf("stub: %T %v", e, err)
	}
	for _, args := range [][4]string{
		{"", "", "", ""},
		{"bedrock", "", "amazon.titan-embed-text-v2:0", ""},
		{build.EmbedTEI, "", "", ""},
		{build.EmbedOpenAI, "", "", "test-key-1"},
	} {
		if _, err := build.NewEmbedder(args[0], args[1], args[2], args[3]); !errors.Is(err, provider.ErrNotConfigured) {
			t.Errorf("%v: want ErrNotConfigured, got %v", args, err)
		}
	}
}
