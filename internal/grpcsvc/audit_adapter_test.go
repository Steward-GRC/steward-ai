// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"testing"

	grpcactor "github.com/Bugs5382/go-grpc-actor"

	"github.com/Steward-GRC/steward-ai/internal/audit"
	"github.com/Steward-GRC/steward-ai/internal/fixture"
)

type stubEmitter struct{ lastEvent audit.Event }

func (s *stubEmitter) Emit(_ context.Context, ev audit.Event) error {
	s.lastEvent = ev
	return nil
}

func TestAuditAdapter_emitsAuditTierEvent(t *testing.T) {
	stub := &stubEmitter{}
	err := NewAuditAdapter(stub).EmitAIAudit(context.Background(), AuditRecord{
		Operation:  "search_and_answer",
		Actor:      grpcactor.Actor{Subject: fixture.Erin},
		CategoryID: fixture.Facilities,
		Query:      "how do I book a desk?",
		SourceIDs:  []string{"ver-1", "ver-2"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := stub.lastEvent
	if ev.Tier != audit.TierAudit {
		t.Fatalf("expected TierAudit, got %v", ev.Tier)
	}
	if ev.Action != "ai.search_and_answer" {
		t.Fatalf("action: got %q", ev.Action)
	}
	if ev.ActorUserID != fixture.Erin || ev.GroupID != fixture.Facilities {
		t.Fatalf("actor/category: got %q/%q", ev.ActorUserID, ev.GroupID)
	}
	if ev.Attributes["source_ids"] != "ver-1,ver-2" || ev.Attributes["query"] != "how do I book a desk?" {
		t.Fatalf("attributes: got %v", ev.Attributes)
	}
	if _, ok := ev.Attributes["impersonated_user_id"]; ok {
		t.Fatal("no act-as attribute without an impersonator")
	}
}

func TestAuditAdapter_sensitiveFlag(t *testing.T) {
	stub := &stubEmitter{}
	_ = NewAuditAdapter(stub).EmitAIAudit(context.Background(), AuditRecord{
		Operation: "search_and_answer", Actor: grpcactor.Actor{Subject: fixture.Erin}, HasSensitive: true,
	})
	if stub.lastEvent.Attributes["has_sensitive"] != "true" {
		t.Fatalf("expected has_sensitive=true, got %v", stub.lastEvent.Attributes)
	}
}

// During act-as the event is credited to the administrator and keeps the
// person acted as.
func TestAuditAdapter_actAsCreditsTheImpersonator(t *testing.T) {
	stub := &stubEmitter{}
	_ = NewAuditAdapter(stub).EmitAIAudit(context.Background(), AuditRecord{
		Operation: "set_ai_enabled", Actor: grpcactor.Actor{Subject: fixture.Erin, Impersonator: fixture.Alice},
	})
	ev := stub.lastEvent
	if ev.ActorUserID != fixture.Alice || ev.Attributes["impersonated_user_id"] != fixture.Erin {
		t.Fatalf("expected Alice credited acting as Erin, got %q / %v", ev.ActorUserID, ev.Attributes)
	}
	if ev.Action != "ai.set_ai_enabled" {
		t.Fatalf("action: got %q", ev.Action)
	}
}
