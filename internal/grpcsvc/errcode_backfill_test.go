// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/fixture"
)

// Narrowing a search to a category outside the scope is refused with
// AI_CATEGORY_FORBIDDEN naming the category id.
func TestSearchAndAnswer_categoryScopeDenied_carriesCode(t *testing.T) {
	f := newFakes()
	_, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{
		Question:   "what is the expense policy?",
		CategoryId: fixture.Finance,
		Scope:      scope(fixture.Facilities),
	})
	info := requireCode(t, err, errcodes.CodeCategoryForbidden, codes.PermissionDenied)
	if info.Symbol != "AI_CATEGORY_FORBIDDEN" {
		t.Fatalf("expected reason AI_CATEGORY_FORBIDDEN, got %q", info.Symbol)
	}
	if info.Metadata["category_id"] != fixture.Finance {
		t.Fatalf("metadata must carry the category id, got %v", info.Metadata)
	}
	if got := status.Convert(err).Message(); got != "You don't have access to that category." {
		t.Fatalf("expected the user-safe message, got %q", got)
	}
	if f.quota.calls != 0 || f.retr.searched != 0 {
		t.Fatal("a denied call is neither counted nor searched")
	}
}

func TestCheckCategory_passesAndDenies(t *testing.T) {
	svc := newFakes().server()
	ctx := context.Background()
	if err := svc.checkCategory(ctx, "", nil); err != nil {
		t.Fatalf("no narrowing must pass, got %v", err)
	}
	if err := svc.checkCategory(ctx, fixture.Finance, &aiv1.ReadScope{AllCategories: true}); err != nil {
		t.Fatalf("an all-categories scope must pass, got %v", err)
	}
	if err := svc.checkCategory(ctx, fixture.Finance, scope(fixture.Finance)); err != nil {
		t.Fatalf("a category in the scope must pass, got %v", err)
	}
	err := svc.checkCategory(ctx, fixture.Finance, scope(fixture.Expenses))
	info := requireCode(t, err, errcodes.CodeCategoryForbidden, codes.PermissionDenied)
	if info.Metadata["category_id"] != fixture.Finance {
		t.Fatalf("expected category_id metadata, got %v", info.Metadata)
	}
}

// The two server faults carry their codes and never the raw cause.
func TestInternalFaultHelpers_carryCodes(t *testing.T) {
	cause := errors.New("pq: connection refused")
	svc := newFakes().server()

	t.Run("retrieval", func(t *testing.T) {
		err := svc.retrievalUnavailable(context.Background(), "embed_question", cause)
		info := requireCode(t, err, errcodes.CodeRetrievalUnavailable, codes.Unavailable)
		if status.Convert(err).Message() == cause.Error() {
			t.Fatal("raw cause must never be on the wire")
		}
		if info.Symbol != "AI_RETRIEVAL_UNAVAILABLE" || info.Metadata["op"] != "embed_question" {
			t.Fatalf("bad ErrorInfo: %+v", info)
		}
	})

	t.Run("generation", func(t *testing.T) {
		err := svc.providerError(context.Background(), cause)
		info := requireCode(t, err, errcodes.CodeGenerationFailed, codes.Unavailable)
		if info.Symbol != "AI_GENERATION_FAILED" {
			t.Fatalf("bad ErrorInfo: %+v", info)
		}
	})
}

func TestSearchAndAnswer_retrievalFailuresCarryTheStep(t *testing.T) {
	f := newFakes()
	f.retr.embedErr = errBoom
	_, err := f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{Question: "q", Scope: scope(fixture.Workplace)})
	if info := requireCode(t, err, errcodes.CodeRetrievalUnavailable, codes.Unavailable); info.Metadata["op"] != "embed_question" {
		t.Fatalf("expected op=embed_question, got %v", info.Metadata)
	}

	f = newFakes()
	f.retr.searchErr = errBoom
	_, err = f.server().SearchAndAnswer(erinCtx(), &aiv1.SearchAndAnswerRequest{Question: "q", Scope: scope(fixture.Workplace)})
	if info := requireCode(t, err, errcodes.CodeRetrievalUnavailable, codes.Unavailable); info.Metadata["op"] != "retrieval" {
		t.Fatalf("expected op=retrieval, got %v", info.Metadata)
	}
}
