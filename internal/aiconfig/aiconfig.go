// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package aiconfig is the module's settings, read on the hot path. Postgres
// (store.AIConfigStore) is the source of truth; Valkey holds a short-lived
// copy of the non-secret settings so an intake check or a provider call
// doesn't read Postgres every time.
//
// The provider credential never goes to Valkey. It is read from Postgres and
// kept in this process's memory for the same short TTL, so a Valkey dump or
// a shared Valkey never exposes it. Only Credential returns it, for the
// provider adapter; everything else sees CredentialSet and CredentialLast4.
//
// Off is off: a settings read that fails returns the error, and callers
// refuse the call rather than treat the module as on.
package aiconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	redis "github.com/Bugs5382/go-redis"

	"github.com/Steward-GRC/steward-ai/internal/airules"
	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/store"
)

const (
	cacheKey = "ai:settings"
	// cacheTTL only bounds how stale another replica's write can look; this
	// process refreshes the cache on its own writes.
	cacheTTL = 15 * time.Second
)

var (
	// ErrNoticeRequired refuses turning the module on before the current
	// data notice is accepted.
	ErrNoticeRequired = errors.New("aiconfig: the current data notice must be accepted before the module is turned on")
	// ErrNoticeVersion refuses accepting a data notice other than the
	// current one.
	ErrNoticeVersion = errors.New("aiconfig: only the current data notice can be accepted")
	// ErrUnknownProvider refuses a provider kind with no adapter.
	ErrUnknownProvider = errors.New("aiconfig: unknown provider")
	// ErrInvalidMonthlyLimit refuses a negative monthly limit.
	ErrInvalidMonthlyLimit = errors.New("aiconfig: monthly limit must be zero or more")
)

// Backend is the durable settings store; store.AIConfigStore satisfies it.
type Backend interface {
	Get(ctx context.Context) (store.AIConfig, error)
	SetEnabled(ctx context.Context, enabled bool) error
	SetModel(ctx context.Context, model string) error
	SetProviderSettings(ctx context.Context, p store.ProviderSettings) error
	SetCredential(ctx context.Context, credential string) error
	AcceptNotice(ctx context.Context, version, acceptedBy string, at time.Time) error
	SetMonthlyLimit(ctx context.Context, limit int64) error
	SetOrgContext(ctx context.Context, orgContext string) error
	SetTopK(ctx context.Context, topK int) error
	SetBlendCoefficients(ctx context.Context, b store.BlendCoefficients) error
}

// Settings are the module's non-secret settings.
type Settings struct {
	Enabled         bool                    `json:"enabled"`
	Provider        store.ProviderSettings  `json:"provider"`
	CredentialSet   bool                    `json:"credentialSet"`
	CredentialLast4 string                  `json:"credentialLast4"`
	Notice          store.DataNotice        `json:"notice"`
	MonthlyLimit    int64                   `json:"monthlyLimit"`
	OrgContext      string                  `json:"orgContext"`
	TopK            int                     `json:"topK"`
	Blend           store.BlendCoefficients `json:"blend"`
}

// NoticeCurrent reports whether the accepted data notice is the current one.
func (s Settings) NoticeCurrent() bool {
	return s.Notice.Version == airules.DataNoticeVersion
}

func fromAIConfig(c store.AIConfig) Settings {
	return Settings{
		Enabled: c.Enabled,
		Provider: store.ProviderSettings{
			Provider: c.Provider, Model: c.Model, BaseURL: c.BaseURL,
			Region: c.Region, Deployment: c.Deployment,
		},
		CredentialSet:   c.CredentialSet(),
		CredentialLast4: c.CredentialLast4,
		Notice:          c.Notice,
		MonthlyLimit:    c.MonthlyLimit,
		OrgContext:      c.OrgContext,
		TopK:            c.TopK,
		Blend:           c.Blend,
	}
}

// Store reads the settings through the Valkey cache and writes them to
// Postgres.
type Store struct {
	db  Backend
	rdb redis.UniversalClient
	now func() time.Time

	credMu      sync.Mutex
	credential  string
	credExpires time.Time
}

// Option configures a Store.
type Option func(*Store)

