// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func TestRenderEnv_ComposesAllLayers(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			LLMDefaultProvider: "deepseek",
			LLMProviders: []hermesv1alpha1.HermesAgentLLMProvider{
				{
					Name: "deepseek",
					Env: []corev1.EnvVar{
						{Name: "DEEPSEEK_API_KEY", ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "creds"},
								Key:                  "DEEPSEEK_API_KEY",
							},
						}},
						{Name: "DEEPSEEK_BASE_URL", Value: "https://api.deepseek.com"},
					},
				},
			},
			Gateways: []hermesv1alpha1.HermesAgentGateway{
				{
					Type: "discord",
					Env: []corev1.EnvVar{
						{Name: "DISCORD_BOT_TOKEN", ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "creds"},
								Key:                  "DISCORD_BOT_TOKEN",
							},
						}},
						{Name: "DISCORD_ALLOWED_USERS", Value: "12345"},
					},
				},
			},
			Env: []corev1.EnvVar{
				{Name: "HERMES_MAX_ITERATIONS", Value: "90"},
				{Name: "TERMINAL_ENV", Value: "local"},
			},
		},
	}

	got := renderEnv(agent)

	// Expected composition order:
	//   1. Per-provider env (2)
	//   2. Per-gateway env (2)
	//   3. spec.env (2)
	//   4. Operator-stamped (HERMES_INFERENCE_PROVIDER, 4 field refs, 2 identity = 7)
	// Total: 13 entries.
	if len(got) != 13 {
		t.Fatalf("expected 13 env entries, got %d: %v", len(got), got)
	}

	// First entry must be from llmProviders (per-provider comes first).
	if got[0].Name != "DEEPSEEK_API_KEY" {
		t.Errorf("position 0: want DEEPSEEK_API_KEY, got %q", got[0].Name)
	}

	// Position 2 (after 2 provider entries) must be the gateway's first entry.
	if got[2].Name != "DISCORD_BOT_TOKEN" {
		t.Errorf("position 2: want DISCORD_BOT_TOKEN, got %q", got[2].Name)
	}

	// Position 4 (after 2 provider + 2 gateway) must be spec.env's first entry.
	if got[4].Name != "HERMES_MAX_ITERATIONS" {
		t.Errorf("position 4: want HERMES_MAX_ITERATIONS, got %q", got[4].Name)
	}

	// Operator-stamped entries follow. Find HERMES_INFERENCE_PROVIDER and check value.
	idx := indexByName(got, "HERMES_INFERENCE_PROVIDER")
	if idx == -1 {
		t.Fatalf("missing HERMES_INFERENCE_PROVIDER")
	}
	if got[idx].Value != "deepseek" {
		t.Errorf("HERMES_INFERENCE_PROVIDER: want deepseek, got %q", got[idx].Value)
	}
	// Must come AFTER spec.env entries (operator-stamped wins on conflict).
	if idx < 6 {
		t.Errorf("HERMES_INFERENCE_PROVIDER at idx %d; expected >= 6 (after all user-supplied)", idx)
	}

	// Field ref check: HERMES_POD_NAME should be a downward-API ref.
	idx = indexByName(got, "HERMES_POD_NAME")
	if idx == -1 || got[idx].ValueFrom == nil || got[idx].ValueFrom.FieldRef == nil {
		t.Fatalf("HERMES_POD_NAME must be a fieldRef, got %+v", got[idx])
	}
	if got[idx].ValueFrom.FieldRef.FieldPath != "metadata.name" {
		t.Errorf("HERMES_POD_NAME fieldPath: want metadata.name, got %q", got[idx].ValueFrom.FieldRef.FieldPath)
	}

	// Field ref check: HERMES_POD_UID should be a downward-API ref to metadata.uid.
	// Required for ownerReference stamping on session pods (Plan A k8s exec backend) —
	// see docs/research/2026-05-25-cr-operator-stamp-hermes-pod-uid.md.
	idx = indexByName(got, "HERMES_POD_UID")
	if idx == -1 || got[idx].ValueFrom == nil || got[idx].ValueFrom.FieldRef == nil {
		t.Fatalf("HERMES_POD_UID must be a fieldRef, got %+v", got[idx])
	}
	if got[idx].ValueFrom.FieldRef.FieldPath != "metadata.uid" {
		t.Errorf("HERMES_POD_UID fieldPath: want metadata.uid, got %q", got[idx].ValueFrom.FieldRef.FieldPath)
	}

	// Identity vars: HERMES_PROFILE must equal the CR's name.
	idx = indexByName(got, "HERMES_PROFILE")
	if idx == -1 || got[idx].Value != "agent1" {
		t.Errorf("HERMES_PROFILE: want value=agent1, got %+v", got[idx])
	}
}

