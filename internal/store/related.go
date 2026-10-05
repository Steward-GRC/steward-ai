// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"

	postgres "github.com/Bugs5382/go-postgres"
	"github.com/jackc/pgx/v5"
)

// Suggestion status values. A suggestion starts 'suggested'; an author who
// accepts it promotes it to core (via SetRelatedPolicies) and the row is kept
// as an 'accepted' tombstone; an author who rejects it marks it 'dismissed'.
// The re-eval diff (RelatedStore.ApplyDiff) NEVER re-proposes a related_id that
// already has an 'accepted' or 'dismissed' row, so a decided suggestion stays
// decided across corpus churn.
const (
	// SuggestionStatusSuggested is an open, un-actioned AI suggestion — the
	// only status the read path surfaces and the only one re-eval deletes when
	// a policy is no longer a neighbour.
	SuggestionStatusSuggested = "suggested"
	// SuggestionStatusAccepted marks a suggestion the author promoted to a real
	// core link; kept as a "don't re-suggest" tombstone/provenance record.
	SuggestionStatusAccepted = "accepted"
	// SuggestionStatusDismissed marks a suggestion the author rejected; kept so
	// re-eval never re-proposes it.
	SuggestionStatusDismissed = "dismissed"
)

// SuggestionSourceCentroid marks a suggestion derived from centroid KNN
// — the cold-start source the re-evaluation writes before the relationship
// substrate has an edge for a pair.
const SuggestionSourceCentroid = "centroid"

// SuggestionSourceBlend marks a suggestion whose Score came from the
// relationship substrate's blended_score rather than raw centroid
// distance — i.e. a pair the RELATIONSHIP_LEARN job has already learned an edge
// for. The re-eval engine stamps this when it prefers a blended score over the
// centroid fallback.
const SuggestionSourceBlend = "blend"

// Suggestion is one row of the ai_related_policies AI-suggestion store: a
// proposed related policy for PolicyID, with the blended Score and the
// CentroidDist component it was derived from. This is NOT a core author-owned
// link — only an author accepting it writes core.policy_relations.
type Suggestion struct {
	PolicyID      string
	RelatedID     string
	Score         float32
	CentroidDist  float32
	Source        string
	Status        string
	CorpusVersion int64
}

// DiffResult is what one policy's re-eval produced against the prior suggestion
// set: the related ids newly proposed this pass and the ids that were dropped
// (a previously-'suggested' row deleted because the policy is no longer a
// neighbour). Accepted/dismissed rows are never touched, so they appear in
// neither list. The completion event carries this so the UI can badge
// "newly suggested / no-longer-suggested" without re-reading the whole set.
type DiffResult struct {
	NewlySuggested    []string `json:"newlySuggested,omitempty"`
	NoLongerSuggested []string `json:"noLongerSuggested,omitempty"`
}

// RelatedStore reads and writes rows in the ai_related_policies suggestion
// store. It is the WRITE side of the read/write split with CentroidStore:
// CentroidStore.RelatedPolicies reads suggestions (preferring them over live
// KNN); RelatedStore.ApplyDiff is how the RELATED_REEVAL job (internal/reeval)
// maintains them.
type RelatedStore struct {
	db *postgres.DB
}

// NewRelatedStore constructs a RelatedStore backed by the given database. A nil
// pool is permitted for compile-time wiring and unit tests; methods return an
// error rather than panic, matching CentroidStore.
func NewRelatedStore(db *postgres.DB) *RelatedStore {
	return &RelatedStore{db: db}
}

// existingRow is the (status) of a prior suggestion for a related_id, loaded
// by ApplyDiff to decide add vs update vs keep-tombstone vs delete.
type existingRow struct {
	status string
}

