// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package tei is the embeddings adapter for a Text Embeddings Inference
// server, the default embeddings backend. The served model must emit
// provider.Dimensions-wide vectors (BAAI/bge-small-en-v1.5 does).
package tei

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/provider"
)

// Embedder posts batches to a TEI server's /embed route.
type Embedder struct {
	client   *http.Client
	endpoint string
	dims     int
	apiKey   string
}

var _ provider.Embedder = (*Embedder)(nil)

// Option adjusts an Embedder.
type Option func(*Embedder)

// WithAPIKey sends key as a Bearer token, for a server started with an API
// key.
func WithAPIKey(key string) Option { return func(e *Embedder) { e.apiKey = key } }

// New builds an embedder that posts to endpoint, the full /embed URL. Embed
// rejects vectors that aren't dims wide, catching a server running another
// model before a wrong-width vector reaches the schema; 0 skips the check.
//
// The HTTP timeout comes from AI_EMBED_TIMEOUT_SECONDS: a CPU-bound server
// needs well over 30 seconds for a large document's chunks, and a timeout
// there would requeue the indexing message forever.
func New(endpoint string, dims int, opts ...Option) *Embedder {
	e := &Embedder{
		client:   &http.Client{Timeout: embedTimeout()},
		endpoint: endpoint,
		dims:     dims,
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

const embedTimeoutEnvVar = airules.EmbedTimeoutEnvVar

const defaultEmbedTimeoutSeconds = airules.EmbedTimeoutSecondsDefault

func embedTimeout() time.Duration {
	return embedTimeoutFromEnv(os.Getenv(embedTimeoutEnvVar))
}

// embedTimeoutFromEnv falls back to the default on an empty, unparseable or
// non-positive value.
func embedTimeoutFromEnv(raw string) time.Duration {
	if raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultEmbedTimeoutSeconds * time.Second
}

// embedRequest always asks the server to truncate: the chunker counts words,
// not tokens, so a chunk can pass the model's 512-token limit, and one long
// chunk must not fail a whole section.
type embedRequest struct {
	Inputs   []string `json:"inputs"`
	Truncate bool     `json:"truncate"`
}

// Embed sends the whole batch in one call; the answer is a bare array of
// vectors in input order.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Inputs: texts, Truncate: true})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: tei request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embed: read tei response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("embed: tei request: status %d: %w", resp.StatusCode, provider.ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embed: tei request: status %d: %s", resp.StatusCode, string(respBody))
	}

	var results [][]float32
	if err := json.Unmarshal(respBody, &results); err != nil {
		return nil, fmt.Errorf("embed: unmarshal tei response: %w", err)
	}
	if len(results) != len(texts) {
		return nil, fmt.Errorf("embed: tei response length mismatch: got %d vectors for %d inputs", len(results), len(texts))
	}
	if e.dims > 0 {
		for i, v := range results {
			if len(v) != e.dims {
				return nil, fmt.Errorf("embed: tei response dimension mismatch: vector %d has width %d, expected %d", i, len(v), e.dims)
			}
		}
	}
	return results, nil
}
