// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
)

// credentialName binds the sealed credential to its column.
const credentialName = "ai_config.credential" // #nosec G101 -- the column name used as AEAD additional data, not a secret

// credentialLast4Len is how many trailing characters are kept for display.
const credentialLast4Len = 4

// AIConfig is the module's settings row. Credential is the decrypted
// provider credential; only internal/aiconfig reads it, to hand to the
// provider adapter, and nothing returns it to a caller.
type AIConfig struct {
	Enabled bool
	// Model is the stored model override; empty uses the provider default.
	Model string
	TopK  int
	Blend BlendCoefficients

	Provider        string
	BaseURL         string
	Region          string
	Deployment      string
	Credential      string
	CredentialLast4 string

	Notice       DataNotice
	MonthlyLimit int64
	OrgContext   string
}

// CredentialSet reports whether a provider credential is stored.
func (c AIConfig) CredentialSet() bool { return c.Credential != "" }

// DataNotice is the last data notice an administrator accepted.
type DataNotice struct {
	Version    string
	AcceptedBy string
	AcceptedAt *time.Time
}

// ProviderSettings are the provider's non-secret settings.
type ProviderSettings struct {
	Provider   string
	Model      string
	BaseURL    string
	Region     string
	Deployment string
}

// BlendCoefficients are the four weights the relationship learner blends
// into ai_policy_relationship.blended_score.
type BlendCoefficients struct {
	Centroid float64
	Usage    float64
	Crossref float64
	Entity   float64
}

// AIConfigStore reads and writes the single-row ai_config table. The
// credential is sealed with box before it is written.
type AIConfigStore struct {
	db  *postgres.DB
	box *SecretBox
}

// NewAIConfigStore returns a store on db that seals the credential with box.
func NewAIConfigStore(db *postgres.DB, box *SecretBox) *AIConfigStore {
	return &AIConfigStore{db: db, box: box}
}

// Get reads the row. The baseline migration seeds exactly one.
func (s *AIConfigStore) Get(ctx context.Context) (AIConfig, error) {
	if s.db == nil {
		return AIConfig{}, fmt.Errorf("store: ai config store has no pool configured")
	}
	var (
		c      AIConfig
		sealed []byte
	)
	const q = `SELECT enabled, model, top_k,
	                  blend_centroid, blend_usage, blend_crossref, blend_entity,
	                  provider, base_url, region, deployment, credential_sealed, credential_last4,
	                  notice_version, notice_accepted_by, notice_accepted_at,
	                  monthly_limit, org_context
	           FROM ai_config WHERE id = true`
	if err := s.db.Pool().QueryRow(ctx, q).Scan(
		&c.Enabled, &c.Model, &c.TopK,
		&c.Blend.Centroid, &c.Blend.Usage, &c.Blend.Crossref, &c.Blend.Entity,
		&c.Provider, &c.BaseURL, &c.Region, &c.Deployment, &sealed, &c.CredentialLast4,
		&c.Notice.Version, &c.Notice.AcceptedBy, &c.Notice.AcceptedAt,
		&c.MonthlyLimit, &c.OrgContext,
	); err != nil {
		return AIConfig{}, fmt.Errorf("store: get ai config: %w", err)
	}
	cred, err := s.box.open(credentialName, sealed)
	if err != nil {
		return AIConfig{}, err
	}
	c.Credential = cred
	return c, nil
}

// SetEnabled persists the module switch.
func (s *AIConfigStore) SetEnabled(ctx context.Context, enabled bool) error {
	return s.update(ctx, "set ai enabled", `UPDATE ai_config SET enabled = $1, updated_at = now() WHERE id = true`, enabled)
}

// SetModel persists the model override; empty clears it.
func (s *AIConfigStore) SetModel(ctx context.Context, model string) error {
	return s.update(ctx, "set ai model", `UPDATE ai_config SET model = $1, updated_at = now() WHERE id = true`, model)
}

// SetProviderSettings persists the provider and its non-secret settings in
// one write.
func (s *AIConfigStore) SetProviderSettings(ctx context.Context, p ProviderSettings) error {
	return s.update(ctx, "set provider settings",
		`UPDATE ai_config SET provider = $1, model = $2, base_url = $3, region = $4, deployment = $5,
		        updated_at = now() WHERE id = true`,
		p.Provider, p.Model, p.BaseURL, p.Region, p.Deployment)
}

// SetCredential seals and stores the provider credential with its last four
// characters for display. An empty credential clears both.
func (s *AIConfigStore) SetCredential(ctx context.Context, credential string) error {
	if s.db == nil {
		return fmt.Errorf("store: ai config store has no pool configured")
	}
	sealed, err := s.box.seal(credentialName, credential)
	if err != nil {
		return fmt.Errorf("store: seal credential: %w", err)
	}
	return s.update(ctx, "set credential",
		`UPDATE ai_config SET credential_sealed = $1, credential_last4 = $2, updated_at = now() WHERE id = true`,
		sealed, Last4(credential))
}

// AcceptNotice records who accepted which data notice version, and when.
func (s *AIConfigStore) AcceptNotice(ctx context.Context, version, acceptedBy string, at time.Time) error {
	return s.update(ctx, "accept data notice",
		`UPDATE ai_config SET notice_version = $1, notice_accepted_by = $2, notice_accepted_at = $3,
		        updated_at = now() WHERE id = true`,
		version, acceptedBy, at.UTC())
}

// SetMonthlyLimit persists the monthly request limit; zero is none.
func (s *AIConfigStore) SetMonthlyLimit(ctx context.Context, limit int64) error {
	return s.update(ctx, "set monthly limit",
		`UPDATE ai_config SET monthly_limit = $1, updated_at = now() WHERE id = true`, limit)
}

// SetOrgContext persists the organisation context for prompts.
func (s *AIConfigStore) SetOrgContext(ctx context.Context, orgContext string) error {
	return s.update(ctx, "set org context",
		`UPDATE ai_config SET org_context = $1, updated_at = now() WHERE id = true`, orgContext)
}

// SetTopK persists the retrieval candidate count. The caller resolves the
// default and the ceiling first.
func (s *AIConfigStore) SetTopK(ctx context.Context, topK int) error {
	return s.update(ctx, "set ai top_k", `UPDATE ai_config SET top_k = $1, updated_at = now() WHERE id = true`, topK)
}

// SetBlendCoefficients persists the relationship blend weights. The caller
// validates them first.
func (s *AIConfigStore) SetBlendCoefficients(ctx context.Context, b BlendCoefficients) error {
	return s.update(ctx, "set ai blend coefficients",
		`UPDATE ai_config
		 SET blend_centroid = $1, blend_usage = $2, blend_crossref = $3, blend_entity = $4,
		     updated_at = now()
		 WHERE id = true`,
		b.Centroid, b.Usage, b.Crossref, b.Entity)
}

func (s *AIConfigStore) update(ctx context.Context, what, q string, args ...any) error {
	if s.db == nil {
		return fmt.Errorf("store: ai config store has no pool configured")
	}
	if _, err := s.db.Pool().Exec(ctx, q, args...); err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	return nil
}

// Last4 is the display form of a credential: its last four characters, or
// nothing for a credential that short, so a short one is never shown whole.
func Last4(credential string) string {
	r := []rune(credential)
	if len(r) <= credentialLast4Len {
		return ""
	}
	return string(r[len(r)-credentialLast4Len:])
}
