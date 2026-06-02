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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HermesAgentSpec defines the desired state of HermesAgent.
// +kubebuilder:validation:XValidation:rule="!has(self.llmDefaultProvider) || size(self.llmDefaultProvider) == 0 || (has(self.llmProviders) && self.llmProviders.exists(p, p.name == self.llmDefaultProvider))",message="spec.llmDefaultProvider must match a name in spec.llmProviders"
type HermesAgentSpec struct {
	// Image is the fully-qualified container image reference for the agent.
	// Should include digest (e.g. registry/repo@sha256:...) for reproducibility.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// ImagePullPolicy for the agent container.
	// +kubebuilder:default=IfNotPresent
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// ExecBackend selects how the agent's terminal/code-execution tools run
	// commands. "local" (default) runs them inside the agent container.
	// "kubernetes" runs each session in a separate pod the agent creates via
	// the in-cluster API; when set, the operator provisions a scoped Role +
	// RoleBinding + a no-perms session ServiceAccount for that purpose, and a
	// cluster ValidatingAdmissionPolicy (if installed) constrains the shape of
	// pods the agent may create. See docs/research/2026-05-23-kubernetes-exec-backend-design.md.
	// +kubebuilder:validation:Enum=local;kubernetes
	// +kubebuilder:default=local
	// +optional
	ExecBackend string `json:"execBackend,omitempty"`

	// ImagePullSecrets references registry credentials.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// ServiceAccountName for the agent pod. If empty, defaults to
	// "hermes-<metadata.name>" and the operator creates it.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Storage configures the agent's PersistentVolumeClaim.
	// +kubebuilder:validation:Required
	Storage HermesAgentStorage `json:"storage"`

	// Suspend scales the agent to zero replicas without deleting the CR or
	// its PVC/ServiceAccount/Service. The Deployment stays at replicas=0,
	// status.phase becomes Suspended, and the agent stops running until
	// Suspend is set back to false. Use to stop an agent declaratively
	// (survives GitOps reconciles, unlike deleting the CR).
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Resources for the agent container (native corev1 type).
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector for pod placement.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations for pod placement.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Affinity for pod placement.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Env to inject into the agent container. Composed AFTER per-provider
	// and per-gateway env, so spec.env overrides those on key conflict.
	// Operator-stamped vars (HERMES_INFERENCE_PROVIDER, field refs, identity)
	// are appended LAST and override everything.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// EnvFrom for the agent container.
	// +optional
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`

	// LLMDefaultProvider sets HERMES_INFERENCE_PROVIDER on the container env.
	// Typically matches the name of an entry in LLMProviders.
	//
	// Caveat: upstream Hermes Agent resolves the active provider in this
	// precedence order: (1) CLI arg, (2) $HERMES_HOME/config.yaml's
	// `model.provider`, (3) the HERMES_INFERENCE_PROVIDER env var. Because
	// the hermes-base image seeds config.yaml with `model.provider` already
	// set on first boot, the env var stamped by this field is effectively
	// shadowed in normal use. This field is preserved as a typo guard (the
	// CRDV validates the value appears in LLMProviders) and for forward
	// compatibility, but does NOT reliably switch the active inference
	// provider at runtime. To change providers in practice, edit
	// $HERMES_HOME/config.yaml's model.provider on the PVC and restart the
	// pod. See docs/research/2026-05-29-hermes-inference-provider-verification.md
	// for full analysis.
	//
	// MaxLength bounds the cost of the cross-field CEL rule on HermesAgentSpec.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	LLMDefaultProvider string `json:"llmDefaultProvider,omitempty"`

	// LLMProviders configures available LLM providers. Each entry's env[]
	// is appended to the container env.
	//
	// MaxItems bounds the cost of the cross-field CEL rule on HermesAgentSpec
	// that scans this list to validate llmDefaultProvider.
	// +kubebuilder:validation:MaxItems=64
	// +optional
	LLMProviders []HermesAgentLLMProvider `json:"llmProviders,omitempty"`

	// Gateways configures messaging gateways. Each entry's env[] is appended
	// to the container env.
	// +optional
	Gateways []HermesAgentGateway `json:"gateways,omitempty"`

	// RBAC declares RoleBindings and ClusterRoleBindings the operator should
	// create for the agent's ServiceAccount.
	// +optional
	RBAC HermesAgentRBAC `json:"rbac,omitempty"`

	// Dashboard configures the optional hermes dashboard sidecar (web UI + REST API).
	// +optional
	Dashboard HermesAgentDashboardSpec `json:"dashboard,omitempty"`

	// NetworkPolicy optionally configures per-agent network isolation via a
	// generated NetworkPolicy resource. All ingress/egress rules pass through
	// unchanged to the K8s NetworkPolicy spec — no operator-side defaults are
	// injected. Effective enforcement depends on the cluster's CNI (Calico,
	// Cilium, etc.); kindnet creates the resource but does not enforce.
	// +optional
	NetworkPolicy HermesAgentNetworkPolicy `json:"networkPolicy,omitempty"`
}

// HermesAgentNetworkPolicy controls per-agent network isolation. When Enabled,
// the operator reconciles a NetworkPolicy resource targeting this agent's pod.
// All ingress/egress rules are direct passthrough of K8s native types — no
// operator-side smart defaults, no rewriting. Users who want operator-side
// probing of the dashboard sidecar must include the operator's namespace in
// the Ingress rules. See docs/operator/install.md for common patterns.
// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled || (has(self.ingress) && size(self.ingress) > 0) || (has(self.egress) && size(self.egress) > 0)",message="networkPolicy.enabled is true but ingress and egress are both empty — this creates a deny-all policy. If intentional, omit ingress/egress; if not, populate one of them."
type HermesAgentNetworkPolicy struct {
	// Enabled toggles NetworkPolicy generation. When false, any
	// previously-generated NetworkPolicy is deleted by the reconciler.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Ingress rules applied to the agent pod. Direct passthrough to the
	// NetworkPolicy resource's spec.ingress. When PolicyTypes includes
	// "Ingress" and this slice is empty, ALL ingress is denied.
	// +optional
	Ingress []networkingv1.NetworkPolicyIngressRule `json:"ingress,omitempty"`

	// Egress rules applied to the agent pod. Direct passthrough to the
	// NetworkPolicy resource's spec.egress. When PolicyTypes includes
	// "Egress" and this slice is empty, ALL egress is denied.
	// +optional
	Egress []networkingv1.NetworkPolicyEgressRule `json:"egress,omitempty"`

	// PolicyTypes selects which directions of traffic the policy governs.
	// Common values: ["Ingress", "Egress"]. If empty, K8s derives the policy
	// types from whether Ingress/Egress slices are non-nil.
	// +optional
	PolicyTypes []networkingv1.PolicyType `json:"policyTypes,omitempty"`
}

// HermesAgentDashboardSpec configures the dashboard sidecar. Default is off.
type HermesAgentDashboardSpec struct {
	// Enabled toggles the dashboard sidecar container.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Image to run the dashboard from. Defaults to spec.image (same hermes binary).
	// +optional
	Image string `json:"image,omitempty"`

	// Resources applied to the dashboard sidecar container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Service exposes the dashboard inside the cluster.
	// +optional
	Service HermesAgentDashboardService `json:"service,omitempty"`

	// Ingress optionally fronts the dashboard externally.
	// +optional
	Ingress HermesAgentDashboardIngress `json:"ingress,omitempty"`
}

// HermesAgentDashboardService configures the dashboard's Service.
type HermesAgentDashboardService struct {
	// Type of the dashboard Service. Defaults to ClusterIP. NodePort and
	// LoadBalancer are accepted but ill-advised: the dashboard has no edge-side
	// auth without an Ingress that gates it.
	// +kubebuilder:default=ClusterIP
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +optional
	Type corev1.ServiceType `json:"type,omitempty"`

	// Annotations passed through to the dashboard Service (e.g., cloud-LB tuning).
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// HermesAgentDashboardIngress configures the dashboard's Ingress. Disabled by
// default. When enabled, host and ingressClassName are required. The operator
// passes annotations through unchanged — auth is the user's responsibility
// (e.g., nginx.ingress.kubernetes.io/auth-url annotations, oauth2-proxy
// fronting, etc.). When ingress is enabled without any annotations,
// authentication is almost certainly absent — see docs for guidance.
// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled || (has(self.host) && size(self.host) > 0)",message="dashboard.ingress.host is required when dashboard.ingress.enabled is true"
// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled || (has(self.ingressClassName) && size(self.ingressClassName) > 0)",message="dashboard.ingress.ingressClassName is required when dashboard.ingress.enabled is true"
type HermesAgentDashboardIngress struct {
	// Enabled toggles Ingress reconciliation.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// IngressClassName names the IngressClass. Required when Enabled.
	// +optional
	IngressClassName string `json:"ingressClassName,omitempty"`

	// Host is the external hostname for the Ingress. Required when Enabled.
	// +optional
	Host string `json:"host,omitempty"`

	// TLS configuration (optional).
	// +optional
	TLS *HermesAgentDashboardIngressTLS `json:"tls,omitempty"`

	// Annotations applied to the Ingress object. Auth is YOUR responsibility:
	// add nginx.ingress.kubernetes.io/auth-url, traefik middleware refs,
	// oauth2-proxy fronting, etc. here. cert-manager.io annotations are also
	// safe to add for TLS provisioning.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// HermesAgentDashboardIngressTLS configures TLS on the Ingress.
type HermesAgentDashboardIngressTLS struct {
	// SecretName names the TLS Secret. Either pre-create it or use
	// cert-manager annotations on Ingress.Annotations to provision.
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="(has(self.existingClaimName) && size(self.existingClaimName) > 0) != (has(self.persistentVolumeClaim) && size(self.persistentVolumeClaim.accessModes) > 0)",message="set exactly one of storage.existingClaimName or storage.persistentVolumeClaim"
// HermesAgentStorage configures the agent's PVC.
type HermesAgentStorage struct {
	// PersistentVolumeClaim is the spec of the PVC the operator creates
	// for HERMES_HOME (/opt/data in the container). Mutually exclusive
	// with ExistingClaimName — set exactly one.
	// +optional
	PersistentVolumeClaim corev1.PersistentVolumeClaimSpec `json:"persistentVolumeClaim,omitempty"`

	// ExistingClaimName mounts a pre-existing PVC of this name instead of
	// the operator generating one. When set, the operator does NOT create,
	// reconcile, or own a PVC — it mounts the named claim verbatim and
	// RetainPolicy is ignored (the operator never owns a pre-existing PVC).
	// Use this to adopt a PVC whose name doesn't match the operator's
	// generated `hermes-<name>-data` convention (e.g. migrating a workload
	// with a legacy claim name).
	// +optional
	ExistingClaimName string `json:"existingClaimName,omitempty"`

	// RetainPolicy on HermesAgent deletion. Retain keeps the PVC (default —
	// strongly recommended for stateful agents). Delete removes it. Ignored
	// when ExistingClaimName is set (the operator does not own that PVC).
	// +kubebuilder:default=Retain
	// +kubebuilder:validation:Enum=Retain;Delete
	// +optional
	RetainPolicy HermesAgentRetainPolicy `json:"retainPolicy,omitempty"`
}

// HermesAgentRetainPolicy controls what happens to the PVC when the CR is deleted.
type HermesAgentRetainPolicy string

const (
	RetainPolicyRetain HermesAgentRetainPolicy = "Retain"
	RetainPolicyDelete HermesAgentRetainPolicy = "Delete"
)

// HermesAgentLLMProvider describes one available LLM provider.
type HermesAgentLLMProvider struct {
	// Name is the provider identifier (e.g. "deepseek", "anthropic").
	// Used for status reporting and to match spec.llmDefaultProvider.
	// MaxLength bounds the cost of the cross-field CEL rule on HermesAgentSpec.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Models the agent is sanctioned to use with this provider. Informational
	// only — not enforced by the operator.
	// +optional
	Models []string `json:"models,omitempty"`

	// Env vars rendered onto the container for this provider. Typically
	// <NAME>_API_KEY via valueFrom.secretKeyRef plus optional base URL.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`
}

