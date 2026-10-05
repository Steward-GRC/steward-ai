// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"bytes"
	"testing"

	"github.com/Steward-GRC/steward-ai/internal/store"
)

func testBox(t *testing.T) *store.SecretBox {
	t.Helper()
	box, err := store.NewSecretBox(bytes.Repeat([]byte{0x01}, store.SettingsKeySize))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestNewSecretBoxNeeds32Bytes(t *testing.T) {
	if _, err := store.NewSecretBox(make([]byte, 16)); err == nil {
		t.Fatal("expected a 16-byte key to be refused")
	}
}

func TestLast4(t *testing.T) {
	cases := map[string]string{"": "", "abcd": "", "abcde": "bcde", "placeholder-0001": "0001"}
	for in, want := range cases {
		if got := store.Last4(in); got != want {
			t.Errorf("Last4(%q) = %q, want %q", in, got, want)
		}
	}
}
