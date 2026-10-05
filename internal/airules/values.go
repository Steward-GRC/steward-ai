// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package airules

// Completion budgets (max output tokens) per operation, and the environment
// variables that override them. Each operation is tuned on its own even
// where two share a value.
const (
	// DraftSectionMaxTokensDefault is the budget for one drafted section. A
	// section that expands fully on an attached source needs far more room
	// than a short paragraph.
	DraftSectionMaxTokensDefault = 32000
	// DraftSectionMaxTokensEnvVar overrides DraftSectionMaxTokensDefault.
	DraftSectionMaxTokensEnvVar = "AI_DRAFT_SECTION_MAX_TOKENS"

	// ReviseMaxTokensDefault is the budget for a revision, which returns the
	// whole policy in one call.
	ReviseMaxTokensDefault = 32000
	// ReviseMaxTokensEnvVar overrides ReviseMaxTokensDefault. "Tokens" is
	// the unit of model output, not a credential.
	ReviseMaxTokensEnvVar = "AI_REVISE_MAX_TOKENS" // #nosec G101 -- env var name; value is an integer token budget, not a secret

	// AssistMaxTokens is the budget for the region-editing assist operations.
	AssistMaxTokens = 2048
	// SummarizeMaxTokens is the budget for a summary, a short overview.
	SummarizeMaxTokens = 512
	// QAMaxTokens is the budget for a grounded answer.
	QAMaxTokens = 2048
	// DefaultMaxTokens is the adapters' fallback when a request sets none.
	DefaultMaxTokens = 2048
)

// Chunking and retrieval defaults, and their environment overrides.
const (
	// ChunkSizeDefault is the approximate words per chunk. 350 words keeps a
	// chunk under the 512-token input of the default embeddings model, so
	// one long chunk can't fail a section's embedding batch.
	ChunkSizeDefault = 350
	// ChunkSizeEnvVar overrides ChunkSizeDefault.
	ChunkSizeEnvVar = "AI_CHUNK_SIZE"
	// ChunkOverlapDefault is the approximate word overlap between chunks.
	ChunkOverlapDefault = 64
	// ChunkOverlapEnvVar overrides ChunkOverlapDefault.
	ChunkOverlapEnvVar = "AI_CHUNK_OVERLAP"

	// RetrievalTopKDefault is the number of candidate chunks retrieved for
	// grounding when no override is stored. A broad pool gives the hybrid
	// fusion and the re-rank room to work.
	RetrievalTopKDefault = 50
	// RetrievalTopKCeiling bounds an administrator's override.
	RetrievalTopKCeiling = 200
	// RetrievalTopKEnvVar overrides RetrievalTopKDefault.
	RetrievalTopKEnvVar = "AI_RETRIEVAL_TOP_K"

	// RelatedPoliciesTopNDefault is the number of related policies returned
	// when the request asks for none in particular.
	RelatedPoliciesTopNDefault = 10
	// RelatedPoliciesTopNCeiling bounds a requested count.
	RelatedPoliciesTopNCeiling = 50
)

// The per-person daily query quota. Overrides are stored in Postgres and the
// day's count in Valkey; the day is a calendar day in UTC.
const (
	// QueryQuotaDefault is the daily limit for anyone without an override.
	QueryQuotaDefault = 50
	// QueryQuotaDefaultEnvVar overrides QueryQuotaDefault.
	QueryQuotaDefaultEnvVar = "AI_QUERY_QUOTA_DEFAULT"
	// UnlimitedQueryQuota means no daily cap.
	UnlimitedQueryQuota = -1
)

// DataNoticeVersion is the data notice an administrator must accept before
// the module can be turned on. A new notice gets a new version, and the
// module can't be turned on again until it is accepted.
const DataNoticeVersion = "2026-10-05"

// The embeddings request timeout.
const (
	// EmbedTimeoutSecondsDefault gives a CPU-bound embeddings server room to
	// embed a large section's batch without the consumer timing out.
	EmbedTimeoutSecondsDefault = 120
	// EmbedTimeoutEnvVar overrides the timeout, in seconds.
	EmbedTimeoutEnvVar = "AI_EMBED_TIMEOUT_SECONDS"
)
