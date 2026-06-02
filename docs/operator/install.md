# Hermes Operator — Install Guide

## Prerequisites
- Kubernetes cluster (kind, k3s, EKS, etc.)
- `kubectl` configured against it
- `make`, `go` 1.24+, `operator-sdk` 1.40+ (for building from source)

## Install
```bash
make install                                  # installs the CRD
make deploy IMG=ghcr.io/undermountaincc/hermes-operator:<tag>
```

The operator deploys to the `hermes-operator-system` namespace by default. The Deployment's image must be reachable from your cluster.

## Create your first HermesAgent
Create a namespace and a Secret for credentials:
```bash
kubectl create namespace hermes
kubectl -n hermes create secret generic hermes-my-agent-secrets \
    --from-literal=DEEPSEEK_API_KEY=… \
    --from-literal=DISCORD_BOT_TOKEN=…
```

Apply the sample CR after editing the image + Secret keys:
```bash
$EDITOR config/samples/hermes_v1alpha1_hermesagent.yaml
kubectl apply -f config/samples/hermes_v1alpha1_hermesagent.yaml
```

Watch reconciliation:
```bash
kubectl -n hermes get hermesagent my-agent -w
```

## Uninstall
```bash
kubectl delete hermesagent --all -A     # delete all agent CRs first
make undeploy                           # remove operator
make uninstall                          # remove CRD
```

PVCs are retained by default (`spec.storage.retainPolicy: Retain`). Manually delete with `kubectl delete pvc -l app=hermes` if you want them gone.

## Phase 1 limitations (will be addressed in later phases)
- ~~No RBAC bindings — apply `RoleBinding`/`ClusterRoleBinding` objects alongside the CR.~~ Resolved in Phase 2: `spec.rbac.{roleBindings,clusterRoleBindings}[]`.
- Missing Secrets surface as pod events, not blocked at CR creation. Inspect `kubectl -n hermes describe pod hermes-<name>-…`. Phase 4 will add a pre-flight `secretRef`-resolution gate.
- ~~Status reports only `Phase` and `PodReady`.~~ Phase 7a: pod readiness now reflects reality via an exec probe (`hermes gateway status | grep -q '✓ Gateway is running'`). Per-gateway `status.gateways[]` returns in Phase 7b via the optional `hermes dashboard` sidecar's `/api/status`.

## Integration notes when using upstream `nousresearch/hermes-agent`

The operator works against the upstream Hermes image directly, but three upstream defaults are worth knowing about. **Validated end-to-end against `docker.io/nousresearch/hermes-agent:v2026.4.30` in a kind cluster.**

### 1. Discord bots need MESSAGE CONTENT INTENT enabled

If you configure a Discord gateway, the bot needs **MESSAGE CONTENT INTENT** toggled on at `https://discord.com/developers/applications/<app>/bot` → "Privileged Gateway Intents". Without it Hermes crashloops with `discord.errors.PrivilegedIntentsRequired`. One-time toggle per bot.

### 2. Upstream's `cli-config.yaml.example` defaults `model.default` to `anthropic/claude-opus-4.6`

When Hermes copies its bundled config to `$HERMES_HOME/config.yaml` on first boot, the model field is `anthropic/claude-opus-4.6`. If you set `spec.llmDefaultProvider: deepseek` (or any non-Anthropic provider), Hermes routes to that provider's endpoint but keeps the (wrong) model name — DeepSeek responds with `HTTP 400 The supported API model names are deepseek-v4-pro or deepseek-v4-flash, but you passed anthropic/claude-opus-4.6`.

**`HERMES_INFERENCE_MODEL` env var won't help** — it's only consulted in `hermes -z` oneshot mode, not in `hermes gateway run` (the mode the operator runs).

Workarounds:

- **Use the UndermountainCC base image** (`ghcr.io/undermountaincc/hermes-agent:…`) instead of `nousresearch/hermes-agent` directly. The base image ships a sanitized `cli-config.yaml.example` with `model.default: deepseek-chat`.

