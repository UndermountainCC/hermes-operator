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
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
)

// fieldOwner identifies the operator in K8s server-side-apply managedFields.
// Stable across releases; do NOT change without coordination — flipping the
// owner name would orphan fields managed under the old name.
const fieldOwner = "hermes-operator"

// HermesAgentReconciler reconciles a HermesAgent object.
type HermesAgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Config OperatorConfig

	// ProbeHealthFn polls the dashboard sidecar's /api/status endpoint to
	// populate per-gateway state on the CR. Defaults to
	// defaultProbeDashboardStatus when SetupWithManager runs. Tests inject a
	// stub returning a synthetic DashboardStatus to drive status.gateways[]
	// without standing up a real dashboard pod.
	//
	// Returning (nil, err) is treated as "probe transient failure": the
	// operator leaves the prior status.gateways[] snapshot in place rather
	// than wiping it on every blip.
	ProbeHealthFn func(ctx context.Context, url string) (*DashboardStatus, error)
}

//+kubebuilder:rbac:groups=hermes.k8s.undermountain.cc,resources=hermesagents,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=hermes.k8s.undermountain.cc,resources=hermesagents/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=hermes.k8s.undermountain.cc,resources=hermesagents/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete

// RBAC management privileges: K8s blocks "privilege escalation" — to create
// a binding that grants rights, the principal creating the binding must
// hold those rights itself OR have the `escalate` verb on the role being
// bound. The `bind` verb is similarly required to reference a role in a new
// binding. Together they let the operator manage RBAC bindings to ANY role
// without itself holding every right it grants — narrower blast radius than
// granting the operator cluster-admin outright. The per-agent-CR
// `--allowed-cluster-roles` allowlist remains the practical guardrail.
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind;escalate
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=bind;escalate

