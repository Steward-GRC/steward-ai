// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package quota counts each person's AI queries per day in Valkey. The
// per-person cap lives in Postgres (store.UserQueryLimitStore); this package
// keeps only the day's count. The day is a calendar day in UTC.
package quota

import (
	"context"
	"fmt"
	"time"

	redis "github.com/Bugs5382/go-redis"
)

const keyPrefix = "ai:quota:"

// Counter is the per-person, per-day query counter.
type Counter struct {
	rdb redis.UniversalClient
}

// New returns a Counter on rdb.
func New(rdb redis.UniversalClient) *Counter {
	return &Counter{rdb: rdb}
}

func dayKey(userID string, now time.Time) string {
	return keyPrefix + now.UTC().Format("2006-01-02") + ":" + userID
}

// ResetAt is the next UTC midnight after now, when the day's count resets.
func ResetAt(now time.Time) time.Time {
	u := now.UTC()
	midnight := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	return midnight.Add(24 * time.Hour)
}

// Consume counts one query for userID on now's UTC day and returns the
// day's count after it. A Valkey error on the increment is returned so the
// caller applies its own policy (the intake gate fails open). The expiry is
// set again on every call, so a lost EXPIRE heals on the next one; a failed
// expiry is not an error because the count is already recorded.
//
// A refused query still counts: it is already over the cap, so the decision
// doesn't change, and the key expires with the day.
func (c *Counter) Consume(ctx context.Context, userID string, now time.Time) (used int64, err error) {
	key := dayKey(userID, now)
	n, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("quota: incr %q: %w", key, err)
	}
	// Derived from now, not the wall clock, so the expiry matches the window.
	ttl := max(ResetAt(now).Sub(now), time.Second)
	_ = c.rdb.Expire(ctx, key, ttl).Err()
	return n, nil
}
