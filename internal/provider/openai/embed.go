// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Bugs5382/go-log"

	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/internal/httpjson"
)

// Embedder embeds text through an OpenAI or OpenAI-compatible embeddings
// endpoint, asking for provider.Dimensions-wide vectors.
type Embedder struct {
	endpoint string
	headers  map[string]string
	model    string
	http     *http.Client
	log      log.Logger
}

var _ provider.Embedder = (*Embedder)(nil)

// NewEmbedder builds an embedder. baseURL empty uses DefaultBaseURL; the
// model is required; the key, when set, is sent as Bearer.
func NewEmbedder(baseURL, model, credential string, opts ...Option) (*Embedder, error) {
	if model == "" {
		return nil, fmt.Errorf("openai embeddings: model is required: %w", provider.ErrNotConfigured)
	}
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	cfg := newConfig(opts)
	e := &Embedder{endpoint: base + "/embeddings", model: model, http: cfg.httpClient, log: cfg.logger, headers: map[string]string{}}
	if credential != "" {
		e.headers["Authorization"] = "Bearer " + credential
	}
	return e, nil
}

type embedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	Dimensions     int      `json:"dimensions"`
	EncodingFormat string   `json:"encoding_format"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns one vector per text, in input order. A server that ignores
// the dimensions field and answers another width is an error, since the
// schema stores provider.Dimensions.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	start := time.Now()
	var out embedResponse
	err := httpjson.Post(ctx, e.http, e.endpoint, e.headers, embedRequest{
		Model: e.model, Input: texts, Dimensions: provider.Dimensions, EncodingFormat: "float",
	}, &out)
	if err != nil {
		err = classify("openai embeddings", err)
		e.log.Ctx(ctx).Warn("embeddings call failed", log.F("model", e.model), log.F("inputs", len(texts)),
			log.F("duration_ms", time.Since(start).Milliseconds()), log.F("error", err.Error()))
		return nil, err
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("openai embeddings: got %d vectors for %d inputs", len(out.Data), len(texts))
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) || vecs[d.Index] != nil {
			return nil, fmt.Errorf("openai embeddings: bad or repeated index %d", d.Index)
		}
		if len(d.Embedding) != provider.Dimensions {
			return nil, fmt.Errorf("openai embeddings: vector %d has width %d, want %d", d.Index, len(d.Embedding), provider.Dimensions)
		}
		vecs[d.Index] = d.Embedding
	}
	e.log.Ctx(ctx).Debug("embeddings call done", log.F("model", e.model), log.F("inputs", len(texts)),
		log.F("duration_ms", time.Since(start).Milliseconds()))
	return vecs, nil
}
