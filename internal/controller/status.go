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
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
	"github.com/UndermountainCC/hermes-operator/internal/metrics"
)

// reconcileStatus reads the live Deployment and writes computed status back
// to the CR. Phase 7b: when spec.dashboard.enabled, the operator also polls
// the dashboard's /api/status to populate per-gateway state.
func (r *HermesAgentReconciler) reconcileStatus(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
) (err error) {
	ctx, span := startSpan(ctx, "Reconcile.Status", agent)
	defer func() { endSpan(span, err) }()

	dep := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      deploymentName(agent),
		Namespace: agent.Namespace,
	}, dep)
	switch {
	case apierrors.IsNotFound(err):
		dep = nil
	case err != nil:
		return err
	}

	var dashStatus *DashboardStatus
	if agent.Spec.Dashboard.Enabled && r.ProbeHealthFn != nil {
		url := fmt.Sprintf("http://%s.%s.svc:9119/api/status",
			dashboardServiceName(agent), agent.Namespace)
		// DashboardProbe gets its own child span so traces show probe latency
		// separate from the surrounding status patch work — probe timeouts
		// (3s by default) otherwise look like a slow Reconcile.Status overall.
		probeCtx, probeSpan := startSpan(ctx, "Reconcile.DashboardProbe", agent)
		probeSpan.SetAttributes(attribute.String("dashboard.url", url))
		ds, probeErr := r.ProbeHealthFn(probeCtx, url)
		if probeErr != nil {
			// Dashboard unreachable — log at V(1) and leave the existing
			// status.gateways[] snapshot in place. Don't wipe a prior good
			// view just because the probe blipped. Bump the probe-failure
			// counter so SREs can alert on persistent operator→dashboard
			// connectivity issues (e.g., NetworkPolicy lockout). Emit a span
			// event AND record on the surrounding reconcile span (so a trace
			// filter on "RBACRejected OR DashboardProbeFailed" surfaces both
			// at the parent level without a child-span join).
			log.FromContext(ctx).V(1).Info("dashboard probe failed", "error", probeErr.Error())
			metrics.AgentDashboardProbeFailures.WithLabelValues(agent.Name, agent.Namespace).Inc()
			probeSpan.RecordError(probeErr)
			probeSpan.SetStatus(codes.Error, probeErr.Error())
			span.AddEvent("Reconcile.DashboardProbeFailed", trace.WithAttributes(
				attribute.String("error", probeErr.Error()),
			))
		} else {
			dashStatus = ds
			probeSpan.SetStatus(codes.Ok, "")
		}
		probeSpan.End()
	}

	// Compute desired status from observed state.
	desired := computeStatus(agent, dep, dashStatus)

	// Phase transition: emit a span event when the agent crosses a phase
	// boundary. Lets operators correlate "agent went Degraded at 12:04:17"
	// with the surrounding reconcile timeline in Jaeger/Tempo without
	// joining against logs. Empty-string fromPhase is intentional — the
	// first reconcile of a fresh CR has an empty agent.Status.Phase and
	// the transition to Bootstrap/Provisioning is itself a meaningful
	// signal to record.
	if agent.Status.Phase != desired.Phase {
		span.AddEvent("Reconcile.PhaseTransition", trace.WithAttributes(
			attribute.String("from", string(agent.Status.Phase)),
			attribute.String("to", string(desired.Phase)),
		))
	}

	// Skip update if nothing changed (cheap optimization; status updates
	// generate apiserver writes + watch events). Even on a skip we refresh
	// the metric exporter — gauges that were unset (operator restart, first
	// boot before any status change) should land on /metrics on the next
	// reconcile pass regardless of whether the CR itself needed a write.
	if statusEqual(agent.Status, desired) {
		metrics.UpdateAgentMetrics(agent)
		return nil
	}

	// Use a merge patch instead of a full Update on the status subresource.
	original := agent.DeepCopy()
	agent.Status = desired
	if err := r.Status().Patch(ctx, agent, client.MergeFrom(original)); err != nil {
		return err
	}
	metrics.UpdateAgentMetrics(agent)
	return nil
}

