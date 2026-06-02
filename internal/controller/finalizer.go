// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
	"github.com/UndermountainCC/hermes-operator/internal/metrics"
)

const finalizerName = "hermes.k8s.undermountain.cc/finalizer"

// ensureFinalizer adds the operator's finalizer to the agent CR if missing,
// so we get a chance to clean up cross-namespace bindings on delete.
func (r *HermesAgentReconciler) ensureFinalizer(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.EnsureFinalizer", agent)
	defer func() { endSpan(span, err) }()

	if controllerutil.ContainsFinalizer(agent, finalizerName) {
		return nil
	}
	controllerutil.AddFinalizer(agent, finalizerName)
	return r.Update(ctx, agent)
}

// handleDeletion runs when the CR is being deleted. Cleans up RoleBindings +
// ClusterRoleBindings that K8s GC can't reach (cross-ns, cluster-scoped),
// then removes our finalizer to let K8s actually delete the CR.
func (r *HermesAgentReconciler) handleDeletion(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.HandleDeletion", agent)
	defer func() { endSpan(span, err) }()

	if !controllerutil.ContainsFinalizer(agent, finalizerName) {
		return nil
	}

	rbs, crbs, err := r.listOwnedBindings(ctx, agent)
	if err != nil {
		return fmt.Errorf("list owned bindings during deletion: %w", err)
	}
	for i := range rbs {
		if err := r.Delete(ctx, &rbs[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete RoleBinding %s/%s during cleanup: %w", rbs[i].Namespace, rbs[i].Name, err)
		}
	}
	for i := range crbs {
		if err := r.Delete(ctx, &crbs[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete ClusterRoleBinding %s during cleanup: %w", crbs[i].Name, err)
		}
	}

	// Scrub per-agent Prometheus metric rows before clearing the finalizer.
	// Without this, the operator's /metrics endpoint would keep exporting
	// stale gauges for deleted CRs forever, breaking dashboards and
	// confusing alerting.
	metrics.DeleteAgentMetrics(agent.Name, agent.Namespace)

	controllerutil.RemoveFinalizer(agent, finalizerName)
	return r.Update(ctx, agent)
}
