// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func execRoleName(agent *hermesv1alpha1.HermesAgent) string {
	return fmt.Sprintf("hermes-%s-exec", agent.Name)
}

func execSessionSAName(agent *hermesv1alpha1.HermesAgent) string {
	return fmt.Sprintf("hermes-%s-session", agent.Name)
}

// desiredExecSessionSA is the identity the agent's session pods run as. It is
// deliberately powerless: automountServiceAccountToken=false and no binding
// references it, so a session pod carries no cluster credentials.
func desiredExecSessionSA(agent *hermesv1alpha1.HermesAgent) *corev1.ServiceAccount {
	no := false
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      execSessionSAName(agent),
			Namespace: agent.Namespace,
			Labels:    agentLabels(agent),
		},
		AutomountServiceAccountToken: &no,
	}
}

// desiredExecRole grants the agent SA exactly the verbs the kubernetes exec
// backend needs to create/exec/delete session pods + their PVCs in its own
// namespace. NOT resourceNames-pinned: session pod/PVC names are chosen by
// the agent at runtime (hermes-ws-<task_id>), so a name pin is impossible.
// The shape of pods this enables is constrained separately by the
// ValidatingAdmissionPolicy (Task 5).
func desiredExecRole(agent *hermesv1alpha1.HermesAgent) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      execRoleName(agent),
			Namespace: agent.Namespace,
			Labels:    agentLabels(agent),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "pods/log", "persistentvolumeclaims"},
				Verbs:     []string{"create", "get", "list", "watch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods/exec"},
				Verbs:     []string{"create", "get"},
			},
		},
	}
}

// desiredExecRoleBinding ties the agent's SA to the exec Role. Same-namespace,
// so an ownerRef on the CR is legal — applyObject (controller ownerRef) is the
// right path, NOT applyBindingSSA. Mirrors self_rbac.go.
func desiredExecRoleBinding(agent *hermesv1alpha1.HermesAgent) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      execRoleName(agent),
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
			Name:     execRoleName(agent),
		},
	}
}

// reconcileExecBackendRBAC provisions the scoped Role + RoleBinding + no-perms
// session SA when spec.execBackend == "kubernetes". When the backend is not
// kubernetes (default "local" or empty), it garbage-collects any previously
// created objects (in-place toggle; ownerRef GC covers CR-delete).
//
// Architecture note: same deliberate, narrow bend on "RBAC reference-only" as
// reconcileSelfRBAC — the operator creates ONE Role per agent with hardcoded
// rules (no user input shapes them). Unlike self-RBAC the rules are NOT
// resourceNames-pinned (session pod names churn per task); the pod *shape* is
// constrained by the ValidatingAdmissionPolicy instead.
//
// BYO-SA opt-out (same rationale as reconcileSelfRBAC, Phase 10.6 / ee710c0):
// when spec.serviceAccountName is user-provided, the operator does NOT layer
// additional grants. BYO SAs may be shared across multiple HermesAgent CRs,
// and silently expanding what such a shared SA can do would (a) violate the
// "I manage permissions for this SA" contract, and (b) let any agent in the
// group exec into ANY sibling's session pods. Cleanup garbage-collects
// previously-created exec RBAC if the user toggled INTO BYO mode.
func (r *HermesAgentReconciler) reconcileExecBackendRBAC(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.ExecBackendRBAC", agent)
	defer func() { endSpan(span, err) }()

	// BYO-SA cleanup: user-managed identity, operator never grants exec.
	// Skip the cleanup deletes if we've never provisioned (cheap signal:
	// absence of the ExecBackendReady condition on status). Otherwise we'd
	// triple-Delete-NotFound on every reconcile of every agent in the
	// cluster — a measurable load multiplier under envtest contention.
	if agent.Spec.ServiceAccountName != "" {
		if !hasExecBackendReadyCondition(agent) {
			return nil
		}
		return r.deleteExecRBACIfExists(ctx, agent)
	}
	if agent.Spec.ExecBackend != "kubernetes" {
		if !hasExecBackendReadyCondition(agent) {
			return nil
		}
		return r.deleteExecRBACIfExists(ctx, agent)
	}

	if err := r.applyObject(ctx, agent, desiredExecSessionSA(agent)); err != nil {
		return fmt.Errorf("reconcile exec session SA: %w", err)
	}
	if err := r.applyObject(ctx, agent, desiredExecRole(agent)); err != nil {
		return fmt.Errorf("reconcile exec Role: %w", err)
	}
	if err := r.applyObject(ctx, agent, desiredExecRoleBinding(agent)); err != nil {
		return fmt.Errorf("reconcile exec RoleBinding: %w", err)
	}
	// Record in-memory that we've provisioned. computeStatus picks this up
	// and persists it; on subsequent reconciles, hasExecBackendReadyCondition
	// short-circuits the no-op cleanup path.
	setExecBackendReadyCondition(agent)
	return nil
}

// hasExecBackendReadyCondition returns true when status.conditions carries an
// ExecBackendReady entry — the signal that the operator has provisioned exec
// RBAC at least once. Used to skip cleanup-Delete API calls on agents that
// have never enabled the kubernetes exec backend (the common case).
func hasExecBackendReadyCondition(agent *hermesv1alpha1.HermesAgent) bool {
	for _, c := range agent.Status.Conditions {
		if c.Type == hermesv1alpha1.ConditionExecBackendReady {
			return true
		}
	}
	return false
}

// setExecBackendReadyCondition records ExecBackendReady=True in memory. The
// caller (reconcileStatus, later in the loop) persists it via merge-patch.
func setExecBackendReadyCondition(agent *hermesv1alpha1.HermesAgent) {
	agent.Status.Conditions = setCondition(agent.Status.Conditions, metav1.Condition{
		Type:    hermesv1alpha1.ConditionExecBackendReady,
		Status:  metav1.ConditionTrue,
		Reason:  "KubernetesExecProvisioned",
		Message: "session-pod Role + RoleBinding + SA reconciled",
	})
}

// clearExecBackendReadyCondition removes the in-memory ExecBackendReady
// condition. Mirrors clearRBACFailureCondition's pattern: status persistence
// happens later in the reconcile loop via reconcileStatus.
func clearExecBackendReadyCondition(agent *hermesv1alpha1.HermesAgent) {
	out := agent.Status.Conditions[:0]
	for _, c := range agent.Status.Conditions {
		if c.Type == hermesv1alpha1.ConditionExecBackendReady {
			continue
		}
		out = append(out, c)
	}
	agent.Status.Conditions = out
}

// deleteExecRBACIfExists garbage-collects the operator-created exec Role +
// RoleBinding + session SA when the user toggles the backend off or into BYO
// mode. Best-effort; NotFound is benign (nothing to clean up). Also clears
// the in-memory ExecBackendReady condition so the next reconcile correctly
// observes "nothing provisioned" via hasExecBackendReadyCondition.
func (r *HermesAgentReconciler) deleteExecRBACIfExists(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) error {
	clearExecBackendReadyCondition(agent)
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{
		Name: execRoleName(agent), Namespace: agent.Namespace,
	}}
	if err := r.Delete(ctx, role); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete obsolete exec Role: %w", err)
	}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{
		Name: execRoleName(agent), Namespace: agent.Namespace,
	}}
	if err := r.Delete(ctx, binding); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete obsolete exec RoleBinding: %w", err)
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: execSessionSAName(agent), Namespace: agent.Namespace,
	}}
	if err := r.Delete(ctx, sa); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete obsolete exec session SA: %w", err)
	}
	return nil
}
