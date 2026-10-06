// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/Steward-GRC/steward-ai/api/v1alpha1"
)

// envClient is the client on the shared control plane; nil when the envtest
// binaries are not available.
var envClient client.Client

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		return m.Run()
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "deployments", "crd")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "envtest: start the control plane: %v\n", err)
		return 1
	}
	defer func() { _ = env.Stop() }()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		fmt.Fprintf(os.Stderr, "envtest: scheme: %v\n", err)
		return 1
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		fmt.Fprintf(os.Stderr, "envtest: scheme: %v\n", err)
		return 1
	}
	if envClient, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		fmt.Fprintf(os.Stderr, "envtest: client: %v\n", err)
		return 1
	}
	return m.Run()
}
