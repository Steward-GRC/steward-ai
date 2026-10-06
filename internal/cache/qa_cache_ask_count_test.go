// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"context"
	"testing"

	pg "github.com/Bugs5382/go-postgres"

	"github.com/Steward-GRC/steward-ai/internal/provider"
)

func newTestQACacheWithDB(t *testing.T, db *pg.DB) *QACache {
	t.Helper()
	return New(newValkey(t), db)
}

// Two zero vectors are distance 0 apart, which stands in for a rephrased
// question without an embeddings model.
func zeroEmbedding() []float32 { return make([]float32, provider.Dimensions) }

func askCountFor(t *testing.T, db *pg.DB, questionText string) int {
	t.Helper()
	var count int
	if err := db.Pool().QueryRow(context.Background(),
		`SELECT ask_count FROM ai_qa_cache WHERE question_text = $1`, questionText,
	).Scan(&count); err != nil {
		t.Fatalf("read ask_count for %q: %v", questionText, err)
	}
	return count
}

func TestQACacheLookup_hotHitIncrementsAskCount(t *testing.T) {
	db := newTestDB(t)
	c := newTestQACacheWithDB(t, db)
	ctx := context.Background()

	question := "what is the leave policy?"
	emb := zeroEmbedding()

	if err := c.Put(ctx, question, emb, scopeOf("c1"), Answer{AnswerText: "20 days"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := askCountFor(t, db, question); got != 1 {
		t.Fatalf("ask_count after Put = %d, want 1 (column default)", got)
	}

	if _, hit, err := c.Lookup(ctx, question, emb, scopeOf("c1")); err != nil {
		t.Fatalf("lookup 1: %v", err)
	} else if !hit {
		t.Fatal("expected a hot-tier hit")
	}
	if got := askCountFor(t, db, question); got != 2 {
		t.Fatalf("ask_count after 1 hot hit = %d, want 2", got)
	}

	if _, hit, err := c.Lookup(ctx, question, emb, scopeOf("c1")); err != nil {
		t.Fatalf("lookup 2: %v", err)
	} else if !hit {
		t.Fatal("expected a second hot-tier hit")
	}
	if got := askCountFor(t, db, question); got != 3 {
		t.Fatalf("ask_count after 2 hot hits = %d, want 3", got)
	}
}

// A semantic hit can match a row with different question text, so it is
// credited by id, not by the asked text.
func TestQACacheLookup_durableHitIncrementsAskCount(t *testing.T) {
	db := newTestDB(t)
	c := newTestQACacheWithDB(t, db)
	ctx := context.Background()

	stored := "how much paid time off do employees get?"
	asked := "how much leave do I get?"
	emb := zeroEmbedding()

	if err := c.Put(ctx, stored, emb, scopeOf("c1"), Answer{AnswerText: "20 days"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := askCountFor(t, db, stored); got != 1 {
		t.Fatalf("ask_count after Put = %d, want 1", got)
	}

	// The hot tier is keyed on the exact text, so this misses Valkey.
	if _, hit, err := c.Lookup(ctx, asked, emb, scopeOf("c1")); err != nil {
		t.Fatalf("lookup: %v", err)
	} else if !hit {
		t.Fatal("expected a durable-tier (semantic) hit")
	}
	if got := askCountFor(t, db, stored); got != 2 {
		t.Fatalf("ask_count on stored row after 1 durable hit = %d, want 2", got)
	}
}

func TestQACacheLookup_durableTierKeepsScopesApart(t *testing.T) {
	db := newTestDB(t)
	c := newTestQACacheWithDB(t, db)
	ctx := context.Background()
	emb := zeroEmbedding()

	if err := c.Put(ctx, "q one", emb, scopeOf("c1"), Answer{AnswerText: "a"}); err != nil {
		t.Fatal(err)
	}
	for name, scope := range map[string]struct {
		categories []string
		sensitive  bool
		all        bool
	}{
		"other category":    {categories: []string{"c2"}},
		"include sensitive": {categories: []string{"c1"}, sensitive: true},
		"all categories":    {categories: []string{"c1"}, all: true},
	} {
		s := scopeOf(scope.categories...)
		s.IncludeSensitive, s.AllCategories = scope.sensitive, scope.all
		if _, hit, err := c.Lookup(ctx, "q two", emb, s); err != nil {
			t.Fatalf("%s: %v", name, err)
		} else if hit {
			t.Fatalf("%s: a semantic hit must stay inside its scope", name)
		}
	}
	got, hit, err := c.Lookup(ctx, "q two", emb, scopeOf("c1"))
	if err != nil || !hit || got.Segments != nil {
		t.Fatalf("same scope: hit=%v err=%v segments=%v, want a hit with no segments", hit, err, got.Segments)
	}
}
