// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package contract replays recorded provider API contracts for the adapter
// tests. A contract file holds the call an adapter is given, the requests it
// must send (method, path, auth header, key body fields) and the responses
// the provider answers with, written from the provider's published API
// reference. An httptest server plays the provider's side.
package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Contract is one recorded exchange sequence and the outcome the adapter must
// report.
type Contract struct {
	Name        string     `json:"-"`
	Description string     `json:"description"`
	Reference   string     `json:"reference"`
	Call        Call       `json:"call"`
	Exchanges   []Exchange `json:"exchanges"`
	Expect      Expect     `json:"expect"`
}

// Call is what the test hands the adapter.
type Call struct {
	Settings       Settings  `json:"settings"`
	System         string    `json:"system"`
	Messages       []Message `json:"messages"`
	MaxTokens      int       `json:"max_tokens"`
	Model          string    `json:"model"`
	EnableWebFetch bool      `json:"enable_web_fetch"`
	Inputs         []string  `json:"inputs"`
}

// Settings mirror provider.Settings without the base URL, which the test
// points at the replay server.
type Settings struct {
	Kind       string `json:"kind"`
	Model      string `json:"model"`
	Region     string `json:"region"`
	Deployment string `json:"deployment"`
	Credential string `json:"credential"`
}

// Message is one conversation turn of the call.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Exchange is one request the adapter must send and the provider's answer.
type Exchange struct {
	Request  Request  `json:"request"`
	Response Response `json:"response"`
}

// Request is what the adapter must send. Body lists only the fields that
// matter; BodyAbsent names top-level fields that must not be sent.
type Request struct {
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Query      map[string]string `json:"query"`
	AuthHeader string            `json:"auth_header"`
	AuthValue  string            `json:"auth_value"`
	AuthPrefix string            `json:"auth_prefix"`
	Headers    map[string]string `json:"headers"`
	// HeadersAbsent names headers that must not be sent.
	HeadersAbsent []string        `json:"headers_absent"`
	Body          json.RawMessage `json:"body"`
	BodyAbsent    []string        `json:"body_absent"`
}

// Response is the provider's answer: a JSON body, a plain text body, or
// server-sent events.
type Response struct {
	Status   int               `json:"status"`
	Headers  map[string]string `json:"headers"`
	Body     json.RawMessage   `json:"body"`
	BodyText string            `json:"body_text"`
	Events   []Event           `json:"events"`
}