- **Patch the PVC after first boot** (one-off):
  ```bash
  kubectl -n hermes exec deployment/hermes-<name> -- \
      sed -i 's|default: "anthropic/claude-opus-4.6"|default: "deepseek-v4-pro"|' \
      /opt/data/config.yaml
  kubectl -n hermes rollout restart deployment/hermes-<name>
  ```

- **Pre-seed the PVC with a config.yaml** before first boot (Job that mounts the PVC, writes the file).

### Pod readiness: exec probe on `hermes gateway status`

The operator generates a K8s exec probe (`hermes gateway status | grep -q '✓ Gateway is running'`) instead of an HTTP probe. The probe runs inside the agent container, calls upstream's documented gateway-status subcommand, and synthesizes the exit code via grep (the binary always exits 0 — Rich-text output is the truth source). `status.podReady` reflects whether this probe succeeds; without the dashboard sidecar (Phase 7b), per-gateway runtime state (`status.gateways[]`) stays empty.

Replicas is pinned to 1 and deploy strategy to Recreate: hermes holds an `fcntl(LOCK_EX|LOCK_NB)` on `$HERMES_HOME/gateway.lock`, so a second pod would crashloop, and a rolling update would briefly run two pods. Both invariants are enforced by the operator-generated Deployment.

### Termination grace: 210s

The agent Pod template carries `terminationGracePeriodSeconds: 210`. K8s sends SIGTERM to tini (PID 1) at pod termination; tini forwards (in `-g` mode) to the gateway process group; the gateway then drains active turns up to `agent.restart_drain_timeout` (upstream default 180s, set in `hermes_cli/config.py::DEFAULT_CONFIG`) before closing the SessionDB and releasing the `gateway.lock` flock.

210s = 180s drain budget + 30s buffer for K8s + tini + our-entrypoint signal forwarding and the post-drain DB/lock teardown. K8s's default 30s would SIGKILL the pod mid-drain — in-flight turns are abandoned, the `.clean_shutdown` marker is never written, and on next boot the session store calls `suspend_recently_active()` on every active session. If you raise `agent.restart_drain_timeout` in `config.yaml` (very-long-reasoning models), the operator's 210s ceiling is not yet exposed as a spec knob; either keep `restart_drain_timeout <= 180s` or patch the Deployment manually until a `spec.terminationGracePeriodSeconds` field lands.

### Self-introspection RBAC (Phase 10.6)

The operator creates a per-agent `Role` and `RoleBinding`, both named `hermes-<agent-name>-self`, in the agent's namespace. They give the agent's ServiceAccount exactly two capabilities — pinned to the agent's own resource names so the grant cannot reach sibling agents in the same namespace:

- `apps/deployments[hermes-<agent-name>]`: `get`, `patch`. The patch verb is what enables `kubectl rollout restart deployment/hermes-<agent-name>` from inside the pod (rollout-restart is a strategic-merge patch that bumps a timestamp annotation on the Pod template, forcing the `Recreate` strategy to roll the Pod).
- `hermes.k8s.undermountain.cc/hermesagents[<agent-name>]`: `get`. The agent can read its own spec and status.

What's **not** in the grant — and why:

- `pods` is omitted: the canonical "restart yourself" path is the gateway's `/restart` slash command (exit 75 → the container is restarted by the Pod's `restartPolicy`; PVC is preserved). Pod-level replacement goes through `kubectl rollout restart deployment` instead.
- `list` / `watch` are omitted: those verbs cannot be `resourceNames`-scoped, so granting either would expose sibling agents in the same namespace.
- `delete`, `create`, `update`, `*` are omitted: none are needed for the rollout-restart workflow, and `delete` against the agent's own Deployment would force a needless outage.

