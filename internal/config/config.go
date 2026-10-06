// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package config reads the ai service's and the operator's settings from the
// environment. It is the only place either reads an environment variable. A
// bad value fails the boot instead of falling back to a default.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/store"
	"github.com/Steward-GRC/steward-ai/internal/workloadauth"
)

// The embeddings providers AI_EMBED_PROVIDER takes.
const (
	EmbedTEI    = "tei"
	EmbedOpenAI = "openai"
	EmbedStub   = "stub"
)

// Defaults the operator shares with its tests.
const (
	DefaultMaxAttempts  = 3
	DefaultSucceededTTL = 15 * time.Minute
	DefaultFailedTTL    = 30 * 24 * time.Hour
)

// Embed configures the embeddings provider. It is set at deployment, not in
// the admin settings: a different model means a reindex.
type Embed struct {
	// Provider is tei (the built-in server, the default), openai (any
	// OpenAI-compatible embeddings endpoint) or stub (local runs only).
	Provider string
	Endpoint string
	Model    string
	// Credential is the API key for an openai endpoint, if it needs one.
	Credential string
}

// Common is what the server and the operator both need.
type Common struct {
	DatabaseDSN  string
	RabbitURL    string
	RedisAddr    string
	RedisPass    string
	OTLPEndpoint string
	// SettingsKey seals the provider credential in ai_config.
	SettingsKey []byte
	ProbePort   string
	Embed       Embed
	// GenerationStub answers every call with a labelled stand-in, with no
	// provider, for local runs only.
	GenerationStub bool
	// JobNamespace is where PolicyAIJob resources live.
	JobNamespace string
}

// Config is the server's settings.
type Config struct {
	Common
	// MigrateDSN is a direct connection for migrations; defaults to
	// DatabaseDSN.
	MigrateDSN        string
	MigrationsDir     string
	GRPCPort          string
	ChunkSize         int
	ChunkOverlap      int
	RetrievalTopK     int
	DefaultQueryLimit int
	// BackfillCentroids runs the one-shot centroid backfill and exits.
	BackfillCentroids bool
	// WorkloadAuth is false only for WORKLOAD_AUTH=disabled, on local runs.
	WorkloadAuth bool
	Workload     workloadauth.Config
}

// Operator is the operator's settings.
type Operator struct {
	Common
	MetricsPort    string
	LeaderElection bool
	MaxAttempts    int
	// SucceededTTL and FailedTTL are how long finished jobs are kept; zero
	// keeps them.
	SucceededTTL          time.Duration
	FailedTTL             time.Duration
	DraftSectionMaxTokens int
	ReviseMaxTokens       int
}

type reader struct {
	getenv func(string) string
	errs   []error
}

func (r *reader) str(k, d string) string {
	if v := r.getenv(k); v != "" {
		return v
	}
	return d
}

func (r *reader) required(k string) string {
	v := r.getenv(k)
	if v == "" {
		r.errs = append(r.errs, fmt.Errorf("%s is required", k))
	}
	return v
}

func (r *reader) int(k string, d int, ok func(int) bool) int {
	v := r.getenv(k)
	if v == "" {
		return d
	}
	n, err := strconv.Atoi(v)
	if err != nil || !ok(n) {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is out of range", k, v))
		return d
	}
	return n
}

func (r *reader) bool(k string, d bool) bool {
	v := r.getenv(k)
	if v == "" {
		return d
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not a boolean", k, v))
		return d
	}
	return b
}

func (r *reader) duration(k string, d time.Duration) time.Duration {
	v := r.getenv(k)
	if v == "" {
		return d
	}
	dur, err := time.ParseDuration(v)
	if err != nil || dur < 0 {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not a duration", k, v))
		return d
	}
	return dur
}

func positive(n int) bool    { return n > 0 }
func nonNegative(n int) bool { return n >= 0 }