// ApplyDiff reconciles the ai_related_policies rows for policyID against the
// freshly-computed desired suggestion set, writing the difference (never a
// blind overwrite) in one transaction, and returns what changed.
//
// Rules:
//   - a desired related_id with NO existing row -> INSERT status='suggested'
//     (reported in NewlySuggested);
//   - a desired related_id whose existing row is 'suggested' -> UPDATE its
//     score/centroid_dist/source/corpus_version (kept, not reported);
//   - a desired related_id whose existing row is 'accepted'/'dismissed' ->
//     LEFT UNTOUCHED (never re-propose a decided suggestion);
//   - an existing 'suggested' row NOT in the desired set -> DELETE (reported in
//     NoLongerSuggested);
//   - an existing 'accepted'/'dismissed' row not in the desired set -> KEPT as
//     a tombstone.
//
// desired's Status/PolicyID fields are ignored — policyID and 'suggested' are
// authoritative here; callers need only fill RelatedID/Score/CentroidDist/
// Source. corpusVersion stamps every inserted/updated row for provenance.
func (s *RelatedStore) ApplyDiff(ctx context.Context, policyID string, desired []Suggestion, corpusVersion int64) (DiffResult, error) {
	var out DiffResult
	if s.db == nil {
		return out, fmt.Errorf("store: pool is nil")
	}
	if policyID == "" {
		return out, fmt.Errorf("store: policyID is required")
	}

	// Dedupe desired by related_id (defensive; UNIQUE(policy_id,related_id)
	// would otherwise reject a dup INSERT) and drop any self-reference.
	desiredByID := make(map[string]Suggestion, len(desired))
	for _, d := range desired {
		if d.RelatedID == "" || d.RelatedID == policyID {
			continue
		}
		desiredByID[d.RelatedID] = d
	}

	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Load the current rows for this policy, locking them for the transaction
	// so a concurrent re-eval of the same policy can't interleave.
	rows, err := tx.Query(ctx,
		`SELECT related_id, status FROM ai_related_policies WHERE policy_id = $1 FOR UPDATE`, policyID)
	if err != nil {
		return out, err
	}
	existing := make(map[string]existingRow)
	for rows.Next() {
		var rid, st string
		if err := rows.Scan(&rid, &st); err != nil {
			rows.Close()
			return out, err
		}
		existing[rid] = existingRow{status: st}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	// Upsert the desired set.
	for rid, d := range desiredByID {
		ex, ok := existing[rid]
		switch {
		case !ok:
			if _, err := tx.Exec(ctx, `
				INSERT INTO ai_related_policies
					(policy_id, related_id, score, centroid_dist, source, status, corpus_version)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
			`, policyID, rid, d.Score, d.CentroidDist, sourceOrDefault(d.Source), SuggestionStatusSuggested, corpusVersion); err != nil {
				return out, err
			}
			out.NewlySuggested = append(out.NewlySuggested, rid)
		case ex.status == SuggestionStatusSuggested:
			if _, err := tx.Exec(ctx, `
				UPDATE ai_related_policies
				SET score = $3, centroid_dist = $4, source = $5, corpus_version = $6, suggested_at = now()
				WHERE policy_id = $1 AND related_id = $2 AND status = 'suggested'
			`, policyID, rid, d.Score, d.CentroidDist, sourceOrDefault(d.Source), corpusVersion); err != nil {
				return out, err
			}
		default:
			// accepted / dismissed -> tombstone; never re-propose.
		}
	}

	// Delete stale 'suggested' rows no longer in the desired set.
	for rid, ex := range existing {
		if _, want := desiredByID[rid]; want {
			continue
		}
		if ex.status != SuggestionStatusSuggested {
			continue // keep accepted/dismissed tombstones
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM ai_related_policies WHERE policy_id = $1 AND related_id = $2 AND status = 'suggested'`,
			policyID, rid); err != nil {
			return out, err
		}
		out.NoLongerSuggested = append(out.NoLongerSuggested, rid)
	}

	if err := tx.Commit(ctx); err != nil {
		return out, err
	}
	return out, nil
}

// ListByPolicy returns every suggestion row (any status) for policyID, ordered
// by score descending. Primarily for tests/inspection; the read path uses
// CentroidStore.RelatedPolicies (which joins centroids for access filtering).
func (s *RelatedStore) ListByPolicy(ctx context.Context, policyID string) ([]Suggestion, error) {
	if s.db == nil {
		return nil, fmt.Errorf("store: pool is nil")
	}
	rows, err := s.db.Pool().Query(ctx, `
		SELECT policy_id, related_id, score, centroid_dist, source, status, corpus_version
		FROM ai_related_policies
		WHERE policy_id = $1
		ORDER BY score DESC
	`, policyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()

	var out []Suggestion
	for rows.Next() {
		var g Suggestion
		if err := rows.Scan(&g.PolicyID, &g.RelatedID, &g.Score, &g.CentroidDist, &g.Source, &g.Status, &g.CorpusVersion); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetStatus records an author decision on a suggestion — 'accepted' after the
// gateway promotes it to a core link, or 'dismissed' on reject — so re-eval
// stops re-proposing it. No-op (returns nil) if the row does not exist. Wiring
// the gateway acceptance path onto this is a later (co-design) slice; the write
// primitive lives here so the tombstone contract is testable now.
func (s *RelatedStore) SetStatus(ctx context.Context, policyID, relatedID, status string) error {
	if s.db == nil {
		return fmt.Errorf("store: pool is nil")
	}
	switch status {
	case SuggestionStatusAccepted, SuggestionStatusDismissed, SuggestionStatusSuggested:
	default:
		return fmt.Errorf("store: invalid suggestion status %q", status)
	}
	_, err := s.db.Pool().Exec(ctx,
		`UPDATE ai_related_policies SET status = $3 WHERE policy_id = $1 AND related_id = $2`,
		policyID, relatedID, status)
	return err
}

func sourceOrDefault(src string) string {
	if src == "" {
		return SuggestionSourceCentroid
	}
	return src
}
