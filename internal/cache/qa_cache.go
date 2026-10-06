// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package cache is the two-tier answer cache for questions:
//
//   - Hot tier: Valkey, an exact match on (question, read scope, corpus
//     version). Bumping the corpus version retires every entry at once.
//   - Durable tier: Postgres with pgvector, a semantic match on the
//     question's embedding within the same scope and corpus version, so a
//     rephrased question still hits. Every miss is written to both tiers.
//
// The durable rows have no TTL; they are kept as a record of what was asked
// and answered, and old corpus versions are simply never matched again.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
	redis "github.com/Bugs5382/go-redis"
	"github.com/jackc/pgx/v5"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// corpusVersionKey is the counter every hot key is namespaced under.
const corpusVersionKey = "ai:corpus:version"

// hotSchemaVersion changes when the cached Answer's shape changes, so an
// entry missing a newer field is never served; the corpus version tracks
// content, this tracks shape.
const hotSchemaVersion = "v1"

// hotTTL bounds a hot entry; the durable tier still serves after an
// eviction or a Valkey restart.
const hotTTL = 1 * time.Hour

// simThreshold is the largest cosine distance (0 identical, 2 opposite) that
// counts as the same question. Rephrasings land well under 0.10 with a real
// embeddings model; looser risks answering a different question.
const simThreshold = 0.10

// Citation identifies the policy version and section an answer drew from.
type Citation struct {
	PolicyID     string `json:"policyId"`
	PolicyTitle  string `json:"policyTitle"`
	VersionNo    int    `json:"versionNo"`
	VersionID    string `json:"versionId"`
	SectionKey   string `json:"sectionKey"`
	ChunkID      string `json:"chunkId"`
	ChunkIndex   int    `json:"chunkIndex"`
	DocumentType string `json:"documentType"`
}

// SegmentSource is one chunk an answer segment is grounded in.
type SegmentSource struct {
	PolicyID   string `json:"policyId"`
	VersionID  string `json:"versionId"`
	SectionKey string `json:"sectionKey"`
	ChunkID    string `json:"chunkId"`
	ChunkIndex int    `json:"chunkIndex"`
}

// AnswerSegment attributes a span of the answer, in UTF-8 byte offsets with
// End exclusive, to its sources.
type AnswerSegment struct {
	Start   int             `json:"start"`
	End     int             `json:"end"`
	Sources []SegmentSource `json:"sources"`
}

// Answer is a cached answer, without the request's retrieved chunks.
//
// Segments travel through the hot tier only. A durable hit is another
// question's answer, whose offsets don't belong to this request, so it
// returns Segments nil, meaning no attribution, not an error.
type Answer struct {
	AnswerText         string          `json:"answer"`
	Citations          []Citation      `json:"citations"`
	NoAuthorizedSource bool            `json:"noAuthorizedSource"`
	HasSensitiveSource bool            `json:"hasSensitiveSource"`
	Segments           []AnswerSegment `json:"segments"`
}

// QACache is the two-tier cache. A nil db runs the hot tier only.
type QACache struct {
	rdb redis.UniversalClient
	db  *postgres.DB
}

// New returns a QACache. db may be nil to run Valkey only; production wires
// both tiers.
func New(rdb redis.UniversalClient, db *postgres.DB) *QACache {
	return &QACache{rdb: rdb, db: db}
}

// BumpCorpusVersion advances the corpus version after a reindex, so no
// answer from before it is served again.
func (c *QACache) BumpCorpusVersion(ctx context.Context) error {
	if err := c.rdb.Incr(ctx, corpusVersionKey).Err(); err != nil {
		return fmt.Errorf("cache: bump corpus version: %w", err)
	}
	return nil
}

// corpusVersion is 0 until the first bump, and on a read error.
func (c *QACache) corpusVersion(ctx context.Context) int64 {
	v, err := c.rdb.Get(ctx, corpusVersionKey).Int64()
	if err != nil {
		return 0
	}
	return v
}

