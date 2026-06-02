# API reference

`hermes.k8s.undermountain.cc/v1alpha1` / `HermesAgent`. Namespaced resource.

Hand-maintained from the godoc on `api/v1alpha1/hermesagent_types.go`. For the canonical machine-readable schema, see [`config/crd/bases/hermes.k8s.undermountain.cc_hermesagents.yaml`](https://github.com/UndermountainCC/hermes-operator/blob/main/config/crd/bases/hermes.k8s.undermountain.cc_hermesagents.yaml).

## `HermesAgentSpec`

| Field | Type | Default | Description |
|---|---|---|---|
| `image` | `string` | — *(required)* | Fully-qualified container image reference. Should include digest (`registry/repo@sha256:…`) in production. |
| `imagePullPolicy` | `corev1.PullPolicy` | `IfNotPresent` | Standard K8s pull policy. |
| `imagePullSecrets` | `[]corev1.LocalObjectReference` | `[]` | Registry credentials for private images. |
| `serviceAccountName` | `string` | `""` (operator creates `hermes-<name>`) | Use a pre-existing SA. See [The RBAC model](../concepts/rbac-model.md#bring-your-own-serviceaccount). |
| `storage` | `HermesAgentStorage` | — *(required)* | PVC config. See below. |
| `suspend` | `bool` | `false` | Scale the agent to zero replicas without deleting the CR or PVC. `status.phase` becomes `Suspended`. Reverse by setting back to `false`. See [Suspend an agent](../how-to/suspend-an-agent.md). |
| `resources` | `corev1.ResourceRequirements` | `{}` | Applied to the agent container directly. |
| `nodeSelector` | `map[string]string` | `{}` | Standard pod placement. |
| `tolerations` | `[]corev1.Toleration` | `[]` | Standard pod placement. |
| `affinity` | `*corev1.Affinity` | `nil` | Standard pod placement. |
| `env` | `[]corev1.EnvVar` | `[]` | Injected into the agent container AFTER per-provider and per-gateway env. Overrides those on key conflict. Operator-stamped vars (`HERMES_INFERENCE_PROVIDER`, field refs, identity) are appended LAST and override everything. |
| `envFrom` | `[]corev1.EnvFromSource` | `[]` | Standard envFrom. |
| `llmDefaultProvider` | `string` | `""` | Becomes `HERMES_INFERENCE_PROVIDER`. The CRD validates that it matches an entry in `llmProviders[].name`. **Caveat: does not reliably switch the active provider.** Upstream resolves the active provider from `$HERMES_HOME/config.yaml`'s `model.provider` before falling back to this env var. Because `config.yaml` is seeded on first boot with a concrete value, this field is shadowed in normal use. To switch providers in practice, edit `$HERMES_HOME/config.yaml` on the PVC and restart; see [Change the LLM provider for an agent](../how-to/change-llm-provider.md). |
| `llmProviders` | `[]HermesAgentLLMProvider` | `[]` | Available LLM providers. See below. |
| `gateways` | `[]HermesAgentGateway` | `[]` | Messaging gateways. See below. |
| `rbac` | `HermesAgentRBAC` | `{}` | Declarative RBAC bindings. See below. |
| `dashboard` | `HermesAgentDashboardSpec` | `{enabled: false}` | Dashboard sidecar. See below. |
| `networkPolicy` | `HermesAgentNetworkPolicy` | `{enabled: false}` | Per-agent NetworkPolicy. See below. |

## `HermesAgentStorage`

Exactly one of `persistentVolumeClaim` or `existingClaimName` must be set (a CEL one-of rule enforces this).

| Field | Type | Default | Description |
|---|---|---|---|
| `persistentVolumeClaim` | `corev1.PersistentVolumeClaimSpec` | `{}` | Native K8s PVC spec. The operator creates this PVC and mounts it at `/opt/data` (`$HERMES_HOME`). Mutually exclusive with `existingClaimName`. |
| `existingClaimName` | `string` | `""` | Name of a pre-existing PVC to mount. The operator does NOT create, reconcile, or own the PVC — it mounts it verbatim. `retainPolicy` is ignored. See [Adopt a pre-existing PVC](../concepts/storage.md#adopting-a-pre-existing-pvc). |
| `retainPolicy` | `Retain` \| `Delete` | `Retain` | `Retain`: CR delete leaves the PVC. `Delete`: CR delete cascades to the PVC via ownerRef GC. Ignored when `existingClaimName` is set. |

**See [Storage](../concepts/storage.md) for PVC sovereignty semantics.**

## `HermesAgentLLMProvider`

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | `string` | — *(required)* | Provider identifier (`deepseek`, `anthropic`, etc.). Used for status reporting and matched against `spec.llmDefaultProvider`. |
| `models` | `[]string` | `[]` | Informational only, not enforced by the operator. Useful for documentation in-CR. |
| `env` | `[]corev1.EnvVar` | `[]` | Per-provider env. Typically `<NAME>_API_KEY` via `valueFrom.secretKeyRef` and optional base URL. Operator concatenates onto the container env. |

## `HermesAgentGateway`

| Field | Type | Default | Description |
|---|---|---|---|
| `type` | `string` | — *(required)* | Discriminator (`discord`, `telegram`, `whatsapp`, etc.). |
| `env` | `[]corev1.EnvVar` | `[]` | Per-gateway env vars: bot tokens via `secretKeyRef`, allowlist CSVs, etc. |

## `HermesAgentRBAC`

| Field | Type | Default | Description |
|---|---|---|---|
| `roleBindings` | `[]HermesAgentRoleBinding` | `[]` | Creates one `RoleBinding` per entry in the named namespace. |
| `clusterRoleBindings` | `[]HermesAgentClusterRoleBinding` | `[]` | Creates one `ClusterRoleBinding` per entry. **Bounded by `--allowed-cluster-roles` allowlist.** Entries referencing roles not in the allowlist fail reconciliation with `RBACSynced=False`. |

### `HermesAgentRoleBinding`

| Field | Type | Default | Description |
|---|---|---|---|
| `namespace` | `string` | — *(required)* | Target namespace for the binding. |
| `roleRef` | `rbacv1.RoleRef` | — *(required)* | Reference to the existing Role or ClusterRole. The operator does NOT create roles. |

### `HermesAgentClusterRoleBinding`

| Field | Type | Default | Description |
|---|---|---|---|
| `roleRef` | `rbacv1.RoleRef` | — *(required)* | Reference to the existing ClusterRole, which must be present in the operator's `--allowed-cluster-roles` allowlist. |

**See [The RBAC model](../concepts/rbac-model.md) for the full semantics.**

## `HermesAgentDashboardSpec`

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `bool` | `false` | Toggle the dashboard sidecar container. |
| `image` | `string` | `""` (falls back to `spec.image`) | Override dashboard image. Defaults to same `hermes` binary. |
| `resources` | `corev1.ResourceRequirements` | `{}` | Resources for the dashboard sidecar. |
| `service` | `HermesAgentDashboardService` | `{type: ClusterIP}` | Dashboard Service shape. |
| `ingress` | `HermesAgentDashboardIngress` | `{enabled: false}` | Optional Ingress to expose the dashboard externally. |

### `HermesAgentDashboardService`

| Field | Type | Default | Description |
|---|---|---|---|
| `type` | `ClusterIP` \| `NodePort` \| `LoadBalancer` | `ClusterIP` | Type of the dashboard Service. `NodePort` / `LoadBalancer` are accepted but ill-advised without edge-side auth. |
| `annotations` | `map[string]string` | `{}` | Passed through unchanged (e.g., cloud-LB tuning). |

### `HermesAgentDashboardIngress`

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `bool` | `false` | Toggle Ingress reconciliation. |
| `ingressClassName` | `string` | `""` | Required when `enabled: true`. |
| `host` | `string` | `""` | Required when `enabled: true`. |
| `tls` | `*HermesAgentDashboardIngressTLS` | `nil` | TLS Secret reference. |
| `annotations` | `map[string]string` | `{}` | Passed through. **Auth is your responsibility**: add `nginx.ingress.kubernetes.io/auth-url`, oauth2-proxy fronting, etc. here. See [Expose the dashboard externally with auth](../how-to/expose-dashboard.md). |

### `HermesAgentDashboardIngressTLS`

| Field | Type | Default | Description |
|---|---|---|---|
| `secretName` | `string` | `""` | TLS Secret name. Pre-create it or use `cert-manager.io` annotations on `ingress.annotations` to provision. |

**See [Enable the dashboard sidecar](../how-to/enable-dashboard.md) for usage.**

## `HermesAgentNetworkPolicy`

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `bool` | `false` | Toggle NetworkPolicy generation. When `false`, any previously-generated NetworkPolicy is deleted in-place. |
| `ingress` | `[]networkingv1.NetworkPolicyIngressRule` | `nil` | Direct passthrough to `spec.ingress`. Empty + `policyTypes` includes `Ingress` → ALL ingress denied. |
| `egress` | `[]networkingv1.NetworkPolicyEgressRule` | `nil` | Direct passthrough to `spec.egress`. Empty + `policyTypes` includes `Egress` → ALL egress denied. |
| `policyTypes` | `[]networkingv1.PolicyType` | `nil` | When empty, K8s derives from whether ingress/egress are non-nil. |

**See [Restrict agent network traffic with NetworkPolicy](../how-to/network-policy.md) for usage patterns and CNI caveats.**

## `HermesAgentStatus`

Reported by the operator; read-only.

| Field | Type | Description |
|---|---|---|
| `phase` | `Bootstrap` \| `Provisioning` \| `Ready` \| `Degraded` \| `Suspended` | High-level lifecycle phase. |
| `observedImage` | `string` | Image last rolled onto the Deployment. |
| `serviceAccountName` | `string` | SA the operator selected. |
| `conditions` | `[]metav1.Condition` | `PodReady`, `SecretsResolved`, `RBACSynced`, `GatewaysReady`. |
| `gateways` | `[]HermesAgentGatewayStatus` | Per-gateway runtime state. Only populated when `spec.dashboard.enabled=true`. |
| `llmProvider` | `HermesAgentLLMStatus` | Observed LLM provider; `current` reflects `spec.llmDefaultProvider`. |

### `HermesAgentGatewayStatus`

| Field | Type | Description |
|---|---|---|
| `type` | `string` | Matches `spec.gateways[].type`. |
| `ready` | `bool` | True iff `gateway_running` AND this platform's `state` is `connected`. |
| `state` | `string` | Raw upstream state: `connecting`, `connected`, `disconnected`, `retrying`, `fatal`. Empty until the dashboard has reported. |
| `message` | `string` | `error_message` from `/api/status` when `state == "fatal"`. |
| `lastProbedAt` | `*metav1.Time` | Set on each successful dashboard probe. |

## Print columns

`kubectl get hermesagent`:

```
NAME     PHASE   IMAGE                                            AGE
my-agent Ready   docker.io/nousresearch/hermes-agent:v2026.4.30   12d
```

## Example CRs

- [Quickstart](../tutorials/quickstart.md) for the smallest valid CR.
- [Your first HermesAgent (Discord)](../tutorials/your-first-hermesagent.md) for a realistic single-gateway agent.
