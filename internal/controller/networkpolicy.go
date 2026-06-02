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
	"context"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// networkPolicyName is the canonical name of the per-agent NetworkPolicy.
// Stable: `hermes-<agent.name>`. Used by the reconciler and the toggle-off
// cleanup path.
func networkPolicyName(agent *hermesv1alpha1.HermesAgent) string {
	return fmt.Sprintf("hermes-%s", agent.Name)
}

// desiredNetworkPolicy returns the K8s NetworkPolicy the operator wants for
// this agent, or nil when networking is disabled. All ingress/egress rules
// pass through unchanged from spec — no operator-side defaults injected.
//
// The pod selector mirrors the labels every other agent-owned child uses
// (agentLabels) — Deployment/Pod templates carry the same set so the
// NetworkPolicy targets exactly the agent pod, no more, no less.
func desiredNetworkPolicy(agent *hermesv1alpha1.HermesAgent) *networkingv1.NetworkPolicy {
	if !agent.Spec.NetworkPolicy.Enabled {
		return nil
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      networkPolicyName(agent),
			Namespace: agent.Namespace,
			Labels:    agentLabels(agent),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: agentLabels(agent),
			},
			PolicyTypes: agent.Spec.NetworkPolicy.PolicyTypes,
			Ingress:     agent.Spec.NetworkPolicy.Ingress,
			Egress:      agent.Spec.NetworkPolicy.Egress,
		},
	}
}

// reconcileNetworkPolicy ensures the per-agent NetworkPolicy matches spec.
// When spec.networkPolicy.enabled is false, any previously-rendered
// NetworkPolicy is deleted (mirrors the dashboard service/ingress toggle-off
// pattern — ownerRef GC would only catch the CR-deleted case, not an in-place
// toggle).
//
// Enforcement of the NetworkPolicy resource depends on the cluster's CNI.
// Calico/Cilium enforce; kindnet (kind default) does not. The operator's job
// stops at materializing the resource; the rest is between user and cluster.
func (r *HermesAgentReconciler) reconcileNetworkPolicy(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.NetworkPolicy", agent)
	defer func() { endSpan(span, err) }()

	if !agent.Spec.NetworkPolicy.Enabled {
		var existing networkingv1.NetworkPolicy
		nn := types.NamespacedName{Name: networkPolicyName(agent), Namespace: agent.Namespace}
		if err := r.Get(ctx, nn, &existing); err == nil {
			if err := r.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete obsolete NetworkPolicy: %w", err)
			}
		}
		return nil
	}
	desired := desiredNetworkPolicy(agent)
	return r.applyObject(ctx, agent, desired)
}