// computeStatus assembles the desired HermesAgentStatus from observed state.
//
// When dashStatus is non-nil (dashboard sidecar is on AND the probe
// succeeded), status.Gateways is populated by walking spec.gateways[] and
// looking up each platform key in the response — that way the CR status
// reflects user intent (declared gateways) and tolerates the runtime case
// where a platform hasn't yet reported.
func computeStatus(
	agent *hermesv1alpha1.HermesAgent,
	dep *appsv1.Deployment,
	dashStatus *DashboardStatus,
) hermesv1alpha1.HermesAgentStatus {
	out := hermesv1alpha1.HermesAgentStatus{
		ServiceAccountName: serviceAccountName(agent),
		Conditions:         agent.Status.DeepCopy().Conditions, // preserve existing conditions to compute transitions
	}

	if dep != nil && len(dep.Spec.Template.Spec.Containers) > 0 {
		out.ObservedImage = dep.Spec.Template.Spec.Containers[0].Image
	} else {
		out.ObservedImage = agent.Spec.Image
	}

	// Suspended: intentional replicas=0. Short-circuit phase + PodReady so
	// the agent never reports Degraded while intentionally stopped. Computed
	// before computePhase so we don't evaluate (and discard) a phase the
	// suspend state overrides.
	if agent.Spec.Suspend {
		out.Phase = hermesv1alpha1.PhaseSuspended
		out.Conditions = setCondition(out.Conditions, metav1.Condition{
			Type:    hermesv1alpha1.ConditionPodReady,
			Status:  metav1.ConditionFalse,
			Reason:  "Suspended",
			Message: "Agent is suspended (spec.suspend=true); replicas intentionally set to 0",
		})
		return out
	}

	// Non-suspended: derive the lifecycle phase from the Deployment +
	// dashboard state.
	out.Phase = computePhase(dep, dashStatus)

	// PodReady condition.
	ready := corev1.ConditionFalse
	reason := "DeploymentNotReady"
	message := "Deployment has zero ready replicas"
	if dep == nil {
		reason = "DeploymentMissing"
		message = "Deployment not yet created"
	} else if dep.Status.ReadyReplicas >= 1 {
		ready = corev1.ConditionTrue
		reason = "PodRunning"
		message = "Agent pod is ready"
	}

	out.Conditions = setCondition(out.Conditions, metav1.Condition{
		Type:    hermesv1alpha1.ConditionPodReady,
		Status:  metav1.ConditionStatus(ready),
		Reason:  reason,
		Message: message,
	})

	// Emit RBACSynced=True if no failure condition was already set by
	// reconcileRBAC. setRBACFailureCondition sets it to False; here we
	// only set True when the existing condition is not already False.
	rbacSynced := true
	for _, c := range out.Conditions {
		if c.Type == hermesv1alpha1.ConditionRBACSynced && c.Status == metav1.ConditionFalse {
			rbacSynced = false
			break
		}
	}
	if rbacSynced {
		out.Conditions = setCondition(out.Conditions, metav1.Condition{
			Type:    hermesv1alpha1.ConditionRBACSynced,
			Status:  metav1.ConditionTrue,
			Reason:  "RBACSynced",
			Message: "All RBAC bindings are in sync",
		})
	}

	// Phase 7b: populate status.gateways[] + GatewaysReady condition when the
	// dashboard probe returned data. When dashStatus is nil but the dashboard
	// IS enabled (transient probe failure), preserve the existing snapshot —
	// the user is better served by stale data than by a wipe. When the
	// dashboard is DISABLED entirely, clear both the snapshot AND the
	// GatewaysReady condition so a toggle from enabled→disabled doesn't leave
	// stale gateway state stuck in the CR. Bug C from real-cluster smoke
	// 2026-05-15.
	switch {
	case dashStatus != nil:
		out.Gateways = renderGatewayStatuses(agent, dashStatus)
		out.Conditions = setCondition(out.Conditions, gatewaysReadyCondition(dashStatus))
	case !agent.Spec.Dashboard.Enabled:
		out.Gateways = nil
		out.Conditions = removeCondition(out.Conditions, hermesv1alpha1.ConditionGatewaysReady)
	default:
		out.Gateways = agent.Status.Gateways
	}

	return out
}

