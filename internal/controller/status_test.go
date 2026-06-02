// Copyright 2026 Undermountain Coding Company
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

func TestComputeStatus_SuspendedWins(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent2", Namespace: "hermes"},
		Spec:       hermesv1alpha1.HermesAgentSpec{Suspend: true},
	}
	// A suspended agent reports Suspended regardless of pod/secret/gateway
	// state — it has intentionally zero replicas.
	out := computeStatus(agent, nil, nil)
	if out.Phase != hermesv1alpha1.PhaseSuspended {
		t.Errorf("suspended: want Suspended, got %q", out.Phase)
	}
	// PodReady condition must be False with reason Suspended, never Degraded.
	var podReady *metav1.Condition
	for i := range out.Conditions {
		if out.Conditions[i].Type == hermesv1alpha1.ConditionPodReady {
			podReady = &out.Conditions[i]
			break
		}
	}
	if podReady == nil {
		t.Fatal("PodReady condition missing for suspended agent")
	}
	if podReady.Status != metav1.ConditionFalse {
		t.Errorf("PodReady: want False, got %s", podReady.Status)
	}
	if podReady.Reason != "Suspended" {
		t.Errorf("PodReady.Reason: want Suspended, got %q", podReady.Reason)
	}
}

func TestComputePhase(t *testing.T) {
	cases := []struct {
		name string
		dep  *appsv1.Deployment
		ds   *DashboardStatus
		want hermesv1alpha1.HermesAgentPhase
	}{
		{
			name: "no deployment yet",
			dep:  nil,
			ds:   nil,
			want: hermesv1alpha1.PhaseBootstrap,
		},
		{
			name: "deployment exists, zero ready replicas",
			dep: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{ReadyReplicas: 0},
			},
			ds:   nil,
			want: hermesv1alpha1.PhaseProvisioning,
		},
		{
			name: "deployment ready (no dashboard data) → Ready",
			dep: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
			},
			ds:   nil,
			want: hermesv1alpha1.PhaseReady,
		},
		{
			name: "deployment condition Progressing=False → Degraded",
			dep: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					ReadyReplicas: 0,
					Conditions: []appsv1.DeploymentCondition{
						{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"},
					},
				},
			},
			ds:   nil,
			want: hermesv1alpha1.PhaseDegraded,
		},
		{
			name: "dashboard reports gateway_state=degraded → Degraded",
			dep: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
			},
			ds:   &DashboardStatus{GatewayRunning: true, GatewayState: "degraded"},
			want: hermesv1alpha1.PhaseDegraded,
		},
		{
			name: "dashboard reports gateway_state=stopped → Degraded",
			dep: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
			},
			ds:   &DashboardStatus{GatewayRunning: false, GatewayState: "stopped"},
			want: hermesv1alpha1.PhaseDegraded,
		},
		{
			name: "dashboard reports gateway_state=startup_failed → Degraded",
			dep: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
			},
			ds:   &DashboardStatus{GatewayRunning: false, GatewayState: "startup_failed"},
			want: hermesv1alpha1.PhaseDegraded,
		},
		{
			name: "dashboard reports gateway_state=running and pod ready → Ready",
			dep: &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
			},
			ds:   &DashboardStatus{GatewayRunning: true, GatewayState: "running"},
			want: hermesv1alpha1.PhaseReady,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := computePhase(c.dep, c.ds)
			if got != c.want {
				t.Errorf("computePhase(%s): want %v, got %v", c.name, c.want, got)
			}
		})
	}
}

