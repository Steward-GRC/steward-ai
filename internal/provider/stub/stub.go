// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package stub holds local stand-ins for a generative provider and an
// embeddings backend. They never touch the network and their output is
// deterministic, so the whole pipeline (retrieval, caching, jobs, events)
// runs on a machine with no provider configured. They are for local runs
// only; nothing they produce is a real model answer or a meaningful
// embedding.
package stub

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/Steward-GRC/steward-ai/internal/provider"
)

// Generator answers every request with a labelled placeholder.
type Generator struct{}

var _ provider.Generator = Generator{}

// reviewFormatMarker appears in the review system prompt's output format;
// seeing it, the stub answers with one finding so a review has a visible
// result.
const reviewFormatMarker = `<finding section="SECTION_KEY_OR_EMPTY"`

var (
	userSectionKeyPattern    = regexp.MustCompile(`<section key="([^"]*)"`)
	currentSectionKeyPattern = regexp.MustCompile(`<current_section key="([^"]*)"`)
)

// Complete returns a placeholder fingerprinted by the system prompt and the
// last user turn. A "draft" call gets plain section text, a review-format
// call one <finding> block on the first section, anything else a paragraph.
// It reports no token usage.
func (Generator) Complete(_ context.Context, req provider.Request) (provider.Response, error) {
	last := lastUserContent(req.Messages)
	sum := sha256.Sum256([]byte(req.System + "\x00" + last))
	label := fmt.Sprintf(
		"[STUB AI RESPONSE: no AI provider is configured; "+
			"this is a deterministic local placeholder, not a real model answer. "+
			"fingerprint=%x]", sum[:6])

	if req.Operation == "draft" {
		sectionKey := ""
		if m := currentSectionKeyPattern.FindStringSubmatch(last); m != nil {
			sectionKey = m[1]
		}
		return provider.Response{Text: fmt.Sprintf(
			"%s This is the generated content for the %q section, standing in for the real draft "+
				"so the pipeline runs end-to-end. Configure an AI provider for real output.",
			label, sectionKey,
		)}, nil
	}

	if strings.Contains(req.System, reviewFormatMarker) {
		sectionKey := ""
		if keys := userSectionKeyPattern.FindAllStringSubmatch(last, -1); len(keys) > 0 {
			sectionKey = keys[0][1]
		}
		return provider.Response{Text: fmt.Sprintf(
			"<finding section=%q severity=\"info\">\n%s This finding stands in for a real review "+
				"so the pipeline runs end-to-end.\n<suggestion>Configure an AI provider "+
				"for a real review.</suggestion>\n</finding>\n",
			sectionKey, label,
		)}, nil
	}

	return provider.Response{Text: fmt.Sprintf(
		"%s\n\nThis stub stands in for the generated content for "+
			"the request below so the rest of the pipeline (retrieval, caching, "+
			"the async job flow, the message broker and the event stream) "+
			"can be exercised end-to-end. Configure an AI provider for real output.",
		label,
	)}, nil
}

func lastUserContent(msgs []provider.Message) string {
	for _, msg := range slices.Backward(msgs) {
		if msg.Role == provider.RoleUser {
			return msg.Content
		}
	}
	return ""
}

// Embedder returns a seeded hash of each text, provider.Dimensions wide and
// L2-normalised. Identical text embeds identically, so exact and
// near-duplicate lookups behave; unrelated text can collide, so it says
// nothing about real search quality.
type Embedder struct{}

var _ provider.Embedder = Embedder{}

// Embed returns one pseudo-embedding per text.
func (Embedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		results[i] = deterministicVector(text, provider.Dimensions)
	}
	return results, nil
}

func deterministicVector(text string, dims int) []float32 {
	v := make([]float32, dims)
	var counter uint32
	for i := 0; i < dims; {
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], counter)
		sum := sha256.Sum256(append([]byte(text), buf[:]...))
		for j := 0; j+4 <= len(sum) && i < dims; j += 4 {
			bits := binary.BigEndian.Uint32(sum[j : j+4])
			v[i] = float32(bits)/float32(math.MaxUint32)*2 - 1
			i++
		}
		counter++
	}
	var sumSq float64
	for _, f := range v {
		sumSq += float64(f) * float64(f)
	}
	norm := float32(math.Sqrt(sumSq))
	if norm > 0 {
		for i := range v {
			v[i] /= norm
		}
	}
	return v
}