This is a deliberate bend on the operator's general "RBAC reference-only" pattern (operator creates bindings, never roles). The bend is narrow: ONE Role per agent, hardcoded rules (no user input shapes them), pinned to the agent's own names. The user-spec'd `spec.rbac.roleBindings[]` flow remains reference-only — the operator never creates Roles in response to spec edits.

Verify the grant from any pod or kubectl session:

```bash
kubectl -n hermes auth can-i patch deployment/hermes-<agent-name> \
  --as system:serviceaccount:hermes:hermes-<agent-name>
# yes

kubectl -n hermes auth can-i list pods \
  --as system:serviceaccount:hermes:hermes-<agent-name>
# no
```

## Dashboard sidecar (Phase 7b)

The operator can run the upstream `hermes dashboard` (a FastAPI web server exposing 20+ REST endpoints + a web UI) as a sidecar container alongside the agent, with an optional Ingress fronting it. Default is off.

### Enabling

```yaml
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: my-agent, namespace: hermes }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 1Gi } }
  dashboard:
    enabled: true
```

This creates:
- A `dashboard` sidecar container in the same pod (port 9119, `--insecure --host 0.0.0.0 --no-open`). The sidecar shares `/opt/data` with the gateway; the spike (`a549da47`, 2026-05-15) validated co-execution: the dashboard takes no `fcntl` locks; the gateway's lock is gateway-vs-gateway only.
- A Service `hermes-my-agent-dashboard` (ClusterIP, port 9119).
- `pod.spec.shareProcessNamespace: true` on the pod template. Upstream's `hermes dashboard` does **PID-based gateway-liveness detection** ([upstream Docker docs](https://hermes-agent.nousresearch.com/docs/user-guide/docker)). K8s containers in the same pod normally have isolated PID namespaces — without this flag the dashboard would never see the gateway PID and would always report `gateway_running: false`. The flag is left nil when the sidecar is disabled.

### Authentication

`/api/status` is **unauthenticated by design** (verified against upstream `v2026.4.30`, `hermes_cli/web_server.py:74`); the operator probes it directly. Browser sessions get an **ephemeral SPA-injected token** generated by the dashboard at process start — there is no env-var hook to override it, and the operator does NOT provision a token Secret.

External exposure auth is **user-managed via Ingress annotations** (`nginx.ingress.kubernetes.io/auth-url`, oauth2-proxy fronting, traefik forwardAuth middleware, etc.).

Reach the dashboard from inside the cluster:

```bash
kubectl -n hermes port-forward svc/hermes-my-agent-dashboard 9119:9119
# then open http://localhost:9119/ in a browser. The SPA loads the session
# token inline; admin endpoints (/api/config, /api/env, /api/cron/jobs) gate
# on it. /api/status (which the operator polls) is unauthenticated by
# upstream design.
```

### Exposing externally with auth

```yaml
spec:
  dashboard:
    enabled: true
    ingress:
      enabled: true
      host: hermes.example.com
      ingressClassName: nginx
      tls: { secretName: hermes-tls }
      annotations:
        cert-manager.io/cluster-issuer: letsencrypt-prod
        nginx.ingress.kubernetes.io/auth-url: https://auth.example.com/verify
        nginx.ingress.kubernetes.io/auth-signin: https://auth.example.com/start
```

**Important — auth is YOUR responsibility.** The dashboard exposes admin endpoints (`/api/config`, `/api/env`, `/api/cron/jobs`) gated by the SPA-embedded session token, but `/api/status` is NOT. If you enable Ingress without auth annotations, the operator emits a Warning at admit time but does not block. Production deployments should always include an edge auth layer (nginx `auth-url`, traefik forwardAuth, oauth2-proxy, etc.).

### Per-gateway status via `/api/status` polling

When `spec.dashboard.enabled=true`, the operator polls `http://hermes-<name>-dashboard.<ns>:9119/api/status` every 30s and populates `status.gateways[]` with per-platform state. The schema (verified against `nousresearch/hermes-agent:v2026.4.30`):

