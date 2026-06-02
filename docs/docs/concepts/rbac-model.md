# The RBAC model

This page explains what RBAC the operator creates for each agent, so a security reviewer can answer "what could an agent do if its API key leaked?" without reading the source.

There are three buckets of RBAC. They are independent of each other.

## 1. Self-introspection (always created, hardcoded)

The operator creates `Role/hermes-<name>-self` and `RoleBinding/hermes-<name>-self` in the agent's namespace for every CR. The user spec does not shape these; the grant is hardcoded.

| Resource | Verbs | `resourceNames` |
|---|---|---|
| `apps/deployments` | `get`, `patch` | `hermes-<name>` |
| `hermes.k8s.undermountain.cc/hermesagents` | `get` | `<name>` |

What this lets the agent do:

- Read its own desired spec and observed status (`get` on the HermesAgent CR).
- Read its own Deployment (`get` on `apps/deployments`).
- Restart itself with `kubectl rollout restart deployment/hermes-<name>` (`patch` on its own Deployment; that command is a strategic-merge patch of the Pod template's `kubectl.kubernetes.io/restartedAt` annotation).

What it explicitly does not let the agent do:

- `pods:*`: the canonical "restart yourself" path is the gateway's `/restart` slash command. Direct pod access is not needed.
- `list` or `watch` on either resource: these verbs cannot be scoped by `resourceNames`, so granting either would expose sibling agents in the same namespace.
- `delete`, `create`, `update`, or `*`: none of these are needed for the rollout-restart workflow, and `delete` on the agent's own Deployment would force a needless outage.

One Role per agent, pinned by `resourceNames` to that agent's own objects. A cluster-wide Role cannot express that pinning, which is why this uses a per-agent Role rather than a shared ClusterRole.

Verify it:

```bash
kubectl -n hermes auth can-i patch deployment/hermes-my-agent \
    --as system:serviceaccount:hermes:hermes-my-agent
# yes

kubectl -n hermes auth can-i list pods \
    --as system:serviceaccount:hermes:hermes-my-agent
# no
```

## 2. Spec-driven `RoleBinding`s (reference-only)

`spec.rbac.roleBindings[]` lets you bind the agent's ServiceAccount to existing namespace-scoped Roles or ClusterRoles. The operator creates a `RoleBinding` in the target namespace; it does not create the Role it references.

| Field | Verbs the operator uses |
|---|---|
| `roleBindings[].namespace` | Target namespace for the binding. |
| `roleBindings[].roleRef` | `rbacv1.RoleRef`. Must reference a Role or ClusterRole that already exists. |

What the agent can do under this grant is bounded by what the referenced Role grants. Namespace-scoped bindings keep the blast radius within that namespace.

What is deliberately not done:

- The operator does not create or define the referenced Role. If `roleRef.name` does not exist, the binding will be created but is a no-op until you create the Role.
- There is no operator-side allowlist for namespace-scoped bindings; namespace boundaries do that job.

## 3. Spec-driven `ClusterRoleBinding`s (gated by an allowlist)

`spec.rbac.clusterRoleBindings[]` lets you bind the agent's ServiceAccount to existing ClusterRoles, granting cluster-wide rights. **These are gated by the operator's install-time `--allowed-cluster-roles` flag** (Helm value `operator.allowedClusterRoles`).

| Field | Verbs the operator uses |
|---|---|
| `clusterRoleBindings[].roleRef` | `rbacv1.RoleRef`. Must reference a ClusterRole present in the operator's allowlist. |

The default allowlist is empty, which means no cluster-scoped bindings can be created until the cluster admin opts in. When a CR references a ClusterRole that is not in the allowlist, reconciliation fails with `RBACSynced=False` and a clear reason:

```
RBACSynced=False  reason=ClusterRoleNotAllowed
  message: ClusterRole "cluster-admin" not in allowlist; set operator.allowedClusterRoles to permit
```

The CR's phase flips to `Degraded`. To resolve: add the role to the allowlist via Helm upgrade and restart the operator, or remove the entry from the CR.

Without the allowlist, a tenant with write access to `HermesAgent` CRs could grant their agent `cluster-admin`. The allowlist puts the cluster admin in control of which cluster-wide privileges are reachable through this CRD.

## How the operator grants rights it does not itself hold

Kubernetes blocks privilege escalation: to create a binding that grants rights, the principal must hold those rights itself or have the `escalate` verb on the role being bound. The operator's own ClusterRole carries `bind` + `escalate` on `rbac.authorization.k8s.io/{clusterroles,roles}`, so it can manage RBAC bindings without being cluster-admin itself, which keeps the operator's blast radius narrower.

The practical guard rail on what bindings agents may request remains the per-install `--allowed-cluster-roles` allowlist described above.

## Bring your own ServiceAccount

Set `spec.serviceAccountName: <existing-sa>` to make the operator use a SA you already manage instead of creating its own `hermes-<name>`. The operator still creates the self-introspection Role + RoleBinding against the named SA, and spec-driven RBAC bindings target the same SA. Use this when your team mints SAs through a separate flow (e.g. Kyverno-mutated SAs with IRSA annotations).

## See also

- [Reference: HermesAgent API](../reference/api-reference.md#hermesagentrbac) — every RBAC field.
- [Reference: Operator CLI flags](../reference/cli-flags.md) — `--allowed-cluster-roles`.
- [Reference: Helm values](../reference/helm-values.md) — `operator.allowedClusterRoles`.
