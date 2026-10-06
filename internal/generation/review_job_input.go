// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import "encoding/json"

// ReviewJobInputSection is one draft section in a REVIEW job's input JSON.
// The keys match the gateway's review input, so the payload passes through
// unchanged.
type ReviewJobInputSection struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// ReviewJobInput is a REVIEW job's input JSON (PolicyAIJob spec.input).
type ReviewJobInput struct {
	Title             string                  `json:"title,omitempty"`
	Sections          []ReviewJobInputSection `json:"sections"`
	StandardsRefs     []string                `json:"standardsRefs,omitempty"`
	RelatedPolicyRefs []string                `json:"relatedPolicyRefs,omitempty"`
	// Enrichments are the attached enrichments, as grounding. The opt-out and
	// library drive the operator's suggestion step, not Review.Generate.
	Enrichments       *EnrichmentContext `json:"enrichments,omitempty"`
	EnrichmentOptOut  EnrichmentOptOut   `json:"enrichmentOptOut"`
	EnrichmentLibrary EnrichmentLibrary  `json:"enrichmentLibrary"`
}

// MarshalReviewJobInput encodes a ReviewJobInput for a job's input.
func MarshalReviewJobInput(in ReviewJobInput) (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UnmarshalReviewJobInput decodes a REVIEW job's input.
func UnmarshalReviewJobInput(raw []byte) (ReviewJobInput, error) {
	var in ReviewJobInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return ReviewJobInput{}, err
	}
	return in, nil
}

// ToReviewRequest builds the Review.Generate request from the input and the
// job spec's actor, category and snapshotted model.
func (in ReviewJobInput) ToReviewRequest(actorUserID, categoryID, modelID string) ReviewRequest {
	sections := make([]ReviewSection, 0, len(in.Sections))
	for _, s := range in.Sections {
		sections = append(sections, ReviewSection(s))
	}
	req := ReviewRequest{
		ActorUserID:       actorUserID,
		CategoryID:        categoryID,
		Title:             in.Title,
		Sections:          sections,
		StandardsRefs:     in.StandardsRefs,
		RelatedPolicyRefs: in.RelatedPolicyRefs,
		ModelID:           modelID,
	}
	if in.Enrichments != nil {
		req.Enrichments = *in.Enrichments
	}
	return req
}
