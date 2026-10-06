// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import "encoding/json"

// ReviseJobInputSection is one existing section in a REVISE job's input JSON.
// It has the same fields as ReviseSection so the two convert directly.
type ReviseJobInputSection struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Order   int    `json:"order"`
}

// ReviseJobInput is a REVISE job's input JSON (PolicyAIJob spec.input).
type ReviseJobInput struct {
	Instruction string                  `json:"instruction"`
	PolicyID    string                  `json:"policyId,omitempty"`
	VersionID   string                  `json:"versionId,omitempty"`
	Sections    []ReviseJobInputSection `json:"sections"`
	// Enrichments are the attached enrichments, as grounding. The opt-out and
	// library drive the operator's suggestion step, not Revise.Generate.
	Enrichments       *EnrichmentContext `json:"enrichments,omitempty"`
	EnrichmentOptOut  EnrichmentOptOut   `json:"enrichmentOptOut"`
	EnrichmentLibrary EnrichmentLibrary  `json:"enrichmentLibrary"`
}

// MarshalReviseJobInput encodes a ReviseJobInput for a job's input.
func MarshalReviseJobInput(in ReviseJobInput) (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UnmarshalReviseJobInput decodes a REVISE job's input.
func UnmarshalReviseJobInput(raw []byte) (ReviseJobInput, error) {
	var in ReviseJobInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return ReviseJobInput{}, err
	}
	return in, nil
}

// ToReviseRequest builds the Revise.Generate request from the input and the
// job spec's actor, category and snapshotted model.
func (in ReviseJobInput) ToReviseRequest(actorUserID, categoryID, modelID string) ReviseRequest {
	sections := make([]ReviseSection, 0, len(in.Sections))
	for _, s := range in.Sections {
		sections = append(sections, ReviseSection(s))
	}
	req := ReviseRequest{
		ActorUserID: actorUserID,
		CategoryID:  categoryID,
		PolicyID:    in.PolicyID,
		VersionID:   in.VersionID,
		Instruction: in.Instruction,
		Sections:    sections,
		ModelID:     modelID,
	}
	if in.Enrichments != nil {
		req.Enrichments = *in.Enrichments
	}
	return req
}
