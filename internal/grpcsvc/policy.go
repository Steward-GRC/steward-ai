// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/workloadauth"
)

// gateway is the only caller: it authenticates the person and forwards them
// as the actor.
var gateway = map[string]workloadauth.Access{"gateway": workloadauth.OnBehalf}

// CallerPolicy is the workload-auth allow-list for AiService.
var CallerPolicy = workloadauth.Policy{
	aiv1.AiService_SearchAndAnswer_FullMethodName:       gateway,
	aiv1.AiService_AuthoringAssist_FullMethodName:       gateway,
	aiv1.AiService_SubmitAIJob_FullMethodName:           gateway,
	aiv1.AiService_GetAIJob_FullMethodName:              gateway,
	aiv1.AiService_GetProviderStatus_FullMethodName:     gateway,
	aiv1.AiService_GetAIEnabled_FullMethodName:          gateway,
	aiv1.AiService_SetAIEnabled_FullMethodName:          gateway,
	aiv1.AiService_GetAIConfig_FullMethodName:           gateway,
	aiv1.AiService_SetProviderConfig_FullMethodName:     gateway,
	aiv1.AiService_SetProviderCredential_FullMethodName: gateway,
	aiv1.AiService_TestProvider_FullMethodName:          gateway,
	aiv1.AiService_AcceptDataNotice_FullMethodName:      gateway,
	aiv1.AiService_SetMonthlyLimit_FullMethodName:       gateway,
	aiv1.AiService_GetUsage_FullMethodName:              gateway,
	aiv1.AiService_SetOrgContext_FullMethodName:         gateway,
	aiv1.AiService_GetTopQuestions_FullMethodName:       gateway,
	aiv1.AiService_GetPolicySummary_FullMethodName:      gateway,
	aiv1.AiService_SetAIRetrievalConfig_FullMethodName:  gateway,
	aiv1.AiService_SetUserAiQueryLimit_FullMethodName:   gateway,
	aiv1.AiService_GetRelatedPolicies_FullMethodName:    gateway,
}