func (r *reader) common() Common {
	c := Common{
		DatabaseDSN:    r.required("DATABASE_DSN"),
		RabbitURL:      r.required("RABBITMQ_URL"),
		RedisAddr:      r.required("REDIS_ADDR"),
		RedisPass:      r.getenv("REDIS_PASSWORD"),
		OTLPEndpoint:   r.str("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		ProbePort:      r.str("PROBE_PORT", "8080"),
		GenerationStub: r.bool("AI_GENERATION_STUB", false),
		JobNamespace:   r.str("AI_JOB_NAMESPACE", r.str("POD_NAMESPACE", "steward")),
		Embed: Embed{
			Provider:   r.str("AI_EMBED_PROVIDER", EmbedTEI),
			Endpoint:   r.str("EMBED_ENDPOINT", "http://steward-ai-embeddings/embed"),
			Model:      r.getenv("AI_EMBED_MODEL"),
			Credential: r.getenv("AI_EMBED_API_KEY"),
		},
	}
	switch c.Embed.Provider {
	case EmbedTEI, EmbedStub:
	case EmbedOpenAI:
		if c.Embed.Model == "" {
			r.errs = append(r.errs, errors.New("AI_EMBED_MODEL is required when AI_EMBED_PROVIDER=openai"))
		}
	default:
		r.errs = append(r.errs, fmt.Errorf("AI_EMBED_PROVIDER must be %s, %s or %s, got %q", EmbedTEI, EmbedOpenAI, EmbedStub, c.Embed.Provider))
	}
	if raw := r.required("AI_SETTINGS_KEY"); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(key) != store.SettingsKeySize {
			r.errs = append(r.errs, fmt.Errorf("AI_SETTINGS_KEY must be %d bytes, base64-encoded", store.SettingsKeySize))
		} else {
			c.SettingsKey = key
		}
	}
	return c
}

// Load reads the server's settings through getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	r := &reader{getenv: getenv}
	c := Config{
		Common:            r.common(),
		MigrationsDir:     r.str("MIGRATIONS_DIR", "migrations"),
		GRPCPort:          r.str("GRPC_PORT", "9090"),
		ChunkSize:         r.int(airules.ChunkSizeEnvVar, airules.ChunkSizeDefault, positive),
		ChunkOverlap:      r.int(airules.ChunkOverlapEnvVar, airules.ChunkOverlapDefault, nonNegative),
		RetrievalTopK:     r.int(airules.RetrievalTopKEnvVar, airules.RetrievalTopKDefault, positive),
		DefaultQueryLimit: r.int(airules.QueryQuotaDefaultEnvVar, airules.QueryQuotaDefault, func(n int) bool { return n > 0 || n == airules.UnlimitedQueryQuota }),
		BackfillCentroids: r.bool("AI_BACKFILL_CENTROIDS", false),
	}
	c.MigrateDSN = r.str("MIGRATE_DSN", c.DatabaseDSN)
	var err error
	if c.Workload, c.WorkloadAuth, err = workloadauth.ServerConfigFromEnv(getenv); err != nil {
		r.errs = append(r.errs, err)
	}
	return c, errors.Join(r.errs...)
}

// LoadOperator reads the operator's settings through getenv.
func LoadOperator(getenv func(string) string) (Operator, error) {
	r := &reader{getenv: getenv}
	c := Operator{
		Common:                r.common(),
		MetricsPort:           r.str("METRICS_PORT", "9090"),
		LeaderElection:        r.bool("ENABLE_LEADER_ELECTION", true),
		MaxAttempts:           r.int("AI_JOB_MAX_ATTEMPTS", DefaultMaxAttempts, positive),
		SucceededTTL:          r.duration("AI_JOB_SUCCEEDED_TTL", DefaultSucceededTTL),
		FailedTTL:             r.duration("AI_JOB_FAILED_TTL", DefaultFailedTTL),
		DraftSectionMaxTokens: r.int(airules.DraftSectionMaxTokensEnvVar, airules.DraftSectionMaxTokensDefault, positive),
		ReviseMaxTokens:       r.int(airules.ReviseMaxTokensEnvVar, airules.ReviseMaxTokensDefault, positive),
	}
	return c, errors.Join(r.errs...)
}
