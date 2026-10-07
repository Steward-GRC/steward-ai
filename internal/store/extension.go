// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrVectorUnavailable means the Postgres server can't provide the vector
// extension (pgvector) the schema needs.
var ErrVectorUnavailable = errors.New("the Postgres server has no vector extension (pgvector): " +
	"install pgvector on the server, or run a Postgres build that ships it (such as pgvector/pgvector:pg16), then restart ai")

// RowQuerier runs one query returning one row; pgx.Conn and pgxpool.Pool
// satisfy it.
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// RequireVector fails with ErrVectorUnavailable unless the vector extension
// is installed in the database or available on the server for the baseline
// migration to create. It runs before the migration, so a server without it
// never leaves the schema half-migrated.
func RequireVector(ctx context.Context, q RowQuerier) error {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')
		OR EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector')`).Scan(&ok)
	if err != nil {
		return fmt.Errorf("check the vector extension: %w", err)
	}
	if !ok {
		return ErrVectorUnavailable
	}
	return nil
}
