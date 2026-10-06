// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	redis "github.com/Bugs5382/go-redis"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Steward-GRC/steward-ai/internal/operator"
)

func TestValkeyResultWriter_writesTheEnvelopeWithTTL(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "valkey/valkey:8", ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	rc, err := redis.Connect(ctx, redis.WithAddr(host+":"+port.Port()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rc.Close() })

	w := operator.NewValkeyResultWriter(rc.Redis())
	key := "ai:job-result:pol-1:ver-1:REVIEW"
	require.NoError(t, w.WriteResult(ctx, key, operator.ResultEnvelope{Operation: "REVIEW", Result: map[string]int{"findings": 2}}, time.Hour))

	raw, err := rc.Redis().Get(ctx, key).Bytes()
	require.NoError(t, err)
	var got struct {
		Operation string         `json:"operation"`
		Result    map[string]int `json:"result"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "REVIEW", got.Operation)
	require.Equal(t, 2, got.Result["findings"])
	ttl, err := rc.Redis().TTL(ctx, key).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, 59*time.Minute)
}
