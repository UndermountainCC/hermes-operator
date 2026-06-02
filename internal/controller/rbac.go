// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// rbacSourceLabel marks (Cluster)RoleBindings reconciled from spec.rbac so
// drift correction's List query in reconcileRBAC can target THEM exclusively.
// Without this marker, operator-internal bindings (hermes-<name>-self for
// self-introspection RBAC, hermes-<name>-exec for the kubernetes exec
// backend) — which also carry agentLabels() — would be matched by the drift
// query and deleted every reconcile, creating a create/delete hot loop.
const rbacSourceLabel = "hermes.undermountain.cc/rbac-source"
const rbacSourceSpec = "spec.rbac"

// agentRBACSpecLabels returns the labels stamped on spec.rbac-managed
// bindings: agentLabels() PLUS the rbacSourceLabel marker. Used by both
// desiredRoleBinding/desiredClusterRoleBinding and by reconcileRBAC's drift
// correction list selector — keeping them in lockstep prevents the two from
// drifting apart silently.
func agentRBACSpecLabels(agent *hermesv1alpha1.HermesAgent) map[string]string {
	out := agentLabels(agent)
	out[rbacSourceLabel] = rbacSourceSpec
	return out
}

// desiredRoleBinding produces the RoleBinding object for one entry in
// spec.rbac.roleBindings. idx is the entry's index, used for unique naming.
func desiredRoleBinding(
	agent *hermesv1alpha1.HermesAgent,
	rb hermesv1alpha1.HermesAgentRoleBinding,
	idx int,
) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("hermes-%s-%d", agent.Name, idx),
			Namespace: rb.Namespace,
			Labels:    agentRBACSpecLabels(agent),
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      serviceAccountName(agent),
			Namespace: agent.Namespace,
		}},
		RoleRef: rb.RoleRef,
	}
}

// desiredClusterRoleBinding produces the ClusterRoleBinding for one entry.
func desiredClusterRoleBinding(
	agent *hermesv1alpha1.HermesAgent,
	crb hermesv1alpha1.HermesAgentClusterRoleBinding,
	idx int,
) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("hermes-%s-cluster-%d", agent.Name, idx),
			Labels: agentRBACSpecLabels(agent),
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      serviceAccountName(agent),
			Namespace: agent.Namespace,
		}},
		RoleRef: crb.RoleRef,
	}
}

// applyBindingSSA applies a binding object via Server-Side Apply without
// setting an ownerRef. Used for RoleBindings (cross-namespace) and
// ClusterRoleBindings (cluster-scoped) where ownerRef would be rejected
// by the API server (cross-namespace / cluster-scoped object owned by a
// namespace-scoped CR). Cleanup is handled by the finalizer instead.
func (r *HermesAgentReconciler) applyBindingSSA(ctx context.Context, obj client.Object) error {
	gvk, err := apiutil.GVKForObject(obj, r.Scheme)
	if err != nil {
		return fmt.Errorf("resolve GVK: %w", err)
	}
	obj.GetObjectKind().SetGroupVersionKind(gvk)

	if err := r.Patch(ctx, obj, client.Apply,
		client.ForceOwnership,
		client.FieldOwner(fieldOwner),
	); err != nil {
		return fmt.Errorf("server-side apply binding: %w", err)
	}
	return nil
}

