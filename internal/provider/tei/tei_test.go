// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package tei

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-ai/internal/provider"
)

func TestTEIEmbedder_embedsBatchInOneCall(t *testing.T) {
	var gotBody embedRequest
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		resp := make([][]float32, len(gotBody.Inputs))
		for i := range gotBody.Inputs {
			resp[i] = make([]float32, provider.Dimensions)
			resp[i][0] = float32(i + 1)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	e := New(srv.URL, provider.Dimensions)
	vecs, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 HTTP call for the whole batch, got %d", calls)
	}
	if len(gotBody.Inputs) != 2 {
		t.Fatalf("expected 2 inputs sent in one request, got %d", len(gotBody.Inputs))
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != provider.Dimensions {
			t.Fatalf("vector %d: expected dim %d, got %d", i, provider.Dimensions, len(v))
		}
	}
}

func TestTEIEmbedder_lengthMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return one fewer vector than requested inputs.
		resp := [][]float32{make([]float32, provider.Dimensions)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := New(srv.URL, provider.Dimensions)
	_, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err == nil {
		t.Fatal("expected error for response/input length mismatch")
	}
}

func TestTEIEmbedder_dimensionMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Right vector count, wrong width — simulates a misconfigured TEI
		// endpoint serving a different model than expected.
		resp := [][]float32{make([]float32, provider.Dimensions+1)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := New(srv.URL, provider.Dimensions)
	_, err := e.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error for vector width mismatch")
	}
}

func TestTEIEmbedder_dimensionCheckDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Wrong width, but dims==0 in the client means "don't check".
		resp := [][]float32{make([]float32, provider.Dimensions+1)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := New(srv.URL, 0)
	vecs, err := e.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vecs[0]) != provider.Dimensions+1 {
		t.Fatalf("expected unchecked width %d, got %d", provider.Dimensions+1, len(vecs[0]))
	}
}

// TestTEIEmbedder_setsTruncateTrue confirms every request asks TEI to
// truncate oversized inputs (truncate:true) rather than reject/mishandle
// them: the word-based chunker only approximates a token count, so an
// individual chunk can still exceed bge-small-en-v1.5's 512-token max
// sequence length, and indexing must not fail an entire section over it.
func TestTEIEmbedder_setsTruncateTrue(t *testing.T) {
	var gotBody embedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		resp := [][]float32{make([]float32, provider.Dimensions)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := New(srv.URL, provider.Dimensions)
	if _, err := e.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gotBody.Truncate {
		t.Fatal("expected embedRequest.Truncate to be true")
	}
}

// TestEmbedTimeoutFromEnv covers the AI_EMBED_TIMEOUT_SECONDS parsing rules:
// default 120s, env override, and fallback to the default on anything
// non-positive or unparseable (so a bad env value can't leave the client
// with a zero/negative timeout).
func TestEmbedTimeoutFromEnv(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"unset", "", 120 * time.Second},
		{"override", "45", 45 * time.Second},
		{"zero falls back", "0", 120 * time.Second},
		{"negative falls back", "-5", 120 * time.Second},
		{"unparseable falls back", "not-a-number", 120 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := embedTimeoutFromEnv(tc.raw)
			if got != tc.want {
				t.Fatalf("embedTimeoutFromEnv(%q): got %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestNewTEIEmbedder_timeoutFromEnv confirms NewTEIEmbedder actually wires
// the parsed AI_EMBED_TIMEOUT_SECONDS value into the HTTP client (not just
// that the parsing helper works in isolation).
func TestNewTEIEmbedder_timeoutFromEnv(t *testing.T) {
	t.Setenv(embedTimeoutEnvVar, "45")
	e := New("http://example.invalid/embed", provider.Dimensions)
	if e.client.Timeout != 45*time.Second {
		t.Fatalf("client timeout: got %v, want 45s", e.client.Timeout)
	}
}

// TestNewTEIEmbedder_defaultTimeout confirms the default (120s, up from the
// old hardcoded 30s that was too short for CPU-bound batch embedding and
// caused the requeue-forever poison loop) applies when the env var is unset.
func TestNewTEIEmbedder_defaultTimeout(t *testing.T) {
	e := New("http://example.invalid/embed", provider.Dimensions)
	if e.client.Timeout != defaultEmbedTimeoutSeconds*time.Second {
		t.Fatalf("client timeout: got %v, want %v", e.client.Timeout, defaultEmbedTimeoutSeconds*time.Second)
	}
}

func TestTEIEmbedder_nonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	e := New(srv.URL, provider.Dimensions)
	_, err := e.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error for non-2xx status")
	}
}
