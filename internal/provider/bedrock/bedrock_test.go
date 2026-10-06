// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package bedrock

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/internal/contract"
)

// isolateAWS keeps the developer's AWS files and variables out of the test.
func isolateAWS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_REGION", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_BEDROCK_RUNTIME"} {
		t.Setenv(k, "")
	}
}

func TestContracts(t *testing.T) {
	isolateAWS(t)
	for _, c := range contract.LoadDir(t, "testdata/contracts/bedrock") {
		t.Run(c.Name, func(t *testing.T) {
			srv := c.Serve(t)
			s := c.Call.Settings
			client, err := New(provider.Settings{
				Kind: provider.Bedrock, Model: s.Model, Region: s.Region,
				Credential: s.Credential, BaseURL: srv.URL,
			}, WithMaxAttempts(1))
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
	isolateAWS(t)
	for name, s := range map[string]provider.Settings{
		"no region":            {Kind: provider.Bedrock, Model: "amazon.nova-pro-v1:0"},
		"no model":             {Kind: provider.Bedrock, Region: "us-east-1"},
		"credential no colon":  {Kind: provider.Bedrock, Model: "m", Region: "us-east-1", Credential: "test-key-1"},
		"credential no id":     {Kind: provider.Bedrock, Model: "m", Region: "us-east-1", Credential: ":test-secret-1"},
		"credential no secret": {Kind: provider.Bedrock, Model: "m", Region: "us-east-1", Credential: "placeholder-access-key-0001:"},
	} {
		if _, err := New(s); !errors.Is(err, provider.ErrNotConfigured) {
			t.Errorf("%s: want ErrNotConfigured, got %v", name, err)
		}
	}
	if _, err := New(provider.Settings{Kind: provider.Bedrock, Model: "m", Region: "us-east-1"}); err != nil {
		t.Fatalf("no credential should fall back to the default chain: %v", err)
	}
}

func TestParseCredential_secretMayHoldColon(t *testing.T) {
	id, secret, err := parseCredential("placeholder-access-key-0001:part:two")
	if err != nil || id != "placeholder-access-key-0001" || secret != "part:two" {
		t.Fatalf("got %q %q %v", id, secret, err)
	}
}
