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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// dashboardServiceName is the canonical Service name fronting the dashboard
// sidecar. Used by the dashboard /api/status poll URL (status.go) and the
// dashboard Ingress backend (ingress.go).
func dashboardServiceName(agent *hermesv1alpha1.HermesAgent) string {
	return fmt.Sprintf("hermes-%s-dashboard", agent.Name)
}

// reconcileDashboardService reconciles the Service fronting the dashboard
// sidecar. Separate from the gateway-fronting Service so the gateway port
// (8080) stays scoped and the dashboard port (9119) has its own selector
// surface for Ingress/etc.
//
// When the toggle flips off (dashboard.enabled=false) we explicitly delete
// any previously-rendered Service. ownerRef GC would only catch the
// CR-deleted case; an in-place toggle would otherwise leave the Service
// dangling, advertising a port that nothing answers anymore. Mirrors the
// disable path in reconcileDashboardIngress. Bug C from real-cluster smoke
// 2026-05-15.
func (r *HermesAgentReconciler) reconcileDashboardService(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.DashboardService", agent)
	defer func() { endSpan(span, err) }()

	if !agent.Spec.Dashboard.Enabled {
		var existing corev1.Service
		nn := types.NamespacedName{Name: dashboardServiceName(agent), Namespace: agent.Namespace}
		if err := r.Get(ctx, nn, &existing); err == nil {
			if err := r.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete obsolete dashboard Service: %w", err)
			}
		}
		return nil
	}

	svcType := agent.Spec.Dashboard.Service.Type
	if svcType == "" {
		svcType = corev1.ServiceTypeClusterIP
	}

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        dashboardServiceName(agent),
			Namespace:   agent.Namespace,
			Labels:      agentLabels(agent),
			Annotations: agent.Spec.Dashboard.Service.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Type: svcType,
			Selector: map[string]string{
				"app":                              "hermes",
				"hermes.undermountain.cc/agent":    agent.Name,
				"hermes.undermountain.cc/agent-ns": agent.Namespace,
			},
			// PublishNotReadyAddresses=true: the dashboard's whole purpose is
			// observability — including DURING gateway outages. With the
			// default (false), kube-proxy excludes the pod from the
			// dashboard Service's endpoints whenever the pod's readiness
			// goes False (e.g., gateway probe failing because the
			// gateway crashed). That makes /api/status unreachable from
			// the operator's polling loop, status.gateways[] stops
			// updating, and the user has no visible signal about why the
			// agent is sick. Bug B from real-cluster smoke 2026-05-15.
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{{
				Name:       "dashboard",
				Port:       9119,
				TargetPort: intstr.FromInt(9119),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	return r.applyObject(ctx, agent, desired)
}
