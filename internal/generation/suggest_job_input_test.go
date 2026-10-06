// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/fixture"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// TestToSuggestRequest_carriesTheJobScope checks the job's read scope,
// sensitive flag included, reaches the suggestion request unchanged.
func TestToSuggestRequest_carriesTheJobScope(t *testing.T) {
	raw := []byte(`{"title":"Desk Booking Policy","sections":[{"key":"scope","title":"Scope","content":"Desks."}],` +
		`"optOut":{"definitions":true},"library":{"references":[{"referenceId":"ref-1","label":"Booking Rules"}]}}`)
	in, err := UnmarshalSuggestJobInput(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	scope := store.AccessFilter{CategoryIDs: []string{fixture.Facilities}, IncludeSensitive: true}
	req := in.ToSuggestRequest("pol-desk", "model-a", scope)

	if req.PolicyID != "pol-desk" || req.ModelID != "model-a" || req.Title != fixture.DeskBookingPolicy {
		t.Fatalf("identity: %+v", req)
	}
	if len(req.Access.CategoryIDs) != 1 || req.Access.CategoryIDs[0] != fixture.Facilities ||
		!req.Access.IncludeSensitive || req.Access.AllCategories {
		t.Fatalf("scope: %+v", req.Access)
	}
	if !req.OptOut.Definitions || req.OptOut.References {
		t.Fatalf("opt-out: %+v", req.OptOut)
	}
	if len(req.Sections) != 1 || req.Sections[0].Key != "scope" || len(req.Library.References) != 1 {
		t.Fatalf("content: %+v", req)
	}
}

// TestToDraftRequest_carriesTheCategory checks the job's category reaches the
// draft request.
func TestToDraftRequest_carriesTheCategory(t *testing.T) {
	req := DraftJobInput{Brief: "b", Sections: []DraftJobInputSection{{Key: "k"}}}.
		ToDraftRequest(fixture.Bob, fixture.Workplace, "model-a")
	if req.ActorUserID != fixture.Bob || req.CategoryID != fixture.Workplace || req.ModelID != "model-a" {
		t.Fatalf("got %+v", req)
	}
}