// Event is one server-sent event frame.
type Event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// Expect is the outcome the adapter must report. Error is "", "auth",
// "provider" or "not_configured".
type Expect struct {
	Text         string `json:"text"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	Error        string `json:"error"`
	Vectors      int    `json:"vectors"`
	Dimensions   int    `json:"dimensions"`
}

// LoadDir reads every contract in dir, sorted by file name.
func LoadDir(t *testing.T, dir string) []*Contract {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("list contracts: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no contracts in %s", dir)
	}
	sort.Strings(files)
	out := make([]*Contract, 0, len(files))
	for _, f := range files {
		out = append(out, Load(t, f))
	}
	return out
}

// Load reads one contract file.
func Load(t *testing.T, file string) *Contract {
	t.Helper()
	raw, err := os.ReadFile(file) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	var c Contract
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("decode contract %s: %v", file, err)
	}
	c.Name = strings.TrimSuffix(filepath.Base(file), ".json")
	return &c
}

// Serve starts a replay server for c. Each request is checked against the
// next exchange; the test fails if an exchange is left unserved or an extra
// request arrives.
func (c *Contract) Serve(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := next
		next++
		mu.Unlock()
		if i >= len(c.Exchanges) {
			t.Errorf("%s: unexpected request %d to %s", c.Name, i+1, r.URL.Path)
			http.Error(w, "no exchange left", http.StatusTeapot)
			return
		}
		ex := c.Exchanges[i]
		for _, problem := range ex.Request.check(r) {
			t.Errorf("%s: request %d: %s", c.Name, i+1, problem)
		}
		ex.Response.write(t, w)
	}))
	t.Cleanup(func() {
		srv.Close()
		mu.Lock()
		defer mu.Unlock()
		if next < len(c.Exchanges) {
			t.Errorf("%s: %d of %d exchanges served", c.Name, next, len(c.Exchanges))
		}
	})
	return srv
}

func (q Request) check(r *http.Request) []string {
	var problems []string
	if q.Method != "" && r.Method != q.Method {
		problems = append(problems, fmt.Sprintf("method %s, want %s", r.Method, q.Method))
	}
	if q.Path != "" && r.URL.Path != q.Path {
		problems = append(problems, fmt.Sprintf("path %s, want %s", r.URL.Path, q.Path))
	}
	for k, v := range q.Query {
		if got := r.URL.Query().Get(k); got != v {
			problems = append(problems, fmt.Sprintf("query %s=%q, want %q", k, got, v))
		}
	}
	if q.AuthHeader != "" {
		got := r.Header.Get(q.AuthHeader)
		switch {
		case got == "":
			problems = append(problems, fmt.Sprintf("auth header %s missing", q.AuthHeader))
		case q.AuthValue != "" && got != q.AuthValue:
			problems = append(problems, fmt.Sprintf("auth header %s=%q, want %q", q.AuthHeader, got, q.AuthValue))
		case q.AuthPrefix != "" && !strings.HasPrefix(got, q.AuthPrefix):
			problems = append(problems, fmt.Sprintf("auth header %s does not start with %q", q.AuthHeader, q.AuthPrefix))
		}
	}
	for k, v := range q.Headers {
		if got := r.Header.Get(k); got != v {
			problems = append(problems, fmt.Sprintf("header %s=%q, want %q", k, got, v))
		}
	}
	for _, k := range q.HeadersAbsent {
		if r.Header.Get(k) != "" {
			problems = append(problems, fmt.Sprintf("header %s present, want absent", k))
		}
	}
	if len(q.Body) == 0 && len(q.BodyAbsent) == 0 {
		return problems
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return append(problems, "read body: "+err.Error())
	}
	var got any
	if err := json.Unmarshal(raw, &got); err != nil {
		return append(problems, "body is not JSON: "+err.Error())
	}
	if len(q.Body) > 0 {
		var want any
		if err := json.Unmarshal(q.Body, &want); err != nil {
			return append(problems, "contract body: "+err.Error())
		}
		problems = append(problems, subset("body", want, got)...)
	}
	if obj, ok := got.(map[string]any); ok {
		for _, k := range q.BodyAbsent {
			if _, present := obj[k]; present {
				problems = append(problems, fmt.Sprintf("body.%s present, want absent", k))
			}
		}
	}
	return problems
}

// subset reports where got does not carry want: objects need every listed
// key, arrays need the same length element by element, scalars must equal.
func subset(at string, want, got any) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s is %T, want object", at, got)}
		}
		var problems []string
		for k, wv := range w {
			gv, present := g[k]
			if !present {
				problems = append(problems, fmt.Sprintf("%s.%s missing", at, k))
				continue
			}
			problems = append(problems, subset(at+"."+k, wv, gv)...)
		}
		return problems
	case []any:
		g, ok := got.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s is %T, want array", at, got)}
		}
		if len(g) != len(w) {
			return []string{fmt.Sprintf("%s has %d elements, want %d", at, len(g), len(w))}
		}
		var problems []string
		for i := range w {
			problems = append(problems, subset(fmt.Sprintf("%s[%d]", at, i), w[i], g[i])...)
		}
		return problems
	default:
		if !reflect.DeepEqual(want, got) {
			return []string{fmt.Sprintf("%s = %v, want %v", at, got, want)}
		}
		return nil
	}
}

func (p Response) write(t *testing.T, w http.ResponseWriter) {
	for k, v := range p.Headers {
		w.Header().Set(k, v)
	}
	if len(p.Events) > 0 {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(p.Status)
		for _, e := range p.Events {
			// One data line per frame: compact the JSON so no newline splits it.
			var buf bytes.Buffer
			if err := json.Compact(&buf, e.Data); err != nil {
				t.Errorf("compact event %s: %v", e.Event, err)
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Event, buf.String())
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		return
	}
	if p.BodyText != "" {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/plain")
		}
		w.WriteHeader(p.Status)
		_, _ = w.Write([]byte(p.BodyText))
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(p.Status)
	_, _ = w.Write(p.Body)
}
