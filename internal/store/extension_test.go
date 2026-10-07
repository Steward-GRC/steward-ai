// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// plainPostgresImage is Postgres without pgvector.
const plainPostgresImage = "postgres:16-alpine"

func TestRequireVectorPassesOnAPgvectorServer(t *testing.T) {
	conn := connect(t, sharedContainerDSN(t))
	if err := store.RequireVector(context.Background(), conn); err != nil {
		t.Fatalf("RequireVector on pgvector: %v", err)
	}
}

func TestRequireVectorStopsOnAServerWithoutIt(t *testing.T) {
	ctx := context.Background()
	c, err := postgres.Run(ctx, plainPostgresImage,
		postgres.WithDatabase("ai_plain"), postgres.WithUsername("test"), postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(90*time.Second)))
	if err != nil {
		t.Fatalf("start plain postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}

	err = store.RequireVector(ctx, connect(t, dsn))
	if !errors.Is(err, store.ErrVectorUnavailable) {
		t.Fatalf("RequireVector without pgvector: got %v, want ErrVectorUnavailable", err)
	}
}

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}
