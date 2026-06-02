// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// selfRoleName is the per-agent introspection Role name. One Role per agent;
// rules are pinned to the agent's own resourceNames so even a token leak
// can't be used to touch sibling agents in the same namespace.
func selfRoleName(agent *hermesv1alpha1.HermesAgent) string {
	return fmt.Sprintf("hermes-%s-self", agent.Name)
}

// selfRoleBindingName binds the agent's SA to its self Role. Matches the
// Role's name with a -binding suffix so the pair is visually paired in
// `kubectl get role,rolebinding`.
func selfRoleBindingName(agent *hermesv1alpha1.HermesAgent) string {
	return fmt.Sprintf("hermes-%s-self", agent.Name)
}

// desiredSelfRole produces the per-agent self-introspection Role.
//
// Scope rationale (resourceNames-pinned, NOT namespace-wide):
//
//   - apps/deployments[hermes-<name>]: get to support `kubectl get deploy
//     hermes-<name>` and `kubectl describe`; patch to support `kubectl
//     rollout restart deployment/hermes-<name>` (rollout-restart is a
//     strategic-merge patch that bumps a timestamp annotation on the
//     Pod template, forcing the Recreate strategy to roll the Pod).
//   - hermes.k8s.undermountain.cc/hermesagents[<name>]: get so the agent
//     can read its own spec + status for self-introspection.
//
// Pods access is deliberately omitted: the canonical "restart yourself"
// path is the gateway's /restart slash command (exit 75 -> the container
// is restarted by the Pod's restartPolicy; PVC is preserved, in-pod state
// is fresh). Pod-level replacement (e.g. to remount the PVC after a
// host-side mutation) goes through `kubectl rollout restart deployment`
// — patching the Deployment, not the Pod. Granting pods:delete would
// either be namespace-wide (exposes siblings) or require yet another
// resourceNames pin on a Pod whose name churns on every rollout. Neither
// is worth the blast-radius increase.
//
// Verbs are minimal: get + patch on the Deployment; get on the CR. No
// list, watch, delete, create, update — anything broader would either
// require dropping resourceNames (defeats the self-scoping invariant)
// or grant capabilities the agent doesn't need.
func desiredSelfRole(agent *hermesv1alpha1.HermesAgent) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      selfRoleName(agent),
			Namespace: agent.Namespace,
			Labels:    agentLabels(agent),
		},
		Rules: []rbacv1.PolicyRule{
			{
				// Self-Deployment: get for read; patch for `kubectl rollout
				// restart deployment/<name>`, which is a strategic-merge
				// patch that updates an annotation on .spec.template.
				APIGroups:     []string{"apps"},
				Resources:     []string{"deployments"},
				ResourceNames: []string{deploymentName(agent)},
				Verbs:         []string{"get", "patch"},
			},
			{
				// Self-CR: get for self-introspection (spec, status,
				// effective labels). No list — list cannot be
				// resourceNames-scoped, so granting it would expose
				// sibling agents in the namespace.
				APIGroups:     []string{"hermes.k8s.undermountain.cc"},
				Resources:     []string{"hermesagents"},
				ResourceNames: []string{agent.Name},
				Verbs:         []string{"get"},
			},
		},
	}
}

// desiredSelfRoleBinding ties the agent's SA to its self Role.
//
// Same-namespace RoleBinding, so an ownerRef on the CR is legal — that's
// why applyObject (which sets a controller ownerRef) is the right path
// here, NOT applyBindingSSA (which is the cross-ns/cluster-scoped escape
// hatch used by the user-spec'd bindings reconciler).
func desiredSelfRoleBinding(agent *hermesv1alpha1.HermesAgent) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      selfRoleBindingName(agent),
			Namespace: agent.Namespace,
			Labels:    agentLabels(agent),
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      serviceAccountName(agent),
			Namespace: agent.Namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     selfRoleName(agent),
		},
	}
}

// reconcileSelfRBAC creates (or updates) the per-agent self-introspection
// Role + RoleBinding. Called from Reconcile AFTER reconcileServiceAccount
// (the SA must exist before the binding references it) and ordered with
// reconcileDeployment such that the Role is already in place by the time
// the agent process inside the Pod might try to use it.
//
// Architecture note: this is a deliberate bend on the CLAUDE.md "RBAC
// reference-only" invariant. The general operator pattern is "users name
// existing Roles, we create bindings, we never create Roles." Here the
// operator creates ONE narrow Role per agent — the rules MUST pin
// resourceNames to the agent's own Deployment + CR, and a cluster-wide
// Role can't express that ("Deployment with name == $owner's name"). The
// alternative would be a namespace-wide deployments:patch ClusterRole,
// which lets any agent rollout-restart sibling agents. That's much worse.
//
// The bend is narrow: we create ONE Role per agent, with hardcoded rules
// (no user input shapes the rules), pinned to the agent's own names. The
// existing user-spec'd spec.rbac.roleBindings[] flow is unchanged and
// remains reference-only.
func (r *HermesAgentReconciler) reconcileSelfRBAC(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.SelfRBAC", agent)
	defer func() { endSpan(span, err) }()

	// BYO-SA opt-out. When spec.serviceAccountName is user-provided, the
	// operator does NOT layer additional grants onto that identity. Two
	// reasons:
	//
	//   1. Contract: the user is signaling "I manage permissions for this
	//      SA." Silently expanding what it can do violates their contract.
	//   2. Cross-agent leak: if a single SA is shared across multiple
	//      HermesAgent CRs, creating per-agent self-Roles all pointing at
	//      the same Subject means each agent's SA can rollout-restart every
	//      sibling. The user-manages-SA flow is the only place where shared
	//      SAs can happen.
	//
	// Cleanup: if the user toggled INTO BYO mode (was operator-managed,
	// now external), garbage-collect the previously-created Role +
	// RoleBinding. ownerRef GC handles the CR-delete case; this handles
	// the in-place toggle.
	if agent.Spec.ServiceAccountName != "" {
		return r.deleteSelfRBACIfExists(ctx, agent)
	}

	if err := r.applyObject(ctx, agent, desiredSelfRole(agent)); err != nil {
		return fmt.Errorf("reconcile self Role: %w", err)
	}
	if err := r.applyObject(ctx, agent, desiredSelfRoleBinding(agent)); err != nil {
		return fmt.Errorf("reconcile self RoleBinding: %w", err)
	}
	return nil
}

// deleteSelfRBACIfExists garbage-collects the operator-created self Role +
// RoleBinding when the user toggles into BYO-SA mode. Best-effort; NotFound
// is benign (nothing to clean up).
func (r *HermesAgentReconciler) deleteSelfRBACIfExists(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) error {
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{
		Name:      selfRoleName(agent),
		Namespace: agent.Namespace,
	}}
	if err := r.Delete(ctx, role); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete obsolete self Role: %w", err)
	}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{
		Name:      selfRoleBindingName(agent),
		Namespace: agent.Namespace,
	}}
	if err := r.Delete(ctx, binding); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete obsolete self RoleBinding: %w", err)
	}
	return nil
}
