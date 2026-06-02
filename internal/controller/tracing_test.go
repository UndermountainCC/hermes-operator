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
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// installRecorder swaps the global TracerProvider for one with an in-memory
// SpanRecorder, returns the recorder, and registers cleanup that restores
// the previous provider. Tests can call this in their first line.
func installRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	prev := otel.GetTracerProvider()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		// Flush is a no-op for the SimpleSpanProcessor used here, but it
		// matches the SDK contract — and we want the previous provider
		// reinstated so cross-test bleed-through is impossible.
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
	return rec
}

// minimalAgent builds a HermesAgent with just enough shape for the
// per-reconcile helpers to run without dereferencing nil. We're testing
// span emission, not reconcile correctness, so anything required by the
// validating webhook does not have to be set.
func minimalAgent() *hermesv1alpha1.HermesAgent {
	sc := "standard"
	return &hermesv1alpha1.HermesAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "trace-test", Namespace: "default"},
		Spec: hermesv1alpha1.HermesAgentSpec{
			Image: "ghcr.io/example/hermes@sha256:0000",
			Storage: hermesv1alpha1.HermesAgentStorage{
				PersistentVolumeClaim: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &sc,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				},
			},
		},
	}
}

func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(hermesv1alpha1.AddToScheme(s))
	return s
}

