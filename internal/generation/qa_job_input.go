// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"encoding/json"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// QAJobInput is a QA job's input JSON (PolicyAIJob spec.input). Only the
// question travels here; the read scope the retrieval step runs under is on
// the job spec itself.
type QAJobInput struct {
	Question string `json:"question"`
}

// MarshalQAJobInput encodes a QAJobInput for a job's input.
func MarshalQAJobInput(in QAJobInput) (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UnmarshalQAJobInput decodes a QA job's input.
func UnmarshalQAJobInput(raw []byte) (QAJobInput, error) {
	var in QAJobInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return QAJobInput{}, err
	}
	return in, nil
}

// ToAnswerRequest builds the QA.Answer request from the input and the chunks
// retrieved for the job's question.
func (in QAJobInput) ToAnswerRequest(chunks []store.SearchResult) AnswerRequest {
	return AnswerRequest{
		Question: in.Question,
		Chunks:   chunks,
	}
}
