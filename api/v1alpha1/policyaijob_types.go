// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// JobOperation is the kind of job. It mirrors steward.ai.v1.JobOperation as
// a plain string, so the resource carries no proto dependency.
// +kubebuilder:validation:Enum=DRAFT;REWRITE;CLARIFY;SUMMARIZE;REVIEW;QA;REVISE;RELATED_REEVAL;SUGGEST_ENRICHMENTS;RELATIONSHIP_LEARN
type JobOperation string

const (
	// OperationDraft drafts a whole policy from a brief and its template
	// sections.
	OperationDraft JobOperation = "DRAFT"
	// OperationRewrite rewrites one editable region.
	OperationRewrite JobOperation = "REWRITE"
	// OperationClarify clarifies or expands one editable region.
	OperationClarify JobOperation = "CLARIFY"
	// OperationSummarize summarises a policy or a section.
	OperationSummarize JobOperation = "SUMMARIZE"
	// OperationReview reviews a policy for gaps and issues.
	OperationReview JobOperation = "REVIEW"
	// OperationQA answers a question from the retrieved sources, as a job.
	OperationQA JobOperation = "QA"
	// OperationRevise revises an existing policy with every section as
	// context, returning only the sections that change.
	OperationRevise JobOperation = "REVISE"
	// OperationRelatedReeval refreshes the suggested related policies of the
	// policies a publish moved. The service starts it after a debounce; it is
	// never submitted by a person and makes no model call.
	OperationRelatedReeval JobOperation = "RELATED_REEVAL"
	// OperationSuggestEnrichments suggests related policies, definitions and
	// references for an existing document without regenerating its body.
	// References come from the retrieved sources only, so none is invented.
	OperationSuggestEnrichments JobOperation = "SUGGEST_ENRICHMENTS"
	// OperationRelationshipLearn rebuilds the weighted relationship table
	// from usage, centroids, cross-references and shared entities. The
	// operator starts it nightly; it makes no model call.
	OperationRelationshipLearn JobOperation = "RELATIONSHIP_LEARN"
)

// JobPhase is the lifecycle phase of a PolicyAIJob.
type JobPhase string

// The phases a job moves through.
const (
	PhasePending   JobPhase = "Pending"
	PhaseRunning   JobPhase = "Running"
	PhaseSucceeded JobPhase = "Succeeded"
	PhaseFailed    JobPhase = "Failed"
)

// PolicyAIJobSpec is one job request. Everything operation-specific is in
// Input, whose shape each operation defines, so a new operation changes only
// the operator's dispatch and its own input type.
type PolicyAIJobSpec struct {
	// Operation selects what the operator runs.
	Operation JobOperation `json:"operation"`

	// ActorUserID is the person who submitted the job. GetAIJob answers only
	// that person, and the completion event and audit name them.
	// +optional
	ActorUserID string `json:"actorUserId,omitempty"`

	// ImpersonatorUserID is the administrator acting as ActorUserID, when the
	// job was submitted during act-as. Audit names them.
	// +optional
	ImpersonatorUserID string `json:"impersonatorUserId,omitempty"`

	// PolicyID is the document the job is about. Empty for a document not
	// created yet.
	// +optional
	PolicyID string `json:"policyId,omitempty"`

	// VersionID is the version the job is about. Empty when there is no draft
	// yet.
	// +optional
	VersionID string `json:"versionId,omitempty"`

	// CategoryID is the owning category, carried for audit.
	// +optional
	CategoryID string `json:"categoryId,omitempty"`

	// ReadCategoryIDs, IncludeSensitive and AllCategories are the
	// submitter's read scope at submission, which a retrieving job (QA,
	// enrichment suggestions) is held to.
	// +optional
	ReadCategoryIDs []string `json:"readCategoryIds,omitempty"`
	// +optional
	IncludeSensitive bool `json:"includeSensitive,omitempty"`
	// +optional
	AllCategories bool `json:"allCategories,omitempty"`

	// Input is the operation's payload.
	// +optional
	Input runtime.RawExtension `json:"input,omitempty"`

	// ModelID is the model the job runs on, resolved when it was submitted,
	// so a later model change never affects a job already created.
	// +optional
	ModelID string `json:"modelId,omitempty"`
}

// PolicyAIJobStatus is the outcome of a PolicyAIJob.
type PolicyAIJobStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase JobPhase `json:"phase,omitempty"`

	// ResultRef is the cache key of the result, set once Phase is Succeeded.
	// +optional
	ResultRef string `json:"resultRef,omitempty"`

	// Error is set when Phase is Failed.
	// +optional
	Error string `json:"error,omitempty"`

	// Attempts counts the failed attempts so far. Retries stop at the
	// operator's limit, and a retry is not started while the module is off.
	// +optional
	Attempts int `json:"attempts,omitempty"`

	// StartedAt is set when the job leaves Pending.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// FinishedAt is set when the job reaches Succeeded or Failed.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// ObservedGeneration is the generation the status was computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// PolicyAIJob is one asynchronous AI job.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Operation",type=string,JSONPath=`.spec.operation`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type PolicyAIJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PolicyAIJobSpec   `json:"spec,omitempty"`
	Status PolicyAIJobStatus `json:"status,omitempty"`
}

// PolicyAIJobList is a list of PolicyAIJob.
//
// +kubebuilder:object:root=true
type PolicyAIJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PolicyAIJob `json:"items"`
}
