// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package aiconfig_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-ai/internal/aiconfig"
	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

// fakeDBStore is an in-memory Backend, so the caching and the write rules
// run without Postgres; the SQL is covered by the store's own tests.
type fakeDBStore struct {
	cfg      store.AIConfig
	getCalls int
	getErr   error
}

func (f *fakeDBStore) Get(context.Context) (store.AIConfig, error) {
	f.getCalls++
	if f.getErr != nil {
		return store.AIConfig{}, f.getErr
	}
	return f.cfg, nil
}

func (f *fakeDBStore) SetEnabled(_ context.Context, enabled bool) error {
	f.cfg.Enabled = enabled
	return nil
}

func (f *fakeDBStore) SetModel(_ context.Context, model string) error {
	f.cfg.Model = model
	return nil
}

func (f *fakeDBStore) SetProviderSettings(_ context.Context, p store.ProviderSettings) error {
	f.cfg.Provider, f.cfg.Model, f.cfg.BaseURL, f.cfg.Region, f.cfg.Deployment =
		p.Provider, p.Model, p.BaseURL, p.Region, p.Deployment
	return nil
}

func (f *fakeDBStore) SetCredential(_ context.Context, credential string) error {
	f.cfg.Credential = credential
	f.cfg.CredentialLast4 = store.Last4(credential)
	return nil
}

func (f *fakeDBStore) AcceptNotice(_ context.Context, version, acceptedBy string, at time.Time) error {
	f.cfg.Notice = store.DataNotice{Version: version, AcceptedBy: acceptedBy, AcceptedAt: &at}
	return nil
}

func (f *fakeDBStore) SetMonthlyLimit(_ context.Context, limit int64) error {
	f.cfg.MonthlyLimit = limit
	return nil
}

func (f *fakeDBStore) SetOrgContext(_ context.Context, orgContext string) error {
	f.cfg.OrgContext = orgContext
	return nil
}

func (f *fakeDBStore) SetTopK(_ context.Context, topK int) error {
	f.cfg.TopK = topK
	return nil
}

func (f *fakeDBStore) SetBlendCoefficients(_ context.Context, b store.BlendCoefficients) error {
	f.cfg.Blend = b
	return nil
}

func newTestStore(t *testing.T, db *fakeDBStore) *aiconfig.Store {
	t.Helper()
	return aiconfig.New(db, newValkey(t))
}

func acceptedNotice() store.DataNotice {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	return store.DataNotice{Version: airules.DataNoticeVersion, AcceptedBy: "alice", AcceptedAt: &at}
}

func TestStore_Enabled_defaultsFromDB(t *testing.T) {
	s := newTestStore(t, &fakeDBStore{cfg: store.AIConfig{Enabled: true}})
	enabled, err := s.Enabled(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("expected enabled=true from the seeded DB row")
	}
}

func TestStore_SetEnabled_roundTripsThroughDBAndCache(t *testing.T) {
	db := &fakeDBStore{cfg: store.AIConfig{Enabled: true}}
	s := newTestStore(t, db)
	ctx := context.Background()

	if err := s.SetEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if db.cfg.Enabled {
		t.Fatal("expected SetEnabled to reach the underlying DB store")
	}
	enabled, err := s.Enabled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("expected enabled=false after SetEnabled(false)")
	}
}

func TestStore_HasCredential_falseUntilSet(t *testing.T) {
	db := &fakeDBStore{}
	s := newTestStore(t, db)
	ctx := context.Background()

	has, err := s.HasCredential(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("expected has_credential=false with no credential set")
	}

	if err := s.SetCredential(ctx, "test-key-1"); err != nil {
		t.Fatal(err)
	}
	has, err = s.HasCredential(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("expected has_credential=true after SetCredential")
	}
}

