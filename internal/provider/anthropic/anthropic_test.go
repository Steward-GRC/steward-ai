// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/internal/contract"
)

func TestContracts(t *testing.T) {
	for _, c := range contract.LoadDir(t, "testdata/contracts/anthropic") {
		t.Run(c.Name, func(t *testing.T) {
			srv := c.Serve(t)
			client, err := New(provider.Settings{
				Kind:       provider.Anthropic,
				Model:      c.Call.Settings.Model,
				Credential: c.Call.Settings.Credential,
				BaseURL:    srv.URL,
			}, WithMaxRetries(0))
			if err != nil {
				t.Fatal(err)
			}
			req := provider.Request{
				System:         c.Call.System,
				MaxTokens:      c.Call.MaxTokens,
				Model:          c.Call.Model,
				EnableWebFetch: c.Call.EnableWebFetch,
			}
			for _, m := range c.Call.Messages {
				req.Messages = append(req.Messages, provider.Message{Role: provider.Role(m.Role), Content: m.Content})
			}
			resp, err := client.Complete(context.Background(), req)
			checkOutcome(t, c.Expect, resp, err)
		})
	}
}

func checkOutcome(t *testing.T, want contract.Expect, resp provider.Response, err error) {
	t.Helper()
	switch want.Error {
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
	default:
		t.Fatalf("unknown expected error %q", want.Error)
	}
	if resp.Text != want.Text {
		t.Errorf("text %q, want %q", resp.Text, want.Text)
	}
	if resp.InputTokens != want.InputTokens || resp.OutputTokens != want.OutputTokens {
		t.Errorf("tokens in=%d out=%d, want in=%d out=%d", resp.InputTokens, resp.OutputTokens, want.InputTokens, want.OutputTokens)
	}
}

func TestNew_requiresAPIKey(t *testing.T) {
	_, err := New(provider.Settings{Kind: provider.Anthropic})
	if !errors.Is(err, provider.ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}

// The adapter must send the settings' key and never pick one up from the
// environment the SDK would otherwise read.
func TestNew_ignoresEnvironmentCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "placeholder-env-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "placeholder-env-token")
	var gotKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotAuth = r.Header.Get("x-api-key"), r.Header.Get("Authorization")
		successResponse("ok")(w)
	}))
	defer srv.Close()
	c, err := New(provider.Settings{Credential: "test-key-1", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if gotKey != "test-key-1" || gotAuth != "" {
		t.Fatalf("x-api-key=%q Authorization=%q", gotKey, gotAuth)
	}
}

func TestComplete_modelPrecedence(t *testing.T) {
	cases := []struct {
		name, settings, request, want string
	}{
		{"default", "", "", DefaultModel},
		{"settings", "claude-opus-4-8", "", "claude-opus-4-8"},
		{"request wins", "claude-opus-4-8", "claude-sonnet-5", "claude-sonnet-5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured []capturedRequest
			srv := fakeMessagesServer(t, &captured, []func(w http.ResponseWriter){successResponse("ok")})
			defer srv.Close()
			c, err := New(provider.Settings{Credential: "test-key-1", BaseURL: srv.URL, Model: tc.settings})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Complete(context.Background(), provider.Request{Model: tc.request}); err != nil {
				t.Fatal(err)
			}
			if captured[0].Model != tc.want {
				t.Fatalf("model %q, want %q", captured[0].Model, tc.want)
			}
		})
	}
}

func TestWebFetchToolFor_versionByModel(t *testing.T) {
	cases := []struct {
		name        string
		modelID     string
		wantCurrent bool
	}{
		{"sonnet 5", "claude-sonnet-5", true},
		{"opus 4.8", "claude-opus-4-8", true},
		{"opus 4.7", "claude-opus-4-7", true},
		{"opus 4.6", "claude-opus-4-6", true},
		{"sonnet 4.6", "claude-sonnet-4-6", true},
		{"empty model id defaults to current", "", true},
		{"unrecognized model id defaults to current", "claude-some-future-model", true},
		{"sonnet 4.5 is legacy", "claude-sonnet-4-5-20250929", false},
		{"sonnet 4.0 dated snapshot is legacy", "claude-sonnet-4-20250514", false},
		{"opus 4.5 is legacy", "claude-opus-4-5-20251101", false},
		{"opus 4.1 is legacy", "claude-opus-4-1-20250805", false},
		{"opus 4.0 dated snapshot is legacy", "claude-opus-4-20250514", false},
		{"haiku is legacy", "claude-haiku-4-5-20251001", false},
		{"claude 3.x is legacy", "claude-3-5-sonnet-20241022", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := webFetchToolFor(tc.modelID)
			gotCurrent := tool.OfWebFetchTool20260209 != nil
			gotLegacy := tool.OfWebFetchTool20250910 != nil
			if gotCurrent == gotLegacy {
				t.Fatalf("expected exactly one of the two web_fetch variants set, got current=%v legacy=%v", gotCurrent, gotLegacy)
			}
			if gotCurrent != tc.wantCurrent {
				t.Fatalf("webFetchToolFor(%q): got current=%v, want current=%v", tc.modelID, gotCurrent, tc.wantCurrent)
			}
			if gotCurrent {
				if !tool.OfWebFetchTool20260209.MaxUses.Valid() || tool.OfWebFetchTool20260209.MaxUses.Value != webFetchMaxUses {
					t.Fatalf("expected max_uses=%d on web_fetch_20260209, got %+v", webFetchMaxUses, tool.OfWebFetchTool20260209.MaxUses)
				}
			} else {
				if !tool.OfWebFetchTool20250910.MaxUses.Valid() || tool.OfWebFetchTool20250910.MaxUses.Value != webFetchMaxUses {
					t.Fatalf("expected max_uses=%d on web_fetch_20250910, got %+v", webFetchMaxUses, tool.OfWebFetchTool20250910.MaxUses)
				}
			}
		})
	}
}