// TestReconcileFunctions_EmitChildSpans: every per-reconcile helper must
// emit a child span with the standard agent name/namespace attribute set.
// Whether the reconcile succeeds or fails against the fake client doesn't
// matter for this assertion — we're testing the tracing boilerplate, not
// the SSA path. We drive each helper and assert its named span lands in
// the recorder.
//
// Notable absences: reconcileDeployment isn't driven here because the
// fake client doesn't accept SSA on non-existent Deployments and the
// resulting noise drowns the span signal; reconcilePVC same. Span shape
// is identical across helpers (same helper produces it) so coverage on
// any one of them validates the rest by construction.
func TestReconcileFunctions_EmitChildSpans(t *testing.T) {
	scheme := buildScheme(t)

	cases := []struct {
		name     string
		spanName string
		run      func(t *testing.T, r *HermesAgentReconciler, agent *hermesv1alpha1.HermesAgent)
	}{
		{
			name:     "reconcileServiceAccount external-SA missing",
			spanName: "Reconcile.ServiceAccount",
			run: func(t *testing.T, r *HermesAgentReconciler, agent *hermesv1alpha1.HermesAgent) {
				t.Helper()
				agent.Spec.ServiceAccountName = "external-sa"
				_ = r.reconcileServiceAccount(context.Background(), agent)
			},
		},
		{
			name:     "reconcileNetworkPolicy disabled (no-op delete path)",
			spanName: "Reconcile.NetworkPolicy",
			run: func(t *testing.T, r *HermesAgentReconciler, agent *hermesv1alpha1.HermesAgent) {
				t.Helper()
				// agent.Spec.NetworkPolicy.Enabled is false by default → toggle-off path
				if err := r.reconcileNetworkPolicy(context.Background(), agent); err != nil {
					t.Fatalf("reconcileNetworkPolicy error = %v, want nil on disabled path", err)
				}
			},
		},
		{
			name:     "reconcileDashboardService disabled (no-op delete path)",
			spanName: "Reconcile.DashboardService",
			run: func(t *testing.T, r *HermesAgentReconciler, agent *hermesv1alpha1.HermesAgent) {
				t.Helper()
				if err := r.reconcileDashboardService(context.Background(), agent); err != nil {
					t.Fatalf("reconcileDashboardService error = %v, want nil on disabled path", err)
				}
			},
		},
		{
			name:     "reconcileDashboardIngress disabled (no-op delete path)",
			spanName: "Reconcile.DashboardIngress",
			run: func(t *testing.T, r *HermesAgentReconciler, agent *hermesv1alpha1.HermesAgent) {
				t.Helper()
				if err := r.reconcileDashboardIngress(context.Background(), agent); err != nil {
					t.Fatalf("reconcileDashboardIngress error = %v, want nil on disabled path", err)
				}
			},
		},
		{
			name:     "validateSecretRefs no refs",
			spanName: "Reconcile.SecretsValidation",
			run: func(t *testing.T, r *HermesAgentReconciler, agent *hermesv1alpha1.HermesAgent) {
				t.Helper()
				if err := r.validateSecretRefs(context.Background(), agent); err != nil {
					t.Fatalf("validateSecretRefs error = %v, want nil with no secret refs", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := installRecorder(t)
			cl := fake.NewClientBuilder().WithScheme(scheme).Build()
			r := &HermesAgentReconciler{Client: cl, Scheme: scheme}
			agent := minimalAgent()

			tc.run(t, r, agent)

			var got sdktrace.ReadOnlySpan
			names := []string{}
			for _, s := range rec.Ended() {
				names = append(names, s.Name())
				if s.Name() == tc.spanName {
					got = s
				}
			}
			if got == nil {
				t.Fatalf("span %q not captured; recorded: %v", tc.spanName, names)
			}

			// Attributes: every reconcile-span carries name + namespace.
			wantName, wantNs := false, false
			for _, kv := range got.Attributes() {
				switch string(kv.Key) {
				case "hermesagent.name":
					if kv.Value.AsString() == "trace-test" {
						wantName = true
					}
				case "hermesagent.namespace":
					if kv.Value.AsString() == "default" {
						wantNs = true
					}
				}
			}
			if !wantName || !wantNs {
				t.Errorf("missing attrs: name=%v ns=%v (attrs=%v)", wantName, wantNs, got.Attributes())
			}
		})
	}
}

// TestReconcileRBAC_AllowlistRejectionEmitsEvent: rejecting a ClusterRole
// that's not in the allowlist must surface as a Reconcile.RBACRejected
// span event AND a span Status=Error. Operators reading a trace see the
// rejection at exactly the timeline point it happened, with the role name
// attached — not just "RBAC reconcile failed" buried in the parent error.
func TestReconcileRBAC_AllowlistRejectionEmitsEvent(t *testing.T) {
	rec := installRecorder(t)

	scheme := buildScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &HermesAgentReconciler{
		Client: cl, Scheme: scheme,
		Config: OperatorConfig{AllowedClusterRoles: []string{"view"}},
	}

	agent := minimalAgent()
	agent.Spec.RBAC.ClusterRoleBindings = []hermesv1alpha1.HermesAgentClusterRoleBinding{
		{RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		}},
	}

	err := r.reconcileRBAC(context.Background(), agent)
	if err == nil {
		t.Fatal("reconcileRBAC with disallowed role returned nil; want allowlist rejection error")
	}

	var rbacSpan sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "Reconcile.RBAC" {
			rbacSpan = s
			break
		}
	}
	if rbacSpan == nil {
		t.Fatal("Reconcile.RBAC span not captured")
	}

	// Span event check: presence of the named event AND roleName attr.
	events := rbacSpan.Events()
	var found, hasRoleName bool
	for _, ev := range events {
		if ev.Name != "Reconcile.RBACRejected" {
			continue
		}
		found = true
		for _, kv := range ev.Attributes {
			if string(kv.Key) == "roleName" && kv.Value.AsString() == "cluster-admin" {
				hasRoleName = true
			}
		}
	}
	if !found {
		t.Errorf("Reconcile.RBACRejected event not found on span; got events: %v", events)
	}
	if !hasRoleName {
		t.Errorf("RBACRejected event missing roleName=cluster-admin attribute")
	}

	// Status=Error on rejection.
	if got := rbacSpan.Status().Code; got != codes.Error {
		t.Errorf("status code = %v, want Error", got)
	}
}

