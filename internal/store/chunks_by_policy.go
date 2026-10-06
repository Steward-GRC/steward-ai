// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
)

// DeleteByPolicyID removes every chunk of a policy, whatever its version. A
// retired document is named only by its policy id, so this is how its chunks
// leave the index.
func (s *ChunkStore) DeleteByPolicyID(ctx context.Context, policyID string) error {
	if s.db == nil {
		return fmt.Errorf("store: pool is nil")
	}
	_, err := s.db.Pool().Exec(ctx,
		`DELETE FROM ai_chunks WHERE policy_id = $1`, policyID)
	return err
}
