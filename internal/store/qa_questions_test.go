// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"testing"
)

// NewQAQuestionStore compile-check: verifies the constructor signature
// exists and a nil pool is accepted (real pool injected in integration
// tests), matching the nil-pool guard shared by every store in this package.
func TestNewQAQuestionStore_compiles(t *testing.T) {
	_ = NewQAQuestionStore(nil)
}

func TestQAQuestionStore_nilPool(t *testing.T) {
	s := NewQAQuestionStore(nil)
	ctx := context.Background()

	if _, err := s.TopQuestions(ctx, AccessFilter{}, 6); err == nil {
		t.Fatal("expected error from nil pool on TopQuestions; got none")
	}
}

// TopQuestions' ranking/filtering behavior against real rows (SUM(ask_count)
// ordering, no_authorized_source/has_sensitive_source exclusion, limit
// clamping) is covered by qa_questions_integration_test.go against a real
// Postgres, since it depends on GROUP BY/aggregate semantics a fake can't
// meaningfully stand in for.
