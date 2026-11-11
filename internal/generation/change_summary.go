// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/provider"
)

// SummaryScope says which summary a SUMMARIZE job asks for.
type SummaryScope string

// The summary scopes a SUMMARIZE job can carry.
const (
	// SummaryScopeChanges summarises the diff between two versions.
	SummaryScopeChanges SummaryScope = "changes"
	// SummaryScopeVersion reads or regenerates one version's stored summary.
	SummaryScopeVersion SummaryScope = "version"
)

// SummaryJobInput is a SUMMARIZE job's input JSON (PolicyAIJob spec.input).
// The caller (the gateway) has already checked it may read the versions and
// leaves obfuscated content out of Diff and Text; this service never sees a
// version it was not handed.
type SummaryJobInput struct {
	Scope SummaryScope `json:"scope"`
	// Regenerate skips the stored summary and replaces it.
	Regenerate bool `json:"regenerate,omitempty"`

	// Changes scope.
	FromVersionID string `json:"fromVersionId,omitempty"`
	ToVersionID   string `json:"toVersionId,omitempty"`
	Diff          string `json:"diff,omitempty"`

	// Version scope.
	VersionID   string `json:"versionId,omitempty"`
	PolicyID    string `json:"policyId,omitempty"`
	PolicyTitle string `json:"policyTitle,omitempty"`
	Text        string `json:"text,omitempty"`
}

// SummaryJobResult is a SUMMARIZE job's result. Cached is true when the
// stored summary was returned instead of a new one being written.
type SummaryJobResult struct {
	Scope SummaryScope `json:"scope"`
	// Bullets is set for the changes scope.
	Bullets []string `json:"bullets,omitempty"`
	// SummaryText is set for the version scope, as the stored Markdown bullets.
	SummaryText string `json:"summaryText,omitempty"`
	GeneratedAt string `json:"generatedAt"`
	Cached      bool   `json:"cached"`
}

// ChangeSummaryRequest is the input to ChangeSummarizer.Summarize.
type ChangeSummaryRequest struct {
	FromVersionID string
	ToVersionID   string
	Diff          string
}

// ChangeSummaryKey names the stored summary for a version pair and the exact
// diff it summarised, so a different diff never matches.
func ChangeSummaryKey(from, to, diff string) string {
	h := sha256.Sum256([]byte(from + "\x00" + to + "\x00" + diff))
	return "ai:summary:changes:" + hex.EncodeToString(h[:])
}

const changeSummarySystemPrompt = `You are a change-summary assistant for a policy management system.

Your role is to tell a reader what changed between two versions of a policy, from a diff.

Rules:
1. Output ONLY a Markdown bulleted list: 2 to 6 bullets, each starting with "- ".
2. Each bullet is one short sentence about one distinct change, in plain language.
3. Describe only what the diff shows. Do not guess at intent, and do not add changes that are not in it.
4. Do NOT include a heading, preamble, closing remark, or any text outside the bullets.`

// ChangeSummarizer summarises the difference between two policy versions.
type ChangeSummarizer struct {
	gen provider.Generator
}

// NewChangeSummarizer returns a ChangeSummarizer over gen.
func NewChangeSummarizer(gen provider.Generator) *ChangeSummarizer {
	return &ChangeSummarizer{gen: gen}
}

// Summarize returns the bullets for req's diff. An empty diff or a reply with
// no bullets is an error, so nothing empty is ever stored.
func (c *ChangeSummarizer) Summarize(ctx context.Context, req ChangeSummaryRequest) ([]string, error) {
	if strings.TrimSpace(req.Diff) == "" {
		return nil, errors.New("change summary: empty diff")
	}
	resp, err := c.gen.Complete(ctx, provider.Request{
		System: changeSummarySystemPrompt,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: fmt.Sprintf(
			"Diff between two versions of a policy (lines starting with - were removed, + were added):\n%s\n\n"+
				"Summarise the changes as concise Markdown bullet points (\"- \").", req.Diff)}},
		MaxTokens: airules.SummarizeMaxTokens,
		Operation: "change_summary",
	})
	if err != nil {
		return nil, fmt.Errorf("change summary: complete: %w", err)
	}
	bullets := parseBullets(resp.Text)
	if len(bullets) == 0 {
		return nil, errors.New("change summary: the model returned no bullets")
	}
	return bullets, nil
}

func parseBullets(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "* ") {
			continue
		}
		if b := strings.TrimSpace(line[2:]); b != "" {
			out = append(out, b)
		}
	}
	return out
}
