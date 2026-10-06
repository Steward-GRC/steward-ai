// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"

	otel "github.com/Bugs5382/go-otel"
)

// The decisions recorded on ai_query_quota_total's "action" attribute.
const (
	quotaAllowed = "allowed"
	quotaBlocked = "blocked"
	// quotaExempt is a person whose limit resolves to unlimited; they are
	// never counted.
	quotaExempt = "exempt"
)

// aiQueryQuota is created on the global meter, which forwards to the
// provider go-otel installs at startup.
var aiQueryQuota = otel.NewCounter("ai_query_quota_total", "Per-person AI query quota decisions, by action")

// recordQuotaDecision takes fixed labels only, never a user id or a question.
func recordQuotaDecision(ctx context.Context, rpc, action string) {
	aiQueryQuota.Add(ctx, 1, otel.KV("action", action), otel.KV("rpc", rpc))
}
