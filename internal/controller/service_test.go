// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func TestDesiredService(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
	}
	svc := desiredService(agent)
	if svc.Name != "hermes-agent1" {
		t.Errorf("name: want hermes-agent1, got %q", svc.Name)
	}
	if svc.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Errorf("type: want ClusterIP, got %v", svc.Spec.Type)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 8080 {
		t.Errorf("ports: want one port 8080, got %v", svc.Spec.Ports)
	}
	if svc.Spec.Selector["hermes.undermountain.cc/agent"] != "agent1" {
		t.Errorf("selector: want agent=agent1, got %v", svc.Spec.Selector)
	}
}
