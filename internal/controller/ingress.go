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
	"k8s.io/utils/ptr"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func dashboardIngressName(agent *hermesv1alpha1.HermesAgent) string {
	return fmt.Sprintf("hermes-%s-dashboard", agent.Name)
}

// reconcileDashboardIngress reconciles the Ingress fronting the dashboard
// sidecar. Disabled by default; opt in via spec.dashboard.ingress.enabled.
//
// Annotations are passed through verbatim — auth is the user's responsibility
// (the webhook emits a Warning when ingress is enabled without annotations).
// TLS is optional via spec.dashboard.ingress.tls.secretName (typically paired
// with cert-manager.io annotations on the Ingress for automatic TLS
// provisioning).
//
// When the toggle flips off (dashboard.enabled=false OR ingress.enabled=false)
// we explicitly delete any previously-rendered Ingress. ownerRef GC would
// only catch the CR-deleted case; an in-place toggle would otherwise leave
// the Ingress dangling.
func (r *HermesAgentReconciler) reconcileDashboardIngress(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.DashboardIngress", agent)
	defer func() { endSpan(span, err) }()

	if !agent.Spec.Dashboard.Enabled || !agent.Spec.Dashboard.Ingress.Enabled {
		var existing networkingv1.Ingress
		nn := types.NamespacedName{Name: dashboardIngressName(agent), Namespace: agent.Namespace}
		if err := r.Get(ctx, nn, &existing); err == nil {
			if err := r.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete obsolete Ingress: %w", err)
			}
		}
		return nil
	}

	pathType := networkingv1.PathTypePrefix
	ingressClass := agent.Spec.Dashboard.Ingress.IngressClassName

	desired := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        dashboardIngressName(agent),
			Namespace:   agent.Namespace,
			Labels:      agentLabels(agent),
			Annotations: agent.Spec.Dashboard.Ingress.Annotations,
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: ptr.To(ingressClass),
			Rules: []networkingv1.IngressRule{{
				Host: agent.Spec.Dashboard.Ingress.Host,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     "/",
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: dashboardServiceName(agent),
									Port: networkingv1.ServiceBackendPort{Number: 9119},
								},
							},
						}},
					},
				},
			}},
		},
	}
	if agent.Spec.Dashboard.Ingress.TLS != nil && agent.Spec.Dashboard.Ingress.TLS.SecretName != "" {
		desired.Spec.TLS = []networkingv1.IngressTLS{{
			Hosts:      []string{agent.Spec.Dashboard.Ingress.Host},
			SecretName: agent.Spec.Dashboard.Ingress.TLS.SecretName,
		}}
	}
	return r.applyObject(ctx, agent, desired)
}