// Lookup checks the hot tier, then the durable tier, for an answer to
// question asked under scope. embedding is the question's own embedding,
// reused by the caller for retrieval on a miss.
//
// The scope's AllCategories is part of the key: an answer drawn from every
// category must never be served to a narrower scope with the same category
// list, nor the other way round.
func (c *QACache) Lookup(ctx context.Context, question string, embedding []float32, scope store.AccessFilter) (Answer, bool, error) {
	version := c.corpusVersion(ctx)
	sh := scopeHash(scope)

	key := hotKey(version, question, sh)
	if raw, err := c.rdb.Get(ctx, key).Bytes(); err == nil {
		var a Answer
		if json.Unmarshal(raw, &a) == nil {
			c.incrementAskCountByQuestion(ctx, question, sh, scope.IncludeSensitive, version)
			recordCacheLookup(ctx, "hot_hit")
			return a, true, nil
		}
	}
	// A corrupt entry or a Valkey error falls through: a cache outage must
	// not fail the request.

	if c.db == nil {
		recordCacheLookup(ctx, "miss")
		return Answer{}, false, nil
	}
	row := c.db.Pool().QueryRow(ctx, `
		SELECT id, answer_text, citations_json, no_authorized_source, has_sensitive_source,
		       (question_embedding <=> $1::vector) AS distance
		FROM ai_qa_cache
		WHERE scope_hash = $2 AND include_sensitive = $3 AND corpus_version = $4
		ORDER BY question_embedding <=> $1::vector
		LIMIT 1
	`, store.PgVectorLiteral(embedding), sh, scope.IncludeSensitive, version)

	var (
		id           string
		a            Answer
		citationsRaw []byte
		distance     float64
	)
	if err := row.Scan(&id, &a.AnswerText, &citationsRaw, &a.NoAuthorizedSource, &a.HasSensitiveSource, &distance); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			recordCacheLookup(ctx, "miss")
			return Answer{}, false, nil
		}
		return Answer{}, false, fmt.Errorf("cache: durable lookup: %w", err)
	}
	if distance > simThreshold {
		recordCacheLookup(ctx, "miss")
		return Answer{}, false, nil
	}
	if err := json.Unmarshal(citationsRaw, &a.Citations); err != nil {
		return Answer{}, false, fmt.Errorf("cache: decode citations: %w", err)
	}

	// A semantic hit's row may hold different question text, so it is
	// credited by id.
	c.incrementAskCountByID(ctx, id)
	c.writeHot(ctx, key, a)

	recordCacheLookup(ctx, "durable_hit")
	return a, true, nil
}

// incrementAskCountByQuestion credits a hot hit to the durable row Put wrote
// for the same tuple. Best effort: the ask count feeds suggestions only.
func (c *QACache) incrementAskCountByQuestion(ctx context.Context, question, sh string, includeSensitive bool, version int64) {
	if c.db == nil {
		return
	}
	_, _ = c.db.Pool().Exec(ctx, `
		UPDATE ai_qa_cache
		SET ask_count = ask_count + 1
		WHERE question_text = $1 AND scope_hash = $2 AND include_sensitive = $3 AND corpus_version = $4
	`, question, sh, includeSensitive, version)
}

func (c *QACache) incrementAskCountByID(ctx context.Context, id string) {
	_, _ = c.db.Pool().Exec(ctx, `UPDATE ai_qa_cache SET ask_count = ask_count + 1 WHERE id = $1`, id)
}

// Put writes answer to both tiers after a miss. scope must be the one the
// request's Lookup used.
func (c *QACache) Put(ctx context.Context, question string, embedding []float32, scope store.AccessFilter, answer Answer) error {
	version := c.corpusVersion(ctx)
	sh := scopeHash(scope)

	c.writeHot(ctx, hotKey(version, question, sh), answer)

	if c.db == nil {
		return nil
	}
	citationsRaw, err := json.Marshal(answer.Citations)
	if err != nil {
		return fmt.Errorf("cache: encode citations: %w", err)
	}
	_, err = c.db.Pool().Exec(ctx, `
		INSERT INTO ai_qa_cache
			(question_text, question_embedding, scope_hash, include_sensitive,
			 corpus_version, answer_text, citations_json, no_authorized_source, has_sensitive_source)
		VALUES ($1,$2::vector,$3,$4,$5,$6,$7,$8,$9)
	`,
		question, store.PgVectorLiteral(embedding), sh, scope.IncludeSensitive,
		version, answer.AnswerText, citationsRaw, answer.NoAuthorizedSource, answer.HasSensitiveSource,
	)
	if err != nil {
		return fmt.Errorf("cache: durable write: %w", err)
	}
	return nil
}

// writeHot is best effort; the answer already exists.
func (c *QACache) writeHot(ctx context.Context, key string, a Answer) {
	b, err := json.Marshal(a)
	if err != nil {
		return
	}
	_ = c.rdb.Set(ctx, key, b, hotTTL).Err()
}

func hotKey(version int64, question, sh string) string {
	sum := sha256.Sum256([]byte(question + "\x00" + sh))
	return fmt.Sprintf("ai:qa:%s:%d:%s", hotSchemaVersion, version, hex.EncodeToString(sum[:]))
}

// scopeHash collapses a read scope into a stable hash: the sorted category
// ids, include_sensitive and all_categories. Sorting makes it independent of
// the caller's order; a NUL separator keeps ids containing commas apart; and
// AllCategories is its own component so an every-category scope never shares
// a bucket with a listed one.
func scopeHash(scope store.AccessFilter) string {
	sorted := slices.Clone(scope.CategoryIDs)
	slices.Sort(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\x00") + "|" + boolStr(scope.IncludeSensitive) + "|" + boolStr(scope.AllCategories)))
	return hex.EncodeToString(sum[:])
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
