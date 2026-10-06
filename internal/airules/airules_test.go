// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package airules

import "testing"

// TestChunkReadable mirrors the scenarios asserted against the SQL in the
// store's chunk tests.
func TestChunkReadable(t *testing.T) {
	cases := []struct {
		name             string
		sensitivity      string
		allCategories    bool
		includeSensitive bool
		categoryID       string
		categoryIDs      []string
		want             bool
	}{
		{"standard in a scoped category", SensitivityStandard, false, false, "finance", []string{"finance"}, true},
		{"standard outside the scope", SensitivityStandard, false, false, "finance", []string{"hr"}, false},
		{"standard with an empty scope", SensitivityStandard, false, false, "finance", nil, false},
		{"sensitive without the sensitive grant", SensitivitySensitive, false, false, "hr", []string{"hr"}, false},
		{"sensitive outside the scope", SensitivitySensitive, false, true, "hr", []string{"finance"}, false},
		{"sensitive in scope with the grant", SensitivitySensitive, false, true, "hr", []string{"hr"}, true},
		{"sensitive with an empty scope", SensitivitySensitive, false, true, "hr", []string{}, false},
		{"all categories reads standard anywhere", SensitivityStandard, true, false, "hr", nil, true},
		{"all categories alone doesn't open sensitive", SensitivitySensitive, true, false, "hr", nil, false},
		{"all categories with the sensitive grant", SensitivitySensitive, true, true, "hr", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChunkReadable(tc.sensitivity, tc.allCategories, tc.includeSensitive, tc.categoryID, tc.categoryIDs)
			if got != tc.want {
				t.Fatalf("ChunkReadable(%q, all=%v, sensitive=%v, category=%q, scope=%v) = %v, want %v",
					tc.sensitivity, tc.allCategories, tc.includeSensitive, tc.categoryID, tc.categoryIDs, got, tc.want)
			}
		})
	}
}

func TestEffectiveQueryQuota(t *testing.T) {
	cases := []struct {
		name          string
		override      int
		hasOverride   bool
		def           int
		wantLimit     int
		wantUnlimited bool
	}{
		{"default applies", 0, false, 50, 50, false},
		{"override wins", 5, true, 50, 5, false},
		{"override unlimited", UnlimitedQueryQuota, true, 50, UnlimitedQueryQuota, true},
		{"default unlimited", 0, false, UnlimitedQueryQuota, UnlimitedQueryQuota, true},
	}
	for _, tc := range cases {
		limit, unlimited := EffectiveQueryQuota(tc.override, tc.hasOverride, tc.def)
		if limit != tc.wantLimit || unlimited != tc.wantUnlimited {
			t.Errorf("%s: got (%d, %v), want (%d, %v)", tc.name, limit, unlimited, tc.wantLimit, tc.wantUnlimited)
		}
	}
}

// TestNormalizeDocumentType: "PROCEDURE" in any case is a procedure; empty
// and anything else is a policy.
func TestNormalizeDocumentType(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"PROCEDURE", DocumentTypeProcedure},
		{"procedure", DocumentTypeProcedure},
		{"  Procedure  ", DocumentTypeProcedure},
		{"", DocumentTypePolicy},
		{"POLICY", DocumentTypePolicy},
		{"policy", DocumentTypePolicy},
		{"something-else", DocumentTypePolicy},
	}
	for _, tc := range cases {
		if got := NormalizeDocumentType(tc.in); got != tc.want {
			t.Fatalf("NormalizeDocumentType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
