// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package reeval

import (
	"context"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Bugs5382/go-redis"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	valkeyOnce      sync.Once
	valkeyContainer testcontainers.Container
	valkeyAddress   string
	valkeyErr       error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if valkeyContainer != nil {
		_ = testcontainers.TerminateContainer(valkeyContainer)
	}
	os.Exit(code)
}

// valkeyAddr starts one Valkey container for the package's tests, on first
// use.
func valkeyAddr(t *testing.T) string {
	t.Helper()
	valkeyOnce.Do(func() {
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
		host, err := valkeyContainer.Host(ctx)
		if err != nil {
			valkeyErr = err
			return
		}
		port, err := valkeyContainer.MappedPort(ctx, "6379/tcp")
		if err != nil {
			valkeyErr = err
			return
		}
		valkeyAddress = host + ":" + port.Port()
	})
	if valkeyErr != nil {
		t.Fatalf("start valkey: %v", valkeyErr)
	}
	return valkeyAddress
}

var testDB = 0

// newTestRedis connects to the shared Valkey on a fresh, flushed database so
// tests don't see each other's keys.
func newTestRedis(t *testing.T) redis.UniversalClient {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker")
	}
	addr := valkeyAddr(t)
	testDB++
	ctx := context.Background()
	c, err := redis.Connect(ctx, redis.WithAddr(addr), redis.WithDB(testDB%16))
	if err != nil {
		t.Fatalf("connect valkey: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	rdb := c.Redis()
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return rdb
}

// TestPendingSet_EnqueueCoalescesBurst is the debounce invariant: a burst of
// enqueues within the window opens exactly ONE window (only the first
// returns opened=true), so exactly one RELATED_REEVAL job is scheduled for the
// whole burst.
func TestPendingSet_EnqueueCoalescesBurst(t *testing.T) {
	ctx := context.Background()
	ps := NewPendingSet(newTestRedis(t), time.Minute)

	openedCount := 0
	for _, id := range []string{"p1", "p2", "p3", "p1", "p4"} {
		opened, err := ps.Enqueue(ctx, id)
		if err != nil {
			t.Fatalf("Enqueue(%s): %v", id, err)
		}
		if opened {
			openedCount++
		}
	}
	if openedCount != 1 {
		t.Fatalf("expected exactly one enqueue to open the window, got %d", openedCount)
	}
}

// TestPendingSet_DrainReturnsDedupedSetAndClears verifies the drain returns the
// full coalesced (deduped) id set and empties it, so a second drain is empty.
func TestPendingSet_DrainReturnsDedupedSetAndClears(t *testing.T) {
	ctx := context.Background()
	ps := NewPendingSet(newTestRedis(t), time.Minute)

	for _, id := range []string{"p1", "p2", "p1", "p3"} {
		if _, err := ps.Enqueue(ctx, id); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	got, err := ps.Drain(ctx)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	sort.Strings(got)
	want := []string{"p1", "p2", "p3"}
	if len(got) != len(want) {
		t.Fatalf("drain: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("drain: got %v want %v", got, want)
		}
	}

	again, err := ps.Drain(ctx)
	if err != nil {
		t.Fatalf("Drain (2nd): %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second drain should be empty, got %v", again)
	}
}

// TestPendingSet_WindowReopensAfterExpiry confirms a new window (and thus a new
// job) opens once the debounce window elapses — a later burst is not swallowed.
func TestPendingSet_WindowReopensAfterExpiry(t *testing.T) {
	ctx := context.Background()
	// A short real window, since Valkey's TTL can't be fast-forwarded.
	ps := NewPendingSet(newTestRedis(t), time.Second)

	opened1, _ := ps.Enqueue(ctx, "p1")
	opened2, _ := ps.Enqueue(ctx, "p2")
	if !opened1 || opened2 {
		t.Fatalf("within window: want first opened, second not; got %v/%v", opened1, opened2)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		opened3, err := ps.Enqueue(ctx, "p3")
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		if opened3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("after window expiry a new enqueue should open a fresh window")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestPendingSet_CorpusVersionDefaultsToZero verifies an unset counter reads 0
// rather than erroring.
func TestPendingSet_CorpusVersionDefaultsToZero(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)
	ps := NewPendingSet(rdb, time.Minute)

	v, err := ps.CorpusVersion(ctx)
	if err != nil || v != 0 {
		t.Fatalf("CorpusVersion default: got (%d,%v) want (0,nil)", v, err)
	}
	if err := rdb.Set(ctx, corpusVersionKey, 7, 0).Err(); err != nil {
		t.Fatalf("seed corpus version: %v", err)
	}
	if v, err := ps.CorpusVersion(ctx); err != nil || v != 7 {
		t.Fatalf("CorpusVersion: got (%d,%v) want (7,nil)", v, err)
	}
}

// TestNewPendingSet_DefaultsWindow guards the <=0 fallback.
func TestNewPendingSet_DefaultsWindow(t *testing.T) {
	ps := NewPendingSet(nil, 0)
	if ps.Window() != DefaultWindow {
		t.Fatalf("window: got %v want %v", ps.Window(), DefaultWindow)
	}
}

// TestPendingSet_DrainEmptyIsNil checks an empty set drains to nil without an
// error.
func TestPendingSet_DrainEmptyIsNil(t *testing.T) {
	ps := NewPendingSet(newTestRedis(t), time.Minute)
	got, err := ps.Drain(context.Background())
	if err != nil || got != nil {
		t.Fatalf("Drain: got (%v,%v) want (nil,nil)", got, err)
	}
}
