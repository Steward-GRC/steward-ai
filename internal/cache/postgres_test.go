// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pg "github.com/Bugs5382/go-postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// pgvectorImage is Postgres with the vector extension the schema needs.
const pgvectorImage = "pgvector/pgvector:pg16"

var (
	containerOnce sync.Once
	containerDSN  string
	containerErr  error
	pgContainer   *postgres.PostgresContainer
)

// newTestDB returns a freshly migrated database for the calling test, on
// DATABASE_TEST_DSN when it is set (a Postgres with pgvector), otherwise on
// one pgvector container shared by the package.
func newTestDB(t *testing.T) *pg.DB {
	t.Helper()
	base := os.Getenv("DATABASE_TEST_DSN")
	if base == "" {
		base = sharedContainerDSN(t)
	}
	return newDatabase(t, base)
}

func sharedContainerDSN(t *testing.T) string {
	t.Helper()
	containerOnce.Do(func() {
		ctx := context.Background()
		pgContainer, containerErr = postgres.Run(ctx, pgvectorImage,
			postgres.WithDatabase("ai_test"),
			postgres.WithUsername("test"),
			postgres.WithPassword("test"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(90*time.Second),
			),
		)
		if containerErr != nil {
			return
		}
		containerDSN, containerErr = pgContainer.ConnectionString(ctx, "sslmode=disable")
	})
	if containerErr != nil {
		t.Fatalf("start postgres container: %v", containerErr)
	}
	return containerDSN
}

func newDatabase(t *testing.T, baseDSN string) *pg.DB {
	t.Helper()
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	defer admin.Close()

	dbName := uniqueDBName()
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, dbName)); err != nil {
		t.Fatalf("create db %s: %v", dbName, err)
	}

	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse base dsn: %v", err)
	}
	u.Path = "/" + dbName
	testDSN := u.String()

	migrationsDir, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatalf("resolve migrations dir: %v", err)
	}
	if err := pg.Migrate(testDSN, migrationsDir); err != nil {
		t.Fatalf("migrate %s: %v", dbName, err)
	}

	db, err := pg.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("postgres.New: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		dropCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		dropAdmin, err := pgxpool.New(dropCtx, baseDSN)
		if err != nil {
			return
		}
		defer dropAdmin.Close()
		_, _ = dropAdmin.Exec(dropCtx, fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, dbName))
	})
	return db
}

func uniqueDBName() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "test_" + hex.EncodeToString(b[:])
}
