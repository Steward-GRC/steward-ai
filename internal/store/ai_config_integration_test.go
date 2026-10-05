// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

func TestAIConfigStore_Get_seededRow(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewAIConfigStore(pool, testBox(t))

	got, err := repo.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Enabled {
		t.Fatal("expected a fresh install to ship with the module off")
	}
	if got.CredentialSet() || got.CredentialLast4 != "" {
		t.Fatalf("expected no credential on the seeded row, got last4 %q", got.CredentialLast4)
	}
	if got.MonthlyLimit != 0 || got.OrgContext != "" || got.Notice.Version != "" || got.Notice.AcceptedAt != nil {
		t.Fatalf("expected empty guardrail settings, got %+v", got)
	}
}

// The blend coefficients default to 0.5/0.3/0.15/0.05 and round-trip.
func TestAIConfigStore_BlendCoefficients_defaultsAndRoundTrip(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewAIConfigStore(pool, testBox(t))
	ctx := context.Background()

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	def := got.Blend
	if !floatEq(def.Centroid, 0.5) || !floatEq(def.Usage, 0.3) ||
		!floatEq(def.Crossref, 0.15) || !floatEq(def.Entity, 0.05) {
		t.Fatalf("unexpected blend defaults: %+v", def)
	}

	want := store.BlendCoefficients{Centroid: 0.6, Usage: 0.25, Crossref: 0.1, Entity: 0.05}
	if err := repo.SetBlendCoefficients(ctx, want); err != nil {
		t.Fatalf("set blend: %v", err)
	}
	got, err = repo.Get(ctx)
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if !floatEq(got.Blend.Centroid, 0.6) || !floatEq(got.Blend.Usage, 0.25) ||
		!floatEq(got.Blend.Crossref, 0.1) || !floatEq(got.Blend.Entity, 0.05) {
		t.Fatalf("blend did not round-trip: %+v", got.Blend)
	}
}

func floatEq(a, b float64) bool {
	d := a - b
	return d < 1e-5 && d > -1e-5
}

func TestAIConfigStore_SetEnabled_roundTrips(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewAIConfigStore(pool, testBox(t))
	ctx := context.Background()

	if err := repo.SetEnabled(ctx, false); err != nil {
		t.Fatalf("set enabled false: %v", err)
	}
	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Enabled {
		t.Fatal("expected enabled=false after SetEnabled(false)")
	}

	if err := repo.SetEnabled(ctx, true); err != nil {
		t.Fatalf("set enabled true: %v", err)
	}
	got, err = repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Enabled {
		t.Fatal("expected enabled=true after SetEnabled(true)")
	}
}

func TestAIConfigStore_SetModel_roundTrips(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewAIConfigStore(pool, testBox(t))
	ctx := context.Background()

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Model != "" {
		t.Fatalf("expected the seeded row to default model=\"\", got %q", got.Model)
	}

	if err := repo.SetModel(ctx, "claude-opus-4-8"); err != nil {
		t.Fatalf("set model: %v", err)
	}
	got, err = repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Model != "claude-opus-4-8" {
		t.Fatalf("expected the stored model to round-trip, got %q", got.Model)
	}

	// Clearing the override persists an empty string.
	if err := repo.SetModel(ctx, ""); err != nil {
		t.Fatalf("clear model: %v", err)
	}
	got, err = repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Model != "" {
		t.Fatalf("expected the model override cleared, got %q", got.Model)
	}
}

func TestAIConfigStore_TopK_defaultsTo50AndRoundTrips(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewAIConfigStore(pool, testBox(t))
	ctx := context.Background()

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TopK != 50 {
		t.Fatalf("expected the seeded row to default top_k=50, got %d", got.TopK)
	}

	if err := repo.SetTopK(ctx, 25); err != nil {
		t.Fatalf("set top_k: %v", err)
	}
	got, err = repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TopK != 25 {
		t.Fatalf("expected the stored top_k to round-trip, got %d", got.TopK)
	}
}

