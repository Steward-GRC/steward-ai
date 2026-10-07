// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"errors"
	"testing"
	"time"

	log "github.com/Bugs5382/go-log"
	"google.golang.org/grpc/codes"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/errcodes"
	"github.com/Steward-GRC/steward-ai/internal/fixture"
)

// withoutKubernetes is a server whose jobs have no Kubernetes API.
func withoutKubernetes() *AiServiceServer {
	d := newFakes().deps()
	d.Jobs = NewUnavailableJobs(errors.New("kubernetes config: no configuration has been provided"))
	return New(d, WithLogger(log.Nop()), WithClock(func() time.Time { return testNow }))
}

func TestSubmitAIJob_withoutTheKubernetesAPIIsJobsUnavailable(t *testing.T) {
	_, err := withoutKubernetes().SubmitAIJob(erinCtx(), &aiv1.SubmitAIJobRequest{
		CategoryId: fixture.Facilities,
		Scope:      &aiv1.ReadScope{CategoryIds: []string{fixture.Facilities}},
		Operation:  aiv1.JobOperation_JOB_OPERATION_DRAFT,
	})
	requireCode(t, err, errcodes.CodeJobsUnavailable, codes.Unavailable)
}

func TestGetAIJob_withoutTheKubernetesAPIIsJobsUnavailable(t *testing.T) {
	_, err := withoutKubernetes().GetAIJob(erinCtx(), &aiv1.GetAIJobRequest{JobId: "aijob-abc123"})
	requireCode(t, err, errcodes.CodeJobsUnavailable, codes.Unavailable)
}
