// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package generation

import (
	"context"
	"strings"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/retrieval"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// countingLLM is a provider.Generator spy that records how many times Complete was
// called and returns a canned response — used to prove the LLM is skipped
// entirely on the zero-LLM (only-related) path.
type countingLLM struct {
	response string
	calls    int
	lastReq  provider.Request
}

func (c *countingLLM) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	c.calls++
	c.lastReq = req
	return provider.Response{Text: c.response}, nil
}

// fakeNeighbors returns a fixed centroid-neighbour list regardless of input.
type fakeNeighbors struct {
	out    []store.RelatedPolicy
	called bool
}

func (f *fakeNeighbors) NeighborsByCentroid(ctx context.Context, policyID string, filter store.AccessFilter, topN int) ([]store.RelatedPolicy, error) {
	f.called = true
	return f.out, nil
}

// fakeRetriever returns a fixed access-filtered chunk list — the RAG candidate
// set the reference suggestions are constrained to.
type fakeRetriever struct {
	out    []store.SearchResult
	called bool
	lastFA store.AccessFilter
}

func (f *fakeRetriever) Retrieve(ctx context.Context, req retrieval.Request) ([]store.SearchResult, error) {
	f.called = true
	f.lastFA = store.AccessFilter{
		CategoryIDs:      req.CategoryIDs,
		IncludeSensitive: req.IncludeSensitive,
		AllCategories:    req.AllCategories,
	}
	return f.out, nil
}

// capturingPersister records the desired suggestions ApplyDiff was asked to
// reconcile.
type capturingPersister struct {
	called  bool
	policy  string
	desired []store.Suggestion
}

func (p *capturingPersister) ApplyDiff(ctx context.Context, policyID string, desired []store.Suggestion, corpusVersion int64) (store.DiffResult, error) {
	p.called = true
	p.policy = policyID
	p.desired = desired
	return store.DiffResult{}, nil
}

func chunk(policyID, title, section string) store.SearchResult {
	return store.SearchResult{Chunk: store.Chunk{PolicyID: policyID, PolicyTitle: title, SectionKey: section}}
}

