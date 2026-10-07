// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
)

func TestJobAdapterPing_passesWhenTheResourceIsServed(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	a := NewJobAdapter(fake.NewClientBuilder().WithScheme(scheme).Build(), "steward")
	if err := a.Ping(context.Background()); err != nil {
		t.Fatalf("Ping with the resource served: %v", err)
	}
}

func TestJobAdapterPing_failsWhenTheResourceIsMissing(t *testing.T) {
	a := NewJobAdapter(fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build(), "steward")
	if err := a.Ping(context.Background()); err == nil {
		t.Fatal("Ping without the PolicyAIJob resource: expected an error")
	}
}