- `gateway_platforms[<type>].state`: one of `connecting`, `connected`, `disconnected`, `retrying`, `fatal`.
- `state == connected` AND top-level `gateway_running == true` ⇒ `status.gateways[i].ready = true`.
- `state == fatal` ⇒ `status.gateways[i].message` carries upstream's `error_message`.
- Top-level `gateway_state == degraded` ⇒ `status.phase = Degraded` (forward-compat — upstream `v2026.4.30` does not emit this value; the wiring is in place for future releases).

`/api/status` itself is unauthenticated by upstream design — safe to poll from the operator without any token plumbing.

### Known constraint

The operator pins `replicas: 1` and `strategy: Recreate` (Phase 7a) — gateway holds `fcntl.flock` on the PVC; multiple replicas would crashloop. Dashboard sidecar inherits the same constraint (it lives in the same pod).

## Webhook / cert-manager dependency (Phase 5+)

As of Phase 5 the operator ships a **validating admission webhook** that rejects malformed HermesAgent CRs at admit time. Two cross-field invariants the CRD's OpenAPI schema can't express:

- `spec.image` must be set.
- `spec.llmDefaultProvider` must reference an entry in `spec.llmProviders[].name`.
- `spec.gateways[].type` and `spec.llmProviders[].name` must be non-empty.

The webhook is served via TLS, with the cert provisioned by **cert-manager**. The chart includes an `Issuer` (self-signed) and a `Certificate` for the webhook serving cert. **Installing the chart requires cert-manager pre-installed in the cluster** — without the `cert-manager.io` CRDs, helm install fails with `no matches for kind "Certificate"`. There is no Helm opt-out: the webhook is a security control we ship on by default. If your cluster cannot run cert-manager (air-gapped, restricted), install the operator via raw kustomize (`make deploy`) and strip the webhook + cert-manager patches from `config/default/kustomization.yaml` first.

Install cert-manager first:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
kubectl -n cert-manager wait --for=condition=available --timeout=120s deployment --all
```

Then install (or upgrade) the operator chart as usual. The webhook will be reachable at `validate-hermes-k8s-undermountain-cc-v1alpha1-hermesagent` on the operator's webhook service.

To run the operator locally (no cluster webhook), set `ENABLE_WEBHOOKS=false` before `make run`:

```bash
ENABLE_WEBHOOKS=false make run
```

## NetworkPolicy (Phase 9)

The operator can generate a per-agent `networking.k8s.io/v1` NetworkPolicy when `spec.networkPolicy.enabled: true`. All ingress/egress rules pass through unchanged — no operator-side defaults are injected. The generated policy's `podSelector.matchLabels` targets the agent pod via the standard `hermes.undermountain.cc/{agent,agent-ns}` labels.

**Effective enforcement depends on the cluster's CNI.** Calico and Cilium enforce NetworkPolicy resources; the default `kindnet` (kind clusters) does not — it creates the resource but ignores it in the data plane.

Common pattern — deny ingress except from the operator namespace (so the operator can probe the dashboard sidecar's `/api/status`):

```yaml
spec:
  networkPolicy:
    enabled: true
    policyTypes: [Ingress, Egress]
    ingress:
      - from:
          - namespaceSelector:
              matchLabels:
                kubernetes.io/metadata.name: hermes-operator-system
    egress:
      - to:
          - namespaceSelector: {}  # any namespace (DNS, LLM upstreams)
        ports:
          - protocol: TCP
            port: 443
      - to:                          # cluster DNS
          - namespaceSelector: {}
        ports:
          - protocol: UDP
            port: 53