func TestRenderGatewayStatuses(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		Spec: hermesv1alpha1.HermesAgentSpec{
			Gateways: []hermesv1alpha1.HermesAgentGateway{
				{Type: "discord"},
				{Type: "slack"},
				{Type: "telegram"},
				{Type: "whatsapp"}, // declared but not present in response — must show State=""
			},
		},
	}
	ds := &DashboardStatus{
		GatewayRunning: true,
		GatewayState:   "running",
		GatewayPlatforms: map[string]PlatformState{
			"discord":  {State: "connected"},
			"slack":    {State: "connecting"},
			"telegram": {State: "fatal", ErrorMessage: "  invalid bot token  "},
		},
	}
	got := renderGatewayStatuses(agent, ds)
	if len(got) != 4 {
		t.Fatalf("want 4 statuses, got %d", len(got))
	}
	m := map[string]hermesv1alpha1.HermesAgentGatewayStatus{}
	for _, g := range got {
		m[g.Type] = g
	}
	// discord: connected → Ready=true
	if !m["discord"].Ready || m["discord"].State != "connected" || m["discord"].Message != "" {
		t.Errorf("discord: want Ready=true State=connected Message=, got %+v", m["discord"])
	}
	// slack: connecting → Ready=false
	if m["slack"].Ready || m["slack"].State != "connecting" {
		t.Errorf("slack: want Ready=false State=connecting, got %+v", m["slack"])
	}
	// telegram: fatal → Ready=false, message trimmed
	if m["telegram"].Ready || m["telegram"].State != "fatal" || m["telegram"].Message != "invalid bot token" {
		t.Errorf("telegram: want Ready=false State=fatal Message=trimmed, got %+v", m["telegram"])
	}
	// whatsapp: not in response → State=""
	if m["whatsapp"].Ready || m["whatsapp"].State != "" {
		t.Errorf("whatsapp: want Ready=false State=, got %+v", m["whatsapp"])
	}
	// All entries have LastProbedAt set.
	for _, g := range got {
		if g.LastProbedAt == nil {
			t.Errorf("%s: LastProbedAt unset", g.Type)
		}
	}
}

