// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package aiconfig_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	redis "github.com/Bugs5382/go-redis"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// valkeyDB keeps this package's keys apart from the other packages' when
// VALKEY_TEST_ADDR points every package at one server.
const valkeyDB = 1

var (
	valkeyOnce      sync.Once
	valkeyAddr      string
	valkeyErr       error
	valkeyContainer testcontainers.Container
)

func TestMain(m *testing.M) {
	code := m.Run()
	if valkeyContainer != nil {
		_ = valkeyContainer.Terminate(context.Background())
	}
	os.Exit(code)
}

// newValkey returns a client on an empty database, on VALKEY_TEST_ADDR when
// it is set, otherwise on one Valkey container shared by the package.
func newValkey(t *testing.T) redis.UniversalClient {
	t.Helper()
	addr := os.Getenv("VALKEY_TEST_ADDR")
	if addr == "" {
		valkeyOnce.Do(startValkey)
		if valkeyErr != nil {
			t.Fatalf("start valkey container: %v", valkeyErr)
		}
		addr = valkeyAddr
	}
	ctx := context.Background()
	c, err := redis.Connect(ctx, redis.WithAddr(addr), redis.WithDB(valkeyDB))
	if err != nil {
		t.Fatalf("connect valkey: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Redis().FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush valkey: %v", err)
	}
	return c.Redis()
}

func startValkey() {
	ctx := context.Background()
	valkeyContainer, valkeyErr = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "valkey/valkey:8",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if valkeyErr != nil {
		return
	}
	valkeyAddr, valkeyErr = valkeyContainer.PortEndpoint(ctx, "6379/tcp", "")
}