// HermesAgentGateway describes one configured messaging gateway.
type HermesAgentGateway struct {
	// Type discriminator (e.g. "discord", "telegram", "whatsapp"). Used
	// for status reporting and (in future phases) per-gateway readiness
	// probing of /health/detailed.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`

	// Env vars rendered onto the container for this gateway. Typically
	// the bot token via valueFrom.secretKeyRef plus allowlist CSVs.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`
}

// HermesAgentRBAC declares the RBAC bindings the operator should create
// for the agent's ServiceAccount. Both lists carry rbacv1.RoleRef — the
// named Role / ClusterRole must already exist; the operator never creates
// roles, only bindings. The agent's SA is always the implicit subject.
type HermesAgentRBAC struct {
	// RoleBindings creates one RoleBinding per entry in the named namespace.
	// +optional
	RoleBindings []HermesAgentRoleBinding `json:"roleBindings,omitempty"`

	// ClusterRoleBindings creates one ClusterRoleBinding per entry.
	// Bounded by the operator's allowedClusterRoles allowlist (set at
	// install time). An entry referencing a ClusterRole NOT in the
	// allowlist causes reconciliation failure with a clear status reason.
	// +optional
	ClusterRoleBindings []HermesAgentClusterRoleBinding `json:"clusterRoleBindings,omitempty"`
}

