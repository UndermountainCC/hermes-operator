// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// desiredServiceAccount returns the ServiceAccount the operator wants
// to exist for this agent.
func desiredServiceAccount(agent *hermesv1alpha1.HermesAgent) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountName(agent),
			Namespace: agent.Namespace,
			Labels:    agentLabels(agent),
		},
	}
}

// serviceAccountName returns the effective SA name: explicit if set in
// the spec, otherwise hermes-<name>.
func serviceAccountName(agent *hermesv1alpha1.HermesAgent) string {
	if agent.Spec.ServiceAccountName != "" {
		return agent.Spec.ServiceAccountName
	}
	return "hermes-" + agent.Name
}

// reconcileServiceAccount ensures the agent's ServiceAccount exists.
// If the spec sets ServiceAccountName, the operator does NOT create or
// modify a pre-existing SA — it only verifies the SA exists. (Users that
// reference an externally-managed SA must create it themselves.)
func (r *HermesAgentReconciler) reconcileServiceAccount(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.ServiceAccount", agent)
	defer func() { endSpan(span, err) }()

	if agent.Spec.ServiceAccountName != "" {
		// External-SA mode: just verify it exists.
		key := types.NamespacedName{
			Name:      agent.Spec.ServiceAccountName,
			Namespace: agent.Namespace,
		}
		var sa corev1.ServiceAccount
		if err := r.Get(ctx, key, &sa); err != nil {
			return fmt.Errorf("external ServiceAccount %s not found: %w",
				agent.Spec.ServiceAccountName, err)
		}
		return nil
	}
	return r.applyObject(ctx, agent, desiredServiceAccount(agent))
}