// Reconcile is the controller's main entrypoint.
func (r *HermesAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	// Root span for the whole reconcile. Child spans (per reconcileXxx) inherit
	// via ctx. Tracing is a global no-op when OTEL_EXPORTER_OTLP_ENDPOINT is
	// unset (see internal/tracing) — these calls are cheap in that case.
	ctx, span := tracer().Start(ctx, "hermesagent.Reconcile")
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()
	// Set req-derived attributes BEFORE Get — even on NotFound (CR deleted
	// between watch event and reconcile) the span carries enough metadata
	// to correlate with the originating event.
	span.SetAttributes(
		attribute.String("hermesagent.name", req.Name),
		attribute.String("hermesagent.namespace", req.Namespace),
	)

	logger := log.FromContext(ctx)
	logger.Info("Reconciling HermesAgent", "namespace", req.Namespace, "name", req.Name)

	agent := &hermesv1alpha1.HermesAgent{}
	if err := r.Get(ctx, req.NamespacedName, agent); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get HermesAgent: %w", err)
	}

	// Deletion handling
	if !agent.DeletionTimestamp.IsZero() {
		if err := r.handleDeletion(ctx, agent); err != nil {
			return ctrl.Result{}, fmt.Errorf("handle deletion: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer on first reconcile.
	if err := r.ensureFinalizer(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure finalizer: %w", err)
	}

	// Order matters: PVC must exist before Deployment references it; SA must
	// exist before the Pod tries to use it. Deployment last.
	if err := r.reconcilePVC(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile PVC: %w", err)
	}
	if err := r.reconcileServiceAccount(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile ServiceAccount: %w", err)
	}
	// Self-introspection Role + RoleBinding: lets the agent get its own
	// Deployment + HermesAgent CR, and `kubectl rollout restart` itself.
	// Pinned to resourceNames (the agent's own names) so the grant cannot
	// reach sibling agents in the same namespace. RBAC isn't required for
	// Pod startup, so this can race the Deployment reconcile below
	// safely — but it MUST run after the SA exists (above) since the
	// RoleBinding's subject references it.
	if err := r.reconcileSelfRBAC(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile self RBAC: %w", err)
	}
	// Exec-backend RBAC: scoped Role + RoleBinding + no-perms session SA. Only
	// provisioned when spec.execBackend=="kubernetes"; otherwise (and on
	// toggle-off / BYO-SA) the reconciler GCs any previously-created objects.
	// Same architectural posture as self-RBAC: one operator-hardcoded Role per
	// agent. See exec_rbac.go for the BYO-SA opt-out rationale.
	if err := r.reconcileExecBackendRBAC(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile exec-backend RBAC: %w", err)
	}
	// Bootstrap-phase gate: refuse to create the Pod until every secretKeyRef
	// and envFrom.secretRef in the spec resolves to an existing Secret (and
	// key, when one is specified). Catches the common UX failure where the CR
	// is applied before its Secrets — without the gate the Pod loops in
	// CreateContainerConfigError with no clear surface on the CR.
	if err := r.validateSecretRefs(ctx, agent); err != nil {
		original := agent.DeepCopy()
		setSecretsResolvedCondition(agent, false, err.Error())
		agent.Status.Phase = hermesv1alpha1.PhaseBootstrap
		if statusErr := r.Status().Patch(ctx, agent, client.MergeFrom(original)); statusErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch Status during secrets gate: %w", statusErr)
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	// Secrets resolve cleanly — record the True condition so it survives the
	// rest of the reconcile loop (computeStatus preserves existing conditions
	// it does not own, so this lands in the final status write).
	setSecretsResolvedCondition(agent, true, "AllSecretsPresent")
	if err := r.reconcileRBAC(ctx, agent); err != nil {
		// RBAC failure → Phase=Degraded with reason. Don't fail-fast — let
		// status update so user sees what's wrong.
		setRBACFailureCondition(agent, err)
		if statusErr := r.reconcileStatus(ctx, agent); statusErr != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile Status after RBAC failure: %w", statusErr)
		}
		return ctrl.Result{}, fmt.Errorf("reconcile RBAC: %w", err)
	}
	// RBAC reconcile succeeded — clear any stale failure condition from a
	// prior cycle so computeStatus can set RBACSynced=True. Without this,
	// computeStatus preserves the False condition forever (locks the user
	// out of a clean status even after the underlying issue is resolved).
	clearRBACFailureCondition(agent)

	if err := r.reconcileDeployment(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile Deployment: %w", err)
	}
	if err := r.reconcileService(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile Service: %w", err)
	}
	if err := r.reconcileDashboardService(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile dashboard Service: %w", err)
	}
	if err := r.reconcileDashboardIngress(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile dashboard Ingress: %w", err)
	}
	if err := r.reconcileNetworkPolicy(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile NetworkPolicy: %w", err)
	}
	if err := r.reconcileStatus(ctx, agent); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile Status: %w", err)
	}

	logger.V(1).Info("reconciled", "agent", req.NamespacedName)

	// Phase 7b: periodic requeue when the dashboard sidecar is enabled. The
	// reconciler polls /api/status to refresh status.gateways[] every cycle.
	// Without the dashboard, pod readiness is observed via the container's
	// exec probes (Phase 7a) — no periodic requeue needed.
	if agent.Spec.Dashboard.Enabled {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *HermesAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.ProbeHealthFn == nil {
		r.ProbeHealthFn = defaultProbeDashboardStatus
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&hermesv1alpha1.HermesAgent{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&networkingv1.NetworkPolicy{}).
		// Self-introspection Role + same-namespace RoleBinding (Phase 10.6).
		// Owned via controller ownerRef from applyObject — Owns() drives
		// reconcile on user-tampering with the child resource.
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Complete(r)
}

// applyObject create-or-patches `desired` using Kubernetes Server-Side Apply.
// The operator owns only the fields it explicitly sets on `desired`; other
// fields managed by different parties (e.g., Deployment.status owned by
// kube-controller-manager) are preserved without resourceVersion conflicts.
//
// SSA requires the object's TypeMeta (apiVersion + kind) to be populated;
// our desired-helpers (desiredPVC, desiredServiceAccount, desiredDeployment)
// don't set it because operator-sdk's scheme-based serialization makes it
// implicit during normal Create/Update calls. We resolve it from the scheme
// here before patching.
//
// ForceOwnership claims fields previously owned by other field managers — the
// operator is the authoritative manager for the resources it creates. Without
// it, an external `kubectl apply --field-manager=other-tool` could lock us
// out of fields we intend to manage.
//
// Why SSA over Get-then-Update (which we previously used): in production K8s,
// kube-controller-manager touches Deployment.status between our Get and our
// Update, bumping resourceVersion. Update then fails with
// "Operation cannot be fulfilled ... the object has been modified", logged at
// ERROR level by controller-runtime. With SSA, K8s merges field-by-field; no
// rv comparison happens on fields we don't manage. Caught by the resilience
// integration test in hermesagent_controller_test.go.
func (r *HermesAgentReconciler) applyObject(
	ctx context.Context,
	agent *hermesv1alpha1.HermesAgent,
	desired client.Object,
) error {
	if err := ctrl.SetControllerReference(agent, desired, r.Scheme); err != nil {
		return fmt.Errorf("set owner ref: %w", err)
	}

	gvk, err := apiutil.GVKForObject(desired, r.Scheme)
	if err != nil {
		return fmt.Errorf("resolve GVK: %w", err)
	}
	desired.GetObjectKind().SetGroupVersionKind(gvk)

	if err := r.Patch(ctx, desired, client.Apply,
		client.ForceOwnership,
		client.FieldOwner(fieldOwner),
	); err != nil {
		return fmt.Errorf("server-side apply: %w", err)
	}
	return nil
}
