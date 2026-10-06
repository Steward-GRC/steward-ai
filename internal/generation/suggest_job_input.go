// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"encoding/json"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// SuggestJobInputSection is one content section in a SUGGEST_ENRICHMENTS
// job's input JSON.
type SuggestJobInputSection struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// SuggestJobInput is a SUGGEST_ENRICHMENTS job's input JSON (PolicyAIJob
// spec.input).
type SuggestJobInput struct {
	Title    string                   `json:"title,omitempty"`
	Sections []SuggestJobInputSection `json:"sections"`
	// OptOut is the per-category opt-out; absent suggests every category.
	OptOut EnrichmentOptOut `json:"optOut"`
	// Library is the existing corpus for attach-existing dedupe.
	Library EnrichmentLibrary `json:"library"`
}

// MarshalSuggestJobInput encodes a SuggestJobInput for a job's input.
func MarshalSuggestJobInput(in SuggestJobInput) (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UnmarshalSuggestJobInput decodes a SUGGEST_ENRICHMENTS job's input.
func UnmarshalSuggestJobInput(raw []byte) (SuggestJobInput, error) {
	var in SuggestJobInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return SuggestJobInput{}, err
	}
	return in, nil
}

// ToSuggestRequest builds the Suggester.Suggest request from the input and
// the job spec's policy, snapshotted model and read scope.
func (in SuggestJobInput) ToSuggestRequest(policyID, modelID string, access store.AccessFilter) SuggestRequest {
	sections := make([]SuggestSection, 0, len(in.Sections))
	for _, s := range in.Sections {
		sections = append(sections, SuggestSection(s))
	}
	return SuggestRequest{
		PolicyID: policyID,
		Title:    in.Title,
		Sections: sections,
		OptOut:   in.OptOut,
		Library:  in.Library,
		Access:   access,
		ModelID:  modelID,
	}
}

// SuggestEnrichmentsResult is a SUGGEST_ENRICHMENTS job's whole result.
type SuggestEnrichmentsResult struct {
	Suggestions EnrichmentSuggestions `json:"suggestions"`
}