// renderGatewayStatuses produces the HermesAgentGatewayStatus slice from the
// dashboard's /api/status response. Iterates over agent.Spec.Gateways (not
// the response map) so the CR status reflects user intent — gateways the
// user specced but that haven't reported yet show State="" / Ready=false
// rather than disappearing.
//
// Ready=true requires THREE conditions: dashboard says gateway_running, the
// platform key is present in the response, AND its state is exactly
// "connected". This correctly excludes "connecting" / "retrying" / "fatal"
// from being reported as ready.
func renderGatewayStatuses(
	agent *hermesv1alpha1.HermesAgent,
	s *DashboardStatus,
) []hermesv1alpha1.HermesAgentGatewayStatus {
	if s == nil {
		return nil
	}
	now := metav1.Now()
	out := make([]hermesv1alpha1.HermesAgentGatewayStatus, 0, len(agent.Spec.Gateways))
	for _, gw := range agent.Spec.Gateways {
		entry, present := s.GatewayPlatforms[gw.Type]
		ready := s.GatewayRunning && present && entry.State == "connected"
		out = append(out, hermesv1alpha1.HermesAgentGatewayStatus{
			Type:         gw.Type,
			Ready:        ready,
			State:        entry.State, // "" when !present
			Message:      strings.TrimSpace(entry.ErrorMessage),
			LastProbedAt: &now,
		})
	}
	return out
}

// gatewaysReadyCondition derives a single GatewaysReady condition from the
// dashboard status. True when the gateway process is running AND every
// declared platform reports State=="connected". False otherwise, with a
// Reason / Message reflecting the salient cause (degraded vs not-running
// vs at-least-one-not-connected).
func gatewaysReadyCondition(s *DashboardStatus) metav1.Condition {
	if s == nil {
		return metav1.Condition{
			Type:    hermesv1alpha1.ConditionGatewaysReady,
			Status:  metav1.ConditionUnknown,
			Reason:  "ProbeUnavailable",
			Message: "Dashboard probe has not yet returned",
		}
	}
	if !s.GatewayRunning {
		msg := "gateway is not running"
		if s.GatewayExitReason != nil && *s.GatewayExitReason != "" {
			msg = "gateway not running: " + *s.GatewayExitReason
		}
		return metav1.Condition{
			Type:    hermesv1alpha1.ConditionGatewaysReady,
			Status:  metav1.ConditionFalse,
			Reason:  "GatewayNotRunning",
			Message: msg,
		}
	}
	if s.GatewayState == "degraded" {
		return metav1.Condition{
			Type:    hermesv1alpha1.ConditionGatewaysReady,
			Status:  metav1.ConditionFalse,
			Reason:  "Degraded",
			Message: "gateway_state=degraded (≥1 platform fatal)",
		}
	}
	notReady := []string{}
	for name, p := range s.GatewayPlatforms {
		if p.State != "connected" {
			notReady = append(notReady, fmt.Sprintf("%s=%s", name, p.State))
		}
	}
	if len(notReady) > 0 {
		return metav1.Condition{
			Type:    hermesv1alpha1.ConditionGatewaysReady,
			Status:  metav1.ConditionFalse,
			Reason:  "PlatformsNotConnected",
			Message: "not connected: " + strings.Join(notReady, ", "),
		}
	}
	return metav1.Condition{
		Type:    hermesv1alpha1.ConditionGatewaysReady,
		Status:  metav1.ConditionTrue,
		Reason:  "AllPlatformsConnected",
		Message: "all declared gateways report connected",
	}
}

// computePhase classifies the agent's high-level phase from observed state.
//
// Phase 7b: when the dashboard sidecar is enabled and reachable, the
// top-level gateway_state from /api/status drives Degraded (upstream's
// authoritative "≥1 platform fatal" signal, plus stopped / startup_failed).
// When dashStatus is nil (sidecar off OR probe failed), behavior matches
// Phase 7a: pod readiness alone drives the phase.
func computePhase(dep *appsv1.Deployment, dashStatus *DashboardStatus) hermesv1alpha1.HermesAgentPhase {
	if dep == nil {
		return hermesv1alpha1.PhaseBootstrap
	}
	if dashStatus != nil {
		switch dashStatus.GatewayState {
		case "degraded", "stopped", "startup_failed":
			return hermesv1alpha1.PhaseDegraded
		}
	}
	// Explicit "stuck rollout" signal from K8s.
	for _, c := range dep.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse {
			return hermesv1alpha1.PhaseDegraded
		}
	}
	if dep.Status.ReadyReplicas >= 1 {
		return hermesv1alpha1.PhaseReady
	}
	return hermesv1alpha1.PhaseProvisioning
}

