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

package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func TestUpdateAgentMetrics_PhaseTransition(t *testing.T) {
	defer DeleteAgentMetrics("transition", "ns")
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "transition", Namespace: "ns"},
		Status:     hermesv1alpha1.HermesAgentStatus{Phase: hermesv1alpha1.PhaseReady},
	}
	UpdateAgentMetrics(agent)

	expected := `
# HELP hermes_agent_phase Current HermesAgent phase (1 = active, 0 otherwise).
# TYPE hermes_agent_phase gauge
hermes_agent_phase{name="transition",namespace="ns",phase="Ready"} 1
`
	if err := testutil.CollectAndCompare(AgentPhase, strings.NewReader(expected)); err != nil {
		t.Errorf("after Ready: %v", err)
	}

	// Transition to Degraded — Ready row should disappear, Degraded should
	// be the sole row at value=1. Without the DeleteLabelValues sweep in
	// UpdateAgentMetrics, both rows would coexist (stale "Ready=1" alongside
	// the actual "Degraded=1"), which makes dashboards lie.
	agent.Status.Phase = hermesv1alpha1.PhaseDegraded
	UpdateAgentMetrics(agent)
	expected = `
# HELP hermes_agent_phase Current HermesAgent phase (1 = active, 0 otherwise).
# TYPE hermes_agent_phase gauge
hermes_agent_phase{name="transition",namespace="ns",phase="Degraded"} 1
`
	if err := testutil.CollectAndCompare(AgentPhase, strings.NewReader(expected)); err != nil {
		t.Errorf("after transition: %v", err)
	}
}

func TestUpdateAgentMetrics_PodReady(t *testing.T) {
	defer DeleteAgentMetrics("podready", "ns")
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "podready", Namespace: "ns"},
		Status: hermesv1alpha1.HermesAgentStatus{
			Phase: hermesv1alpha1.PhaseReady,
			Conditions: []metav1.Condition{
				{Type: hermesv1alpha1.ConditionPodReady, Status: metav1.ConditionTrue},
			},
		},
	}
	UpdateAgentMetrics(agent)
	got := testutil.ToFloat64(AgentPodReady.WithLabelValues("podready", "ns"))
	if got != 1 {
		t.Errorf("PodReady=True → metric=1; got %v", got)
	}

	// Flip to False — metric drops to 0.
	agent.Status.Conditions[0].Status = metav1.ConditionFalse
	UpdateAgentMetrics(agent)
	got = testutil.ToFloat64(AgentPodReady.WithLabelValues("podready", "ns"))
	if got != 0 {
		t.Errorf("PodReady=False → metric=0; got %v", got)
	}
}

func TestUpdateAgentMetrics_GatewayReady(t *testing.T) {
	defer DeleteAgentMetrics("gw", "ns")
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "ns"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			Gateways: []hermesv1alpha1.HermesAgentGateway{
				{Type: "discord"},
				{Type: "telegram"},
			},
		},
		Status: hermesv1alpha1.HermesAgentStatus{
			Phase: hermesv1alpha1.PhaseReady,
			Gateways: []hermesv1alpha1.HermesAgentGatewayStatus{
				{Type: "discord", Ready: true},
				{Type: "telegram", Ready: false},
			},
		},
	}
	UpdateAgentMetrics(agent)
	discord := testutil.ToFloat64(AgentGatewayReady.WithLabelValues("gw", "ns", "discord"))
	telegram := testutil.ToFloat64(AgentGatewayReady.WithLabelValues("gw", "ns", "telegram"))
	if discord != 1 {
		t.Errorf("discord ready → 1; got %v", discord)
	}
	if telegram != 0 {
		t.Errorf("telegram not ready → 0; got %v", telegram)
	}

	// Gateway spec-declared but not in status (e.g., probe hasn't reported
	// yet) → 0. This is the "declared but not seen" surface.
	agent.Status.Gateways = nil
	UpdateAgentMetrics(agent)
	discord = testutil.ToFloat64(AgentGatewayReady.WithLabelValues("gw", "ns", "discord"))
	if discord != 0 {
		t.Errorf("after status wipe, discord → 0; got %v", discord)
	}
}

func TestDeleteAgentMetrics_RemovesAllRows(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "to-delete", Namespace: "ns"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			Gateways: []hermesv1alpha1.HermesAgentGateway{{Type: "discord"}},
		},
		Status: hermesv1alpha1.HermesAgentStatus{
			Phase: hermesv1alpha1.PhaseReady,
			Gateways: []hermesv1alpha1.HermesAgentGatewayStatus{
				{Type: "discord", Ready: true},
			},
		},
	}
	UpdateAgentMetrics(agent)
	// Sanity: rows exist before delete.
	if testutil.CollectAndCount(AgentPhase, "hermes_agent_phase") < 1 {
		t.Fatalf("expected at least one phase row before delete")
	}

	DeleteAgentMetrics("to-delete", "ns")

	// Confirm zero rows remain for this agent. CollectAndCount totals across
	// all labelsets; in this isolated test there should be no other rows.
	if got := testutil.CollectAndCount(AgentPhase, "hermes_agent_phase"); got != 0 {
		t.Errorf("after delete, AgentPhase rows: want 0, got %d", got)
	}
	if got := testutil.CollectAndCount(AgentGatewayReady, "hermes_agent_gateway_ready"); got != 0 {
		t.Errorf("after delete, AgentGatewayReady rows: want 0, got %d", got)
	}
	if got := testutil.CollectAndCount(AgentPodReady, "hermes_agent_pod_ready"); got != 0 {
		t.Errorf("after delete, AgentPodReady rows: want 0, got %d", got)
	}
}