func TestStore_Model_roundTripsAndDefaultsEmpty(t *testing.T) {
	db := &fakeDBStore{}
	s := newTestStore(t, db)
	ctx := context.Background()

	m, err := s.Model(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m != "" {
		t.Fatalf("expected empty model (no override) by default, got %q", m)
	}

	if err := s.SetModel(ctx, "example-model-large"); err != nil {
		t.Fatal(err)
	}
	m, err = s.Model(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m != "example-model-large" {
		t.Fatalf("expected the model to round-trip, got %q", m)
	}
}

// The one seam that returns the raw credential must actually return it.
func TestStore_Credential_internalOnly_returnsRawValue(t *testing.T) {
	db := &fakeDBStore{}
	s := newTestStore(t, db)
	ctx := context.Background()

	if err := s.SetCredential(ctx, "test-key-2"); err != nil {
		t.Fatal(err)
	}
	cred, has, err := s.Credential(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !has || cred != "test-key-2" {
		t.Fatalf("expected (\"test-key-2\", true), got (%q, %v)", cred, has)
	}
}

func TestStore_TopK_roundTripsThroughDBAndCache(t *testing.T) {
	db := &fakeDBStore{cfg: store.AIConfig{TopK: 50}}
	s := newTestStore(t, db)
	ctx := context.Background()

	got, err := s.TopK(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 50 {
		t.Fatalf("expected top_k=50 from the seeded row, got %d", got)
	}

	if err := s.SetTopK(ctx, 25); err != nil {
		t.Fatal(err)
	}
	if db.cfg.TopK != 25 {
		t.Fatal("expected SetTopK to reach the underlying DB store")
	}
	got, err = s.TopK(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 25 {
		t.Fatalf("expected top_k=25 after SetTopK(25), got %d", got)
	}
}

// A second read within the TTL hits Valkey, not Postgres.
func TestStore_get_cachesAcrossReads(t *testing.T) {
	db := &fakeDBStore{cfg: store.AIConfig{Enabled: true, Credential: "test-key-3"}}
	s := newTestStore(t, db)
	ctx := context.Background()

	if _, err := s.Enabled(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HasCredential(ctx); err != nil {
		t.Fatal(err)
	}
	if db.getCalls != 1 {
		t.Fatalf("expected exactly 1 DB read across 2 cached reads, got %d", db.getCalls)
	}
}

// A write refreshes the caches, so the next read on this process doesn't go
// back to Postgres.
func TestStore_SetCredential_refreshesCacheImmediately(t *testing.T) {
	db := &fakeDBStore{}
	s := newTestStore(t, db)
	ctx := context.Background()

	if err := s.SetCredential(ctx, "test-key-4"); err != nil {
		t.Fatal(err)
	}
	callsBefore := db.getCalls
	cred, has, err := s.Credential(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !has || cred != "test-key-4" {
		t.Fatalf("expected (\"test-key-4\", true), got (%q, %v)", cred, has)
	}
	if db.getCalls != callsBefore {
		t.Fatalf("expected the post-write read to hit the refreshed cache, not the DB (calls %d -> %d)", callsBefore, db.getCalls)
	}
}

func TestStore_credentialNeverReachesValkey(t *testing.T) {
	const secret = "test-key-5"
	db := &fakeDBStore{cfg: store.AIConfig{Enabled: true, Provider: string(provider.OpenAI)}}
	rdb := newValkey(t)
	s := aiconfig.New(db, rdb)
	ctx := context.Background()

	if err := s.SetCredential(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settings(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProviderSettings(ctx); err != nil {
		t.Fatal(err)
	}

	keys, err := rdb.Keys(ctx, "*").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) == 0 {
		t.Fatal("expected the settings to be cached")
	}
	for _, k := range keys {
		v, err := rdb.Get(ctx, k).Result()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(v, secret) {
			t.Fatalf("key %q holds the plaintext credential", k)
		}
	}
	got, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CredentialSet || got.CredentialLast4 != "ey-5" {
		t.Fatalf("expected set with last four ey-5, got set=%v last4=%q", got.CredentialSet, got.CredentialLast4)
	}
}

// The credential is held in memory for the TTL and read again from
// Postgres after it.
func TestStore_Credential_rereadAfterTTL(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	db := &fakeDBStore{cfg: store.AIConfig{Credential: "test-key-6"}}
	s := aiconfig.New(db, newValkey(t), aiconfig.WithClock(func() time.Time { return now }))
	ctx := context.Background()

	if _, _, err := s.Credential(ctx); err != nil {
		t.Fatal(err)
	}
	db.cfg.Credential = "test-key-7"
	if cred, _, _ := s.Credential(ctx); cred != "test-key-6" {
		t.Fatalf("within the TTL expected the held credential, got %q", cred)
	}
	now = now.Add(time.Minute)
	if cred, _, _ := s.Credential(ctx); cred != "test-key-7" {
		t.Fatalf("after the TTL expected the stored credential, got %q", cred)
	}
}

func TestStore_readFailureIsReturnedNotEnabled(t *testing.T) {
	boom := errors.New("postgres down")
	s := newTestStore(t, &fakeDBStore{cfg: store.AIConfig{Enabled: true}, getErr: boom})
	ctx := context.Background()

	enabled, err := s.Enabled(ctx)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the read error, got %v", err)
	}
	if enabled {
		t.Fatal("a failed read must never report enabled")
	}
	if _, err := s.Settings(ctx); !errors.Is(err, boom) {
		t.Fatalf("Settings: expected the read error, got %v", err)
	}
	if _, _, err := s.Credential(ctx); !errors.Is(err, boom) {
		t.Fatalf("Credential: expected the read error, got %v", err)
	}
}

func TestStore_SetEnabled_requiresCurrentNotice(t *testing.T) {
	ctx := context.Background()

	t.Run("no notice", func(t *testing.T) {
		db := &fakeDBStore{}
		s := newTestStore(t, db)
		if err := s.SetEnabled(ctx, true); !errors.Is(err, aiconfig.ErrNoticeRequired) {
			t.Fatalf("expected ErrNoticeRequired, got %v", err)
		}
		if db.cfg.Enabled {
			t.Fatal("the switch must stay off")
		}
	})

	t.Run("old notice", func(t *testing.T) {
		db := &fakeDBStore{cfg: store.AIConfig{Notice: store.DataNotice{Version: "2000-01-01"}}}
		s := newTestStore(t, db)
		if err := s.SetEnabled(ctx, true); !errors.Is(err, aiconfig.ErrNoticeRequired) {
			t.Fatalf("expected ErrNoticeRequired, got %v", err)
		}
	})

	t.Run("current notice", func(t *testing.T) {
		db := &fakeDBStore{cfg: store.AIConfig{Notice: acceptedNotice()}}
		s := newTestStore(t, db)
		if err := s.SetEnabled(ctx, true); err != nil {
			t.Fatal(err)
		}
		if on, _ := s.Enabled(ctx); !on {
			t.Fatal("expected the module on")
		}
	})

	t.Run("turning off needs no notice", func(t *testing.T) {
		db := &fakeDBStore{cfg: store.AIConfig{Enabled: true}}
		s := newTestStore(t, db)
		if err := s.SetEnabled(ctx, false); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStore_AcceptNotice(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	db := &fakeDBStore{}
	s := newTestStore(t, db)

	if err := s.AcceptNotice(ctx, "2000-01-01", "alice", at); !errors.Is(err, aiconfig.ErrNoticeVersion) {
		t.Fatalf("expected ErrNoticeVersion, got %v", err)
	}
	if db.cfg.Notice.Version != "" {
		t.Fatal("a refused notice must not be stored")
	}
	if err := s.AcceptNotice(ctx, airules.DataNoticeVersion, "alice", at); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NoticeCurrent() || got.Notice.AcceptedBy != "alice" || !got.Notice.AcceptedAt.Equal(at) {
		t.Fatalf("notice did not round-trip: %+v", got.Notice)
	}
	if err := s.SetEnabled(ctx, true); err != nil {
		t.Fatalf("expected the module to turn on after the notice, got %v", err)
	}
}

func TestStore_SetProviderSettings(t *testing.T) {
	ctx := context.Background()
	db := &fakeDBStore{}
	s := newTestStore(t, db)

	for _, bad := range []string{"", "not-a-provider"} {
		err := s.SetProviderSettings(ctx, store.ProviderSettings{Provider: bad})
		if !errors.Is(err, aiconfig.ErrUnknownProvider) {
			t.Fatalf("provider %q: expected ErrUnknownProvider, got %v", bad, err)
		}
	}
	for _, k := range provider.Kinds {
		if err := s.SetProviderSettings(ctx, store.ProviderSettings{Provider: string(k)}); err != nil {
			t.Fatalf("provider %q: %v", k, err)
		}
	}

	want := store.ProviderSettings{
		Provider: string(provider.AzureOpenAI), Model: "example-model",
		BaseURL: "https://ai.example.org", Region: "", Deployment: "example-deployment",
	}
	if err := s.SetProviderSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCredential(ctx, "test-key-8"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != want {
		t.Fatalf("provider settings: got %+v, want %+v", got.Provider, want)
	}
	ps, err := s.ProviderSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantPS := provider.Settings{
		Kind: provider.AzureOpenAI, Model: "example-model", BaseURL: "https://ai.example.org",
		Deployment: "example-deployment", Credential: "test-key-8",
	}
	if ps != wantPS {
		t.Fatalf("adapter settings: got kind=%s model=%s, want kind=%s model=%s", ps.Kind, ps.Model, wantPS.Kind, wantPS.Model)
	}
}

func TestStore_SetMonthlyLimit(t *testing.T) {
	ctx := context.Background()
	db := &fakeDBStore{}
	s := newTestStore(t, db)

	if err := s.SetMonthlyLimit(ctx, -1); !errors.Is(err, aiconfig.ErrInvalidMonthlyLimit) {
		t.Fatalf("expected ErrInvalidMonthlyLimit, got %v", err)
	}
	for _, v := range []int64{0, 1000} {
		if err := s.SetMonthlyLimit(ctx, v); err != nil {
			t.Fatal(err)
		}
		got, err := s.MonthlyLimit(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got != v {
			t.Fatalf("monthly limit: got %d, want %d", got, v)
		}
	}
}

func TestStore_OrgContextAndBlend_roundTrip(t *testing.T) {
	ctx := context.Background()
	db := &fakeDBStore{}
	s := newTestStore(t, db)

	if err := s.SetOrgContext(ctx, "Example Organisation writes these policies."); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.OrgContext(ctx); got != "Example Organisation writes these policies." {
		t.Fatalf("org context: got %q", got)
	}
	b := store.BlendCoefficients{Centroid: 0.4, Usage: 0.3, Crossref: 0.2, Entity: 0.1}
	if err := s.SetBlendCoefficients(ctx, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.BlendCoefficients(ctx); got != b {
		t.Fatalf("blend: got %+v, want %+v", got, b)
	}
}
