// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package httpjson posts a JSON request and decodes a JSON answer, for the
// adapters that speak to a provider over plain net/http.
package httpjson

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxErrorBody bounds how much of an error answer is kept for the message.
const maxErrorBody = 4096

// StatusError is a non-2xx answer. Body is the start of the answer, for the
// adapter to pull the provider's message out of.
type StatusError struct {
	Status int
	Body   []byte
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("status %d: %s", e.Status, bytes.TrimSpace(e.Body))
}

// Post sends body as JSON to url with headers and decodes a 2xx answer into
// out. A non-2xx answer is a *StatusError.
func Post(ctx context.Context, client *http.Client, url string, headers map[string]string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return &StatusError{Status: resp.StatusCode, Body: b}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
