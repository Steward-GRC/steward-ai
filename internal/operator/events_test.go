// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-ai/internal/operator"
)

type recordingPublisher struct {
	keys   []string
	bodies [][]byte
	err    error
}

func (p *recordingPublisher) Publish(_ context.Context, routingKey string, body []byte) error {
	p.keys = append(p.keys, routingKey)
	p.bodies = append(p.bodies, body)
	return p.err
}

func TestMQEventPublisher_routesByPhase(t *testing.T) {
	pub := &recordingPublisher{}
	ep := operator.NewMQEventPublisher(pub)
	ctx := context.Background()
	finished := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	require.NoError(t, ep.PublishJobCompletion(ctx, operator.CompletionEvent{JobID: "aijob-1", Operation: "DRAFT", CategoryID: "category-hr", Phase: "Succeeded", ResultRef: "ai:job-result:aijob-1:DRAFT", FinishedAt: finished}))
	require.NoError(t, ep.PublishJobCompletion(ctx, operator.CompletionEvent{JobID: "aijob-2", Operation: "QA", Phase: "Failed", Error: "boom", FinishedAt: finished}))

	require.Equal(t, []string{operator.RoutingKeySucceeded, operator.RoutingKeyFailed}, pub.keys)
	var ev map[string]any
	require.NoError(t, json.Unmarshal(pub.bodies[0], &ev))
	require.Equal(t, "aijob-1", ev["job_id"])
	require.Equal(t, "category-hr", ev["category_id"])
	require.Equal(t, "Succeeded", ev["phase"])
}

func TestMQEventPublisher_reportsAPublishError(t *testing.T) {
	ep := operator.NewMQEventPublisher(&recordingPublisher{err: errors.New("broker down")})
	require.Error(t, ep.PublishJobCompletion(context.Background(), operator.CompletionEvent{Phase: "Succeeded"}))
}