type capturedRequest struct {
	Model string `json:"model"`
	Tools []struct {
		Type string `json:"type"`
	} `json:"tools"`
}

func fakeMessagesServer(t *testing.T, captured *[]capturedRequest, responses []func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	call := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body capturedRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		*captured = append(*captured, body)
		if call >= len(responses) {
			t.Errorf("unexpected extra request (call %d, only %d responses configured)", call, len(responses))
			http.Error(w, "no response", http.StatusTeapot)
			return
		}
		responses[call](w)
		call++
	}))
}

func successResponse(text string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSEEvent(w, "message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            "msg_test",
				"type":          "message",
				"role":          "assistant",
				"model":         "claude-sonnet-5",
				"content":       []any{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage":         map[string]any{"input_tokens": 10, "output_tokens": 0},
			},
		})
		writeSSEEvent(w, "content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         0,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		writeSSEEvent(w, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": text},
		})
		writeSSEEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		writeSSEEvent(w, "message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 5},
		})
		writeSSEEvent(w, "message_stop", map[string]any{"type": "message_stop"})
	}
}

// writeSSEEvent writes one frame; the JSON must stay on one data line or the
// decoder drops the continuation lines.
func writeSSEEvent(w http.ResponseWriter, event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		panic(fmt.Sprintf("writeSSEEvent: marshal %s payload: %v", event, err))
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func webFetchRejectionResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{
		"type": "error",
		"error": {
			"type": "invalid_request_error",
			"message": "The web_fetch tool is not available for this authentication method."
		}
	}`))
}

func unrelatedErrorResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{
		"type": "error",
		"error": {
			"type": "invalid_request_error",
			"message": "max_tokens: field required"
		}
	}`))
}