func TestRenderEnv_NoDefaultProvider_SkipsHermesInferenceProvider(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "hermes"},
		Spec:       hermesv1alpha1.HermesAgentSpec{},
	}
	got := renderEnv(agent)
	if indexByName(got, "HERMES_INFERENCE_PROVIDER") != -1 {
		t.Errorf("HERMES_INFERENCE_PROVIDER must not be set when LLMDefaultProvider is empty")
	}
}

func TestRenderEnv_KubernetesExecBackend_StampsTerminalVars(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "hermes"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			ExecBackend: "kubernetes",
		},
	}
	got := renderEnv(agent)

	// TERMINAL_ENV must be "kubernetes" so the agent's terminal_tool.py
	// selects the kubernetes backend instead of the default local one.
	idx := indexByName(got, "TERMINAL_ENV")
	if idx < 0 {
		t.Fatalf("TERMINAL_ENV missing when spec.execBackend=kubernetes")
	}
	if got[idx].Value != "kubernetes" {
		t.Errorf("TERMINAL_ENV: want kubernetes, got %q", got[idx].Value)
	}

	// TERMINAL_KUBERNETES_POD_SA must point at the session SA name the
	// operator's exec_rbac.go provisions (execSessionSAName).
	idx = indexByName(got, "TERMINAL_KUBERNETES_POD_SA")
	if idx < 0 {
		t.Fatalf("TERMINAL_KUBERNETES_POD_SA missing when spec.execBackend=kubernetes")
	}
	if want := execSessionSAName(agent); got[idx].Value != want {
		t.Errorf("TERMINAL_KUBERNETES_POD_SA: want %q, got %q", want, got[idx].Value)
	}

	// TERMINAL_KUBERNETES_NAMESPACE uses Downward API metadata.namespace
	// rather than a hardcoded value (see env.go rationale).
	idx = indexByName(got, "TERMINAL_KUBERNETES_NAMESPACE")
	if idx < 0 {
		t.Fatalf("TERMINAL_KUBERNETES_NAMESPACE missing when spec.execBackend=kubernetes")
	}
	if got[idx].ValueFrom == nil || got[idx].ValueFrom.FieldRef == nil ||
		got[idx].ValueFrom.FieldRef.FieldPath != "metadata.namespace" {
		t.Errorf("TERMINAL_KUBERNETES_NAMESPACE must be a Downward API ref to metadata.namespace, got %+v",
			got[idx])
	}
}

func TestRenderEnv_LocalExecBackend_OmitsTerminalVars(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "beta", Namespace: "hermes"},
		Spec:       hermesv1alpha1.HermesAgentSpec{}, // ExecBackend "" == local default
	}
	got := renderEnv(agent)

	for _, name := range []string{
		"TERMINAL_ENV",
		"TERMINAL_KUBERNETES_POD_SA",
		"TERMINAL_KUBERNETES_NAMESPACE",
	} {
		if indexByName(got, name) != -1 {
			t.Errorf("%s must not be stamped when spec.execBackend is empty/local", name)
		}
	}
}

// indexByName returns the position of the first env var with the given name, or -1.
func indexByName(env []corev1.EnvVar, name string) int {
	for i, e := range env {
		if e.Name == name {
			return i
		}
	}
	return -1
}
