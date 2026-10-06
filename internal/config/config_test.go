// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-ai/internal/config"
)

var testKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func required() map[string]string {
	return map[string]string{
		"DATABASE_DSN":    "postgres://ai:ai@db.example.org:5432/ai",
		"RABBITMQ_URL":    "amqp://guest:guest@mq.example.org:5672/",
		"REDIS_ADDR":      "valkey.example.org:6379",
		"AI_SETTINGS_KEY": testKey,
		"WORKLOAD_AUTH":   "disabled",
	}
}

func with(m map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func TestLoadDefaults(t *testing.T) {
	c, err := config.Load(env(required()))
	require.NoError(t, err)
	require.Equal(t, c.DatabaseDSN, c.MigrateDSN)
	require.Equal(t, "migrations", c.MigrationsDir)
	require.Equal(t, "9090", c.GRPCPort)
	require.Equal(t, "8080", c.ProbePort)
	require.Len(t, c.SettingsKey, 32)
	require.Equal(t, "tei", c.Embed.Provider)
	require.Equal(t, "http://steward-ai-embeddings/embed", c.Embed.Endpoint)
	require.False(t, c.GenerationStub)
	require.Equal(t, 350, c.ChunkSize)
	require.Equal(t, 64, c.ChunkOverlap)
	require.Equal(t, 50, c.RetrievalTopK)
	require.Equal(t, 50, c.DefaultQueryLimit)
	require.Equal(t, "steward", c.JobNamespace)
	require.False(t, c.WorkloadAuth)
	require.False(t, c.BackfillCentroids)
}

func TestLoadRequiresTheDependencies(t *testing.T) {
	for _, k := range []string{"DATABASE_DSN", "RABBITMQ_URL", "REDIS_ADDR", "AI_SETTINGS_KEY"} {
		_, err := config.Load(env(with(required(), k, "")))
		require.ErrorContains(t, err, k, k)
	}
}

func TestLoadRefusesABadSettingsKey(t *testing.T) {
	_, err := config.Load(env(with(required(), "AI_SETTINGS_KEY", base64.StdEncoding.EncodeToString([]byte("short")))))
	require.ErrorContains(t, err, "AI_SETTINGS_KEY")
	_, err = config.Load(env(with(required(), "AI_SETTINGS_KEY", "not base64!")))
	require.ErrorContains(t, err, "AI_SETTINGS_KEY")
}

func TestLoadRefusesBadValues(t *testing.T) {
	cases := [][]string{
		{"AI_EMBED_PROVIDER", "bedrock"},
		{"AI_CHUNK_SIZE", "many"},
		{"AI_CHUNK_SIZE", "0"},
		{"AI_CHUNK_OVERLAP", "-1"},
		{"AI_RETRIEVAL_TOP_K", "x"},
		{"AI_QUERY_QUOTA_DEFAULT", "0"},
		{"AI_GENERATION_STUB", "sometimes"},
		{"AI_BACKFILL_CENTROIDS", "maybe"},
	}
	for _, kv := range cases {
		_, err := config.Load(env(with(required(), kv...)))
		require.ErrorContains(t, err, kv[0], kv)
	}
}

func TestLoadOpenAIEmbeddingsNeedAModel(t *testing.T) {
	_, err := config.Load(env(with(required(), "AI_EMBED_PROVIDER", "openai", "EMBED_ENDPOINT", "https://embeddings.example.org/v1")))
	require.ErrorContains(t, err, "AI_EMBED_MODEL")
	c, err := config.Load(env(with(required(), "AI_EMBED_PROVIDER", "openai", "EMBED_ENDPOINT", "https://embeddings.example.org/v1",
		"AI_EMBED_MODEL", "example-embed", "AI_EMBED_API_KEY", "test-key-1")))
	require.NoError(t, err)
	require.Equal(t, "test-key-1", c.Embed.Credential)
}

func TestLoadQuotaCanBeUnlimited(t *testing.T) {
	c, err := config.Load(env(with(required(), "AI_QUERY_QUOTA_DEFAULT", "-1")))
	require.NoError(t, err)
	require.Equal(t, -1, c.DefaultQueryLimit)
}

func TestLoadJobNamespaceFollowsThePod(t *testing.T) {
	c, err := config.Load(env(with(required(), "POD_NAMESPACE", "steward-test")))
	require.NoError(t, err)
	require.Equal(t, "steward-test", c.JobNamespace)
	c, err = config.Load(env(with(required(), "POD_NAMESPACE", "steward-test", "AI_JOB_NAMESPACE", "steward-jobs")))
	require.NoError(t, err)
	require.Equal(t, "steward-jobs", c.JobNamespace)
}

func TestLoadWorkloadAuthIsOnByDefault(t *testing.T) {
	m := required()
	delete(m, "WORKLOAD_AUTH")
	_, err := config.Load(env(m))
	require.Error(t, err, "auth on with no issuer settings must fail the boot")
}

func TestLoadOperatorDefaults(t *testing.T) {
	c, err := config.LoadOperator(env(required()))
	require.NoError(t, err)
	require.Equal(t, "8080", c.ProbePort)
	require.Equal(t, "9090", c.MetricsPort)
	require.True(t, c.LeaderElection)
	require.Equal(t, 3, c.MaxAttempts)
	require.Equal(t, 15*time.Minute, c.SucceededTTL)
	require.Equal(t, 30*24*time.Hour, c.FailedTTL)
	require.Equal(t, "steward", c.JobNamespace)
	require.Equal(t, 32000, c.DraftSectionMaxTokens)
	require.Equal(t, 32000, c.ReviseMaxTokens)
}

func TestLoadOperatorRefusesBadValues(t *testing.T) {
	for _, kv := range [][]string{
		{"AI_JOB_MAX_ATTEMPTS", "0"},
		{"AI_JOB_SUCCEEDED_TTL", "soon"},
		{"AI_JOB_FAILED_TTL", "later"},
		{"ENABLE_LEADER_ELECTION", "perhaps"},
		{"AI_DRAFT_SECTION_MAX_TOKENS", "-5"},
	} {
		_, err := config.LoadOperator(env(with(required(), kv...)))
		require.ErrorContains(t, err, kv[0], kv)
	}
	for _, k := range []string{"DATABASE_DSN", "RABBITMQ_URL", "REDIS_ADDR", "AI_SETTINGS_KEY"} {
		_, err := config.LoadOperator(env(with(required(), k, "")))
		require.ErrorContains(t, err, k, k)
	}
}

func TestLoadOperatorTTLsMayBeZero(t *testing.T) {
	c, err := config.LoadOperator(env(with(required(), "AI_JOB_SUCCEEDED_TTL", "0s", "AI_JOB_FAILED_TTL", "0s")))
	require.NoError(t, err)
	require.Zero(t, c.SucceededTTL, "zero keeps finished jobs")
	require.Zero(t, c.FailedTTL)
}