func testClient(t *testing.T, srv *httptest.Server, modelID string) *Client {
	t.Helper()
	c, err := New(provider.Settings{Kind: provider.Anthropic, Model: modelID, BaseURL: srv.URL, Credential: "test-key-1"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestComplete_webFetch_attachedWhenEnabled(t *testing.T) {
	var captured []capturedRequest
	srv := fakeMessagesServer(t, &captured, []func(w http.ResponseWriter){
		successResponse("ok"),
	})
	defer srv.Close()

	c := testClient(t, srv, "claude-sonnet-5")
	resp, err := c.Complete(context.Background(), provider.Request{
		System:         "system",
		Messages:       []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
		EnableWebFetch: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("got text %q", resp.Text)
	}
	if len(captured) != 1 {
		t.Fatalf("expected exactly 1 request, got %d", len(captured))
	}
	if len(captured[0].Tools) != 1 || captured[0].Tools[0].Type != "web_fetch_20260209" {
		t.Fatalf("expected tools=[web_fetch_20260209] for claude-sonnet-5, got %+v", captured[0].Tools)
	}
}

func TestComplete_webFetch_omittedWhenDisabled(t *testing.T) {
	var captured []capturedRequest
	srv := fakeMessagesServer(t, &captured, []func(w http.ResponseWriter){
		successResponse("ok"),
	})
	defer srv.Close()

	c := testClient(t, srv, "claude-sonnet-5")
	_, err := c.Complete(context.Background(), provider.Request{
		System:   "system",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("expected exactly 1 request, got %d", len(captured))
	}
	if len(captured[0].Tools) != 0 {
		t.Fatalf("expected no tools attached, got %+v", captured[0].Tools)
	}
}

func TestComplete_webFetch_legacyModelUsesBasicToolVersion(t *testing.T) {
	var captured []capturedRequest
	srv := fakeMessagesServer(t, &captured, []func(w http.ResponseWriter){
		successResponse("ok"),
	})
	defer srv.Close()

	c := testClient(t, srv, "claude-3-5-sonnet-20241022")
	_, err := c.Complete(context.Background(), provider.Request{
		System:         "system",
		Messages:       []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
		EnableWebFetch: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(captured) != 1 || len(captured[0].Tools) != 1 || captured[0].Tools[0].Type != "web_fetch_20250910" {
		t.Fatalf("expected tools=[web_fetch_20250910] for a legacy model, got %+v", captured)
	}
}

func TestComplete_webFetch_degradesGracefullyOnToolRejection(t *testing.T) {
	var captured []capturedRequest
	srv := fakeMessagesServer(t, &captured, []func(w http.ResponseWriter){
		webFetchRejectionResponse,
		successResponse("degraded ok"),
	})
	defer srv.Close()

	c := testClient(t, srv, "claude-sonnet-5")
	resp, err := c.Complete(context.Background(), provider.Request{
		System:         "system",
		Messages:       []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
		EnableWebFetch: true,
	})
	if err != nil {
		t.Fatalf("expected the retry-without-tool to succeed, got error: %v", err)
	}
	if resp.Text != "degraded ok" {
		t.Fatalf("got text %q", resp.Text)
	}
	if len(captured) != 2 {
		t.Fatalf("expected exactly 2 requests (original + retry), got %d", len(captured))
	}
	if len(captured[0].Tools) != 1 || captured[0].Tools[0].Type != "web_fetch_20260209" {
		t.Fatalf("expected the first request to carry web_fetch_20260209, got %+v", captured[0].Tools)
	}
	if len(captured[1].Tools) != 0 {
		t.Fatalf("expected the retry to omit tools entirely, got %+v", captured[1].Tools)
	}
}

func TestComplete_webFetch_unrelatedErrorNeverRetriedOrSwallowed(t *testing.T) {
	var captured []capturedRequest
	srv := fakeMessagesServer(t, &captured, []func(w http.ResponseWriter){
		unrelatedErrorResponse,
	})
	defer srv.Close()

	c := testClient(t, srv, "claude-sonnet-5")
	_, err := c.Complete(context.Background(), provider.Request{
		System:         "system",
		Messages:       []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
		EnableWebFetch: true,
	})
	if err == nil {
		t.Fatal("expected an error for an unrelated 4xx, got nil")
	}
	if len(captured) != 1 {
		t.Fatalf("expected no retry for an unrelated error, got %d requests", len(captured))
	}
}

func TestIsWebFetchToolRejection(t *testing.T) {
	mkErr := func(status int, body string) *sdk.Error {
		aerr := &sdk.Error{StatusCode: status}
		if body != "" {
			if err := aerr.UnmarshalJSON([]byte(body)); err != nil {
				t.Fatalf("unmarshal synthetic error: %v", err)
			}
		}
		return aerr
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			"400 mentioning web_fetch",
			mkErr(400, `{"type":"error","error":{"type":"invalid_request_error","message":"web_fetch tool is not permitted"}}`),
			true,
		},
		{
			"403 mentioning web fetch (spaced)",
			mkErr(403, `{"type":"error","error":{"type":"permission_error","message":"Web fetch is not enabled for this token"}}`),
			true,
		},
		{
			"400 unrelated",
			mkErr(400, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: field required"}}`),
			false,
		},
		{
			"429 rate limit even if it mentioned web_fetch would still not match (5xx/429 excluded)",
			mkErr(429, `{"type":"error","error":{"type":"rate_limit_error","message":"web_fetch rate limited"}}`),
			false,
		},
		{
			"500 server error",
			mkErr(500, `{"type":"error","error":{"type":"api_error","message":"internal error"}}`),
			false,
		},
		{"non-API error", errors.New("network timeout"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWebFetchToolRejection(tc.err); got != tc.want {
				t.Fatalf("isWebFetchToolRejection(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A refused credential needs a person to fix it; anything else may clear by
// itself.
func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"unauthorized", &sdk.Error{StatusCode: 401}, provider.ReasonAuthFailed},
		{"forbidden", &sdk.Error{StatusCode: 403}, provider.ReasonAuthFailed},
		{"rate limited", &sdk.Error{StatusCode: 429}, provider.ReasonProviderError},
		{"non-api error", errors.New("network timeout"), provider.ReasonProviderError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := provider.Reason(classify(tc.err)); got != tc.want {
				t.Fatalf("Reason(classify(%v)) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