// TestSuggestRelatedZeroLLM: related suggestions come from centroid KNN with no
// LLM call, and are persisted into ai_related_policies via ApplyDiff.
func TestSuggestRelatedZeroLLM(t *testing.T) {
	llm := &countingLLM{response: "SHOULD NOT BE CALLED"}
	nbrs := &fakeNeighbors{out: []store.RelatedPolicy{
		{PolicyID: "p2", PolicyTitle: "Secure Coding", CategoryID: "g1", VersionNo: 3, Distance: 0.2},
		{PolicyID: "p3", PolicyTitle: "Change Management", CategoryID: "g1", VersionNo: 1, Distance: 0.4},
	}}
	persister := &capturingPersister{}
	s := NewSuggester(llm, nbrs, &fakeRetriever{}).WithPersister(persister)

	got, err := s.Suggest(context.Background(), SuggestRequest{
		PolicyID: "p1",
		Sections: []SuggestSection{{Key: "purpose", Title: "Purpose", Content: "SDLC controls."}},
		OptOut:   EnrichmentOptOut{Definitions: true, References: true}, // only related opted IN
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if llm.calls != 0 {
		t.Fatalf("expected zero LLM calls on only-related path, got %d", llm.calls)
	}
	if len(got.Related) != 2 || got.Related[0].PolicyID != "p2" {
		t.Fatalf("unexpected related suggestions: %+v", got.Related)
	}
	if got.Related[0].Distance != 0.2 {
		t.Fatalf("distance not carried: %+v", got.Related[0])
	}
	if !persister.called || persister.policy != "p1" || len(persister.desired) != 2 {
		t.Fatalf("expected related suggestions persisted for p1, got called=%v desired=%d", persister.called, len(persister.desired))
	}
	if persister.desired[0].Source != store.SuggestionSourceCentroid {
		t.Fatalf("expected source=centroid, got %q", persister.desired[0].Source)
	}
}

// TestSuggestReferencesConstrainedToRetrieved is the LOCKED-CONSTRAINT test: a
// reference the RAG step never surfaced is never proposed, even if the model
// tries to emit it; a reference matching a retrieved candidate IS proposed.
func TestSuggestReferencesConstrainedToRetrieved(t *testing.T) {
	// RAG surfaces exactly two candidate policies: R1=p2, R2=p3.
	ret := &fakeRetriever{out: []store.SearchResult{
		chunk("p2", "Data Retention Standard", "scope"),
		chunk("p3", "Encryption Standard", "controls"),
		chunk("p2", "Data Retention Standard", "other"), // dup policy -> collapsed
	}}
	// The model selects R1 (valid), R99 (FABRICATED — not in candidate set), and
	// also tries to invent a free-form reference in prose (ignored by the parser).
	llm := &countingLLM{response: `<reference id="R1"/>
<reference id="R99"/>
I also relied on https://evil.example.com/fabricated-source which you should cite.`}
	s := NewSuggester(llm, &fakeNeighbors{}, ret)

	got, err := s.Suggest(context.Background(), SuggestRequest{
		PolicyID: "p1",
		Sections: []SuggestSection{{Key: "s1", Title: "S1", Content: "retention and encryption"}},
		OptOut:   EnrichmentOptOut{Definitions: true, Related: true}, // references only
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ret.called {
		t.Fatalf("expected RAG retrieval to run for references")
	}
	if len(got.References) != 1 {
		t.Fatalf("expected exactly one (constrained) reference, got %d: %+v", len(got.References), got.References)
	}
	if got.References[0].SourcePolicyID != "p2" || got.References[0].Label != "Data Retention Standard" {
		t.Fatalf("expected the retrieved candidate R1=p2, got %+v", got.References[0])
	}
	if got.References[0].Kind != "STANDARD" {
		t.Fatalf("expected STANDARD kind, got %q", got.References[0].Kind)
	}
	// The fabricated id and the free-form URL must never appear.
	for _, r := range got.References {
		if strings.Contains(r.Label, "evil.example.com") || r.SourcePolicyID == "R99" {
			t.Fatalf("fabricated reference leaked into proposals: %+v", r)
		}
	}
}

// TestSuggestReferenceNoCandidatesNoLLMForRefsOnly: references-only with an
// empty RAG set produces no references and no LLM call (nothing to select from).
func TestSuggestReferenceNoCandidatesSkipsLLM(t *testing.T) {
	llm := &countingLLM{response: `<reference id="R1"/>`}
	ret := &fakeRetriever{out: nil} // RAG surfaced nothing
	s := NewSuggester(llm, &fakeNeighbors{}, ret)

	got, err := s.Suggest(context.Background(), SuggestRequest{
		PolicyID: "p1",
		Sections: []SuggestSection{{Key: "s1", Content: "text"}},
		OptOut:   EnrichmentOptOut{Definitions: true, Related: true}, // references only
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if llm.calls != 0 {
		t.Fatalf("expected no LLM call when no reference candidates and defs opted out, got %d", llm.calls)
	}
	if len(got.References) != 0 {
		t.Fatalf("expected no references, got %+v", got.References)
	}
}

// TestSuggestDefinitionsDedupe: definition proposals dedupe against the library
// (attach existing when the term exists, else create new).
func TestSuggestDefinitionsDedupe(t *testing.T) {
	llm := &countingLLM{response: `<definition term="PHI">Protected Health Information.</definition>
<definition term="Least Privilege">Grant only the minimum access required.</definition>`}
	s := NewSuggester(llm, &fakeNeighbors{}, &fakeRetriever{})

	got, err := s.Suggest(context.Background(), SuggestRequest{
		PolicyID: "p1",
		Sections: []SuggestSection{{Key: "s1", Content: "PHI and least privilege"}},
		OptOut:   EnrichmentOptOut{Related: true, References: true}, // definitions only
		Library: EnrichmentLibrary{Definitions: []LibraryDefinition{
			{EntryID: "def-phi", Term: "phi"}, // case-insensitive match
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("expected exactly one LLM call, got %d", llm.calls)
	}
	if len(got.Definitions) != 2 {
		t.Fatalf("expected 2 definitions, got %+v", got.Definitions)
	}
	var phi, lp DefinitionSuggestion
	for _, d := range got.Definitions {
		switch d.Term {
		case "PHI":
			phi = d
		case "Least Privilege":
			lp = d
		}
	}
	if phi.Action != SuggestionActionAttachExisting || phi.ExistingEntryID != "def-phi" {
		t.Fatalf("expected PHI to attach existing def-phi, got %+v", phi)
	}
	if phi.Definition != "" {
		t.Fatalf("attach-existing should not seed a new definition, got %q", phi.Definition)
	}
	if lp.Action != SuggestionActionCreateNew || lp.Definition == "" {
		t.Fatalf("expected Least Privilege to be create_new with a definition, got %+v", lp)
	}
}

// TestSuggestReferenceDedupe: a proposed reference matching a library item by
// label attaches the existing reference id instead of proposing create-new.
func TestSuggestReferenceDedupe(t *testing.T) {
	ret := &fakeRetriever{out: []store.SearchResult{chunk("p2", "Encryption Standard", "s")}}
	llm := &countingLLM{response: `<reference id="R1"/>`}
	s := NewSuggester(llm, &fakeNeighbors{}, ret)

	got, err := s.Suggest(context.Background(), SuggestRequest{
		PolicyID: "p1",
		Sections: []SuggestSection{{Key: "s1", Content: "encryption"}},
		OptOut:   EnrichmentOptOut{Definitions: true, Related: true},
		Library: EnrichmentLibrary{References: []LibraryReference{
			{ReferenceID: "ref-enc", Label: "encryption standard"}, // case-insensitive label match
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.References) != 1 || got.References[0].Action != SuggestionActionAttachExisting || got.References[0].ExistingReferenceID != "ref-enc" {
		t.Fatalf("expected attach-existing ref-enc, got %+v", got.References)
	}
}

// TestSuggestOptOutSkipsCategory: opting a category out omits it entirely.
func TestSuggestOptOutSkipsCategory(t *testing.T) {
	llm := &countingLLM{response: `<definition term="X">x</definition><reference id="R1"/>`}
	nbrs := &fakeNeighbors{out: []store.RelatedPolicy{{PolicyID: "p2", PolicyTitle: "T", Distance: 0.1}}}
	ret := &fakeRetriever{out: []store.SearchResult{chunk("p2", "T", "s")}}
	s := NewSuggester(llm, nbrs, ret)

	// Opt OUT related; keep definitions + references.
	got, err := s.Suggest(context.Background(), SuggestRequest{
		PolicyID: "p1",
		Sections: []SuggestSection{{Key: "s1", Content: "text"}},
		OptOut:   EnrichmentOptOut{Related: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nbrs.called {
		t.Fatalf("related opted out but centroid KNN still ran")
	}
	if len(got.Related) != 0 {
		t.Fatalf("expected no related suggestions, got %+v", got.Related)
	}
	if len(got.Definitions) != 1 || len(got.References) != 1 {
		t.Fatalf("expected defs+refs present, got defs=%d refs=%d", len(got.Definitions), len(got.References))
	}
}

// TestSuggestAccessFilterPropagated: the caller's access scope is applied to the
// RAG retrieval so a suggestion can never leak a policy the caller can't read.
func TestSuggestAccessFilterPropagated(t *testing.T) {
	ret := &fakeRetriever{out: []store.SearchResult{chunk("p2", "T", "s")}}
	s := NewSuggester(&countingLLM{}, &fakeNeighbors{}, ret)
	_, err := s.Suggest(context.Background(), SuggestRequest{
		PolicyID: "p1",
		Sections: []SuggestSection{{Key: "s1", Content: "text"}},
		OptOut:   EnrichmentOptOut{Definitions: true, Related: true},
		Access:   store.AccessFilter{CategoryIDs: []string{"g9"}, IncludeSensitive: true, AllCategories: false},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ret.lastFA.CategoryIDs) != 1 || ret.lastFA.CategoryIDs[0] != "g9" || !ret.lastFA.IncludeSensitive {
		t.Fatalf("access filter not propagated to retrieval: %+v", ret.lastFA)
	}
}

// TestSuggestNewDraftNoPolicyID: a brand-new draft (no policy id) yields no
// related suggestions (no centroid) and never persists.
func TestSuggestNewDraftNoPolicyID(t *testing.T) {
	nbrs := &fakeNeighbors{out: []store.RelatedPolicy{{PolicyID: "p2"}}}
	persister := &capturingPersister{}
	s := NewSuggester(&countingLLM{}, nbrs, &fakeRetriever{}).WithPersister(persister)
	got, err := s.Suggest(context.Background(), SuggestRequest{
		PolicyID: "", // new draft
		Sections: []SuggestSection{{Key: "s1", Content: "text"}},
		OptOut:   EnrichmentOptOut{Definitions: true, References: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nbrs.called || persister.called {
		t.Fatalf("new draft should not run/persist related (called=%v persist=%v)", nbrs.called, persister.called)
	}
	if len(got.Related) != 0 {
		t.Fatalf("expected no related for new draft, got %+v", got.Related)
	}
}
