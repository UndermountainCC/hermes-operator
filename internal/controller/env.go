// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	corev1 "k8s.io/api/core/v1"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// renderEnv composes the container env []corev1.EnvVar in the order:
//
//  1. Per-provider env (from spec.llmProviders[].env)
//  2. Per-gateway env (from spec.gateways[].env)
//  3. Top-level spec.env (user extras)
//  4. Operator-stamped env (HERMES_INFERENCE_PROVIDER, field refs, identity)
//
// K8s rule: when multiple env entries share a name, the LAST wins. Composition
// order therefore expresses precedence — operator-stamped vars override user
// values, user spec.env overrides per-provider/per-gateway env.
func renderEnv(agent *hermesv1alpha1.HermesAgent) []corev1.EnvVar {
	var out []corev1.EnvVar

	for _, p := range agent.Spec.LLMProviders {
		out = append(out, p.Env...)
	}
	for _, g := range agent.Spec.Gateways {
		out = append(out, g.Env...)
	}
	out = append(out, agent.Spec.Env...)

	if agent.Spec.LLMDefaultProvider != "" {
		out = append(out, corev1.EnvVar{
			Name:  "HERMES_INFERENCE_PROVIDER",
			Value: agent.Spec.LLMDefaultProvider,
		})
	}

	out = append(out,
		corev1.EnvVar{Name: "HERMES_NAMESPACE", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
		}},
		corev1.EnvVar{Name: "HERMES_POD_NAME", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
		}},
		corev1.EnvVar{Name: "HERMES_POD_UID", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"},
		}},
		corev1.EnvVar{Name: "HERMES_NODE_NAME", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
		}},
		corev1.EnvVar{Name: "HERMES_PROFILE", Value: agent.Name},
		corev1.EnvVar{Name: "HERMES_DEPLOYMENT", Value: deploymentName(agent)},
	)

	// When the agent is configured for the Kubernetes exec backend, stamp the
	// three env vars the agent's terminal_tool.py reads to select the backend
	// and find the operator-provisioned session SA / namespace. Without these,
	// the operator's RBAC reconciliation in exec_rbac.go is dormant — the agent
	// silently falls back to TERMINAL_ENV=local.
	//
	// TERMINAL_KUBERNETES_NAMESPACE uses Downward API rather than a hardcoded
	// value so the env survives namespace renames (CR re-creation) without a
	// stale value lingering across pod restarts.
	if agent.Spec.ExecBackend == "kubernetes" {
		out = append(out,
			corev1.EnvVar{Name: "TERMINAL_ENV", Value: "kubernetes"},
			corev1.EnvVar{Name: "TERMINAL_KUBERNETES_POD_SA", Value: execSessionSAName(agent)},
			corev1.EnvVar{Name: "TERMINAL_KUBERNETES_NAMESPACE", ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
			}},
		)
	}

	return out
}

// deploymentName returns the standard Deployment name for an agent CR.
// Defined here (not in deployment.go) so env.go has no inter-file dep
// when running its unit test in isolation. The deployment.go file uses
// the same helper.
func deploymentName(agent *hermesv1alpha1.HermesAgent) string {
	return "hermes-" + agent.Name
}
