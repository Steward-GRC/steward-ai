// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
	"github.com/jackc/pgx/v5"
)

// Usage is the provider traffic counted for one calendar month (UTC).
type Usage struct {
	Requests     int64
	InputTokens  int64
	OutputTokens int64
}

// UsageStore keeps the usage meter in ai_usage_monthly.
type UsageStore struct {
	db *postgres.DB
}

// NewUsageStore returns a store on db.
func NewUsageStore(db *postgres.DB) *UsageStore { return &UsageStore{db: db} }

// MonthStart is the first instant of t's calendar month in UTC.
func MonthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Add counts u against the month of at.
func (s *UsageStore) Add(ctx context.Context, at time.Time, u Usage) error {
	const q = `
INSERT INTO ai_usage_monthly (month, requests, input_tokens, output_tokens)
VALUES ($1, $2, $3, $4)
ON CONFLICT (month) DO UPDATE SET
    requests      = ai_usage_monthly.requests + EXCLUDED.requests,
    input_tokens  = ai_usage_monthly.input_tokens + EXCLUDED.input_tokens,
    output_tokens = ai_usage_monthly.output_tokens + EXCLUDED.output_tokens,
    updated_at    = now()`
	if _, err := s.db.Pool().Exec(ctx, q, MonthStart(at), u.Requests, u.InputTokens, u.OutputTokens); err != nil {
		return fmt.Errorf("store: add usage: %w", err)
	}
	return nil
}

// Month reads the usage of at's month; a month with no traffic is zero.
func (s *UsageStore) Month(ctx context.Context, at time.Time) (Usage, error) {
	var u Usage
	err := s.db.Pool().QueryRow(ctx,
		`SELECT requests, input_tokens, output_tokens FROM ai_usage_monthly WHERE month = $1`,
		MonthStart(at)).Scan(&u.Requests, &u.InputTokens, &u.OutputTokens)
	if errors.Is(err, pgx.ErrNoRows) {
		return Usage{}, nil
	}
	if err != nil {
		return Usage{}, fmt.Errorf("store: read usage: %w", err)
	}
	return u, nil
}
