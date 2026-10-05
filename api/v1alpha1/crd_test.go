// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	aiv1alpha1 "github.com/Steward-GRC/steward-ai/api/v1alpha1"
)

const crdPath = "../../deployments/crd/ai.steward-grc.com_policyaijobs.yaml"

func TestGroupVersion(t *testing.T) {
	t.Parallel()
	if got, want := aiv1alpha1.GroupVersion.String(), "ai.steward-grc.com/v1alpha1"; got != want {
		t.Errorf("GroupVersion = %q, want %q", got, want)
	}
}

func loadCRD(t *testing.T) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	raw, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.UnmarshalStrict(raw, &crd); err != nil {
		t.Fatalf("parse CRD: %v", err)
	}
	return &crd
}

// TestCRDContract pins the names and fields the server creates and the
// operator reads.
func TestCRDContract(t *testing.T) {
	t.Parallel()
	crd := loadCRD(t)

	if crd.Name != "policyaijobs.ai.steward-grc.com" {
		t.Errorf("name = %q", crd.Name)
	}
	if crd.Spec.Names.Kind != "PolicyAIJob" || crd.Spec.Names.Plural != "policyaijobs" || crd.Spec.Names.ListKind != "PolicyAIJobList" {
		t.Errorf("names = %+v", crd.Spec.Names)
	}
	if crd.Spec.Scope != apiextensionsv1.NamespaceScoped {
		t.Errorf("scope = %q, want Namespaced", crd.Spec.Scope)
	}
	if len(crd.Spec.Versions) != 1 {
		t.Fatalf("versions = %d, want 1", len(crd.Spec.Versions))
	}
	v := crd.Spec.Versions[0]
	if v.Name != "v1alpha1" || !v.Served || !v.Storage {
		t.Errorf("version = %q served=%v storage=%v", v.Name, v.Served, v.Storage)
	}
	if v.Subresources == nil || v.Subresources.Status == nil {
		t.Errorf("status subresource missing")
	}
	spec := v.Schema.OpenAPIV3Schema.Properties["spec"]
	for _, f := range []string{"operation", "actorUserId", "impersonatorUserId", "policyId", "versionId", "categoryId",
		"readCategoryIds", "includeSensitive", "allCategories", "input", "modelId"} {
		if _, ok := spec.Properties[f]; !ok {
			t.Errorf("spec.%s missing", f)
		}
	}
	if !slices.Equal(spec.Required, []string{"operation"}) {
		t.Errorf("spec.required = %v, want [operation]", spec.Required)
	}
	status := v.Schema.OpenAPIV3Schema.Properties["status"]
	for _, f := range []string{"phase", "resultRef", "error", "attempts", "startedAt", "finishedAt", "observedGeneration"} {
		if _, ok := status.Properties[f]; !ok {
			t.Errorf("status.%s missing", f)
		}
	}
}

// TestCRDAcceptsEveryOperation guards the enum: the API server rejects an
// operation the schema doesn't list, so every constant must be in it.
func TestCRDAcceptsEveryOperation(t *testing.T) {
	t.Parallel()
	crd := loadCRD(t)
	op := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["operation"]
	var listed []string
	for _, e := range op.Enum {
		var s string
		if err := json.Unmarshal(e.Raw, &s); err != nil {
			t.Fatalf("enum value %s: %v", e.Raw, err)
		}
		listed = append(listed, s)
	}
	for _, want := range []aiv1alpha1.JobOperation{
		aiv1alpha1.OperationDraft, aiv1alpha1.OperationRewrite, aiv1alpha1.OperationClarify,
		aiv1alpha1.OperationSummarize, aiv1alpha1.OperationReview, aiv1alpha1.OperationQA,
		aiv1alpha1.OperationRevise, aiv1alpha1.OperationRelatedReeval,
		aiv1alpha1.OperationSuggestEnrichments, aiv1alpha1.OperationRelationshipLearn,
	} {
		if !slices.Contains(listed, string(want)) {
			t.Errorf("operation %q is not in the CRD enum %v", want, listed)
		}
	}
}

func TestSchemeRegistersBothKinds(t *testing.T) {
	t.Parallel()
	s := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"PolicyAIJob", "PolicyAIJobList"} {
		if !s.Recognizes(aiv1alpha1.GroupVersion.WithKind(kind)) {
			t.Errorf("scheme does not recognise %s", kind)
		}
	}
}

func TestDeepCopyIsIndependent(t *testing.T) {
	t.Parallel()
	now := metav1.Now()
	in := &aiv1alpha1.PolicyAIJob{
		Spec: aiv1alpha1.PolicyAIJobSpec{
			Operation:       aiv1alpha1.OperationQA,
			ActorUserID:     "erin",
			ReadCategoryIDs: []string{"finance"},
			Input:           runtime.RawExtension{Raw: []byte(`{"question":"How long are records kept?"}`)},
		},
		Status: aiv1alpha1.PolicyAIJobStatus{Phase: aiv1alpha1.PhaseRunning, StartedAt: &now},
	}
	out := in.DeepCopy()
	out.Spec.ReadCategoryIDs[0] = "hr"
	out.Spec.Input.Raw[0] = '['
	out.Status.StartedAt.Time = now.Add(1)
	if in.Spec.ReadCategoryIDs[0] != "finance" || in.Spec.Input.Raw[0] != '{' || !in.Status.StartedAt.Equal(&now) {
		t.Error("DeepCopy shares memory with the original")
	}
}
