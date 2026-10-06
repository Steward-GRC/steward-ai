// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package airules holds the ai service's access and governance rules and its
// fixed values in one place.
//
// The rules:
//
//   - Read access (ChunkReadable, store.AccessFilter): a document is readable
//     when its category is in the scope (or the scope reads every category)
//     and it is standard or the scope includes sensitive documents. Reading
//     every category never opens sensitive documents on its own, as in
//     steward-authz. The rule runs inside the SQL; ChunkReadable states it in Go.
//   - Off is off: while the module is off no intake call is accepted and
//     nothing calls a provider. A settings read failure refuses the call
//     rather than run with the module possibly off.
//   - GovernancePublishedOnlyIndexing and GovernanceLatestVersionOnly: only
//     published content is indexed, and only the current version.
//   - GovernanceBestEffortDegradation: AI never blocks the operation it
//     sits beside, such as a publish.
package airules

import (
	"slices"
	"strings"
)

// The document kinds a chunk can come from. Core's procedure events carry
// "PROCEDURE"; policy events carry none, which reads as a policy.
const (
	DocumentTypePolicy    = "POLICY"
	DocumentTypeProcedure = "PROCEDURE"
)

// NormalizeDocumentType maps a wire or stored value onto one of the two
// kinds; anything else is a policy.
func NormalizeDocumentType(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case DocumentTypeProcedure:
		return DocumentTypeProcedure
	default:
		return DocumentTypePolicy
	}
}

// The sensitivity values a document carries.
const (
	SensitivityStandard  = "standard"
	SensitivitySensitive = "sensitive"
)

// EffectiveQueryQuota resolves a person's daily query limit: their override
// when they have one (which may be UnlimitedQueryQuota), otherwise the
// default. unlimited reports an uncapped limit; otherwise limit is the cap.
func EffectiveQueryQuota(override int, hasOverride bool, defaultLimit int) (limit int, unlimited bool) {
	if hasOverride {
		if override == UnlimitedQueryQuota {
			return UnlimitedQueryQuota, true
		}
		return override, false
	}
	if defaultLimit == UnlimitedQueryQuota {
		return UnlimitedQueryQuota, true
	}
	return defaultLimit, false
}

// ChunkReadable is the read rule the SQL predicates enforce, in Go, for
// documentation and parity tests. It is not called on the query path:
// filtering in Go would mean fetching unreadable rows first.
func ChunkReadable(sensitivity string, allCategories, includeSensitive bool, categoryID string, categoryIDs []string) bool {
	if !allCategories && !slices.Contains(categoryIDs, categoryID) {
		return false
	}
	return sensitivity == SensitivityStandard || includeSensitive
}

// Governance facts as named statements of rules that are otherwise only
// implicit in the code's structure.
const (
	GovernancePublishedOnlyIndexing = "AI indexing, embedding, and summarization operate only on published policy versions; unpublished drafts are never indexed, embedded, or retrievable."
	GovernanceLatestVersionOnly     = "At most one version of a given policy is searchable/summarized at a time; unpublish/archive removes the superseded version's chunks so retrieval and summaries never reflect stale content."
	GovernanceBestEffortDegradation = "AI is best-effort: a down/misconfigured LLM provider or a failed publish-time summary must never block or fail the underlying policy operation."
)
