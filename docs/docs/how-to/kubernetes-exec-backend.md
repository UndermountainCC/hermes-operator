# Run sessions in their own pods (kubernetes exec backend)

!!! info "Background"
    For the why-and-how of this feature in long form, see [Per-Session Pods: How hermes-operator Sandboxes Shell-Calling Agents](https://undermountain.cc/blog/kubernetes-exec-backend-isolated-session-pods) on the Undermountain blog. This page is the operational reference.

This guide shows you how to enable `spec.execBackend: kubernetes`, which makes the agent create a separate Pod for each terminal/code-execution session via the in-cluster API. Sessions inherit
no cluster credentials (the session ServiceAccount is powerless and has
`automountServiceAccountToken: false`), and the shape of pods the agent can
create is constrained by a cluster `ValidatingAdmissionPolicy` (VAP). Together, these two controls are what make `pods/create` safe to grant.

## Prerequisites

- Kubernetes **>= 1.30** (`ValidatingAdmissionPolicy` GA, including the
  `quantity()` CEL extension).
- A hermes-agent image that bundles the `kubernetes` client extra (the
  agent's `kubernetes_subprocess.ExecutionBackend` driver imports it lazily).
  The agent-side backend is proposed upstream in
  [NousResearch/hermes-agent#37591](https://github.com/NousResearch/hermes-agent/pull/37591);
  until it ships in a tagged release, use an image built from that branch
  (e.g. `ghcr.io/undermountaincc/hermes-agent`).
- The session-pod VAP installed cluster-wide (see below). The CR's
  `spec.execBackend: kubernetes` reconciles the per-agent Role / RoleBinding /
  session SA regardless, but **without the VAP the bare `pods/create` Role
  permits arbitrary pod shape** (privileged, hostNetwork, mount any PVC,
  steal credentials by referencing other SAs).

## Install the cluster-side VAP (one-time)

```bash
kubectl apply -k config/admission-policy/
```

This installs `ValidatingAdmissionPolicy` and `ValidatingAdmissionPolicyBinding`
both named `hermes-session-pod-security`. The binding is cluster-wide; the
policy's `matchConditions` keys on `request.userInfo.username` matching
`^system:serviceaccount:[^:]+:hermes-[^:]+$`, so only pod creates from
operator-managed agent SAs are evaluated.

## Enable on a HermesAgent

```yaml
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata:
  name: shell-agent
  namespace: hermes
spec:
  image: ghcr.io/undermountaincc/hermes-agent:v2026.5.23
  execBackend: kubernetes        # default is "local"
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 10Gi } }
  llmDefaultProvider: deepseek
  llmProviders:
    - name: deepseek
      env:
        - name: DEEPSEEK_API_KEY
          valueFrom:
            secretKeyRef: { name: shell-creds, key: DEEPSEEK_API_KEY }
  env:
    - name: TERMINAL_ENV
      value: kubernetes
```

When `spec.execBackend: kubernetes`, the operator provisions:

- `Role/hermes-shell-agent-exec`: `pods`, `pods/log`, `pods/exec`,
  `persistentvolumeclaims` (create/get/list/watch/delete; create+get on
  pods/exec). Not resourceNames-pinned, since session pod names are
  agent-chosen at runtime; the VAP constrains pod *shape* instead.
- `RoleBinding/hermes-shell-agent-exec`: binds the agent's SA to that Role.
- `ServiceAccount/hermes-shell-agent-session`: the powerless identity each
  session pod runs as: `automountServiceAccountToken: false`, no bindings.

The agent's `kubernetes_subprocess` driver creates sessions in the SAME
namespace as the agent. Session pods are named `hermes-ws-<task_id>` (the
PVC name allowlist the VAP enforces).

## BYO-SA opt-out

When `spec.serviceAccountName` is user-provided, the operator does not layer
exec-backend grants on that identity. BYO SAs may be shared across multiple HermesAgent CRs, and silently
expanding what a shared SA can do would violate the user's "I manage this
SA's permissions" contract and let any agent in the group `exec` into any
sibling's session pods.

If you need the kubernetes exec backend with a user-managed SA, create the
Role / RoleBinding / session SA yourself (mirror the operator's hardcoded
shape; see `internal/controller/exec_rbac.go`).

## Toggling off

Patching `spec.execBackend` from `kubernetes` back to `local` (or removing
the field; the CRD default is `local`) triggers the reconciler to delete
the exec Role, RoleBinding, and session SA on the next pass. The agent
container itself must be rebooted (or signaled) to pick up the new
`TERMINAL_ENV`; the operator does not auto-rollout on the spec change.

## Status condition

`status.conditions[type=ExecBackendReady]` reflects whether the exec RBAC
has been provisioned. The condition is `True` with reason
`KubernetesExecProvisioned` once the three child objects are applied, and
absent otherwise. The condition is used internally as a short-circuit
signal: when present, the reconciler's BYO/toggle-off cleanup path runs;
when absent, the cleanup path skips three NotFound Delete round-trips per
reconcile.

## Cross-link

The agent-side implementation (the `kubernetes_subprocess` backend, the
`TERMINAL_ENV=kubernetes` plumbing, the lazy-loadable Python dep) lives in
the upstream [hermes-agent](https://github.com/NousResearch/hermes-agent)
repo, contributed in
[PR #37591](https://github.com/NousResearch/hermes-agent/pull/37591). This
operator provides the cluster-side half — the per-agent exec RBAC, the
powerless session ServiceAccount, and the session-pod `ValidatingAdmissionPolicy`
that make granting `pods/create` safe. Track that PR for the upstream status;
once it lands in a tagged release, the stock `nousresearch/hermes-agent` image
works without a custom build.