// TestReconcileStatus_PhaseTransitionEvent: when the computed Phase differs
// from the CR's current Phase, reconcileStatus must emit a
// Reconcile.PhaseTransition event with from/to attrs. This is the
// timeline annotation operators use to find "when did this agent go
// Degraded" in Jaeger/Tempo without log-correlation.
//
// We drive the empty→Bootstrap transition (no Deployment exists yet,
// computeStatus returns PhaseBootstrap). The fake client must be told
// about the HermesAgent so the status subresource patch succeeds.
func TestReconcileStatus_PhaseTransitionEvent(t *testing.T) {
	rec := installRecorder(t)

	scheme := buildScheme(t)
	agent := minimalAgent()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agent).
		WithStatusSubresource(agent).
		Build()
	r := &HermesAgentReconciler{Client: cl, Scheme: scheme}

	if err := r.reconcileStatus(context.Background(), agent); err != nil {
		t.Fatalf("reconcileStatus error = %v, want nil", err)
	}

	var statusSpan sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "Reconcile.Status" {
			statusSpan = s
			break
		}
	}
	if statusSpan == nil {
		t.Fatal("Reconcile.Status span not captured")
	}

	var fromVal, toVal string
	var found bool
	for _, ev := range statusSpan.Events() {
		if ev.Name == "Reconcile.PhaseTransition" {
			found = true
			for _, kv := range ev.Attributes {
				switch string(kv.Key) {
				case "from":
					fromVal = kv.Value.AsString()
				case "to":
					toVal = kv.Value.AsString()
				}
			}
		}
	}
	if !found {
		t.Fatalf("Reconcile.PhaseTransition event not emitted; events=%v", statusSpan.Events())
	}
	if fromVal != "" {
		t.Errorf("from = %q, want \"\" (fresh CR has empty Phase)", fromVal)
	}
	if toVal != string(hermesv1alpha1.PhaseBootstrap) {
		t.Errorf("to = %q, want %q", toVal, hermesv1alpha1.PhaseBootstrap)
	}
}

// TestReconcileStatus_DashboardProbeFailedEvent: when ProbeHealthFn
// returns an error and dashboard is enabled, reconcileStatus must emit a
// Reconcile.DashboardProbeFailed event on the surrounding Status span
// (so a parent-level trace filter catches it without joining child spans).
func TestReconcileStatus_DashboardProbeFailedEvent(t *testing.T) {
	rec := installRecorder(t)

	scheme := buildScheme(t)
	agent := minimalAgent()
	agent.Spec.Dashboard.Enabled = true
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(agent).
		WithStatusSubresource(agent).
		Build()
	r := &HermesAgentReconciler{
		Client: cl, Scheme: scheme,
		ProbeHealthFn: func(_ context.Context, _ string) (*DashboardStatus, error) {
			return nil, errors.New("synthetic probe failure")
		},
	}

	if err := r.reconcileStatus(context.Background(), agent); err != nil {
		t.Fatalf("reconcileStatus error = %v, want nil (probe failure is non-fatal)", err)
	}

	var statusSpan sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "Reconcile.Status" {
			statusSpan = s
			break
		}
	}
	if statusSpan == nil {
		t.Fatal("Reconcile.Status span not captured")
	}

	var found bool
	for _, ev := range statusSpan.Events() {
		if ev.Name == "Reconcile.DashboardProbeFailed" {
			found = true
			for _, kv := range ev.Attributes {
				if string(kv.Key) == "error" {
					if got := kv.Value.AsString(); got != "synthetic probe failure" {
						t.Errorf("event error attr = %q, want %q", got, "synthetic probe failure")
					}
				}
			}
		}
	}
	if !found {
		t.Errorf("Reconcile.DashboardProbeFailed event not emitted; events=%v", statusSpan.Events())
	}
}

// TestEndSpan_SetsErrorStatus: direct unit on the helper — endSpan with a
// non-nil error must set Status=Error and call RecordError. Tracetest's
// recorder exposes both; a regression in the helper would break every
// downstream reconcile-span error path.
func TestEndSpan_SetsErrorStatus(t *testing.T) {
	rec := installRecorder(t)

	_, span := tracer().Start(context.Background(), "test.span")
	endSpan(span, errors.New("synthetic"))

	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("captured %d spans; want 1", len(ended))
	}
	if got := ended[0].Status().Code; got != codes.Error {
		t.Errorf("status = %v, want Error", got)
	}
	if msg := ended[0].Status().Description; msg != "synthetic" {
		t.Errorf("description = %q, want %q", msg, "synthetic")
	}
}
