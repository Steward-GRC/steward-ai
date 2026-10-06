// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package readiness registers the ai service's dependencies with
// go-buildinfo's health checker. Postgres, RabbitMQ and Valkey are required:
// the indexes, the publish events, the job results and the counters live
// there. While service-to-service authentication is on, the issuer's key set
// is required too. The embeddings server and the generative provider are
// optional: without them the AI calls fail with a coded error, but the
// service keeps serving its settings and stored results, so it reports
// degraded instead of draining. With authentication switched off
// (WORKLOAD_AUTH=disabled) it reports degraded as well.
package readiness

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Bugs5382/go-buildinfo/health"
	postgres "github.com/Bugs5382/go-postgres"
)

// Dependency names, as they appear in the report and the
// steward-depstate-<name> headers.
const (
	Postgres   = "postgres"
	RabbitMQ   = "rabbitmq"
	Valkey     = "valkey"
	Embeddings = "embeddings"
	Provider   = "provider"
	// JWKS is the workload-token issuer's key set.
	JWKS = "jwks"
	// WorkloadAuth is reported, degraded, only while authentication is off.
	WorkloadAuth = "workloadauth"
	// Kubernetes is the API server the operator reconciles through.
	Kubernetes = "kubernetes"
)

// Database is the Postgres the service runs on.
type Database interface {
	Ping(ctx context.Context) error
	ServerVersion(ctx context.Context) (string, error)
}

// Broker is the RabbitMQ connection; go-rabbitmq's Conn reports it.
type Broker interface{ Healthy() bool }

// Deps are the dependencies to report. A nil Embeddings or Provider is not
// reported.
type Deps struct {
	Postgres Database
	Broker   Broker
	// Valkey pings the cache.
	Valkey func(ctx context.Context) error
	// Embeddings checks the embeddings server.
	Embeddings func(ctx context.Context) error
	// Provider reports the generative provider's last known status; it must
	// not call the provider.
	Provider func(ctx context.Context) error
	// JWKS checks the issuer's key set; nil while authentication is off.
	JWKS func(ctx context.Context) error
	// WorkloadAuthDisabled reports WORKLOAD_AUTH=disabled as degraded.
	WorkloadAuthDisabled bool
	// Kubernetes checks the API server; required when set, which only the
	// operator does.
	Kubernetes func(ctx context.Context) error
}

var (
	errBrokerDown           = errors.New("rabbitmq connection is down")
	errWorkloadAuthDisabled = errors.New("service-to-service authentication is disabled (WORKLOAD_AUTH=disabled)")
)

// New returns a checker with deps registered.
func New(d Deps, opts ...health.Option) (*health.Checker, error) {
	deps := []health.Dependency{
		{Name: Postgres, Required: true, Check: d.Postgres.Ping, Version: d.Postgres.ServerVersion},
		{Name: RabbitMQ, Required: true, Check: func(context.Context) error {
			if !d.Broker.Healthy() {
				return errBrokerDown
			}
			return nil
		}},
	}
	deps = append(deps, health.Dependency{Name: Valkey, Required: true, Check: d.Valkey})
	if d.Embeddings != nil {
		deps = append(deps, health.Dependency{Name: Embeddings, Check: d.Embeddings})
	}
	if d.Provider != nil {
		deps = append(deps, health.Dependency{Name: Provider, Check: d.Provider})
	}
	if d.JWKS != nil {
		deps = append(deps, health.Dependency{Name: JWKS, Required: true, Check: d.JWKS})
	}
	if d.Kubernetes != nil {
		deps = append(deps, health.Dependency{Name: Kubernetes, Required: true, Check: d.Kubernetes})
	}
	if d.WorkloadAuthDisabled {
		deps = append(deps, health.Dependency{Name: WorkloadAuth, Check: func(context.Context) error { return errWorkloadAuthDisabled }})
	}
	c := health.New(opts...)
	return c, c.Register(deps...)
}

// RecheckEvery wraps check so a success is kept for every, while a failure is
// retried on the next call. It keeps the JWKS check from fetching the key set
// on every probe yet lets readiness recover as soon as the issuer is back.
func RecheckEvery(check func(ctx context.Context) error, every time.Duration, now func() time.Time) func(ctx context.Context) error {
	var mu sync.Mutex
	var okAt time.Time
	return func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if !okAt.IsZero() && now().Sub(okAt) < every {
			return nil
		}
		if err := check(ctx); err != nil {
			okAt = time.Time{}
			return err
		}
		okAt = now()
		return nil
	}
}

// PostgresDB adapts go-postgres's DB.
func PostgresDB(db *postgres.DB) Database { return pgDB{db} }

type pgDB struct{ db *postgres.DB }

func (p pgDB) Ping(ctx context.Context) error { return p.db.Ping(ctx) }

// ServerVersion drops the build suffix ("16.4 (Debian 16.4-1)"), which the
// header would redact.
func (p pgDB) ServerVersion(ctx context.Context) (string, error) {
	var v string
	if err := p.db.Pool().QueryRow(ctx, "SHOW server_version").Scan(&v); err != nil {
		return "", err
	}
	if f := strings.Fields(v); len(f) > 0 {
		return f[0], nil
	}
	return v, nil
}
