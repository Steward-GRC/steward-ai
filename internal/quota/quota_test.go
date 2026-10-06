// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package quota

import (
	"context"
	"testing"
	"time"

	redis "github.com/Bugs5382/go-redis"
)

func newTestCounter(t *testing.T) (*Counter, redis.UniversalClient) {
	t.Helper()
	rdb := newValkey(t)
	return New(rdb), rdb
}

func TestConsume_incrementsPerDay(t *testing.T) {
	c, _ := newTestCounter(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	for want := int64(1); want <= 3; want++ {
		got, err := c.Consume(ctx, "user-1", now)
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		if got != want {
			t.Fatalf("consume #%d: expected count %d, got %d", want, want, got)
		}
	}
}

func TestConsume_isolatedPerUser(t *testing.T) {
	c, _ := newTestCounter(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	if n, _ := c.Consume(ctx, "a", now); n != 1 {
		t.Fatalf("user a first: got %d", n)
	}
	if n, _ := c.Consume(ctx, "a", now); n != 2 {
		t.Fatalf("user a second: got %d", n)
	}
	if n, _ := c.Consume(ctx, "b", now); n != 1 {
		t.Fatalf("user b must have its own counter, got %d", n)
	}
}

func TestConsume_windowResetsAcrossDays(t *testing.T) {
	c, _ := newTestCounter(t)
	ctx := context.Background()
	day1 := time.Date(2026, 8, 24, 23, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 8, 25, 0, 30, 0, 0, time.UTC)

	if n, _ := c.Consume(ctx, "user-1", day1); n != 1 {
		t.Fatalf("day1: got %d", n)
	}
	if n, _ := c.Consume(ctx, "user-1", day1); n != 2 {
		t.Fatalf("day1 again: got %d", n)
	}
	if n, _ := c.Consume(ctx, "user-1", day2); n != 1 {
		t.Fatalf("day2 must reset the counter, got %d", n)
	}
}

func TestConsume_setsTTL(t *testing.T) {
	c, rdb := newTestCounter(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	if _, err := c.Consume(ctx, "user-1", now); err != nil {
		t.Fatalf("consume: %v", err)
	}
	ttl, err := rdb.TTL(ctx, dayKey("user-1", now)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 {
		t.Fatalf("expected a positive TTL on the daily counter key, got %v", ttl)
	}
	// Bound by the scenario's own now, not the wall clock, so the test
	// doesn't start failing once the real date passes it.
	maxTTL := ResetAt(now).Sub(now)
	if ttl > maxTTL+time.Second {
		t.Fatalf("TTL %v exceeds time until UTC midnight %v", ttl, maxTTL)
	}
}

func TestResetAt_isNextUTCMidnight(t *testing.T) {
	now := time.Date(2026, 8, 24, 15, 4, 5, 0, time.UTC)
	got := ResetAt(now)
	want := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("expected reset at %v, got %v", want, got)
	}
}

func TestConsume_returnsValkeyError(t *testing.T) {
	rdb := newValkey(t)
	c := New(rdb)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Consume(ctx, "user-1", time.Now()); err == nil {
		t.Fatal("expected the increment error to be returned")
	}
}
