// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package tei

import (
	"context"
	"errors"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/provider"
	"github.com/Steward-GRC/steward-ai/internal/provider/internal/contract"
)

func TestContracts(t *testing.T) {
	for _, c := range contract.LoadDir(t, "testdata/contracts/tei") {
		t.Run(c.Name, func(t *testing.T) {
			srv := c.Serve(t)
			var opts []Option
			if c.Call.Settings.Credential != "" {
				opts = append(opts, WithAPIKey(c.Call.Settings.Credential))
			}
			vecs, err := New(srv.URL+"/embed", provider.Dimensions, opts...).Embed(context.Background(), c.Call.Inputs)
			switch c.Expect.Error {
			case "":
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			case "auth":
				if !errors.Is(err, provider.ErrAuth) {
					t.Fatalf("want ErrAuth, got %v", err)
				}
				return
			case "provider":
				if err == nil || provider.Reason(err) != provider.ReasonProviderError {
					t.Fatalf("want a provider error, got %v", err)
				}
				return
			}
			if len(vecs) != c.Expect.Vectors {
				t.Fatalf("%d vectors, want %d", len(vecs), c.Expect.Vectors)
			}
			for i, v := range vecs {
				if len(v) != c.Expect.Dimensions {
					t.Errorf("vector %d width %d, want %d", i, len(v), c.Expect.Dimensions)
				}
			}
		})
	}
}
