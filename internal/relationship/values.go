// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package relationship learns how policies relate from use: a scheduled or
// administrator-started job, with no provider call, that combines
// co-retrieval, centroid similarity, cross-references and shared categories
// into the ai_policy_relationship weighted edges, with a blended score per
// edge under coefficients tunable at runtime. It builds the edges only; there
// is no learned re-ranker.
package relationship

import (
	"os"
	"strconv"
	"time"
)

const (
	// DefaultUsageHalfLife is the decay half-life of co-retrieval counts. A
	// co-citation loses half its weight every 30 days, so recent pairs
	// outweigh stale ones without history ever dropping out entirely.
	DefaultUsageHalfLife = 30 * 24 * time.Hour
	// UsageHalfLifeEnvVar overrides DefaultUsageHalfLife with a Go duration
	// such as "720h".
	UsageHalfLifeEnvVar = "AI_RELATIONSHIP_USAGE_HALFLIFE"

	// DefaultCandidateTopK is how many nearest centroid neighbours per policy
	// seed the candidate pairs, so the job never scores all N² pairs. It
	// matches the related-policies read size, so the edges cover what can be
	// shown.
	DefaultCandidateTopK = 10
	// CandidateTopKEnvVar overrides DefaultCandidateTopK.
	CandidateTopKEnvVar = "AI_RELATIONSHIP_CANDIDATE_TOPK"

	// DefaultScheduleInterval is how often the operator's leader creates a
	// RELATIONSHIP_LEARN job. The full job reconciles nightly; the debounced
	// re-evaluation keeps suggestions fresh in between.
	DefaultScheduleInterval = 24 * time.Hour
	// ScheduleIntervalEnvVar overrides DefaultScheduleInterval with a Go
	// duration such as "24h".
	ScheduleIntervalEnvVar = "AI_RELATIONSHIP_LEARN_INTERVAL"
)

// UsageHalfLifeFromEnv reads UsageHalfLifeEnvVar, falling back to
// DefaultUsageHalfLife when it is unset or unparseable.
func UsageHalfLifeFromEnv() time.Duration {
	return durationFromEnv(UsageHalfLifeEnvVar, DefaultUsageHalfLife)
}

// CandidateTopKFromEnv reads CandidateTopKEnvVar, falling back to
// DefaultCandidateTopK.
func CandidateTopKFromEnv() int {
	if v := os.Getenv(CandidateTopKEnvVar); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultCandidateTopK
}

// ScheduleIntervalFromEnv reads ScheduleIntervalEnvVar, falling back to
// DefaultScheduleInterval.
func ScheduleIntervalFromEnv() time.Duration {
	return durationFromEnv(ScheduleIntervalEnvVar, DefaultScheduleInterval)
}

func durationFromEnv(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}
