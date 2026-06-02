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

// Package metrics registers and updates per-HermesAgent Prometheus metrics.
//
// Registered on controller-runtime's shared metrics registry — they're served
// on the operator's existing /metrics endpoint, no additional listener needed.
// Updated synchronously after every successful status patch; cleaned on CR
// delete by the finalizer to avoid ghost gauges.
//
// Cardinality: per-agent labels are bounded by the number of HermesAgent CRs
// in the cluster. AgentGatewayReady carries an additional `gateway_type` label
// sourced from spec.gateways[].type (user input). In practice each agent has
// ≤5 gateways, so total cardinality is bounded by len(agents) * (4 phases + 1
// pod_ready + ~5 gateways) ≈ 10 * len(agents). Acceptable for v1alpha1.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// Per-agent metrics. All labels are stable across releases — renaming a label
// or adding a required one is a breaking change for downstream alerts.
var (
	// AgentPhase reports the current Phase as a gauge keyed by phase value.
	// One agent has exactly one row with value=1; other phase rows for the
	// same agent are deleted via UpdateAgentMetrics on phase transitions to
	// avoid stale 1-values.
	AgentPhase = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "hermes_agent_phase",
			Help: "Current HermesAgent phase (1 = active, 0 otherwise).",
		},
		[]string{"name", "namespace", "phase"},
	)

	// AgentPodReady reports whether the agent pod's PodReady condition is True.
	AgentPodReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "hermes_agent_pod_ready",
			Help: "1 if the agent pod's PodReady condition is True.",
		},
		[]string{"name", "namespace"},
	)

	// AgentGatewayReady reports per-platform connection state from the
	// dashboard's /api/status. Only populated when spec.dashboard.enabled —
	// without the dashboard sidecar the operator has no per-gateway runtime
	// signal.
	AgentGatewayReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "hermes_agent_gateway_ready",
			Help: "1 if the agent's gateway for a given platform reports connected.",
		},
		[]string{"name", "namespace", "gateway_type"},
	)

	// AgentDashboardProbeFailures counts dashboard /api/status probe failures
	// over time. Useful for alerting on operator→dashboard connectivity
	// issues (e.g., NetworkPolicy locking the operator out).
	AgentDashboardProbeFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hermes_agent_dashboard_probe_failures_total",
			Help: "Cumulative count of dashboard /api/status probe failures.",
		},
		[]string{"name", "namespace"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		AgentPhase,
		AgentPodReady,
		AgentGatewayReady,
		AgentDashboardProbeFailures,
	)
}

// allPhases enumerates the known HermesAgentPhase values used to clear stale
// label-rows on phase transition. Adding a new phase requires extending this
// slice — without it, UpdateAgentMetrics would leave the old phase row at
// value=1 indefinitely.
var allPhases = []hermesv1alpha1.HermesAgentPhase{
	hermesv1alpha1.PhaseBootstrap,
	hermesv1alpha1.PhaseProvisioning,
	hermesv1alpha1.PhaseReady,
	hermesv1alpha1.PhaseDegraded,
}

// UpdateAgentMetrics rewrites all per-agent metrics from the CR's status.
// Safe to call on every reconcile; cheap (handful of label-set operations).
// Called from reconcileStatus after the status patch succeeds.
func UpdateAgentMetrics(agent *hermesv1alpha1.HermesAgent) {
	name, ns := agent.Name, agent.Namespace

	// Phase: delete other phase rows for this agent (to avoid stale 1-values
	// after a transition), then set the current phase to 1. Empty phase
	// (transient — first reconcile) results in all rows deleted.
	for _, p := range allPhases {
		if p == agent.Status.Phase {
			AgentPhase.WithLabelValues(name, ns, string(p)).Set(1)
		} else {
			AgentPhase.DeleteLabelValues(name, ns, string(p))
		}
	}

	// PodReady: read the existing condition (set by computeStatus in
	// status.go). Status=True → 1; anything else (False, Unknown, missing)
	// → 0.
	podReady := 0.0
	for _, c := range agent.Status.Conditions {
		if c.Type == hermesv1alpha1.ConditionPodReady && c.Status == "True" {
			podReady = 1
		}
	}
	AgentPodReady.WithLabelValues(name, ns).Set(podReady)

	// Gateways: one row per spec-declared gateway. Ready=true only when the
	// dashboard sidecar reported the platform as connected (status.gateways[i].Ready).
	// Build a map for O(1) lookup; absent status entries → 0.
	statusByType := map[string]bool{}
	for _, g := range agent.Status.Gateways {
		statusByType[g.Type] = g.Ready
	}
	for _, g := range agent.Spec.Gateways {
		ready := 0.0
		if statusByType[g.Type] {
			ready = 1
		}
		AgentGatewayReady.WithLabelValues(name, ns, g.Type).Set(ready)
	}
}

// DeleteAgentMetrics removes ALL per-agent metric rows. Called from the
// finalizer's deletion handler so deleted CRs don't leave ghost gauges
// reporting stale values forever. Safe to call on a never-tracked agent
// (Prometheus delete is idempotent).
func DeleteAgentMetrics(name, ns string) {
	AgentPodReady.DeleteLabelValues(name, ns)
	AgentDashboardProbeFailures.DeleteLabelValues(name, ns)
	// AgentPhase + AgentGatewayReady have additional label dimensions —
	// use DeletePartialMatch to scrub every row sharing this agent's
	// (name, namespace) regardless of phase / gateway_type.
	AgentPhase.DeletePartialMatch(prometheus.Labels{"name": name, "namespace": ns})
	AgentGatewayReady.DeletePartialMatch(prometheus.Labels{"name": name, "namespace": ns})
}
