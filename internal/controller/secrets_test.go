/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func TestCollectSecretRefs_DedupesAcrossLayers(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "hermes"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			LLMProviders: []hermesv1alpha1.HermesAgentLLMProvider{
				{Env: []corev1.EnvVar{
					{Name: "API1", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "s1"}, Key: "k1"}}},
				}},
			},
			Gateways: []hermesv1alpha1.HermesAgentGateway{
				{Env: []corev1.EnvVar{
					{Name: "API2", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "s2"}, Key: "k2"}}},
				}},
			},
			Env: []corev1.EnvVar{
				{Name: "API3", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "s1"}, Key: "k1"}}}, // dup
				{Name: "API4", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "s3"}, Key: "k3"}}},
			},
			EnvFrom: []corev1.EnvFromSource{
				{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "s4"}}},
			},
		},
	}
	got := collectSecretRefs(agent)
	if len(got) != 4 {
		t.Errorf("want 4 unique refs, got %d: %v", len(got), got)
	}
	// envFrom-style refs use Key="" sentinel.
	found := false
	for _, r := range got {
		if r.SecretName == "s4" && r.Key == "" {
			found = true
		}
	}
	if !found {
		t.Errorf("envFrom secret s4 missing from refs")
	}
}
