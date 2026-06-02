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
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func TestDesiredNetworkPolicy_DisabledReturnsNil(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
	}
	if np := desiredNetworkPolicy(agent); np != nil {
		t.Errorf("expected nil when not enabled, got %+v", np)
	}
}

func TestDesiredNetworkPolicy_PassesThroughRules(t *testing.T) {
	port := intstr.FromInt(9119)
	tcp := networkingv1.NetworkPolicyPort{
		Protocol: ptr.To(corev1.ProtocolTCP),
		Port:     &port,
	}
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			NetworkPolicy: hermesv1alpha1.HermesAgentNetworkPolicy{
				Enabled: true,
				PolicyTypes: []networkingv1.PolicyType{
					networkingv1.PolicyTypeIngress,
					networkingv1.PolicyTypeEgress,
				},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					Ports: []networkingv1.NetworkPolicyPort{tcp},
				}},
				Egress: []networkingv1.NetworkPolicyEgressRule{{
					Ports: []networkingv1.NetworkPolicyPort{tcp},
				}},
			},
		},
	}
	np := desiredNetworkPolicy(agent)
	if np == nil {
		t.Fatalf("expected NetworkPolicy, got nil")
	}
	if np.Name != "hermes-x" {
		t.Errorf("name: want hermes-x, got %q", np.Name)
	}
	if np.Namespace != "ns" {
		t.Errorf("namespace: want ns, got %q", np.Namespace)
	}
	if len(np.Spec.PolicyTypes) != 2 {
		t.Errorf("policy types: want 2, got %d", len(np.Spec.PolicyTypes))
	}
	if len(np.Spec.Ingress) != 1 || len(np.Spec.Ingress[0].Ports) != 1 {
		t.Errorf("ingress passthrough broken: %+v", np.Spec.Ingress)
	}
	if len(np.Spec.Egress) != 1 || len(np.Spec.Egress[0].Ports) != 1 {
		t.Errorf("egress passthrough broken: %+v", np.Spec.Egress)
	}
	// PodSelector must match the labels every other agent-owned child uses.
	if np.Spec.PodSelector.MatchLabels["hermes.undermountain.cc/agent"] != "x" {
		t.Errorf("pod selector: want agent=x, got %v", np.Spec.PodSelector.MatchLabels)
	}
	if np.Spec.PodSelector.MatchLabels["hermes.undermountain.cc/agent-ns"] != "ns" {
		t.Errorf("pod selector: want agent-ns=ns, got %v", np.Spec.PodSelector.MatchLabels)
	}
}

func TestDesiredNetworkPolicy_EmptyRulesStillRenders(t *testing.T) {
	// enabled=true with no rules → a NetworkPolicy with empty ingress/egress
	// slices. K8s interprets PolicyTypes=[Ingress] + empty ingress as
	// "deny all ingress" — the webhook warns about this, but the operator
	// still renders the resource if the user opts in.
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "deny-all", Namespace: "ns"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			NetworkPolicy: hermesv1alpha1.HermesAgentNetworkPolicy{
				Enabled:     true,
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			},
		},
	}
	np := desiredNetworkPolicy(agent)
	if np == nil {
		t.Fatalf("expected NetworkPolicy, got nil")
	}
	if len(np.Spec.Ingress) != 0 {
		t.Errorf("expected empty ingress slice, got %+v", np.Spec.Ingress)
	}
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
		t.Errorf("expected PolicyTypes=[Ingress], got %v", np.Spec.PolicyTypes)
	}
}
