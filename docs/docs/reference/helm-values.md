# Helm values

Every key in `charts/hermes-operator/values.yaml`. Generated against current `main`.

## `image`

| Key | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/undermountaincc/hermes-operator` | Operator image. |
| `image.tag` | `""` | Defaults to `.Chart.AppVersion`. Override for nightlies. |
| `image.pullPolicy` | `IfNotPresent` | Standard K8s pull policy. |
| `image.pullSecrets` | `[]` | Registry credentials for private images. |

## `replicas`

| Key | Default | Description |
|---|---|---|
| `replicas` | `1` | Operator replica count. Leader election keeps only one active. |

## `operator`

| Key | Default | Description |
|---|---|---|
| `operator.allowedClusterRoles` | `""` | Comma-separated allowlist of ClusterRole names permitted in `HermesAgent.spec.rbac.clusterRoleBindings[]`. Default empty: no cluster-scoped agent bindings allowed unless the cluster admin opts in. |
| `operator.logLevel` | `""` | Optional log verbosity override (0=info, higher=more detail). |

## `resources`

| Key | Default | Description |
|---|---|---|
| `resources.requests.cpu` | `100m` | Operator pod CPU request. |
| `resources.requests.memory` | `128Mi` | Operator pod memory request. |
| `resources.limits.cpu` | `500m` | Operator pod CPU limit. |
| `resources.limits.memory` | `256Mi` | Operator pod memory limit. |

## `nodeSelector`, `tolerations`, `affinity`

| Key | Default | Description |
|---|---|---|
| `nodeSelector` | `{}` | Standard pod placement. |
| `tolerations` | `[]` | Standard pod placement. |
| `affinity` | `{}` | Standard pod placement. |

## Admission validation

There are no Helm values for admission validation; the CRD's own OpenAPI schema and `x-kubernetes-validations` rules cover it. cert-manager is not required.

## `serviceAccount`

| Key | Default | Description |
|---|---|---|
| `serviceAccount.create` | `true` | Create the operator's ServiceAccount. |
| `serviceAccount.name` | `""` | Override the SA name. |
| `serviceAccount.annotations` | `{}` | Annotations on the SA (e.g. IRSA). |

## `crds`

| Key | Default | Description |
|---|---|---|
| `crds.install` | `true` | If `false`, the chart skips CRD installation. Useful when CRDs are managed separately (e.g., GitOps that requires CRDs upserted out-of-band). |

## `prometheus.serviceMonitor` (opt-in)

| Key | Default | Description |
|---|---|---|
| `prometheus.serviceMonitor.enabled` | `false` | Render a `ServiceMonitor` for the operator's `/metrics` endpoint. **Requires `monitoring.coreos.com/v1` CRDs pre-installed.** The chart fails fast on apply if they're missing. |

Enabling it makes the operator's `hermes_agent_*` gauges + controller-runtime reconcile metrics queryable by Prometheus. See [Enable Prometheus metrics + Grafana dashboards](../how-to/metrics-dashboards.md).

## `tracing.otlp` (opt-in)

| Key | Default | Description |
|---|---|---|
| `tracing.otlp.endpoint` | `""` | OTLP gRPC endpoint. Leave empty to disable tracing (zero overhead). |
| `tracing.otlp.serviceName` | `""` | `resource.service.name` on every span. Defaults to `hermes-operator` when unset. |

Common collector endpoints:

- OTel Collector (in-cluster): `otel-collector.observability.svc.cluster.local:4317`
- Grafana Tempo: `tempo.observability.svc:4317`
- Jaeger (via OTel Collector)
- Honeycomb (direct): `api.honeycomb.io:443` with auth via `OTEL_EXPORTER_OTLP_HEADERS`

See [Send OpenTelemetry traces to a collector](../how-to/opentelemetry.md) for the full set of OTel env vars the operator honors.

## See also

- [Install the operator](../how-to/install.md) — Helm and raw-kustomize install paths.
- [Reference: HermesAgent API](api-reference.md) — CR fields.
- [Reference: Operator CLI flags](cli-flags.md) — operator-binary flags wired from these values.
