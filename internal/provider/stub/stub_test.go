// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package stub_test

import (
	"context"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/stub"
)

func TestStubEmbedder_dims(t *testing.T) {
	e := stub.Embedder{}
	vecs, err := e.Embed(context.Background(), []string{"hello", "hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != provider.Dimensions {
			t.Fatalf("vector %d: expected dim %d, got %d", i, provider.Dimensions, len(v))
		}
	}
	if vecs[0][0] != vecs[1][0] || vecs[0][len(vecs[0])-1] != vecs[1][len(vecs[1])-1] {
		t.Fatal("identical text should produce identical stub vectors")
	}
}

func TestStubEmbedder_normalisedAndDistinct(t *testing.T) {
	vecs, _ := stub.Embedder{}.Embed(context.Background(), []string{"hello", "world"})
	var sumSq float64
	for _, f := range vecs[0] {
		sumSq += float64(f) * float64(f)
	}
	if math.Abs(sumSq-1) > 1e-4 {
		t.Fatalf("vector norm squared %v, want 1", sumSq)
	}
	if vecs[0][0] == vecs[1][0] && vecs[0][1] == vecs[1][1] {
		t.Fatal("different text should give different vectors")
	}
}

var findingPattern = regexp.MustCompile(`<finding section="([^"]*)" severity="info">`)

// A stub review returns a non-empty, visible finding, the way a stub draft
// returns per-section content.
func TestStubComplete_reviewFormat_emitsAFindingBlock(t *testing.T) {
	resp, err := stub.Generator{}.Complete(context.Background(), provider.Request{
		System: "Report each issue as:\n" + `<finding section="SECTION_KEY_OR_EMPTY" severity="info|warning|critical">`,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: `<policy>` +
			`<section key="purpose" title="Purpose">Body.</section>` +
			`<section key="scope" title="Scope">Body.</section></policy>`}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	findings := findingPattern.FindAllStringSubmatch(resp.Text, -1)
	if len(findings) != 1 || findings[0][1] != "purpose" {
		t.Fatalf("expected 1 stub finding on section \"purpose\", got %q", resp.Text)
	}
}

func TestStubComplete_draftSection(t *testing.T) {
	resp, err := stub.Generator{}.Complete(context.Background(), provider.Request{
		Operation: "draft",
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: `<current_section key="scope" title="Scope">`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, `"scope" section`) || strings.Contains(resp.Text, "<") {
		t.Fatalf("want plain section text for scope, got %q", resp.Text)
	}
}

func TestStubComplete_labelledAndDeterministic(t *testing.T) {
	req := provider.Request{System: "s", Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "first"},
		{Role: provider.RoleAssistant, Content: "reply"},
		{Role: provider.RoleUser, Content: "second"},
	}}
	a, _ := stub.Generator{}.Complete(context.Background(), req)
	b, _ := stub.Generator{}.Complete(context.Background(), req)
	if a.Text != b.Text {
		t.Fatal("same request should give the same text")
	}
	if !strings.HasPrefix(a.Text, "[STUB AI RESPONSE") {
		t.Fatalf("stub text must be labelled, got %q", a.Text)
	}
	if a.InputTokens != 0 || a.OutputTokens != 0 {
		t.Fatal("the stub uses no tokens")
	}
	req.Messages[2].Content = "third"
	c, _ := stub.Generator{}.Complete(context.Background(), req)
	if c.Text == a.Text {
		t.Fatal("the fingerprint should follow the last user turn")
	}
}
