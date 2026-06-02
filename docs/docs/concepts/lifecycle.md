# Lifecycle, phases, and conditions

This page explains how a `HermesAgent` CR moves from `kubectl apply` through `Ready` and onward to deletion, so you can read `status` and know what to look at when something is stuck.

## Phases

`status.phase` is one of five values, computed by the operator from the underlying pod and condition state. There is no manual transition.

| Phase | When you see it |
|---|---|
| `Bootstrap` | Pre-flight checks have not all passed. Today this means a `secretKeyRef` or `envFrom.secretRef` in the spec cannot be resolved. The operator refuses to create the Pod until this clears. |
| `Provisioning` | Pre-flight checks have passed; child resources are being applied and the Pod is still becoming ready. |
| `Ready` | The Pod's `PodReady` condition is True, and (when `spec.dashboard.enabled` is set) at least one gateway is `connected`. |
| `Degraded` | The Pod is not ready, RBAC failed, the dashboard reports a fatal gateway state, or the agent reported `gateway_state == degraded`. |
| `Suspended` | `spec.suspend: true`: the agent is intentionally scaled to zero. The CR, PVC, SA, and Service all remain. `PodReady` is `False` with reason `Suspended`. See [Suspend an agent](../how-to/suspend-an-agent.md). |

If you only ever look at one field on a HermesAgent, look at `status.phase`.

## Conditions

`status.conditions[]` uses the standard `metav1.Condition` shape (`type`, `status`, `reason`, `message`, `lastTransitionTime`).

| Type | Meaning |
|---|---|
| `PodReady` | True iff the agent Pod's `PodReady` core condition is True. |
| `SecretsResolved` | True when every `secretKeyRef` and `envFrom.secretRef` in the spec resolves to a real Secret (and key, if specified). |
| `RBACSynced` | True when all spec-driven `roleBindings[]` and `clusterRoleBindings[]` apply cleanly. False with `reason=ClusterRoleNotAllowed` when a ClusterRoleBinding references a role outside the `--allowed-cluster-roles` allowlist. |
| `GatewaysReady` | True when every entry in `status.gateways[]` has `ready: true`. Only populated when `spec.dashboard.enabled: true`; absent otherwise. |

A single False condition does not by itself drive `Phase=Degraded`. The phase reflects the aggregate over pod readiness, conditions, and (when present) per-gateway state.

## Finalizer

The operator stamps a finalizer on every HermesAgent CR on first reconcile. The finalizer is what guarantees the operator gets to run cleanup before the CR disappears from etcd. On `kubectl delete hermesagent <name>`:

1. The operator scrubs custom metrics (`hermes_agent_*`) for the CR, leaving no ghost gauges in Prometheus.
2. Resources that do not carry an ownerRef are deleted explicitly (e.g. the NetworkPolicy when it was toggled off in-place rather than deleted with the CR).
3. The PVC retention policy is respected: `Retain` (default) leaves the PVC alone; `Delete` cascades it via ownerRef GC.
4. Owned children (Deployment, ServiceAccount, Service, self-Role + RoleBinding, optional dashboard objects) cascade-delete via standard Kubernetes garbage collection.
5. ClusterRoleBindings cannot carry an ownerRef pointing to a namespaced CR, so the operator deletes them explicitly.
6. The operator removes the finalizer; the CR is gone.

## Two ways to restart a pod

There are two distinct restart paths and they do different things.

**In-container restart**: the gateway's `/restart` slash command exits the gateway process with code 75. The Pod's `restartPolicy` restarts the container in place. The Pod (and the PVC mount) is preserved. The operator is not involved. Use this when you want to recycle the gateway without re-mounting the PVC.

**Pod-level rollout**: `kubectl rollout restart deployment/hermes-<name>`. This bumps a timestamp annotation on the Pod template, which (combined with `strategy: Recreate`) tears down the old pod, waits for it to release the `gateway.lock` flock, and starts a fresh one. Use this for a clean boot, for example after editing `/opt/data/config.yaml` so the new process re-reads it.

The per-agent self-introspection Role grants `apps/deployments[<self>]: get, patch` so the agent can run `kubectl rollout restart` against itself from inside the pod. See [The RBAC model](rbac-model.md).

## Termination

The agent Pod template hardcodes `terminationGracePeriodSeconds: 210`. Breakdown:

- **180s**: upstream's `agent.restart_drain_timeout` default. The gateway drains active turns within this window.
- **30s**: buffer for Kubernetes + `tini -g` + the entrypoint's signal forwarding + post-drain teardown (closing SessionDB, releasing `gateway.lock`, writing the `.clean_shutdown` marker).

Kubernetes' default 30s would SIGKILL the pod mid-drain: in-flight turns get abandoned, `.clean_shutdown` is never written, and the next boot calls `suspend_recently_active()` on every session that was live at kill time.

If you raise `agent.restart_drain_timeout` in `config.yaml` past about 180s, the 210s ceiling is not exposed as a spec field today. Open an issue or keep the drain timeout under the ceiling.

## Status freshness

Without the dashboard sidecar, status updates are event-driven: pod readiness changes drive Deployment-watch reconciles; spec edits drive CR-watch reconciles. There is no polling loop.

With `spec.dashboard.enabled: true`, the operator also requeues every 30s to poll the sidecar's `/api/status` and refresh `status.gateways[]`. That is the only reason the dashboard-enabled path has a periodic reconcile at all.

## See also

- [How a HermesAgent runs in your cluster](architecture.md) — what objects the operator creates.
- [The RBAC model](rbac-model.md) — self-introspection vs. spec-driven grants.
- [Known Hermes upstream behaviours](upstream-behaviours.md) — the `gateway.lock` flock contract, signal handling.
- [Troubleshoot an agent that isn't Ready](../how-to/troubleshoot-not-ready.md) — phase-driven decision tree.
