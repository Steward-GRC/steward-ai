// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package v1alpha1 holds the ai.steward-grc.com/v1alpha1 API: the PolicyAIJob
// resource behind every asynchronous AI job. One resource kind serves every
// operation; the operator dispatches on spec.operation.
//
// +kubebuilder:object:generate=true
// +groupName=ai.steward-grc.com
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the API group and version of the types in this package.
// v1alpha1: the shape may still change before v1.
var GroupVersion = schema.GroupVersion{Group: "ai.steward-grc.com", Version: "v1alpha1"}

var (
	// SchemeBuilder registers the types in this package with a scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &PolicyAIJob{}, &PolicyAIJobList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
