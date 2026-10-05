// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	pg "github.com/Bugs5382/go-postgres"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// zeroEmbedding satisfies ai_qa_cache.question_embedding's vector(384)
// NOT NULL constraint; TopQuestions doesn't touch the embedding column at
// all, so its actual content is irrelevant to these tests.
func zeroEmbedding() []float32 { return make([]float32, 384) }

// insertQARow writes one ai_qa_cache row directly (bypassing
// internal/cache.QACache, which isn't under test here) so tests can set up
// exactly the ask_count/no_authorized_source/has_sensitive_source
// combinations TopQuestions needs to filter/rank on.
func insertQARow(t *testing.T, pool *pg.DB, question string, askCount int, noAuthorizedSource, hasSensitiveSource bool) {
	t.Helper()
	_, err := pool.Pool().Exec(context.Background(), `
		INSERT INTO ai_qa_cache
			(question_text, question_embedding, scope_hash, include_sensitive,
			 corpus_version, answer_text, citations_json, no_authorized_source,
			 has_sensitive_source, ask_count)
		VALUES ($1,$2::vector,'scope',false,0,'an answer','[]',$3,$4,$5)
	`, question, store.PgVectorLiteral(zeroEmbedding()), noAuthorizedSource, hasSensitiveSource, askCount)
	if err != nil {
		t.Fatalf("insert qa row %q: %v", question, err)
	}
}

func TestQAQuestionStore_TopQuestions_ranksByAskCount(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewQAQuestionStore(pool)
	ctx := context.Background()

	insertQARow(t, pool, "what is the PTO policy?", 10, false, false)
	insertQARow(t, pool, "how do I request leave?", 3, false, false)
	insertQARow(t, pool, "what is the dress code?", 7, false, false)

	got, err := repo.TopQuestions(ctx, everything, 2)
	if err != nil {
		t.Fatalf("TopQuestions: %v", err)
	}
	want := []string{"what is the PTO policy?", "what is the dress code?"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestQAQuestionStore_TopQuestions_excludesUnauthorizedAndSensitive(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewQAQuestionStore(pool)
	ctx := context.Background()

	insertQARow(t, pool, "safe question", 5, false, false)
	insertQARow(t, pool, "no source found for this one", 100, true, false)
	insertQARow(t, pool, "touches sensitive material", 100, false, true)

	got, err := repo.TopQuestions(ctx, everything, 10)
	if err != nil {
		t.Fatalf("TopQuestions: %v", err)
	}
	if len(got) != 1 || got[0] != "safe question" {
		t.Fatalf("got %v, want only [\"safe question\"]", got)
	}
}

func TestQAQuestionStore_TopQuestions_sumsAskCountAcrossDuplicateRows(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewQAQuestionStore(pool)
	ctx := context.Background()

	// Two rows for the same question_text (as if a semantic-tier hit
	// incremented a different row than an exact hot-tier hit did) must
	// combine their ask_count via SUM, not just count the row with the
	// highest individual value.
	insertQARow(t, pool, "combined question", 2, false, false)
	insertQARow(t, pool, "combined question", 2, false, false)
	insertQARow(t, pool, "single higher count", 3, false, false)

	got, err := repo.TopQuestions(ctx, everything, 10)
	if err != nil {
		t.Fatalf("TopQuestions: %v", err)
	}
	if len(got) != 2 || got[0] != "combined question" {
		t.Fatalf("got %v, want [\"combined question\", \"single higher count\"]", got)
	}
}

func TestQAQuestionStore_TopQuestions_clampsLimit(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewQAQuestionStore(pool)
	ctx := context.Background()

	for i := 0; i < 25; i++ {
		insertQARow(t, pool, string(rune('a'+i))+" question", 1, false, false)
	}

	// A limit above the max is clamped rather than returning everything.
	got, err := repo.TopQuestions(ctx, everything, 1000)
	if err != nil {
		t.Fatalf("TopQuestions: %v", err)
	}
	if len(got) != 20 {
		t.Fatalf("got %d questions, want 20 (clamped)", len(got))
	}

	// A non-positive limit falls back to the default.
	got, err = repo.TopQuestions(ctx, everything, 0)
	if err != nil {
		t.Fatalf("TopQuestions: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d questions, want 6 (default)", len(got))
	}
}

var everything = store.AccessFilter{AllCategories: true}

// A question is suggested only to someone who could read every policy its
// answer cited, so the question text can't reveal a policy outside the scope.
func TestQAQuestionStore_TopQuestions_filtersByScope(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewQAQuestionStore(pool)
	cs := store.NewCentroidStore(pool)
	ctx := context.Background()

	seedCentroidRow(t, cs, "pol-hr", "hr", "standard", 0)
	seedCentroidRow(t, cs, "pol-fin", "finance", "standard", 1)
	insertCitedRow(t, pool, "how much leave do I get?", 5, `[{"policyId":"pol-hr"}]`)
	insertCitedRow(t, pool, "what is the budget freeze?", 9, `[{"policyId":"pol-fin"}]`)
	insertCitedRow(t, pool, "leave and budget?", 7, `[{"policyId":"pol-hr"},{"policyId":"pol-fin"}]`)
	insertCitedRow(t, pool, "a removed policy?", 8, `[{"policyId":"pol-gone"}]`)

	got, err := repo.TopQuestions(ctx, store.AccessFilter{CategoryIDs: []string{"hr"}}, 10)
	if err != nil {
		t.Fatalf("TopQuestions: %v", err)
	}
	if len(got) != 1 || got[0] != "how much leave do I get?" {
		t.Fatalf("hr scope got %v, want only the hr question", got)
	}
	all, err := repo.TopQuestions(ctx, everything, 10)
	if err != nil {
		t.Fatalf("TopQuestions: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all-categories scope got %v, want the three questions with readable sources", all)
	}
}

func insertCitedRow(t *testing.T, pool *pg.DB, question string, askCount int, citations string) {
	t.Helper()
	_, err := pool.Pool().Exec(context.Background(), `
		INSERT INTO ai_qa_cache
			(question_text, question_embedding, scope_hash, include_sensitive,
			 corpus_version, answer_text, citations_json, ask_count)
		VALUES ($1,$2::vector,'scope',false,0,'an answer',$3::jsonb,$4)
	`, question, store.PgVectorLiteral(zeroEmbedding()), citations, askCount)
	if err != nil {
		t.Fatalf("insert qa row %q: %v", question, err)
	}
}