```

Toggling `enabled: false` deletes the operator-rendered NetworkPolicy (in-place cleanup, not ownerRef GC). The webhook emits a Warning if `enabled: true` with no ingress AND no egress rules — that combination denies ALL traffic, almost certainly user error.

**FQDN-based egress** (e.g., "allow only api.deepseek.com") is NOT expressible in standard NetworkPolicy. Calico's `GlobalNetworkPolicy` with DNS-based selectors or Cilium's `CiliumNetworkPolicy` can do this — out of scope for the operator's CRD, but the cluster admin can layer their own policies in addition to the operator-managed one.

## Prometheus + Grafana (Phase 9)

The operator exposes custom metrics on its existing `/metrics` endpoint (HTTPS, port 8443, service account token auth — same surface as controller-runtime's reconcile metrics):

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `hermes_agent_phase` | gauge | `name, namespace, phase` | 1 for the active phase, 0 (deleted row) otherwise |
| `hermes_agent_pod_ready` | gauge | `name, namespace` | 1 when the agent pod's `PodReady` condition is True |
| `hermes_agent_gateway_ready` | gauge | `name, namespace, gateway_type` | 1 when the dashboard reports the platform as connected (requires `spec.dashboard.enabled`) |
| `hermes_agent_dashboard_probe_failures_total` | counter | `name, namespace` | Cumulative dashboard probe failures (operator → sidecar `/api/status`) |

All rows are scrubbed by the finalizer when a HermesAgent CR is deleted — no ghost gauges remain.

### ServiceMonitor (opt-in)

The chart ships a `ServiceMonitor` gated on `prometheus.serviceMonitor.enabled` (default `false`). Requires the Prometheus Operator's `monitoring.coreos.com/v1` CRDs pre-installed:

```bash
helm install hermes-operator charts/hermes-operator \
  --set prometheus.serviceMonitor.enabled=true \
  --namespace hermes-operator-system
```

For raw kustomize installs, uncomment the `../prometheus` entry in `config/default/kustomization.yaml` before `make deploy`.

### Grafana dashboards

Two starter dashboards under `dashboards/`:

- `operator.json` — controller-runtime reconcile rate, errors, queue depth, latency p50/p95 (the operator's view of itself)
- `agent.json` — per-CR state with `$namespace` + `$name` template variable dropdowns (phase, pod ready, gateway connections, probe failure rate)

Import via the Grafana UI (**Dashboards → Import → upload JSON**). For `kube-prometheus-stack` users with the Grafana sidecar, wrap each in a ConfigMap with label `grafana_dashboard: "1"`:

```bash
kubectl -n monitoring create configmap hermes-operator-dashboards \
  --from-file=operator.json=dashboards/operator.json \
  --from-file=agent.json=dashboards/agent.json
kubectl -n monitoring label configmap hermes-operator-dashboards grafana_dashboard=1
```

Dashboard JSON depends on the exact metric names + labels. Renaming a metric in a future operator release is a breaking change for downstream alerts; deprecation cycles are TBD.

## Distributed tracing (Phase 10)

The operator emits OpenTelemetry trace spans for every reconcile loop. Opt in by pointing the operator at an OTLP gRPC collector — when unset, tracing is a complete no-op (zero overhead, no connection attempt, no startup latency).

### Enabling

**Via Helm:**

```bash
helm install hermes-operator charts/hermes-operator \
  --set tracing.otlp.endpoint="otel-collector.observability.svc.cluster.local:4317" \
  --set tracing.otlp.serviceName="hermes-operator"
