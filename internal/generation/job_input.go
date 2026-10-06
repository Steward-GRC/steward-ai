// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import "encoding/json"

// DraftJobInputSection is one template section in a DRAFT job's input JSON.
// The keys match the gateway's draft input, so the payload passes through
// unchanged.
type DraftJobInputSection struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Guidance string `json:"guidance,omitempty"`
	Order    int    `json:"order"`
}

// DraftJobInput is a DRAFT job's input JSON (PolicyAIJob spec.input).
type DraftJobInput struct {
	Title          string                 `json:"title,omitempty"`
	Brief          string                 `json:"brief"`
	Sections       []DraftJobInputSection `json:"sections"`
	ReferenceHints []string               `json:"referenceHints,omitempty"`
	// ReferenceDocuments are author-attached source documents, as extracted
	// text, for the draft to ground on.
	ReferenceDocuments []ReferenceDocument `json:"referenceDocuments,omitempty"`
	// Enrichments are the policy's attached enrichments, as grounding.
	Enrichments *EnrichmentContext `json:"enrichments,omitempty"`
	// EnrichmentOptOut and EnrichmentLibrary drive the suggestion step the
	// operator adds to the result; Draft.Generate doesn't read them. Absent
	// means every category is suggested against an empty library.
	EnrichmentOptOut  EnrichmentOptOut  `json:"enrichmentOptOut"`
	EnrichmentLibrary EnrichmentLibrary `json:"enrichmentLibrary"`
}

// MarshalJobInput encodes a DraftJobInput for a job's input.
func MarshalJobInput(in DraftJobInput) (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UnmarshalDraftJobInput decodes a DRAFT job's input.
func UnmarshalDraftJobInput(raw []byte) (DraftJobInput, error) {
	var in DraftJobInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return DraftJobInput{}, err
	}
	return in, nil
}

// ToDraftRequest builds the Draft.Generate request from the input and the
// job spec's actor, category and snapshotted model.
func (in DraftJobInput) ToDraftRequest(actorUserID, categoryID, modelID string) DraftRequest {
	sections := make([]DraftSection, 0, len(in.Sections))
	for _, s := range in.Sections {
		sections = append(sections, DraftSection(s))
	}
	req := DraftRequest{
		ActorUserID:        actorUserID,
		CategoryID:         categoryID,
		Title:              in.Title,
		Brief:              in.Brief,
		Sections:           sections,
		ReferenceHints:     in.ReferenceHints,
		ReferenceDocuments: in.ReferenceDocuments,
		ModelID:            modelID,
	}
	if in.Enrichments != nil {
		req.Enrichments = *in.Enrichments
	}
	return req
}
