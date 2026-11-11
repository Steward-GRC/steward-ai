// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/fixture"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

const storedDraft = `{"sections":[{"sectionKey":"purpose","content":"Body."}]}`

func withStoredResult(f *fakes) {
	f.stored.rows = map[string]store.JobResult{
		"aijob-erin": {JobID: "aijob-erin", Operation: "DRAFT", ActorUserID: fixture.Erin, ResultJSON: storedDraft},
		"aijob-bob":  {JobID: "aijob-bob", Operation: "REVIEW", ActorUserID: fixture.Bob, ResultJSON: `{"findings":[]}`},
		"aijob-none": {JobID: "aijob-none", Operation: "DRAFT", ResultJSON: storedDraft},
	}
}

func TestGetAIJobResult_returnsTheStoredResultToTheSubmitter(t *testing.T) {
	f := newFakes()
	withStoredResult(f)
	resp, err := f.server().GetAIJobResult(erinCtx(), &aiv1.GetAIJobResultRequest{JobId: "aijob-erin"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOperation() != "DRAFT" || resp.GetResultJson() != storedDraft {
		t.Fatalf("expected the stored DRAFT result, got %+v", resp)
	}
	if f.results.calls != 0 {
		t.Fatalf("the durable read must not need the cache, got %d cache reads", f.results.calls)
	}
}

func TestGetAIJobResult_actAsReadsTheSubjectsResult(t *testing.T) {
	f := newFakes()
	withStoredResult(f)
	if _, err := f.server().GetAIJobResult(actAsCtx(), &aiv1.GetAIJobResultRequest{JobId: "aijob-erin"}); err != nil {
		t.Fatalf("expected the act-as read of the subject's result, got %v", err)
	}
}

func TestGetAIJobResult_anotherUsersResultReadsAsNotFound(t *testing.T) {
	f := newFakes()
	withStoredResult(f)
	resp, err := f.server().GetAIJobResult(erinCtx(), &aiv1.GetAIJobResultRequest{JobId: "aijob-bob"})
	requireCode(t, err, errcodes.CodeJobNotFound, codes.NotFound)
	if resp != nil {
		t.Fatalf("a refused read returns nothing, got %+v", resp)
	}
	// Same answer as an id that does not exist, so an id reveals nothing.
	_, errMissing := f.server().GetAIJobResult(erinCtx(), &aiv1.GetAIJobResultRequest{JobId: "aijob-missing"})
	requireCode(t, errMissing, errcodes.CodeJobNotFound, codes.NotFound)
	if err.Error() != errMissing.Error() {
		t.Fatalf("a refused read must look like a missing one: %q vs %q", err, errMissing)
	}
}

func TestGetAIJobResult_aRowWithNoOwnerIsOwnedByNoOne(t *testing.T) {
	f := newFakes()
	withStoredResult(f)
	_, err := f.server().GetAIJobResult(erinCtx(), &aiv1.GetAIJobResultRequest{JobId: "aijob-none"})
	requireCode(t, err, errcodes.CodeJobNotFound, codes.NotFound)
}

func TestGetAIJobResult_requiresJobID(t *testing.T) {
	_, err := newFakes().server().GetAIJobResult(erinCtx(), &aiv1.GetAIJobResultRequest{})
	requireCode(t, err, errcodes.CodeRequestInvalid, codes.InvalidArgument)
}

func TestGetAIJobResult_requiresAnActor(t *testing.T) {
	f := newFakes()
	withStoredResult(f)
	_, err := f.server().GetAIJobResult(context.Background(), &aiv1.GetAIJobResultRequest{JobId: "aijob-erin"})
	requireCode(t, err, errcodes.CodeActorRequired, codes.Unauthenticated)
}

func TestGetAIJobResult_aStoreFailureIsAnErrorNotAnAbsence(t *testing.T) {
	f := newFakes()
	f.stored.err = errors.New("connection refused")
	_, err := f.server().GetAIJobResult(erinCtx(), &aiv1.GetAIJobResultRequest{JobId: "aijob-erin"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("the store's detail must not reach the caller: %v", err)
	}
	if status.Code(err) == codes.NotFound {
		t.Fatalf("a failed read is not the same as no result: %v", err)
	}
}
