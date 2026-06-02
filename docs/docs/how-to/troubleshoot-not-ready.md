# Troubleshoot an agent that isn't Ready

This guide walks you from `kubectl get hermesagent` to a likely root cause, keyed on `status.phase`. For a flat symptom-keyed reference, see [Reference: Troubleshooting catalogue](../reference/troubleshooting.md).

## Step 1: read the phase

```bash
kubectl -n hermes get hermesagent my-agent
```

```
NAME       PHASE          IMAGE                                            AGE
my-agent   Bootstrap      docker.io/nousresearch/hermes-agent:v2026.4.30   3m
```

The `PHASE` column tells you which subsection below to read.

| Phase | What it means | Jump to |
|---|---|---|
| `Bootstrap` | The operator refuses to create the Pod yet. | [Bootstrap](#bootstrap) |
| `Provisioning` | Pod is being created or hasn't reached readiness yet. | [Provisioning](#provisioning) |
| `Ready` | The agent is healthy. Verify with [Verify a HermesAgent is healthy](verify-health.md). | — |
| `Degraded` | Something is wrong at runtime. | [Degraded](#degraded) |

## Bootstrap

Today this means a `secretKeyRef` or `envFrom.secretRef` in your spec did not resolve.

```bash
kubectl -n hermes describe hermesagent my-agent | grep -A2 SecretsResolved
```

If the message says a Secret or key is missing, create it (or fix the reference) and the operator re-reconciles within ~15s.

Common causes:

- The Secret is in a different namespace than the HermesAgent. Secret references are namespace-local; the Secret must live alongside the CR.
- A typo in `secretKeyRef.name` or `secretKeyRef.key`. Both must match exactly.
- The Secret was created after the HermesAgent and the operator hasn't re-reconciled yet. Wait ~15s or `kubectl annotate hermesagent my-agent kick=now --overwrite` to nudge it.

## Provisioning

The CR's gates passed but the Pod is not ready yet. Look at the Pod.

```bash
kubectl -n hermes get pod -l app.kubernetes.io/instance=my-agent
kubectl -n hermes describe pod -l app.kubernetes.io/instance=my-agent
```

If the Pod is missing entirely, the operator may have hit a reconcile error. Check operator logs:

```bash
kubectl -n hermes-operator-system logs deploy/hermes-operator-controller-manager --tail=50
```

If the Pod is `Pending`, it's a scheduling issue (`Insufficient cpu`, `node(s) had volume node affinity conflict`, etc.). Check `kubectl describe pod`'s Events section.

If the Pod is `Running` but `READY 0/1`, the readiness probe is failing. Tail the agent container's logs:

```bash
kubectl -n hermes logs deployment/hermes-my-agent -c agent --tail=100
```

Common patterns:

| Log substring | Likely cause | See |
|---|---|---|
| `discord.errors.PrivilegedIntentsRequired` | MESSAGE CONTENT INTENT not enabled. | [Set up a Discord bot](setup-discord-bot.md) |
| `HTTP 400 ... but you passed anthropic/claude-opus-4.6` | `config.yaml` model default does not match active provider. | [Change the LLM provider for an agent](change-llm-provider.md) |
| `Gateway already running (PID ...)` | A previous pod did not release the `gateway.lock` flock. | Check Deployment is `replicas: 1`, `strategy: Recreate`. |
| `PermissionError: ... /opt/data` | Container UID does not match PVC ownership. | File an issue with the pod spec. |
| `Cannot connect to ... :443` | Egress blocked. | Check NetworkPolicy egress rules, cluster egress firewall. |

## Degraded

Something is wrong at runtime. Check the conditions to narrow down.

```bash
kubectl -n hermes get hermesagent my-agent -o jsonpath='{.status.conditions}' | jq
```

| Condition False | Meaning | Fix |
|---|---|---|
| `PodReady=False` | The agent Pod's `PodReady` is False; usually the readiness probe is failing. | Check agent container logs as in [Provisioning](#provisioning). |
| `RBACSynced=False` with `reason=ClusterRoleNotAllowed` | A `spec.rbac.clusterRoleBindings[]` entry references a role outside the install-time allowlist. | Either remove the entry, or `helm upgrade --set operator.allowedClusterRoles=...` and restart the operator. See [The RBAC model](../concepts/rbac-model.md). |
| `GatewaysReady=False` | The dashboard sidecar reports at least one gateway as not connected. | Check `status.gateways[]` for the platform and message; see [Reference: Troubleshooting catalogue](../reference/troubleshooting.md). |

If `Phase=Degraded` but every condition is True, the agent is reporting `gateway_state == degraded` via the dashboard's `/api/status`. Port-forward to the dashboard and check the raw status:

```bash
kubectl -n hermes port-forward svc/hermes-my-agent-dashboard 9119:9119
curl -s http://localhost:9119/api/status | jq
```

## When all else fails

- [Reference: Troubleshooting catalogue](../reference/troubleshooting.md) — flat symptom-to-fix table.
- [Known Hermes upstream behaviours](../concepts/upstream-behaviours.md) — the things about upstream that surprise operator users.
- [File an issue](https://github.com/UndermountainCC/hermes-operator/issues) — include `kubectl version`, operator version, the CR (with credentials redacted), and operator logs.