// setCondition updates or inserts a condition by Type, preserving
// LastTransitionTime when Status hasn't changed.
func setCondition(conds []metav1.Condition, want metav1.Condition) []metav1.Condition {
	now := metav1.Now()
	for i, c := range conds {
		if c.Type == want.Type {
			if c.Status == want.Status {
				want.LastTransitionTime = c.LastTransitionTime
			} else {
				want.LastTransitionTime = now
			}
			conds[i] = want
			return conds
		}
	}
	want.LastTransitionTime = now
	return append(conds, want)
}

// removeCondition strips any condition with the given Type from the slice.
// Returns the slice unchanged when no entry matches. Used by computeStatus
// to clear a stale GatewaysReady when the dashboard sidecar is disabled
// after having been enabled (Bug C).
func removeCondition(conds []metav1.Condition, t string) []metav1.Condition {
	out := conds[:0]
	for _, c := range conds {
		if c.Type == t {
			continue
		}
		out = append(out, c)
	}
	return out
}

// setSecretsResolvedCondition records the SecretsResolved condition on the
// agent status in memory (caller must persist via either a direct status
// patch in the bootstrap-gate short-circuit path, or via reconcileStatus on
// the success path).
func setSecretsResolvedCondition(agent *hermesv1alpha1.HermesAgent, ok bool, msg string) {
	status := metav1.ConditionFalse
	reason := "SecretsMissing"
	if ok {
		status = metav1.ConditionTrue
		reason = "AllSecretsPresent"
	}
	agent.Status.Conditions = setCondition(agent.Status.Conditions, metav1.Condition{
		Type:    hermesv1alpha1.ConditionSecretsResolved,
		Status:  status,
		Reason:  reason,
		Message: msg,
	})
}

// setRBACFailureCondition records a RBACSynced=False condition on the agent
// status in memory (caller must persist via reconcileStatus).
func setRBACFailureCondition(agent *hermesv1alpha1.HermesAgent, err error) {
	agent.Status.Conditions = setCondition(agent.Status.Conditions, metav1.Condition{
		Type:    hermesv1alpha1.ConditionRBACSynced,
		Status:  metav1.ConditionFalse,
		Reason:  "RBACPolicyViolation",
		Message: err.Error(),
	})
}

// clearRBACFailureCondition removes a stale RBACSynced=False condition after
// a subsequent successful reconcileRBAC. Without this, computeStatus's
// "preserve existing False" guard would lock the condition False even after
// the underlying issue is resolved (e.g., cluster admin added the missing
// ClusterRole to the allowlist and the operator restarted). Surfaced by
// Phase 2 kind smoke.
func clearRBACFailureCondition(agent *hermesv1alpha1.HermesAgent) {
	out := agent.Status.Conditions[:0]
	for _, c := range agent.Status.Conditions {
		if c.Type == hermesv1alpha1.ConditionRBACSynced && c.Status == metav1.ConditionFalse {
			continue
		}
		out = append(out, c)
	}
	agent.Status.Conditions = out
}

// statusEqual returns true when two HermesAgentStatus values are
// equivalent for the purposes of skipping a no-op write.
func statusEqual(a, b hermesv1alpha1.HermesAgentStatus) bool {
	if a.Phase != b.Phase {
		return false
	}
	if a.ObservedImage != b.ObservedImage {
		return false
	}
	if a.ServiceAccountName != b.ServiceAccountName {
		return false
	}
	if len(a.Conditions) != len(b.Conditions) {
		return false
	}
	for i := range a.Conditions {
		ca, cb := a.Conditions[i], b.Conditions[i]
		if ca.Type != cb.Type || ca.Status != cb.Status || ca.Reason != cb.Reason || ca.Message != cb.Message {
			return false
		}
	}
	return gatewayStatusesEqual(a.Gateways, b.Gateways)
}

// gatewayStatusesEqual compares two HermesAgentGatewayStatus slices for
// no-op-detection purposes. LastProbedAt is ignored — it ticks on every
// probe and would force a status write on every reconcile.
func gatewayStatusesEqual(a, b []hermesv1alpha1.HermesAgentGatewayStatus) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type {
			return false
		}
		if a[i].Ready != b[i].Ready {
			return false
		}
		if a[i].State != b[i].State {
			return false
		}
		if a[i].Message != b[i].Message {
			return false
		}
	}
	return true
}
