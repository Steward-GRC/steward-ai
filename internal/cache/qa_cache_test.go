// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"context"
	"sort"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

// newTestQACache is a hot-tier-only cache (no Postgres).
func newTestQACache(t *testing.T) *QACache {
	t.Helper()
	return New(newValkey(t), nil)
}

func scopeOf(categories ...string) store.AccessFilter {
	return store.AccessFilter{CategoryIDs: categories}
}

// lookupResultLabels collects every result label recorded on
// ai_qa_cache_lookups_total since reader was installed.
func lookupResultLabels(t *testing.T, reader sdkmetric.Reader) []string {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	var got []string
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "ai_qa_cache_lookups_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("ai_qa_cache_lookups_total: unexpected data type %T", m.Data)
			}
			for _, dp := range sum.DataPoints {
				for i := int64(0); i < dp.Value; i++ {
					v, _ := dp.Attributes.Value("result")
					got = append(got, v.AsString())
				}
			}
		}
	}
	return got
}

// A durable hit needs Postgres and is covered by the ask count tests.
func TestQACacheLookup_recordsMissAndHotHit(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	c := newTestQACache(t)
	ctx := context.Background()

	if _, hit, err := c.Lookup(ctx, "what is the leave policy?", nil, scopeOf("c1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if hit {
		t.Fatal("expected a miss on an empty cache")
	}

	if err := c.Put(ctx, "what is the leave policy?", nil, scopeOf("c1"), Answer{AnswerText: "20 days"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, hit, err := c.Lookup(ctx, "what is the leave policy?", nil, scopeOf("c1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if !hit {
		t.Fatal("expected a hot-tier hit after Put")
	}

	got := lookupResultLabels(t, reader)
	want := []string{"hot_hit", "miss"}
	// Data points come back in no fixed order.
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("recorded results = %v, want (any order) %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recorded results = %v, want (any order) %v", got, want)
		}
	}
}

func TestQACache_hotTierRoundTripsSegments(t *testing.T) {
	c := newTestQACache(t)
	ctx := context.Background()

	answer := Answer{
		AnswerText: "Employees may travel up to 5000 km per year.",
		Citations:  []Citation{{PolicyID: "pol-travel", ChunkID: "chunk-1", ChunkIndex: 2}},
		Segments: []AnswerSegment{{
			Start: 0, End: 44,
			Sources: []SegmentSource{{PolicyID: "pol-travel", VersionID: "ver-3", SectionKey: "allowances", ChunkID: "chunk-1", ChunkIndex: 2}},
		}},
	}
	if err := c.Put(ctx, "how much travel?", nil, scopeOf("hr"), answer); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, hit, err := c.Lookup(ctx, "how much travel?", nil, scopeOf("hr"))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !hit {
		t.Fatal("expected a hot-tier hit")
	}
	if len(got.Segments) != 1 {
		t.Fatalf("expected 1 segment round-tripped, got %d", len(got.Segments))
	}
	if got.Segments[0].Sources[0].ChunkID != "chunk-1" || got.Segments[0].End != 44 {
		t.Fatalf("segment did not round-trip intact: %+v", got.Segments[0])
	}
	if got.Citations[0].ChunkID != "chunk-1" || got.Citations[0].ChunkIndex != 2 {
		t.Fatalf("citation chunk fields did not round-trip: %+v", got.Citations[0])
	}
}

func TestRecordCacheLookup_doesNotPanic(t *testing.T) {
	recordCacheLookup(context.Background(), "miss")
}

// An every-category scope reads more than a listed one with the same ids,
// so the two must never share an entry.
func TestQACacheLookup_allCategoriesScopeNeverCrossServesListedScope(t *testing.T) {
	c := newTestQACache(t)
	ctx := context.Background()

	question := "what is the confidential pay policy?"
	listed := store.AccessFilter{CategoryIDs: []string{"c1"}}
	all := store.AccessFilter{CategoryIDs: []string{"c1"}, AllCategories: true}

	if err := c.Put(ctx, question, nil, listed, Answer{AnswerText: "listed answer"}); err != nil {
		t.Fatalf("put (listed): %v", err)
	}

	if _, hit, err := c.Lookup(ctx, question, nil, all); err != nil {
		t.Fatalf("lookup (all): %v", err)
	} else if hit {
		t.Fatal("an every-category lookup must not hit a listed scope's entry")
	}

	if err := c.Put(ctx, question, nil, all, Answer{AnswerText: "all answer"}); err != nil {
		t.Fatalf("put (all): %v", err)
	}
	got, hit, err := c.Lookup(ctx, question, nil, listed)
	if err != nil {
		t.Fatalf("lookup (listed, after all put): %v", err)
	}
	if !hit {
		t.Fatal("expected the listed scope's own entry to still be cached")
	}
	if got.AnswerText != "listed answer" {
		t.Fatalf("listed lookup got %q, want its own cached answer", got.AnswerText)
	}

	got, hit, err = c.Lookup(ctx, question, nil, all)
	if err != nil {
		t.Fatalf("lookup (all, after its own put): %v", err)
	}
	if !hit {
		t.Fatal("expected the every-category entry to be cached")
	}
	if got.AnswerText != "all answer" {
		t.Fatalf("every-category lookup got %q, want its own cached answer", got.AnswerText)
	}
}

func TestScopeHash(t *testing.T) {
	base := scopeHash(store.AccessFilter{CategoryIDs: []string{"b", "a"}})
	if base != scopeHash(store.AccessFilter{CategoryIDs: []string{"a", "b"}}) {
		t.Fatal("the hash must not depend on category order")
	}
	for name, other := range map[string]store.AccessFilter{
		"include_sensitive": {CategoryIDs: []string{"a", "b"}, IncludeSensitive: true},
		"all_categories":    {CategoryIDs: []string{"a", "b"}, AllCategories: true},
		"other categories":  {CategoryIDs: []string{"a"}},
		"joined id":         {CategoryIDs: []string{"a,b"}},
	} {
		if scopeHash(other) == base {
			t.Fatalf("%s must change the hash", name)
		}
	}
}

func TestQACache_bumpCorpusVersionRetiresHotEntries(t *testing.T) {
	c := newTestQACache(t)
	ctx := context.Background()

	if err := c.Put(ctx, "q", nil, scopeOf("c1"), Answer{AnswerText: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := c.BumpCorpusVersion(ctx); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := c.Lookup(ctx, "q", nil, scopeOf("c1")); err != nil {
		t.Fatal(err)
	} else if hit {
		t.Fatal("an entry from the previous corpus version must not be served")
	}
}
