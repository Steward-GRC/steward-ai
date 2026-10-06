// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"strconv"
	"strings"

	grpcactor "github.com/Bugs5382/go-grpc-actor"
	log "github.com/Bugs5382/go-log"

	"github.com/Steward-GRC/steward-ai/internal/audit"
)

// AuditRecord is one audited AI call or settings change.
type AuditRecord struct {
	// Operation is the RPC in snake case; the action is "ai.<operation>".
	Operation  string
	Actor      grpcactor.Actor
	CategoryID string
	// Query is the question, the instruction or the setting's new value;
	// never a credential.
	Query        string
	SourceIDs    []string
	HasSensitive bool
}

// eventEmitter publishes audit events; audit.Emitter satisfies it.
type eventEmitter interface {
	Emit(ctx context.Context, ev audit.Event) error
}

// AuditAdapter turns AuditRecords into audit-tier events.
type AuditAdapter struct{ emitter eventEmitter }

// NewAuditAdapter returns an AuditAdapter on emitter.
func NewAuditAdapter(emitter eventEmitter) *AuditAdapter { return &AuditAdapter{emitter: emitter} }

// EmitAIAudit publishes rec. During act-as the event is credited to the
// administrator and keeps the person acted as. Only audit attribution
// changes: the job owner and the quota stay the person acted as.
func (a *AuditAdapter) EmitAIAudit(ctx context.Context, rec AuditRecord) error {
	ev := audit.Event{
		Tier:        audit.TierAudit,
		Action:      "ai." + rec.Operation,
		ActorUserID: rec.Actor.Subject,
		GroupID:     rec.CategoryID,
		Subject:     "ai_operation:" + rec.Operation,
		Attributes: map[string]string{
			"query":         rec.Query,
			"source_ids":    strings.Join(rec.SourceIDs, ","),
			"has_sensitive": strconv.FormatBool(rec.HasSensitive),
		},
	}
	if rec.Actor.Impersonated() {
		ev = ev.ActedAs(rec.Actor.Impersonator)
	}
	return a.emitter.Emit(ctx, ev)
}

// emit records rec. An audit failure is logged and never fails the call.
func (s *AiServiceServer) emit(ctx context.Context, rec AuditRecord) {
	if err := s.d.Audit.EmitAIAudit(ctx, rec); err != nil {
		s.log.Ctx(ctx).Error(err, "audit emit failed", log.F("operation", rec.Operation))
	}
}
