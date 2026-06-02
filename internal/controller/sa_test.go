// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func TestDesiredServiceAccount_DefaultName(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
		Spec:       hermesv1alpha1.HermesAgentSpec{},
	}
	sa := desiredServiceAccount(agent)
	if sa.Name != "hermes-agent1" {
		t.Errorf("default SA name: want hermes-agent1, got %q", sa.Name)
	}
	if sa.Namespace != "hermes" {
		t.Errorf("ns: want hermes, got %q", sa.Namespace)
	}
}

func TestDesiredServiceAccount_HonorsSpec(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1", Namespace: "hermes"},
		Spec:       hermesv1alpha1.HermesAgentSpec{ServiceAccountName: "custom-sa"},
	}
	sa := desiredServiceAccount(agent)
	if sa.Name != "custom-sa" {
		t.Errorf("explicit SA name: want custom-sa, got %q", sa.Name)
	}
}

func TestServiceAccountName_DefaultsAreStable(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent1"},
	}
	if got := serviceAccountName(agent); got != "hermes-agent1" {
		t.Errorf("serviceAccountName default: want hermes-agent1, got %q", got)
	}
}