// WithClock replaces time.Now for the in-memory credential's expiry.
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// New returns a Store over db, caching in rdb.
func New(db Backend, rdb redis.UniversalClient, opts ...Option) *Store {
	s := &Store{db: db, rdb: rdb, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Settings returns the non-secret settings.
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	if raw, err := s.rdb.Get(ctx, cacheKey).Bytes(); err == nil {
		var c Settings
		if json.Unmarshal(raw, &c) == nil {
			return c, nil
		}
	}
	// A miss, a corrupt entry and a Valkey error all fall through: Postgres
	// is the source of truth and a degraded cache must not block a read.
	return s.load(ctx)
}

// load reads Postgres and refreshes both caches.
func (s *Store) load(ctx context.Context) (Settings, error) {
	c, err := s.db.Get(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("aiconfig: read settings: %w", err)
	}
	s.rememberCredential(c.Credential)
	out := fromAIConfig(c)
	if raw, err := json.Marshal(out); err == nil {
		_ = s.rdb.Set(ctx, cacheKey, raw, cacheTTL).Err()
	}
	return out, nil
}

func (s *Store) rememberCredential(credential string) {
	s.credMu.Lock()
	s.credential = credential
	s.credExpires = s.now().Add(cacheTTL)
	s.credMu.Unlock()
}

// reload refreshes the caches after a write. A failure only means the next
// read goes to Postgres; the write itself already succeeded.
func (s *Store) reload(ctx context.Context) {
	_, _ = s.load(ctx)
}

// Enabled reports the module switch. On a read failure it returns false and
// the error; the caller refuses the call.
func (s *Store) Enabled(ctx context.Context) (bool, error) {
	c, err := s.Settings(ctx)
	if err != nil {
		return false, err
	}
	return c.Enabled, nil
}

// SetEnabled turns the module on or off. Turning it on is refused with
// ErrNoticeRequired until the current data notice is accepted; the check
// reads Postgres, not the cache.
func (s *Store) SetEnabled(ctx context.Context, enabled bool) error {
	if enabled {
		c, err := s.db.Get(ctx)
		if err != nil {
			return fmt.Errorf("aiconfig: read settings: %w", err)
		}
		if c.Notice.Version != airules.DataNoticeVersion {
			return ErrNoticeRequired
		}
	}
	if err := s.db.SetEnabled(ctx, enabled); err != nil {
		return fmt.Errorf("aiconfig: set enabled: %w", err)
	}
	s.reload(ctx)
	return nil
}

// HasCredential reports whether a provider credential is stored.
func (s *Store) HasCredential(ctx context.Context) (bool, error) {
	c, err := s.Settings(ctx)
	if err != nil {
		return false, err
	}
	return c.CredentialSet, nil
}

// Credential returns the provider credential, for the provider adapter
// only. It is never logged, cached in Valkey or returned to a client.
// has=false with a nil error means none is stored.
func (s *Store) Credential(ctx context.Context) (credential string, has bool, err error) {
	s.credMu.Lock()
	if s.now().Before(s.credExpires) {
		credential = s.credential
		s.credMu.Unlock()
		return credential, credential != "", nil
	}
	s.credMu.Unlock()
	if _, err := s.load(ctx); err != nil {
		return "", false, err
	}
	s.credMu.Lock()
	credential = s.credential
	s.credMu.Unlock()
	return credential, credential != "", nil
}

// SetCredential stores a new provider credential; empty clears it.
func (s *Store) SetCredential(ctx context.Context, credential string) error {
	if err := s.db.SetCredential(ctx, credential); err != nil {
		return fmt.Errorf("aiconfig: set credential: %w", err)
	}
	s.reload(ctx)
	return nil
}

// ProviderSettings returns the provider settings with the credential, ready
// for an adapter. For the provider path only.
func (s *Store) ProviderSettings(ctx context.Context) (provider.Settings, error) {
	c, err := s.Settings(ctx)
	if err != nil {
		return provider.Settings{}, err
	}
	cred, _, err := s.Credential(ctx)
	if err != nil {
		return provider.Settings{}, err
	}
	return provider.Settings{
		Kind:       provider.Kind(c.Provider.Provider),
		Model:      c.Provider.Model,
		BaseURL:    c.Provider.BaseURL,
		Region:     c.Provider.Region,
		Deployment: c.Provider.Deployment,
		Credential: cred,
	}, nil
}

// SetProviderSettings stores the provider and its non-secret settings. The
// kind must be one of provider.Kinds.
func (s *Store) SetProviderSettings(ctx context.Context, p store.ProviderSettings) error {
	if !slices.Contains(provider.Kinds, provider.Kind(p.Provider)) {
		return fmt.Errorf("%w: %q", ErrUnknownProvider, p.Provider)
	}
	if err := s.db.SetProviderSettings(ctx, p); err != nil {
		return fmt.Errorf("aiconfig: set provider settings: %w", err)
	}
	s.reload(ctx)
	return nil
}

// Model returns the configured model, or "" for the adapter's default.
func (s *Store) Model(ctx context.Context) (string, error) {
	c, err := s.Settings(ctx)
	if err != nil {
		return "", err
	}
	return c.Provider.Model, nil
}

// SetModel stores the model override; empty clears it.
func (s *Store) SetModel(ctx context.Context, model string) error {
	if err := s.db.SetModel(ctx, model); err != nil {
		return fmt.Errorf("aiconfig: set model: %w", err)
	}
	s.reload(ctx)
	return nil
}

// AcceptNotice records that actor accepted the data notice version at now.
// Only airules.DataNoticeVersion can be accepted.
func (s *Store) AcceptNotice(ctx context.Context, version, actor string, now time.Time) error {
	if version != airules.DataNoticeVersion {
		return fmt.Errorf("%w: got %q, current is %q", ErrNoticeVersion, version, airules.DataNoticeVersion)
	}
	if err := s.db.AcceptNotice(ctx, version, actor, now); err != nil {
		return fmt.Errorf("aiconfig: accept notice: %w", err)
	}
	s.reload(ctx)
	return nil
}

// MonthlyLimit returns the monthly request limit; zero is none.
func (s *Store) MonthlyLimit(ctx context.Context) (int64, error) {
	c, err := s.Settings(ctx)
	if err != nil {
		return 0, err
	}
	return c.MonthlyLimit, nil
}

// SetMonthlyLimit stores the monthly request limit; zero is none.
func (s *Store) SetMonthlyLimit(ctx context.Context, limit int64) error {
	if limit < 0 {
		return ErrInvalidMonthlyLimit
	}
	if err := s.db.SetMonthlyLimit(ctx, limit); err != nil {
		return fmt.Errorf("aiconfig: set monthly limit: %w", err)
	}
	s.reload(ctx)
	return nil
}

// OrgContext returns the organisation context for prompts; empty is none.
func (s *Store) OrgContext(ctx context.Context) (string, error) {
	c, err := s.Settings(ctx)
	if err != nil {
		return "", err
	}
	return c.OrgContext, nil
}

// SetOrgContext stores the organisation context; empty clears it.
func (s *Store) SetOrgContext(ctx context.Context, orgContext string) error {
	if err := s.db.SetOrgContext(ctx, orgContext); err != nil {
		return fmt.Errorf("aiconfig: set org context: %w", err)
	}
	s.reload(ctx)
	return nil
}

// TopK returns the retrieval candidate count. The caller applies its own
// fallback to a non-positive value.
func (s *Store) TopK(ctx context.Context) (int, error) {
	c, err := s.Settings(ctx)
	if err != nil {
		return 0, err
	}
	return c.TopK, nil
}

// SetTopK stores the retrieval candidate count. The caller resolves the
// default and the ceiling first.
func (s *Store) SetTopK(ctx context.Context, topK int) error {
	if err := s.db.SetTopK(ctx, topK); err != nil {
		return fmt.Errorf("aiconfig: set top_k: %w", err)
	}
	s.reload(ctx)
	return nil
}

// BlendCoefficients returns the relationship blend weights; the learner
// reads them at the start of each run.
func (s *Store) BlendCoefficients(ctx context.Context) (store.BlendCoefficients, error) {
	c, err := s.Settings(ctx)
	if err != nil {
		return store.BlendCoefficients{}, err
	}
	return c.Blend, nil
}

// SetBlendCoefficients stores the blend weights. The caller validates them.
func (s *Store) SetBlendCoefficients(ctx context.Context, b store.BlendCoefficients) error {
	if err := s.db.SetBlendCoefficients(ctx, b); err != nil {
		return fmt.Errorf("aiconfig: set blend coefficients: %w", err)
	}
	s.reload(ctx)
	return nil
}
