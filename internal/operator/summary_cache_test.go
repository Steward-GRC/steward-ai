// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"testing"
	"time"

	redis "github.com/Bugs5382/go-redis"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Steward-GRC/steward-ai/internal/generation"
	"github.com/Steward-GRC/steward-ai/internal/operator"
)

// newSummaryValkey starts a Valkey container for one test and terminates it
// when the test ends. It returns a connected client and the server address.
func newSummaryValkey(t *testing.T) (redis.UniversalClient, string) {
	t.Helper()
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
	t.Cleanup(func() {
		t.Logf("terminating valkey container %s", c.GetContainerID())
		require.NoError(t, testcontainers.TerminateContainer(c))
	})
	addr, err := c.PortEndpoint(ctx, "6379/tcp", "")
	require.NoError(t, err)
	rc, err := redis.Connect(ctx, redis.WithAddr(addr))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rc.Close() })
	return rc.Redis(), addr
}

func TestValkeySummaryCache(t *testing.T) {
	ctx := context.Background()
	rdb, addr := newSummaryValkey(t)
	cache := operator.NewValkeySummaryCache(rdb)

	changes := generation.SummaryJobResult{
		Scope:       generation.SummaryScopeChanges,
		Bullets:     []string{"Tightened the retention period", "Removed the legacy exception"},
		GeneratedAt: "2026-11-12T10:00:00Z",
	}

	t.Run("put then get round-trips the summary", func(t *testing.T) {
		require.NoError(t, cache.Put(ctx, "sum:round-trip", changes))
		got, found, err := cache.Get(ctx, "sum:round-trip")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, changes, got)
	})

	t.Run("a missing key is a miss, not an error", func(t *testing.T) {
		got, found, err := cache.Get(ctx, "sum:never-written")
		require.NoError(t, err)
		require.False(t, found)
		require.Equal(t, generation.SummaryJobResult{}, got)
	})

	t.Run("put stores the 30 day ttl", func(t *testing.T) {
		require.NoError(t, cache.Put(ctx, "sum:ttl", changes))
		ttl, err := rdb.TTL(ctx, "sum:ttl").Result()
		require.NoError(t, err)
		require.Greater(t, ttl, 30*24*time.Hour-time.Minute)
		require.LessOrEqual(t, ttl, 30*24*time.Hour)
	})

	t.Run("an expired entry is a miss", func(t *testing.T) {
		require.NoError(t, cache.Put(ctx, "sum:expiry", changes))
		require.NoError(t, rdb.PExpire(ctx, "sum:expiry", 100*time.Millisecond).Err())
		require.Eventually(t, func() bool {
			_, found, err := cache.Get(ctx, "sum:expiry")
			return err == nil && !found
		}, 5*time.Second, 50*time.Millisecond)
	})

	t.Run("put overwrites the value and resets the ttl", func(t *testing.T) {
		require.NoError(t, cache.Put(ctx, "sum:overwrite", changes))
		require.NoError(t, rdb.Expire(ctx, "sum:overwrite", time.Hour).Err())
		next := generation.SummaryJobResult{
			Scope: generation.SummaryScopeVersion, SummaryText: "- one\n- two",
			GeneratedAt: "2026-11-13T10:00:00Z", Cached: true,
		}
		require.NoError(t, cache.Put(ctx, "sum:overwrite", next))
		got, found, err := cache.Get(ctx, "sum:overwrite")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, next, got)
		ttl, err := rdb.TTL(ctx, "sum:overwrite").Result()
		require.NoError(t, err)
		require.Greater(t, ttl, 29*24*time.Hour)
	})

	t.Run("keys are isolated from each other", func(t *testing.T) {
		other := generation.SummaryJobResult{
			Scope: generation.SummaryScopeVersion, SummaryText: "- other", GeneratedAt: "2026-11-14T10:00:00Z",
		}
		require.NoError(t, cache.Put(ctx, "sum:iso-a", changes))
		require.NoError(t, cache.Put(ctx, "sum:iso-b", other))
		a, _, err := cache.Get(ctx, "sum:iso-a")
		require.NoError(t, err)
		b, _, err := cache.Get(ctx, "sum:iso-b")
		require.NoError(t, err)
		require.Equal(t, changes, a)
		require.Equal(t, other, b)
		require.NoError(t, rdb.Del(ctx, "sum:iso-a").Err())
		_, found, err := cache.Get(ctx, "sum:iso-a")
		require.NoError(t, err)
		require.False(t, found)
		_, found, err = cache.Get(ctx, "sum:iso-b")
		require.NoError(t, err)
		require.True(t, found, "deleting one key must leave the other")
	})

	t.Run("a stored value that is not a summary is a decode error, not a hit", func(t *testing.T) {
		require.NoError(t, rdb.Set(ctx, "sum:corrupt", "{not json", 0).Err())
		got, found, err := cache.Get(ctx, "sum:corrupt")
		require.ErrorContains(t, err, "operator: decode summary")
		require.False(t, found)
		require.Equal(t, generation.SummaryJobResult{}, got)
	})

	t.Run("an unreachable server is an error on get and put, not a miss", func(t *testing.T) {
		down, err := redis.Connect(ctx, redis.WithAddr(addr))
		require.NoError(t, err)
		broken := operator.NewValkeySummaryCache(down.Redis())
		require.NoError(t, down.Close())

		_, found, err := broken.Get(ctx, "sum:round-trip")
		require.ErrorContains(t, err, "operator: read summary")
		require.False(t, found)
		require.ErrorContains(t, broken.Put(ctx, "sum:down", changes), "operator: write summary")
	})
}