// reconcileRBAC ensures the RoleBindings and ClusterRoleBindings declared
// in spec.rbac exist. Bindings labeled with this agent but no longer in
// spec are deleted (drift correction). Returns an error if any cluster-
// scoped binding references a ClusterRole not in the operator's allowlist.
func (r *HermesAgentReconciler) reconcileRBAC(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.RBAC", agent)
	defer func() { endSpan(span, err) }()

	// Validate allowlist FIRST. If invalid, refuse to create any binding —
	// don't partially apply.
	for _, crb := range agent.Spec.RBAC.ClusterRoleBindings {
		if !r.Config.IsClusterRoleAllowed(crb.RoleRef.Name) {
			// Emit a span event so the trace pinpoints which role tripped the
			// allowlist guard — the reconcile-level "RBAC reconcile failed"
			// span status alone wouldn't tell you which entry caused it.
			span.AddEvent("Reconcile.RBACRejected", trace.WithAttributes(
				attribute.String("roleName", crb.RoleRef.Name),
			))
			return fmt.Errorf("ClusterRole %q not in operator allowedClusterRoles allowlist", crb.RoleRef.Name)
		}
	}

	desiredRBNames := map[string]struct{}{}
	for idx, rb := range agent.Spec.RBAC.RoleBindings {
		desired := desiredRoleBinding(agent, rb, idx)
		desiredRBNames[desired.Namespace+"/"+desired.Name] = struct{}{}
		if err := r.applyBindingSSA(ctx, desired); err != nil {
			return fmt.Errorf("reconcile RoleBinding %s/%s: %w", desired.Namespace, desired.Name, err)
		}
	}

	// Drift: delete labeled RBs that the spec no longer declares.
	// MUST match on rbacSourceLabel=spec.rbac so we only target spec.rbac-
	// managed bindings — NOT operator-internal ones (hermes-<name>-self,
	// hermes-<name>-exec) which also carry agentLabels() but are reconciled
	// by separate sub-controllers and would otherwise be hot-loop deleted.
	var allRBs rbacv1.RoleBindingList
	if err := r.List(ctx, &allRBs, client.MatchingLabels{
		"hermes.undermountain.cc/agent":    agent.Name,
		"hermes.undermountain.cc/agent-ns": agent.Namespace,
		rbacSourceLabel:                    rbacSourceSpec,
	}); err != nil {
		return fmt.Errorf("list RoleBindings: %w", err)
	}
	for i := range allRBs.Items {
		rb := &allRBs.Items[i]
		key := rb.Namespace + "/" + rb.Name
		if _, want := desiredRBNames[key]; !want {
			if err := r.Delete(ctx, rb); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete stale RoleBinding %s: %w", key, err)
			}
		}
	}

	// Same dance for ClusterRoleBindings.
	desiredCRBNames := map[string]struct{}{}
	for idx, crb := range agent.Spec.RBAC.ClusterRoleBindings {
		desired := desiredClusterRoleBinding(agent, crb, idx)
		desiredCRBNames[desired.Name] = struct{}{}
		if err := r.applyBindingSSA(ctx, desired); err != nil {
			return fmt.Errorf("reconcile ClusterRoleBinding %s: %w", desired.Name, err)
		}
	}

	var allCRBs rbacv1.ClusterRoleBindingList
	if err := r.List(ctx, &allCRBs, client.MatchingLabels{
		"hermes.undermountain.cc/agent":    agent.Name,
		"hermes.undermountain.cc/agent-ns": agent.Namespace,
		rbacSourceLabel:                    rbacSourceSpec,
	}); err != nil {
		return fmt.Errorf("list ClusterRoleBindings: %w", err)
	}
	for i := range allCRBs.Items {
		crb := &allCRBs.Items[i]
		if _, want := desiredCRBNames[crb.Name]; !want {
			if err := r.Delete(ctx, crb); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete stale ClusterRoleBinding %s: %w", crb.Name, err)
			}
		}
	}

	return nil
}

// listOwnedBindings returns all RoleBindings + ClusterRoleBindings labeled
// for this agent (used by finalizer cleanup in Task 18).
func (r *HermesAgentReconciler) listOwnedBindings(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (rbs []rbacv1.RoleBinding, crbs []rbacv1.ClusterRoleBinding, err error) {
	sel := client.MatchingLabels{
		"hermes.undermountain.cc/agent":    agent.Name,
		"hermes.undermountain.cc/agent-ns": agent.Namespace,
	}
	var rbList rbacv1.RoleBindingList
	if err := r.List(ctx, &rbList, sel); err != nil {
		return nil, nil, err
	}
	var crbList rbacv1.ClusterRoleBindingList
	if err := r.List(ctx, &crbList, sel); err != nil {
		return nil, nil, err
	}
	return rbList.Items, crbList.Items, nil
}
