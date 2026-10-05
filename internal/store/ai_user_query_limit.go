// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"

	postgres "github.com/Bugs5382/go-postgres"
	"github.com/jackc/pgx/v5"
)

// UserQueryLimitStore reads and writes per-user AI query limit overrides
// in the ai_user_query_limit table. A user WITHOUT a row is governed
// by the global default (airules.QueryQuotaDefault); a row stores an explicit
// per-day cap (daily_limit >= 1) or the unlimited sentinel
// (airules.UnlimitedQueryQuota == -1). Clearing an override deletes the row.
//
// This store holds only the durable per-user CAP; the rolling per-day COUNT
// lives in Redis (internal/quota).
type UserQueryLimitStore struct {
	db *postgres.DB
}

// NewUserQueryLimitStore constructs a UserQueryLimitStore backed by the given
// pool. A nil database is permitted for compile-time wiring (mirroring
// AIConfigStore/JobResultStore) — every method then returns an error rather
// than panicking.
func NewUserQueryLimitStore(db *postgres.DB) *UserQueryLimitStore {
	return &UserQueryLimitStore{db: db}
}

// Get returns the user's stored per-day limit override and whether one exists.
// found=false (with a nil error) means the user has no override and the caller
// must apply the global default. A genuinely missing row is NOT an error.
func (s *UserQueryLimitStore) Get(ctx context.Context, userID string) (limit int, found bool, err error) {
	if s.db == nil {
		return 0, false, fmt.Errorf("store: user query limit store has no pool configured")
	}
	const q = `SELECT daily_limit FROM ai_user_query_limit WHERE user_id = $1`
	if scanErr := s.db.Pool().QueryRow(ctx, q, userID).Scan(&limit); scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("store: get user query limit: %w", scanErr)
	}
	return limit, true, nil
}

// Set upserts the user's per-day limit override. limit must be >= 1 (an
// explicit cap) or -1 (unlimited); the caller (SetUserAiQueryLimit) resolves
// "0 clears" to Delete and validates the range before calling Set. The CHECK
// constraint rejects a 0 defensively.
func (s *UserQueryLimitStore) Set(ctx context.Context, userID string, limit int) error {
	if s.db == nil {
		return fmt.Errorf("store: user query limit store has no pool configured")
	}
	const q = `INSERT INTO ai_user_query_limit (user_id, daily_limit)
	           VALUES ($1, $2)
	           ON CONFLICT (user_id) DO UPDATE
	             SET daily_limit = EXCLUDED.daily_limit, updated_at = now()`
	if _, err := s.db.Pool().Exec(ctx, q, userID, limit); err != nil {
		return fmt.Errorf("store: set user query limit: %w", err)
	}
	return nil
}

// Delete removes any override for the user so they revert to the global
// default. Deleting a user with no override is a no-op (not an error).
func (s *UserQueryLimitStore) Delete(ctx context.Context, userID string) error {
	if s.db == nil {
		return fmt.Errorf("store: user query limit store has no pool configured")
	}
	const q = `DELETE FROM ai_user_query_limit WHERE user_id = $1`
	if _, err := s.db.Pool().Exec(ctx, q, userID); err != nil {
		return fmt.Errorf("store: delete user query limit: %w", err)
	}
	return nil
}