// TestGatewaysReadyCondition_AllBranches drives each branch of
// gatewaysReadyCondition and asserts the resulting condition state +
// reason. Bug D regression (smoke 2026-05-15): when the gateway crashes,
// dashboard reports gateway_running=false with an empty platforms map.
// The function MUST return False/GatewayNotRunning, not fall through to
// True/AllPlatformsConnected just because the platforms loop found no
// non-connected entries.
func TestGatewaysReadyCondition_AllBranches(t *testing.T) {
	exit := "boom"
	cases := []struct {
		name       string
		s          *DashboardStatus
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{
			name:       "nil DashboardStatus → Unknown/ProbeUnavailable",
			s:          nil,
			wantStatus: metav1.ConditionUnknown,
			wantReason: "ProbeUnavailable",
		},
		{
			name: "gateway not running (empty platforms map) → False/GatewayNotRunning",
			s: &DashboardStatus{
				GatewayRunning:   false,
				GatewayState:     "startup_failed",
				GatewayPlatforms: map[string]PlatformState{},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "GatewayNotRunning",
		},
		{
			name: "gateway not running with exit reason → False/GatewayNotRunning (message includes exit reason)",
			s: &DashboardStatus{
				GatewayRunning:    false,
				GatewayExitReason: &exit,
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "GatewayNotRunning",
		},
		{
			name: "gateway running but state=degraded → False/Degraded",
			s: &DashboardStatus{
				GatewayRunning: true,
				GatewayState:   "degraded",
				GatewayPlatforms: map[string]PlatformState{
					"discord":  {State: "connected"},
					"telegram": {State: "fatal"},
				},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "Degraded",
		},
		{
			name: "gateway running, one platform not connected → False/PlatformsNotConnected",
			s: &DashboardStatus{
				GatewayRunning: true,
				GatewayState:   "running",
				GatewayPlatforms: map[string]PlatformState{
					"discord": {State: "connected"},
					"slack":   {State: "connecting"},
				},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "PlatformsNotConnected",
		},
		{
			name: "gateway running, all platforms connected → True/AllPlatformsConnected",
			s: &DashboardStatus{
				GatewayRunning: true,
				GatewayState:   "running",
				GatewayPlatforms: map[string]PlatformState{
					"discord":  {State: "connected"},
					"telegram": {State: "connected"},
				},
			},
			wantStatus: metav1.ConditionTrue,
			wantReason: "AllPlatformsConnected",
		},
		{
			// Critical Bug D regression: zero-platform "running" map combined
			// with GatewayRunning=true MUST stay True (no platforms is an OK
			// state for an agent with no gateways declared); but combined with
			// GatewayRunning=false MUST be False — never let an empty
			// platforms map mask the "gateway dead" signal.
			name: "gateway not running AND empty platforms → False (not True via 'len(notReady)==0' fallthrough)",
			s: &DashboardStatus{
				GatewayRunning:   false,
				GatewayPlatforms: map[string]PlatformState{},
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "GatewayNotRunning",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := gatewaysReadyCondition(c.s)
			if got.Type != hermesv1alpha1.ConditionGatewaysReady {
				t.Errorf("Type: want %s, got %s", hermesv1alpha1.ConditionGatewaysReady, got.Type)
			}
			if got.Status != c.wantStatus {
				t.Errorf("Status: want %s, got %s", c.wantStatus, got.Status)
			}
			if got.Reason != c.wantReason {
				t.Errorf("Reason: want %s, got %s (message=%q)", c.wantReason, got.Reason, got.Message)
			}
		})
	}
}

// TestGatewaysReadyCondition_TransitionsBothWays asserts that
// setCondition can flip True→False AND False→True across reconciles.
// Bug D's failure mode was the prior True condition surviving a
// gateway crash; verifying both directions catches any future
// short-circuit in setCondition or equality guards.
func TestGatewaysReadyCondition_TransitionsBothWays(t *testing.T) {
	// Start: all connected → True.
	good := &DashboardStatus{
		GatewayRunning: true,
		GatewayState:   "running",
		GatewayPlatforms: map[string]PlatformState{
			"discord": {State: "connected"},
		},
	}
	conds := []metav1.Condition{}
	conds = setCondition(conds, gatewaysReadyCondition(good))
	if len(conds) != 1 || conds[0].Status != metav1.ConditionTrue {
		t.Fatalf("expected initial True, got %+v", conds)
	}
	t0 := conds[0].LastTransitionTime

	// Gateway crashes: GatewayRunning=false, empty platforms map.
	bad := &DashboardStatus{
		GatewayRunning:   false,
		GatewayPlatforms: map[string]PlatformState{},
	}
	conds = setCondition(conds, gatewaysReadyCondition(bad))
	if len(conds) != 1 {
		t.Fatalf("setCondition should overwrite not append, got len=%d", len(conds))
	}
	if conds[0].Status != metav1.ConditionFalse {
		t.Errorf("after gateway crash: want False, got %s", conds[0].Status)
	}
	if conds[0].Reason != "GatewayNotRunning" {
		t.Errorf("after gateway crash: want reason GatewayNotRunning, got %s", conds[0].Reason)
	}
	// LastTransitionTime should have moved (status changed).
	if conds[0].LastTransitionTime.Equal(&t0) {
		t.Errorf("LastTransitionTime should advance on True→False transition; t0=%v now=%v", t0, conds[0].LastTransitionTime)
	}
	t1 := conds[0].LastTransitionTime

	// Gateway recovers: back to all-connected. False→True must also fire.
	conds = setCondition(conds, gatewaysReadyCondition(good))
	if conds[0].Status != metav1.ConditionTrue {
		t.Errorf("after recovery: want True, got %s", conds[0].Status)
	}
	if conds[0].Reason != "AllPlatformsConnected" {
		t.Errorf("after recovery: want reason AllPlatformsConnected, got %s", conds[0].Reason)
	}
	if conds[0].LastTransitionTime.Equal(&t1) {
		t.Errorf("LastTransitionTime should advance on False→True transition")
	}
}

// TestComputeStatus_PreservesStaleConditionOnProbeFailure verifies the
// "transient probe failure" path: when the dashboard is still enabled
// but the probe blipped (dashStatus=nil), the prior GatewaysReady
// condition is preserved rather than reset. This is the deliberate
// design — we don't want to flap to Unknown on every probe error.
//
// The Bug D production failure was this path firing AFTER a crash,
// preserving a stale True. The fix is upstream: Bug B keeps the probe
// reachable so dashStatus is non-nil during gateway outages, and the
// fresh probe returns gateway_running=false → condition flips False.
func TestComputeStatus_PreservesStaleConditionOnProbeFailure(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		Spec: hermesv1alpha1.HermesAgentSpec{
			Image: "img",
			Dashboard: hermesv1alpha1.HermesAgentDashboardSpec{
				Enabled: true,
			},
		},
		Status: hermesv1alpha1.HermesAgentStatus{
			Conditions: []metav1.Condition{
				{
					Type:    hermesv1alpha1.ConditionGatewaysReady,
					Status:  metav1.ConditionTrue,
					Reason:  "AllPlatformsConnected",
					Message: "all declared gateways report connected",
				},
			},
			Gateways: []hermesv1alpha1.HermesAgentGatewayStatus{
				{Type: "discord", Ready: true, State: "connected"},
			},
		},
	}
	dep := &appsv1.Deployment{Status: appsv1.DeploymentStatus{ReadyReplicas: 1}}
	// dashStatus=nil simulates a probe failure with dashboard still enabled.
	out := computeStatus(agent, dep, nil)
	// Snapshot preserved.
	if len(out.Gateways) != 1 || out.Gateways[0].Type != "discord" || !out.Gateways[0].Ready {
		t.Errorf("expected prior gateways snapshot preserved on probe failure, got %+v", out.Gateways)
	}
	// Condition preserved.
	var found *metav1.Condition
	for i := range out.Conditions {
		if out.Conditions[i].Type == hermesv1alpha1.ConditionGatewaysReady {
			found = &out.Conditions[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected GatewaysReady condition preserved on probe failure")
	}
	if found.Status != metav1.ConditionTrue {
		t.Errorf("expected preserved True condition, got %s", found.Status)
	}
}

// TestComputeStatus_DashboardDisabledClearsGatewaysReady verifies Bug C's
// other half: turning off the dashboard sidecar must clear the
// GatewaysReady condition (since there's no longer any source of truth
// for it).
func TestComputeStatus_DashboardDisabledClearsGatewaysReady(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		Spec: hermesv1alpha1.HermesAgentSpec{
			Image: "img",
			// Dashboard: Enabled is false (zero value).
		},
		Status: hermesv1alpha1.HermesAgentStatus{
			Conditions: []metav1.Condition{
				{
					Type:   hermesv1alpha1.ConditionGatewaysReady,
					Status: metav1.ConditionTrue,
					Reason: "AllPlatformsConnected",
				},
			},
			Gateways: []hermesv1alpha1.HermesAgentGatewayStatus{
				{Type: "discord", Ready: true, State: "connected"},
			},
		},
	}
	dep := &appsv1.Deployment{Status: appsv1.DeploymentStatus{ReadyReplicas: 1}}
	out := computeStatus(agent, dep, nil)
	if out.Gateways != nil {
		t.Errorf("expected gateways cleared when dashboard disabled, got %+v", out.Gateways)
	}
	for _, c := range out.Conditions {
		if c.Type == hermesv1alpha1.ConditionGatewaysReady {
			t.Errorf("GatewaysReady condition should be removed when dashboard disabled, got %+v", c)
		}
	}
}

func TestRenderGatewayStatuses_GatewayNotRunningKeepsReadyFalse(t *testing.T) {
	agent := &hermesv1alpha1.HermesAgent{
		Spec: hermesv1alpha1.HermesAgentSpec{
			Gateways: []hermesv1alpha1.HermesAgentGateway{{Type: "discord"}},
		},
	}
	// Even if the platform claims state=connected, GatewayRunning=false forces Ready=false.
	ds := &DashboardStatus{
		GatewayRunning: false,
		GatewayPlatforms: map[string]PlatformState{
			"discord": {State: "connected"},
		},
	}
	got := renderGatewayStatuses(agent, ds)
	if len(got) != 1 || got[0].Ready {
		t.Errorf("want Ready=false when gateway not running, got %+v", got)
	}
}
