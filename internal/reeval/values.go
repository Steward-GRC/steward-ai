// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package reeval

import (
	"os"
	"time"
)

// DefaultWindow is the debounce window a burst of publish and unpublish
// events is folded into before one RELATED_REEVAL job runs: long enough to
// fold a publishing wave into one run, short enough to keep suggestions
// fresh.
const DefaultWindow = 45 * time.Second

// WindowEnvVar overrides DefaultWindow with a Go duration such as "30s".
const WindowEnvVar = "AI_REEVAL_WINDOW"

// DefaultTopN is how many centroid neighbours make a policy's suggested set.
// It matches airules.RelatedPoliciesTopNDefault, so what re-evaluation writes
// is what the read path shows.
const DefaultTopN = 10

// WindowFromEnv reads WindowEnvVar, falling back to DefaultWindow when it is
// unset, unparseable or not positive.
func WindowFromEnv() time.Duration {
	if v := os.Getenv(WindowEnvVar); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return DefaultWindow
}