func TestAIConfigStore_SetCredential_sealsAndStaysSingleRow(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewAIConfigStore(pool, testBox(t))
	ctx := context.Background()

	if err := repo.SetCredential(ctx, "placeholder-credential-0001"); err != nil {
		t.Fatalf("set credential: %v", err)
	}
	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Credential != "placeholder-credential-0001" || got.CredentialLast4 != "0001" {
		t.Fatalf("credential did not round-trip: last4 %q", got.CredentialLast4)
	}
	var sealed []byte
	if err := pool.Pool().QueryRow(ctx, `SELECT credential_sealed FROM ai_config`).Scan(&sealed); err != nil {
		t.Fatalf("read sealed: %v", err)
	}
	if strings.Contains(string(sealed), "placeholder-credential") {
		t.Fatal("the credential is stored in the clear")
	}

	if err := repo.SetCredential(ctx, "placeholder-credential-0002"); err != nil {
		t.Fatalf("replace credential: %v", err)
	}
	var count int
	if err := pool.Pool().QueryRow(ctx, `SELECT count(*) FROM ai_config`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row in ai_config, got %d", count)
	}
	got, err = repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Credential != "placeholder-credential-0002" || got.CredentialLast4 != "0002" {
		t.Fatalf("expected the replacement credential, last4 %q", got.CredentialLast4)
	}

	if err := repo.SetCredential(ctx, ""); err != nil {
		t.Fatalf("clear credential: %v", err)
	}
	got, err = repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CredentialSet() || got.CredentialLast4 != "" {
		t.Fatal("expected the credential cleared")
	}
}

func TestAIConfigStore_CredentialUnreadableWithAnotherKey(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	if err := store.NewAIConfigStore(pool, testBox(t)).SetCredential(ctx, "placeholder-credential-0001"); err != nil {
		t.Fatalf("set credential: %v", err)
	}
	other, err := store.NewSecretBox(bytes.Repeat([]byte{0x02}, store.SettingsKeySize))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewAIConfigStore(pool, other).Get(ctx); !errors.Is(err, store.ErrSecretUnreadable) {
		t.Fatalf("expected ErrSecretUnreadable, got %v", err)
	}
}

func TestAIConfigStore_ProviderSettingsRoundTrip(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewAIConfigStore(pool, testBox(t))
	ctx := context.Background()

	want := store.ProviderSettings{Provider: "azure_openai", Model: "example-model",
		BaseURL: "https://ai.example.org", Region: "", Deployment: "example-deployment"}
	if err := repo.SetProviderSettings(ctx, want); err != nil {
		t.Fatalf("set provider: %v", err)
	}
	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Provider != want.Provider || got.Model != want.Model || got.BaseURL != want.BaseURL || got.Deployment != want.Deployment {
		t.Fatalf("provider settings did not round-trip: %+v", got)
	}
}

func TestAIConfigStore_GuardrailsRoundTrip(t *testing.T) {
	pool := newTestDB(t)
	repo := store.NewAIConfigStore(pool, testBox(t))
	ctx := context.Background()

	at := time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC)
	if err := repo.AcceptNotice(ctx, "2026-10-05", "alice", at); err != nil {
		t.Fatalf("accept notice: %v", err)
	}
	if err := repo.SetMonthlyLimit(ctx, 1000); err != nil {
		t.Fatalf("set monthly limit: %v", err)
	}
	if err := repo.SetOrgContext(ctx, "Example Organisation"); err != nil {
		t.Fatalf("set org context: %v", err)
	}
	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Notice.Version != "2026-10-05" || got.Notice.AcceptedBy != "alice" || got.Notice.AcceptedAt == nil || !got.Notice.AcceptedAt.Equal(at) {
		t.Fatalf("notice did not round-trip: %+v", got.Notice)
	}
	if got.MonthlyLimit != 1000 || got.OrgContext != "Example Organisation" {
		t.Fatalf("limit or context did not round-trip: %+v", got)
	}
	if err := repo.SetMonthlyLimit(ctx, -1); err == nil {
		t.Fatal("expected a negative monthly limit to be refused")
	}
}
