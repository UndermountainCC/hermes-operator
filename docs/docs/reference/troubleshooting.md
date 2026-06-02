# Troubleshooting catalogue

Symptom-keyed reference for known failure modes. Each entry covers symptom, diagnosis, and fix. For a decision-tree walkthrough starting from `kubectl get hermesagent`, see [Troubleshoot an agent that isn't Ready](../how-to/troubleshoot-not-ready.md).

## Install / chart

### Orphaned `webhook-server-cert` (or `metrics-server-cert`) Secret after upgrade

**Symptom**: After upgrading from an older operator release, you see a `webhook-server-cert` or `metrics-server-cert` Secret in `hermes-operator-system` that no resource references.

**Diagnosis**: A prior release depended on cert-manager to mint a serving certificate for an admission webhook. The webhook is gone now, but the Secret cert-manager generated remains until you remove it.

**Fix**:

```bash
kubectl -n hermes-operator-system delete secret webhook-server-cert metrics-server-cert --ignore-not-found
```

Safe to run unconditionally; `--ignore-not-found` is a no-op on fresh installs.

### `Cannot patch resource ... the object has been modified`

**Symptom**: Operator logs are full of `Operation cannot be fulfilled` conflict errors during reconciliation.

**Diagnosis**: A child resource is being updated by a non-operator manager racing the operator's reconcile.

**Fix**: Identify the foreign manager (check `metadata.managedFields` on the conflicted resource). If you can't, file an issue with the reconcile log.

### Helm install conflicts with prior `make install` CRDs

**Symptom**: Helm install fails with `Apply: existing resource conflict: namespace: ..., kind: CustomResourceDefinition`.

**Diagnosis**: The CRD was installed via `make install` (kubectl-managed) before; Helm refuses to take ownership of an unlabelled resource.

**Fix**: Relabel + annotate for Helm adoption:

```bash
kubectl annotate crd hermesagents.hermes.k8s.undermountain.cc \
    meta.helm.sh/release-name=hermes-operator \
    meta.helm.sh/release-namespace=hermes-operator-system \
    --overwrite
kubectl label crd hermesagents.hermes.k8s.undermountain.cc \
    app.kubernetes.io/managed-by=Helm --overwrite
```

## CR / reconcile

### Phase stuck at `Bootstrap`

**Symptom**: `kubectl get hermesagent` shows `Bootstrap` indefinitely.

**Diagnosis**: A `secretKeyRef` or `envFrom.secretRef` in the spec does not resolve. Check `kubectl describe hermesagent <name>` for `SecretsResolved=False`.

**Fix**: Create the missing Secret, or fix the `name`/`key` reference. The operator re-reconciles every ~15s in this state.

### `RBACSynced=False` with `reason=ClusterRoleNotAllowed`

**Symptom**: Agent is `Degraded`; condition message says e.g. `ClusterRole "cluster-admin" not in allowlist`.

**Diagnosis**: A `spec.rbac.clusterRoleBindings[]` entry references a role not in the operator's `--allowed-cluster-roles` allowlist. The default allowlist is empty.

**Fix**: Either remove the entry from the CR, or upgrade the operator with `operator.allowedClusterRoles=cluster-admin,admin` (Helm value). The new allowlist takes effect on operator restart.

## Pod / runtime

### `discord.errors.PrivilegedIntentsRequired` crashloop

**Symptom**: Agent pod crashes shortly after start; logs show `PrivilegedIntentsRequired`.

**Diagnosis**: Discord's MESSAGE CONTENT INTENT is not enabled for the bot.

**Fix**: Go to `https://discord.com/developers/applications/<app>/bot` → **Privileged Gateway Intents** → toggle on **MESSAGE CONTENT INTENT**. One-time per bot. Full walkthrough: [Set up a Discord bot](../how-to/setup-discord-bot.md).

### DeepSeek returns `HTTP 400 The supported API model names are deepseek-v4-pro or deepseek-v4-flash, but you passed anthropic/claude-opus-4.6`

**Symptom**: Agent crashes (or refuses to respond) on first message. Logs show the above.

**Diagnosis**: Upstream's bundled `cli-config.yaml.example` defaults `model.default` to `anthropic/claude-opus-4.6`. On first boot, this lands as `config.yaml` on the PVC. `HERMES_INFERENCE_MODEL` does not help: upstream only consults it in `hermes -z` oneshot mode, not in `hermes gateway run`.

**Fix** (one-off after first boot):

```bash
kubectl -n hermes exec deployment/hermes-<name> -- \
    sed -i 's|anthropic/claude-opus-4.6|deepseek-v4-pro|' /opt/data/config.yaml
kubectl -n hermes rollout restart deployment/hermes-<name>
```

Full set of options including pre-seeding the PVC: [Change the LLM provider for an agent](../how-to/change-llm-provider.md).

### `Gateway already running (PID ...)` crashloop on the second pod

**Symptom**: When you scale or rolling-update, the new pod crashloops on `Gateway already running`.

**Diagnosis**: Hermes holds `fcntl(LOCK_EX|LOCK_NB)` on `$HERMES_HOME/gateway.lock`; a second hermes process cannot start until the first releases the flock.

**Fix**: The operator pins `replicas: 1` + `strategy: Recreate`. If you see this regardless, something edited the Deployment spec out from under the operator. Inspect with `kubectl describe deployment hermes-<name>` and check `metadata.managedFields` for foreign managers.

### `PermissionError` writing to `/opt/data` from dashboard sidecar

**Symptom**: Dashboard sidecar crashloops with `PermissionError: [Errno 13] Permission denied: '/opt/data/...'`.

**Diagnosis**: The sidecar container is running as root (UID 0) and writing to a PVC owned by UID 10000 (the `hermes` user upstream creates).

**Fix**: The operator stamps `securityContext.runAsUser: 10000` on the sidecar. If you see this on a current release, file an issue with the pod spec.

### Dashboard always reports `gateway_running: false` even though the agent is healthy

**Symptom**: `status.gateways[]` shows all gateways as not ready; `kubectl exec` into the dashboard sidecar and curling `localhost:9119/api/status` confirms `gateway_running: false`.

**Diagnosis**: `pod.spec.shareProcessNamespace` is false (or absent). Upstream's dashboard uses PID-based gateway-liveness detection; without shared PID namespace, the dashboard cannot see the gateway PID.

**Fix**: The operator stamps `shareProcessNamespace: true` when the dashboard is enabled. If you see this on a current release, file an issue.

### Pod terminates mid-drain on rollout / delete

**Symptom**: In-flight conversations are abandoned during pod restart; next boot logs "suspending recently active sessions."

**Diagnosis**: Termination grace period is shorter than upstream's `restart_drain_timeout`. K8s default 30s SIGKILLs the pod before the gateway can drain.

**Fix**: The operator stamps `terminationGracePeriodSeconds: 210` (180s drain + 30s buffer). If you raised `agent.restart_drain_timeout` in `config.yaml` past ~180s, the ceiling needs to grow; file an issue for a `spec.terminationGracePeriodSeconds` field.

## Operator binary

### `lsof -ti:8081 | xargs kill -9`: port already in use

**Symptom**: `make run` fails with `address already in use` on port 8081.

**Diagnosis**: Stale `make run` from a prior session is still holding the health probe port.

**Fix**:

```bash
lsof -ti:8081 | xargs kill -9
```

### Operator's own SA can't grant `cluster-admin`

**Symptom**: `Failed to create ClusterRoleBinding: forbidden: user "system:serviceaccount:hermes-operator-system:hermes-operator" is attempting to grant RBAC permissions not currently held`.

**Diagnosis**: K8s's escalation prevention. The operator's own ClusterRole has `bind` + `escalate` on `clusterroles`/`roles`, which lets it grant rights it does not itself hold, but only through binding mechanics. If you see this error, the operator install is missing those verbs.

**Fix**: Verify the operator's ClusterRole has `escalate` and `bind` verbs. They are stamped via kubebuilder markers and should be present in the chart-rendered ClusterRole.

## OTel / tracing

### Operator pod fails fast at startup with `unable to initialize OpenTelemetry tracing`

**Symptom**: Operator pod is in CrashLoopBackOff; `kubectl logs` shows `unable to initialize OpenTelemetry tracing`.

**Diagnosis**: Malformed `OTEL_EXPORTER_OTLP_ENDPOINT` value.

**Fix**: Use the correct format: `host:port` for gRPC (e.g., `otel-collector.observability.svc:4317`), or `https://host:port` for HTTP. No trailing path for gRPC.

### Traces never arrive at the collector

**Symptom**: Tracing is enabled but Jaeger / Tempo / Honeycomb show no spans.

**Diagnosis**: The OTel SDK retries silently in the background; reconciles proceed. There is no operator-side metric for export failures today.

**Fix**: Watch operator pod logs for `otel` entries (the SDK logs at WARN to stderr). Verify the collector is reachable from the operator pod, e.g. `kubectl exec` into the operator pod and curl the endpoint.

## More

- [Troubleshoot an agent that isn't Ready](../how-to/troubleshoot-not-ready.md) — phase-keyed decision tree.
- [Known Hermes upstream behaviours](../concepts/upstream-behaviours.md) — every catalogued upstream gotcha.
- [GitHub Issues](https://github.com/UndermountainCC/hermes-operator/issues) — open a report if your case is not covered.