// HermesAgentRoleBinding describes one namespace-scoped binding.
type HermesAgentRoleBinding struct {
	// Namespace is the target namespace for the binding.
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`

	// RoleRef references the Role or ClusterRole to bind into Namespace.
	// +kubebuilder:validation:Required
	RoleRef rbacv1.RoleRef `json:"roleRef"`
}

// HermesAgentClusterRoleBinding describes one cluster-scoped binding.
type HermesAgentClusterRoleBinding struct {
	// RoleRef references the ClusterRole. The named role must be present
	// in the operator's allowedClusterRoles allowlist.
	// +kubebuilder:validation:Required
	RoleRef rbacv1.RoleRef `json:"roleRef"`
}

// Condition type constants added in Phase 2.
const (
	ConditionRBACSynced = "RBACSynced"
)

// HermesAgentGatewayStatus reports per-gateway connection state.
type HermesAgentGatewayStatus struct {
	// Type matches spec.gateways[].type (e.g., "discord", "telegram").
	Type string `json:"type"`
	// Ready is true iff gateway_running AND this platform's State is "connected".
	Ready bool `json:"ready"`
	// State is the raw platform state from /api/status — connecting | connected |
	// disconnected | retrying | fatal. Empty when the platform has not yet
	// reported (e.g., during startup or when the dashboard is not enabled).
	// +optional
	State string `json:"state,omitempty"`
	// Message carries error_message from /api/status when State is fatal. Empty
	// otherwise.
	// +optional
	Message string `json:"message,omitempty"`
	// LastProbedAt is set on each successful dashboard probe.
	// +optional
	LastProbedAt *metav1.Time `json:"lastProbedAt,omitempty"`
}

// HermesAgentLLMStatus reports LLM-provider observed state.
type HermesAgentLLMStatus struct {
	Current string `json:"current,omitempty"`
}

// Condition type constants added in Phase 3.
const (
	ConditionGatewaysReady = "GatewaysReady"
)

// Condition type constants added in Phase 4.
const (
	ConditionSecretsResolved = "SecretsResolved"
)

// Condition type constants added in Phase 10.7 (kubernetes exec backend).
const (
	// ConditionExecBackendReady reports that the operator has provisioned the
	// scoped Role + RoleBinding + no-perms session ServiceAccount for the
	// kubernetes exec backend. Only set when spec.execBackend == "kubernetes"
	// and the operator manages the SA; absent otherwise. Used by the
	// reconciler as a short-circuit signal: when this condition is absent and
	// the backend is not kubernetes, the reconciler skips the cleanup-Delete
	// API calls (nothing was ever created).
	ConditionExecBackendReady = "ExecBackendReady"
)

// HermesAgentStatus is the observed state of HermesAgent.
type HermesAgentStatus struct {
	// Phase is the high-level lifecycle phase.
	// +kubebuilder:validation:Enum=Bootstrap;Provisioning;Ready;Degraded;Suspended
	// +optional
	Phase HermesAgentPhase `json:"phase,omitempty"`

	// ObservedImage records the image the operator most recently rolled
	// onto the agent's Deployment.
	// +optional
	ObservedImage string `json:"observedImage,omitempty"`

	// ServiceAccountName records the SA the operator created or selected.
	// External RoleBinding/ClusterRoleBinding objects should reference this.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Conditions report detailed state via the standard K8s pattern.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Gateways reports per-gateway runtime state. This is populated ONLY when
	// spec.dashboard.enabled=true (Phase 7b) — the operator polls the dashboard
	// sidecar's /api/status endpoint to populate it. Without the dashboard, the
	// operator has no observable per-gateway runtime data and this field stays
	// nil. Pod readiness alone (status.podReady) reflects overall health.
	// +optional
	Gateways []HermesAgentGatewayStatus `json:"gateways,omitempty"`

	// LLMProvider reports observed LLM provider state.
	// +optional
	LLMProvider HermesAgentLLMStatus `json:"llmProvider,omitempty"`
}

// HermesAgentPhase is the high-level lifecycle phase of a HermesAgent.
type HermesAgentPhase string

const (
	PhaseBootstrap    HermesAgentPhase = "Bootstrap"
	PhaseProvisioning HermesAgentPhase = "Provisioning"
	PhaseReady        HermesAgentPhase = "Ready"
	PhaseDegraded     HermesAgentPhase = "Degraded"
	PhaseSuspended    HermesAgentPhase = "Suspended"
)

// Condition type constants used in Status.Conditions.
const (
	ConditionPodReady = "PodReady"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.status.observedImage`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HermesAgent is the Schema for the hermesagents API.
type HermesAgent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HermesAgentSpec   `json:"spec,omitempty"`
	Status HermesAgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HermesAgentList contains a list of HermesAgent.
type HermesAgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HermesAgent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HermesAgent{}, &HermesAgentList{})
}
