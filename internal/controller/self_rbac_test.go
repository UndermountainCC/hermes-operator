// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func TestDesiredSelfRole_NameAndScope(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
	}
	role := desiredSelfRole(agent)

	if role.Name != "hermes-agent1-self" {
		t.Errorf("name: want hermes-agent1-self, got %q", role.Name)
	}
	if role.Namespace != "hermes" {
		t.Errorf("namespace: want hermes, got %q", role.Namespace)
	}
	if role.Labels["hermes.undermountain.cc/agent"] != "agent1" {
		t.Errorf("missing agent label")
	}
}

func TestDesiredSelfRole_RulesAreResourceNameScoped(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
	}
	role := desiredSelfRole(agent)

	if len(role.Rules) != 2 {
		t.Fatalf("rules: want 2 (Deployment, HermesAgent CR), got %d", len(role.Rules))
	}

	// Every rule MUST pin resourceNames — that's the whole point. A rule
	// without resourceNames would grant namespace-wide access and let the
	// agent touch sibling agents.
	for i, rule := range role.Rules {
		if len(rule.ResourceNames) == 0 {
			t.Errorf("rule[%d]: missing resourceNames — would grant namespace-wide access (verbs=%v on resources=%v)", i, rule.Verbs, rule.Resources)
		}
	}

	// Deployment rule
	depRule := role.Rules[0]
	if len(depRule.APIGroups) != 1 || depRule.APIGroups[0] != "apps" {
		t.Errorf("deployment rule apiGroups: want [apps], got %v", depRule.APIGroups)
	}
	if len(depRule.Resources) != 1 || depRule.Resources[0] != "deployments" {
		t.Errorf("deployment rule resources: want [deployments], got %v", depRule.Resources)
	}
	if len(depRule.ResourceNames) != 1 || depRule.ResourceNames[0] != "hermes-agent1" {
		t.Errorf("deployment rule resourceNames: want [hermes-agent1], got %v", depRule.ResourceNames)
	}
	wantDepVerbs := map[string]bool{"get": true, "patch": true}
	if len(depRule.Verbs) != len(wantDepVerbs) {
		t.Errorf("deployment rule verbs: want exactly {get, patch}, got %v", depRule.Verbs)
	}
	for _, v := range depRule.Verbs {
		if !wantDepVerbs[v] {
			t.Errorf("deployment rule has unexpected verb %q (expected only get + patch)", v)
		}
	}

	// HermesAgent CR rule
	crRule := role.Rules[1]
	if len(crRule.APIGroups) != 1 || crRule.APIGroups[0] != "hermes.k8s.undermountain.cc" {
		t.Errorf("CR rule apiGroups: want [hermes.k8s.undermountain.cc], got %v", crRule.APIGroups)
	}
	if len(crRule.Resources) != 1 || crRule.Resources[0] != "hermesagents" {
		t.Errorf("CR rule resources: want [hermesagents], got %v", crRule.Resources)
	}
	if len(crRule.ResourceNames) != 1 || crRule.ResourceNames[0] != "agent1" {
		t.Errorf("CR rule resourceNames: want [agent1], got %v", crRule.ResourceNames)
	}
	if len(crRule.Verbs) != 1 || crRule.Verbs[0] != "get" {
		t.Errorf("CR rule verbs: want exactly [get], got %v", crRule.Verbs)
	}
}

func TestDesiredSelfRole_DoesNotGrantPodsAccess(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
	}
	role := desiredSelfRole(agent)

	for _, rule := range role.Rules {
		for _, res := range rule.Resources {
			if res == "pods" {
				t.Errorf("self-Role grants %v on pods — should use /restart slash command instead (canonical restart path)", rule.Verbs)
			}
		}
	}
}

func TestDesiredSelfRole_DoesNotGrantDangerousVerbs(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
	}
	role := desiredSelfRole(agent)

	// resourceNames-scoping does NOT mitigate every verb. Even pinned to
	// `hermes-agent1`, granting `delete` would let the agent delete its own
	// Deployment — which the operator would then recreate, but with a
	// momentary outage. None of these verbs are needed for the rollout-
	// restart workflow; explicit allowlist (get, patch) is enforced above,
	// this test catches regressions where someone widens the verb set.
	banned := map[string]bool{
		"delete":           true,
		"deletecollection": true,
		"create":           true,
		"update":           true,
		"*":                true,
	}
	for _, rule := range role.Rules {
		for _, verb := range rule.Verbs {
			if banned[verb] {
				t.Errorf("self-Role grants dangerous verb %q on %v — only get + patch are needed for self-introspection + rollout-restart", verb, rule.Resources)
			}
		}
	}
}

func TestDesiredSelfRoleBinding_TiesSAToRole(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
	}
	rb := desiredSelfRoleBinding(agent)

	if rb.Name != "hermes-agent1-self" {
		t.Errorf("name: want hermes-agent1-self, got %q", rb.Name)
	}
	if rb.Namespace != "hermes" {
		t.Errorf("namespace: want hermes, got %q", rb.Namespace)
	}
	if rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != "hermes-agent1-self" {
		t.Errorf("roleRef: want Role/hermes-agent1-self, got %s/%s", rb.RoleRef.Kind, rb.RoleRef.Name)
	}
	if rb.RoleRef.APIGroup != "rbac.authorization.k8s.io" {
		t.Errorf("roleRef.apiGroup: want rbac.authorization.k8s.io, got %q", rb.RoleRef.APIGroup)
	}
	if len(rb.Subjects) != 1 {
		t.Fatalf("subjects: want 1, got %d", len(rb.Subjects))
	}
	s := rb.Subjects[0]
	if s.Kind != "ServiceAccount" || s.Name != "hermes-agent1" || s.Namespace != "hermes" {
		t.Errorf("subject: want SA hermes/hermes-agent1, got %+v", s)
	}
}

func TestDesiredSelfRoleBinding_HonorsCustomServiceAccountName(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			ServiceAccountName: "custom-sa",
		},
	}
	rb := desiredSelfRoleBinding(agent)

	if rb.Subjects[0].Name != "custom-sa" {
		t.Errorf("subject name: want custom-sa (honor spec.serviceAccountName), got %q", rb.Subjects[0].Name)
	}
}