```

`tracing.otlp.serviceName` is optional — when unset the operator reports `service.name=hermes-operator` on every span (matching the binary's built-in default).

**Via raw kustomize (`make deploy`):** uncomment the `env:` block in `config/manager/manager.yaml` and re-deploy. Same OTel env vars apply.

**Standard OTel env vars** the operator honors (set via Helm values, raw env in manager.yaml, or downstream `kubectl patch`):

| Env var | Effect |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP gRPC endpoint (required to enable tracing) |
| `OTEL_SERVICE_NAME` | `resource.service.name` on every span (defaults to `hermes-operator`) |
| `OTEL_RESOURCE_ATTRIBUTES` | Additional resource attributes (comma-separated `k=v` pairs — standard OTel) |
| `OTEL_EXPORTER_OTLP_HEADERS` | Outbound headers — used by Honeycomb / Grafana Cloud for auth |

### Collector endpoints

The operator speaks OTLP gRPC. Common backends:

| Backend | Endpoint format |
|---|---|
| OTel Collector (in-cluster) | `otel-collector.observability.svc.cluster.local:4317` |
| Grafana Tempo | `tempo.observability.svc:4317` |
| Jaeger (via OTel Collector) | route OTLP → Jaeger via the collector's `jaeger` exporter |
| Honeycomb (direct OTLP) | `api.honeycomb.io:443` + `OTEL_EXPORTER_OTLP_HEADERS=x-honeycomb-team=…` |
| Grafana Cloud | host from your stack settings + `OTEL_EXPORTER_OTLP_HEADERS=Authorization=Basic …` |

If your tracing backend doesn't speak OTLP natively, run the [OTel Collector](https://opentelemetry.io/docs/collector/) in front of it — that's the canonical translation layer (and how Jaeger users typically wire OTel-instrumented services).

### Span shape

Every reconcile loop produces:

- **Root span** `hermesagent.Reconcile` — attributes: `hermesagent.name`, `hermesagent.namespace`
- **Child spans** per reconcile helper:
  - `Reconcile.EnsureFinalizer`
  - `Reconcile.PVC`
  - `Reconcile.ServiceAccount`
  - `Reconcile.SecretsValidation`
  - `Reconcile.RBAC`
  - `Reconcile.Deployment`
  - `Reconcile.Service`
  - `Reconcile.DashboardService`
  - `Reconcile.DashboardIngress`
  - `Reconcile.NetworkPolicy`
  - `Reconcile.Status` (parent of `Reconcile.DashboardProbe` when the dashboard sidecar is on)
  - `Reconcile.HandleDeletion` (when the CR is being deleted)

**Span events** (timeline annotations on the surrounding span):

- `Reconcile.PhaseTransition` — emitted by `Reconcile.Status` when Phase changes (attributes: `from`, `to`)
- `Reconcile.RBACRejected` — emitted by `Reconcile.RBAC` when a `ClusterRoleBinding` references a role not in the operator's `--allowed-cluster-roles` allowlist (attribute: `roleName`)
- `Reconcile.DashboardProbeFailed` — emitted by both `Reconcile.DashboardProbe` (child) and `Reconcile.Status` (parent) when the dashboard `/api/status` probe errors (attribute: `error`)

**Span status:** `Ok` on clean return, `Error` with the error message on any non-nil error.

### Viewing traces

- **Jaeger UI:** filter on `service.name = hermes-operator`. Reconciles for a specific CR: filter `hermesagent.name = <name>` AND `hermesagent.namespace = <ns>`.
- **Grafana Tempo:** equivalent via the TraceQL `{service.name="hermes-operator" && resource.hermesagent.name="<name>"}` form.
- **Honeycomb / Grafana Cloud:** same `service.name` filter; their UIs expose `hermesagent.*` as facet filters automatically once data lands.

### Failure modes

- **Malformed endpoint URL** → operator pod fails fast at startup. The Init path returns an error rather than silently dropping spans for the pod's lifetime; you'll see `unable to initialize OpenTelemetry tracing` in the operator's `setup` log.
- **Unreachable endpoint** → the OTel SDK retries silently in the background; reconciles proceed. There is currently no operator-side metric for export failures (the OTel SDK logs to stderr at WARN level). Watching the operator pod's logs for `otel` entries is the workaround.
- **Tracing disabled but the operator is being instrumented by a sidecar** (e.g. an OTel auto-instrumentation agent) → safe; the global TracerProvider stays no-op when `OTEL_EXPORTER_OTLP_ENDPOINT` is empty, so the sidecar's instrumentation runs independently.

