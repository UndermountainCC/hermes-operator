// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// desiredPVC returns the PersistentVolumeClaim object the operator wants
// to exist for this agent. The spec block is copied verbatim from the
// HermesAgent's storage.persistentVolumeClaim (a native PVC spec).
func desiredPVC(agent *hermesv1alpha1.HermesAgent) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName(agent),
			Namespace: agent.Namespace,
			Labels:    agentLabels(agent),
		},
		Spec: *agent.Spec.Storage.PersistentVolumeClaim.DeepCopy(),
	}
}

// pvcName returns the PVC name the operator mounts for an agent. When
// spec.storage.existingClaimName is set, that name is used verbatim;
// otherwise the operator's generated convention applies.
func pvcName(agent *hermesv1alpha1.HermesAgent) string {
	if agent.Spec.Storage.ExistingClaimName != "" {
		return agent.Spec.Storage.ExistingClaimName
	}
	return "hermes-" + agent.Name + "-data"
}

// agentLabels returns the standard label set the operator stamps on every
// resource it creates for an agent. Used for both selector matching and
// cleanup queries (future phases will rely on these for finalizer logic).
func agentLabels(agent *hermesv1alpha1.HermesAgent) map[string]string {
	return map[string]string{
		"app":                              "hermes",
		"hermes.undermountain.cc/agent":    agent.Name,
		"hermes.undermountain.cc/agent-ns": agent.Namespace,
	}
}

// reconcilePVC ensures the agent's PVC exists. When RetainPolicy=Delete
// (explicit opt-in), an ownerRef is set so K8s GC cascades deletion.
// When RetainPolicy=Retain (default), no ownerRef is set so the PVC
// outlives the CR — preserving agent state by default.
//
// Uses Server-Side Apply (Patch with client.Apply) instead of the
// previous Get-then-Update path. SSA merges field-level: PVC spec fields
// the operator declares stay reconciled; fields managed by other parties
// (PVC.status, volume controllers' field-level edits) don't trigger
// resourceVersion conflicts. The pre-SSA implementation regressed and
// produced "Operation cannot be fulfilled on persistentvolumeclaims"
// errors in production K8s — caught by Phase 3 kind smoke.
func (r *HermesAgentReconciler) reconcilePVC(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.PVC", agent)
	defer func() { endSpan(span, err) }()

	// Adopt-by-name: the operator does not create, reconcile, or own a
	// pre-existing PVC. It is mounted verbatim by the Deployment via
	// pvcName(). Nothing to reconcile here.
	if agent.Spec.Storage.ExistingClaimName != "" {
		return nil
	}

	desired := desiredPVC(agent)

	if agent.Spec.Storage.RetainPolicy == hermesv1alpha1.RetainPolicyDelete {
		if err := ctrl.SetControllerReference(agent, desired, r.Scheme); err != nil {
			return fmt.Errorf("set owner ref on PVC: %w", err)
		}
	}

	gvk, err := apiutil.GVKForObject(desired, r.Scheme)
	if err != nil {
		return fmt.Errorf("resolve GVK: %w", err)
	}
	desired.GetObjectKind().SetGroupVersionKind(gvk)

	if err := r.Patch(ctx, desired, client.Apply,
		client.ForceOwnership,
		client.FieldOwner(fieldOwner),
	); err != nil {
		return fmt.Errorf("server-side apply PVC: %w", err)
	}
	return nil
}
