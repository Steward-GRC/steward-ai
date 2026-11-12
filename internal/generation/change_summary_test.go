// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"context"
	"strings"
	"testing"
)

func TestChangeSummarizer_parsesBulletsAndSendsTheDiff(t *testing.T) {
	llm := &stubLLMClient{response: "- Raises the retention period to 7 years.\n* Adds an exception for legal holds.\n\nnot a bullet"}
	got, err := NewChangeSummarizer(llm).Summarize(context.Background(), ChangeSummaryRequest{
		FromVersionID: "v1", ToVersionID: "v2", Diff: "-retain 5 years\n+retain 7 years",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"Raises the retention period to 7 years.", "Adds an exception for legal holds."}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("bullets = %q, want %q", got, want)
	}
	if !strings.Contains(llm.lastReq.Messages[0].Content, "+retain 7 years") {
		t.Fatalf("the diff was not sent to the model: %q", llm.lastReq.Messages[0].Content)
	}
}

func TestChangeSummarizer_emptyDiffIsRefusedWithoutACall(t *testing.T) {
	llm := &stubLLMClient{response: "- x"}
	if _, err := NewChangeSummarizer(llm).Summarize(context.Background(), ChangeSummaryRequest{FromVersionID: "v1", ToVersionID: "v2"}); err == nil {
		t.Fatal("expected an error for an empty diff")
	}
	if llm.lastReq.Operation != "" {
		t.Fatal("the model must not be called for an empty diff")
	}
}

func TestChangeSummarizer_noBulletsIsAnError(t *testing.T) {
	llm := &stubLLMClient{response: "I cannot help with that."}
	if _, err := NewChangeSummarizer(llm).Summarize(context.Background(), ChangeSummaryRequest{FromVersionID: "v1", ToVersionID: "v2", Diff: "+a"}); err == nil {
		t.Fatal("expected an error when the model returns no bullets")
	}
}

func TestChangeSummaryKey_isStableAndDiffSensitive(t *testing.T) {
	a := ChangeSummaryKey("v1", "v2", "+a")
	if a != ChangeSummaryKey("v1", "v2", "+a") {
		t.Fatal("key is not stable")
	}
	if a == ChangeSummaryKey("v1", "v2", "+b") || a == ChangeSummaryKey("v2", "v1", "+a") {
		t.Fatal("key must change with the diff and the version order")
	}
}
