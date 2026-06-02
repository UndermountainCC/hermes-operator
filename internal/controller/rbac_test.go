// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func TestDesiredRoleBinding(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
	}
	in := hermesv1alpha1.HermesAgentRoleBinding{
		Namespace: "hermes-test",
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "admin",
		},
	}

	rb := desiredRoleBinding(agent, in, 0)

	if rb.Namespace != "hermes-test" {
		t.Errorf("namespace: want hermes-test, got %q", rb.Namespace)
	}
	if rb.Name != "hermes-agent1-0" {
		t.Errorf("name: want hermes-agent1-0, got %q", rb.Name)
	}
	if rb.RoleRef.Name != "admin" {
		t.Errorf("roleRef.name: want admin, got %q", rb.RoleRef.Name)
	}
	if len(rb.Subjects) != 1 {
		t.Fatalf("subjects: want 1, got %d", len(rb.Subjects))
	}
	s := rb.Subjects[0]
	if s.Kind != "ServiceAccount" || s.Name != "hermes-agent1" || s.Namespace != "hermes" {
		t.Errorf("subject: want SA hermes/hermes-agent1, got %+v", s)
	}
	if rb.Labels["hermes.undermountain.cc/agent"] != "agent1" {
		t.Errorf("missing agent label")
	}
	// rbacSourceLabel MUST be set on spec.rbac-managed bindings so the drift
	// correction in reconcileRBAC targets ONLY these (not operator-internal
	// bindings like hermes-<name>-self / hermes-<name>-exec). Without this
	// marker, drift correction creates a hot create/delete loop on every
	// reconcile.
	if rb.Labels[rbacSourceLabel] != rbacSourceSpec {
		t.Errorf("missing %s=%s marker label (required for drift-correction targeting); got %q",
			rbacSourceLabel, rbacSourceSpec, rb.Labels[rbacSourceLabel])
	}
}

func TestDesiredClusterRoleBinding(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
	}
	in := hermesv1alpha1.HermesAgentClusterRoleBinding{
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
	}
	crb := desiredClusterRoleBinding(agent, in, 0)
	if crb.Name != "hermes-agent1-cluster-0" {
		t.Errorf("name: want hermes-agent1-cluster-0, got %q", crb.Name)
	}
	if crb.RoleRef.Name != "cluster-admin" {
		t.Errorf("roleRef.name: want cluster-admin, got %q", crb.RoleRef.Name)
	}
	if len(crb.Subjects) != 1 || crb.Subjects[0].Namespace != "hermes" {
		t.Errorf("subject: want SA in hermes ns, got %+v", crb.Subjects)
	}
	if crb.Labels[rbacSourceLabel] != rbacSourceSpec {
		t.Errorf("missing %s=%s marker label on CRB; got %q",
			rbacSourceLabel, rbacSourceSpec, crb.Labels[rbacSourceLabel])
	}
}

func TestAgentRBACSpecLabels_OperatorInternalBindingsHaveNoMarker(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha", Namespace: "hermes",
			UID: "test-uid",
		},
		Spec: hermesv1alpha1.HermesAgentSpec{ExecBackend: "kubernetes"},
	}
	// The self-introspection and exec-backend bindings are reconciled by
	// separate sub-controllers and MUST NOT carry the rbacSourceLabel —
	// otherwise reconcileRBAC's drift correction would delete them.
	for _, b := range []map[string]string{
		desiredSelfRoleBinding(agent).Labels,
		desiredExecRoleBinding(agent).Labels,
	} {
		if _, present := b[rbacSourceLabel]; present {
			t.Errorf("operator-internal binding carries %s — drift correction will delete it: %v",
				rbacSourceLabel, b)
		}
	}
}
